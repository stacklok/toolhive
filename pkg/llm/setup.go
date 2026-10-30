// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/llmgateway"
	"github.com/stacklok/toolhive/pkg/networking"
	pkgsecrets "github.com/stacklok/toolhive/pkg/secrets"
)

// LoginFunc performs the interactive OIDC login during setup. It is a
// parameter so that tests can inject a no-op without touching the keyring.
type LoginFunc func(ctx context.Context, cfg *Config) error

// GatewayManager is the subset of client.ClientManager used by Setup and
// Teardown. Defined here so pkg/llm does not import pkg/client.
type GatewayManager interface {
	// DetectedLLMGatewayClients returns tool names for all installed LLM-gateway-capable tools.
	DetectedLLMGatewayClients() []string
	// ConfigureLLMGateway patches the tool's config file and returns the config path.
	ConfigureLLMGateway(clientType string, cfg llmgateway.ApplyConfig) (string, error)
	// LLMGatewayModeFor returns "direct", "proxy", or "" for the given client.
	LLMGatewayModeFor(clientType string) string
	// PromptCacheConflict names a setting that may override ToolHive's
	// one-hour Claude Code prompt-cache configuration.
	PromptCacheConflict(clientType string) (string, error)
	// IsManaged reports whether a managed configuration overrides the
	// client's local config (so the config setup writes would be ignored).
	IsManaged(clientType string) bool
	// LLMClientDetectionHint explains why clientType was not detected, or "".
	// Used when --client names a CLI tool whose settings dir is leftover after uninstall.
	LLMClientDetectionHint(clientType string) string
	// ConfigureEnvFile writes .env file entries for the client and returns the
	// env file path. Returns ("", nil) when the client has no env-file entries.
	ConfigureEnvFile(clientType string, cfg llmgateway.ApplyConfig) (string, error)
	// RevertEnvFile removes the .env file entries that setup wrote. envFilePath
	// is the value returned by ConfigureEnvFile; a no-op when empty.
	RevertEnvFile(clientType, envFilePath string) error
	// RevertLLMGateway removes the LLM gateway settings from the tool's config file.
	RevertLLMGateway(clientType, configPath string) error
}

// ConfigUpdater is the subset of config.Provider used by Setup and Teardown.
// Defined here so pkg/llm does not import pkg/config.
type ConfigUpdater interface {
	// GetLLMConfig returns the current LLM section of the config.
	GetLLMConfig() Config
	// UpdateLLMConfig atomically reads, applies fn, and persists the LLM config.
	UpdateLLMConfig(fn func(*Config) error) error
}

// SecretsProviderFunc returns the secrets provider used to delete cached LLM
// credentials. Teardown calls it only when the requested operation must purge
// tokens.
type SecretsProviderFunc func() (pkgsecrets.Provider, error)

// Setup configures detected AI tools to use the LLM gateway.
//
// When targetClient is non-empty only that client is configured; an error is
// returned if the client is not installed. Pass an empty string to configure
// all detected clients (the original behaviour).
//
// It applies inlineOpts in-memory before login so a failed login leaves no
// persisted state. Tool config files are patched only after login succeeds;
// on any persistence failure the patches are rolled back.
//
// When lazy is true, the interactive OIDC login (login) is skipped entirely:
// tool detection, config-file patching, and config persistence still run, and a
// message is printed telling the user that login will occur on first gateway
// access. lazy is intended for unattended provisioning (e.g. an MDM profile);
// it is opt-in and does not change behaviour for interactive users.
//
// When inlineOpts changes the requested scopes, the cached tokens are deleted
// just before login (see invalidateTokensOnScopeChange). getSecrets is called
// only in that case.
func Setup(
	ctx context.Context, out, errOut io.Writer,
	gm GatewayManager, provider ConfigUpdater, login LoginFunc,
	inlineOpts SetOptions, anthropicPathPrefix string, anthropicPathPrefixSet bool, targetClient string,
	lazy bool, getSecrets SecretsProviderFunc,
) error {
	llmCfg, err := prepareSetupConfig(provider, inlineOpts)
	if err != nil {
		return err
	}

	proxyBaseURL := fmt.Sprintf("http://localhost:%d/v1", llmCfg.EffectiveProxyPort())

	// Detect tools before login so we skip the interactive browser flow when
	// there is nothing to configure. In non-lazy mode login still runs before any
	// files are patched, preserving the guarantee that a failed login leaves no
	// state.
	detected, done, err := setupClients(
		gm, targetClient, lazy, llmCfg.Bedrock.Compat, len(llmCfg.Models) > 0, out, errOut)
	if err != nil {
		return err
	}
	if done {
		return nil
	}

	tokenHelperPath, tokenHelperArgs, err := buildTokenHelperArgv()
	if err != nil {
		return err
	}

	// Changed scopes must not meet the old refresh token, so the cache is dropped
	// right before login. This runs only once the flags validated and there are
	// tools to configure, so an early return above never costs the user a session.
	if err := invalidateTokensOnScopeChange(
		ctx, errOut, provider, provider.GetLLMConfig(), llmCfg, getSecrets,
	); err != nil {
		return err
	}

	// In lazy mode the interactive login is deferred until a configured tool
	// first accesses the gateway (via "thv llm token" or the proxy). Everything
	// below — tool detection, file patching, config persistence — is
	// non-interactive and still runs, so the on-disk result is identical to a
	// normal setup except that no OIDC token is obtained yet.
	if lazy {
		_, _ = fmt.Fprintln(out, "Lazy mode: skipping OIDC login. You'll be signed in on the first")
		_, _ = fmt.Fprintln(out, "request a configured tool makes to the LLM gateway.")
	} else {
		_, _ = fmt.Fprintln(out, "Ensuring you are logged in to the LLM gateway…")
		if err := login(ctx, &llmCfg); err != nil {
			if portErr := setupCallbackPortError(err); portErr != nil {
				return fmt.Errorf("OIDC login failed: %w", portErr)
			}
			return fmt.Errorf("OIDC login failed: %s", SanitizeTokenError(err))
		}
		_, _ = fmt.Fprintln(out, "Login successful.")
	}

	// Resolve the effective path prefix for the Anthropic API before discovery
	// so Pi uses the same endpoint to list models and serve requests.
	anthropicPrefix := resolveAnthropicPrefix(ctx, gm, detected, llmCfg, anthropicPathPrefix, anthropicPathPrefixSet)

	detected, discoveredModels, err := discoverSetupModels(
		ctx, errOut, detected, targetClient, llmCfg, anthropicPrefix,
	)
	if err != nil {
		return err
	}
	if len(detected) == 0 {
		return nil
	}

	configured, err := configureDetectedToolsWithDiscovery(
		out, errOut, gm, detected, llmCfg.GatewayURL, proxyBaseURL,
		tokenHelperPath, tokenHelperArgs, llmCfg.TLSSkipVerify, anthropicPrefix,
		llmCfg.Models, discoveredModels, llmCfg.ShortPromptCache, llmCfg.Bedrock,
	)
	if err != nil {
		return err
	}

	// Warn about bedrock flags the user passed THIS run that had no effect:
	// --bedrock-compat when Claude Code was not configured, and --enable-1m when
	// bedrock-compat is off. Keyed off inlineOpts (not the merged config) because
	// bedrock-compat is persisted — gating on llmCfg.Bedrock would nag on every
	// later setup that omits claude-code. llmCfg.Bedrock.Compat is the effective
	// (persisted + inline) compat state used for the --enable-1m check.
	warnBedrockNoEffect(errOut, inlineOpts, llmCfg.Bedrock.Compat, configured)
	warnShortPromptCacheNoEffect(errOut, inlineOpts, configured)
	warnPromptCacheBedrockCompatibility(errOut, inlineOpts, llmCfg.ShortPromptCache, configured)

	warnTLSSkipVerify(errOut, llmCfg.TLSSkipVerify, configured)
	warnCredentialHelperTools(out, errOut, gm, configured)

	if err := provider.UpdateLLMConfig(func(c *Config) error {
		// SetFields applies inline opts to the on-disk config (preserving any
		// concurrent writes to unrelated fields) and merges ConfiguredTools
		// atomically in a single write.
		if err := c.SetFields(inlineOpts); err != nil {
			return fmt.Errorf("persisting inline flags: %w", err)
		}
		c.ConfiguredTools = mergeToolConfigs(c.ConfiguredTools, configured)
		return nil
	}); err != nil {
		// Roll back every tool we successfully patched so the tool config files
		// are not left in a modified state without a persisted record of what
		// was changed (which would make teardown unable to revert them).
		rollbackConfiguredTools(errOut, gm, configured)
		return fmt.Errorf("persisting tool configuration: %w", err)
	}

	printPiLongCacheHint(out, configured, llmCfg.ShortPromptCache)
	if hasProxyMode(configured) {
		_, _ = fmt.Fprintln(out, "One or more tools use proxy mode. Start the proxy with: thv llm proxy start")
	}

	return nil
}

func prepareSetupConfig(provider ConfigUpdater, inlineOpts SetOptions) (Config, error) {
	llmCfg := provider.GetLLMConfig()
	// Apply inline flags in-memory so login and tool detection use the merged
	// config without touching disk. Persistence happens only after login and
	// tool patching succeed, so a failed login leaves no persisted state.
	if err := llmCfg.SetFields(inlineOpts); err != nil {
		if portErr := setupCallbackPortError(err); portErr != nil {
			return Config{}, fmt.Errorf("invalid inline flag values: %w", portErr)
		}
		return Config{}, fmt.Errorf("invalid inline flag values: %w", err)
	}
	if !llmCfg.IsConfigured() {
		return Config{}, fmt.Errorf("LLM gateway is not configured — run \"thv llm config set\" first")
	}
	return llmCfg, nil
}

func setupCallbackPortError(err error) error {
	var portErr *networking.CallbackPortInUseError
	if !errors.As(err, &portErr) {
		return nil
	}
	return fmt.Errorf(
		"requested callback port %d is already in use; "+
			"stop the process using it or choose another provider-registered port with "+
			`"thv llm setup --callback-port <port>"`,
		portErr.Port,
	)
}

// Teardown removes LLM gateway configuration from all (or one) configured tools.
//
// targetTool selects which tool to revert; pass an empty string to revert all
// configured tools. An error is returned when targetTool is non-empty but not
// found in the configured tool list.
//
// Reverting the last configured tool also resets the persisted LLM config to its
// zero value, so settings that are deliberately sticky across "thv llm setup"
// re-runs (e.g. Bedrock compat) are not silently re-applied by a later setup.
// A targeted teardown that leaves other tools configured keeps the config, which
// those tools still need to reach the gateway.
//
// Cached OIDC tokens are deleted by default when no configured tool remains.
// keepTokens opts out of that full-teardown default. A partial targeted
// teardown retains the shared session unless purgeTokens is true.
func Teardown(
	ctx context.Context,
	out, errOut io.Writer,
	gm GatewayManager,
	targetTool string,
	keepTokens bool,
	purgeTokens bool,
	provider ConfigUpdater,
	getSecretsProvider SecretsProviderFunc,
) error {
	if keepTokens && purgeTokens {
		return fmt.Errorf("keepTokens and purgeTokens cannot both be set")
	}

	llmCfg := provider.GetLLMConfig()
	targets, err := selectTeardownTargets(llmCfg.ConfiguredTools, targetTool)
	if err != nil {
		return err
	}

	// Separate tools into those to revert and those to keep without touching
	// any files yet.
	toRevert, remaining := partitionTeardownTools(llmCfg.ConfiguredTools, targets)
	fullTeardown := len(remaining) == 0
	shouldPurge := purgeTokens || (fullTeardown && !keepTokens)

	// Delete credentials before changing the configured-tool record. If cleanup
	// fails, the same targeted teardown remains fully retryable.
	if err := purgeTokensBeforeTeardown(ctx, shouldPurge, getSecretsProvider); err != nil {
		return err
	}

	// Persist the updated tool list (and clear token metadata if purging) in a
	// single write before mutating any tool config files. Token cleanup may have
	// completed already, but a config-write failure leaves every tool record and
	// settings file intact so the caller can retry teardown safely.
	if err := provider.UpdateLLMConfig(func(c *Config) error {
		applyTeardownToConfig(c, remaining, shouldPurge)
		return nil
	}); err != nil {
		if shouldPurge {
			return fmt.Errorf("cached LLM tokens were deleted, but persisting tool configuration: %w", err)
		}
		return fmt.Errorf("persisting tool configuration: %w", err)
	}

	if len(targets) == 0 {
		_, _ = fmt.Fprintln(out, "No tools are currently configured.")
	} else if fullTeardown {
		_, _ = fmt.Fprintln(out,
			"Cleared the LLM gateway configuration: no tools are configured anymore.")
	}

	// Revert tool config files best-effort; warn on failure but do not undo
	// the config update above (the user can re-run setup+teardown to reconcile).
	for _, tc := range toRevert {
		revertToolConfig(out, errOut, gm, tc)
	}

	if shouldPurge {
		_, _ = fmt.Fprintln(out,
			"Deleted locally cached LLM tokens. This does not revoke tokens at the identity provider or sign out the browser session.")
		_, _ = fmt.Fprintln(out,
			"Stop any running LLM proxy or token helper before relying on cleanup; an active process may recreate cached credentials.")
		if purgeTokens && !fullTeardown {
			_, _ = fmt.Fprintln(out,
				"Remaining configured tools share this session and will need to authenticate again.")
		}
	}

	return nil
}

// applyTeardownToConfig updates the persisted LLM config for a teardown that
// leaves remaining configured. purgeTokens reports whether the teardown drops
// cached OIDC token state.
//
// When no tool remains, the config is reset wholesale rather than pruned field by
// field. Settings such as Bedrock compat are deliberately sticky across "thv llm
// setup" re-runs, so keeping them past the last teardown would let the next setup
// silently re-apply settings the user just removed — possibly against a gateway
// they have since repointed elsewhere. While any tool remains the config is left
// intact, since those tools still need it to reach the gateway.
//
// Cached token state survives a reset only when token retention was explicitly
// requested. The secret lives in the encrypted secrets store, and dropping the
// only reference without deleting it would strand it once the user points at a
// different gateway (the fallback key is derived from the gateway URL and
// issuer).
func applyTeardownToConfig(c *Config, remaining []ToolConfig, purgeTokens bool) {
	if len(remaining) > 0 {
		c.ConfiguredTools = remaining
		if purgeTokens {
			c.OIDC.CachedRefreshTokenRef = ""
			c.OIDC.CachedTokenExpiry = time.Time{}
		}
		return
	}

	tokenState := c.OIDC
	*c = Config{}
	if !purgeTokens {
		c.OIDC.CachedRefreshTokenRef = tokenState.CachedRefreshTokenRef
		c.OIDC.CachedTokenExpiry = tokenState.CachedTokenExpiry
	}
}

// PurgeTokens deletes all cached OIDC tokens from the provided secrets provider.
func PurgeTokens(ctx context.Context, provider pkgsecrets.Provider) error {
	if err := DeleteCachedTokens(ctx, provider); err != nil {
		return fmt.Errorf("removing cached LLM tokens: %w", err)
	}
	return nil
}

func selectTeardownTargets(configured []ToolConfig, targetTool string) ([]ToolConfig, error) {
	if targetTool == "" {
		return configured, nil
	}
	for _, tool := range configured {
		if tool.Tool == targetTool {
			return []ToolConfig{tool}, nil
		}
	}
	return nil, fmt.Errorf("tool %q is not configured", targetTool)
}

func partitionTeardownTools(configured, targets []ToolConfig) ([]ToolConfig, []ToolConfig) {
	var toRevert, remaining []ToolConfig
	for _, tool := range configured {
		if isTarget(targets, tool.Tool) {
			toRevert = append(toRevert, tool)
		} else {
			remaining = append(remaining, tool)
		}
	}
	return toRevert, remaining
}

func purgeTokensBeforeTeardown(
	ctx context.Context, shouldPurge bool, getSecretsProvider SecretsProviderFunc,
) error {
	if !shouldPurge {
		return nil
	}
	if getSecretsProvider == nil {
		return fmt.Errorf("cannot remove cached LLM tokens: secrets provider is unavailable")
	}
	provider, err := getSecretsProvider()
	if err != nil {
		return fmt.Errorf("getting secrets provider to remove cached LLM tokens: %w", err)
	}
	if provider == nil {
		return fmt.Errorf("cannot remove cached LLM tokens: secrets provider is unavailable")
	}
	if err := PurgeTokens(ctx, provider); err != nil {
		return fmt.Errorf("cached token cleanup failed; no configuration changes were made: %w", err)
	}
	return nil
}

// isTarget reports whether toolName appears in the targets slice.
func isTarget(targets []ToolConfig, toolName string) bool {
	for _, t := range targets {
		if t.Tool == toolName {
			return true
		}
	}
	return false
}

// mergeToolConfigs merges newly configured tools into the existing list,
// replacing any entry with the same tool name.
func mergeToolConfigs(existing, incoming []ToolConfig) []ToolConfig {
	index := make(map[string]int, len(existing))
	result := make([]ToolConfig, len(existing))
	copy(result, existing)
	for i, tc := range result {
		index[tc.Tool] = i
	}
	for _, tc := range incoming {
		if i, ok := index[tc.Tool]; ok {
			result[i] = tc
		} else {
			index[tc.Tool] = len(result)
			result = append(result, tc)
		}
	}
	return result
}

// warnTLSSkipVerify prints mode-accurate warnings when TLS verification is
// disabled. The impact differs by tool mode:
//   - direct (Node.js tools like Claude Code, Gemini CLI): NODE_TLS_REJECT_UNAUTHORIZED=0
//     is written to the tool's settings, disabling TLS for ALL of that tool's outbound
//     connections — not just the LLM gateway.
//   - proxy-backed: only the proxy's upstream connection to the gateway has TLS verification
//     disabled; the tool itself is unaffected.
func warnTLSSkipVerify(errOut io.Writer, skip bool, configured []ToolConfig) {
	if !skip {
		return
	}
	for _, tc := range configured {
		switch tc.Mode {
		case llmgateway.ModeDirect:
			_, _ = fmt.Fprintf(errOut,
				"Warning: %s uses direct mode — NODE_TLS_REJECT_UNAUTHORIZED=0 has been written to its "+
					"settings, disabling TLS certificate verification for ALL of %s's outbound connections "+
					"(LLM provider APIs, MCP registry, etc.), not just the LLM gateway. "+
					"Use only in isolated local environments.\n", tc.Tool, tc.Tool)
		case llmgateway.ModeProxy, llmgateway.ModeVSCode:
			if tc.Tool == "gemini-cli" {
				_, _ = fmt.Fprintf(errOut,
					"Note: --tls-skip-verify is not supported for Gemini CLI "+
						"(setting NODE_TLS_REJECT_UNAUTHORIZED would affect all HTTPS connections in the process). "+
						"Ensure your proxy certificate is trusted by the system store instead.\n")
			} else {
				_, _ = fmt.Fprintf(errOut,
					"Warning: %s uses proxy mode — TLS certificate verification is disabled for the "+
						"proxy's upstream gateway connection only. Use only in isolated local environments.\n", tc.Tool)
			}
		case llmgateway.ModePi:
			_, _ = fmt.Fprintf(errOut,
				"Warning: --tls-skip-verify was NOT applied to %s — trust the gateway certificate in the system store.\n", tc.Tool)
		case llmgateway.ModeCodexAuth:
			_, _ = fmt.Fprintf(errOut,
				"Warning: --tls-skip-verify was NOT applied to %s — its config.toml has no "+
					"TLS-skip option. Codex will fail against a self-signed gateway until you "+
					"trust the certificate in the system store.\n", tc.Tool)
		}
	}
}

// filterDetectedClients narrows the detected client list to a single entry when
// targetClient is non-empty. It returns an error if the named client is not in
// the detected list. When targetClient is empty the list is returned unchanged.
func filterDetectedClients(gm GatewayManager, targetClient string) ([]string, error) {
	detected := gm.DetectedLLMGatewayClients()
	if targetClient == "" {
		return detected, nil
	}
	for _, c := range detected {
		if c == targetClient {
			return []string{targetClient}, nil
		}
	}
	if hint := gm.LLMClientDetectionHint(targetClient); hint != "" {
		return nil, fmt.Errorf("client %q is not installed or not detected: %s", targetClient, hint)
	}
	return nil, fmt.Errorf("client %q is not installed or not detected", targetClient)
}

func setupClients(
	gm GatewayManager, targetClient string, lazy, bedrockCompat, hasModels bool, out, errOut io.Writer,
) ([]string, bool, error) {
	detected, err := filterDetectedClients(gm, targetClient)
	if err != nil {
		return nil, false, err
	}
	if len(detected) == 0 {
		_, _ = fmt.Fprintln(out, "No supported AI tools detected.")
		return nil, true, nil
	}
	if !lazy {
		return detected, false, nil
	}
	detected, err = filterLazyModelDiscoveryClients(detected, targetClient, bedrockCompat, hasModels, errOut)
	if err != nil {
		return nil, false, err
	}
	return detected, len(detected) == 0, nil
}

const (
	vsCodeClient        = "vscode"
	vsCodeInsiderClient = "vscode-insider"
	// claudeCodeClient is the canonical client identifier for Claude Code.
	// Declared here as a string literal because pkg/llm does not import
	// pkg/client (which owns the ClientApp constant) to avoid an import cycle.
	claudeCodeClient = "claude-code"
	// promptCacheVerificationCommand reports which TTL Claude Code used under
	// usage.cache_creation in the command's JSON output.
	promptCacheVerificationCommand = `claude -p "hello" --output-format json`
)

func isVSCodeClient(clientType string) bool {
	return clientType == vsCodeClient || clientType == vsCodeInsiderClient
}

func hasVSCodeClient(clients []string) bool {
	for _, clientType := range clients {
		if isVSCodeClient(clientType) {
			return true
		}
	}
	return false
}

func filterLazyModelDiscoveryClients(
	clients []string, targetClient string, bedrockCompat, hasModels bool, errOut io.Writer,
) ([]string, error) {
	if targetClient == "pi" {
		return nil, fmt.Errorf("client %q does not support --lazy: Pi needs authenticated model discovery "+
			"to select each model's API; rerun without --lazy", targetClient)
	}
	if targetClient != "" && isVSCodeClient(targetClient) {
		return nil, fmt.Errorf(
			"client %q does not support --lazy: VS Code requires non-lazy setup for authenticated model discovery; "+
				"rerun without --lazy", targetClient)
	}
	// --bedrock-compat pins each tier to a Bedrock model discovered from the
	// gateway, which lazy mode cannot do (it never logs in). Without pins, Claude
	// Code falls back to Anthropic-form IDs a Bedrock-only gateway rejects — so
	// refuse unless the user pins the tiers explicitly with --models.
	if targetClient == claudeCodeClient && bedrockCompat && !hasModels {
		return nil, fmt.Errorf("client %q does not support --lazy with --bedrock-compat: Bedrock tiers need "+
			"authenticated model discovery; pass --models to pin them, or rerun without --lazy", targetClient)
	}
	filtered := make([]string, 0, len(clients))
	for _, clientType := range clients {
		if clientType == "pi" {
			_, _ = fmt.Fprintln(errOut, "Warning: skipping pi in lazy mode; Pi needs authenticated model discovery.")
			continue
		}
		if isVSCodeClient(clientType) {
			_, _ = fmt.Fprintf(
				errOut,
				"Warning: skipping %s in lazy mode; VS Code requires non-lazy setup for authenticated model discovery.\n",
				clientType,
			)
			continue
		}
		if clientType == claudeCodeClient && bedrockCompat && !hasModels {
			_, _ = fmt.Fprintln(errOut, "Warning: skipping claude-code in lazy mode; --bedrock-compat needs "+
				"authenticated model discovery to pin tiers. Pass --models to pin them, or rerun without --lazy.")
			continue
		}
		filtered = append(filtered, clientType)
	}
	return filtered, nil
}

type discoveredGatewayModels struct {
	openAI                  []string
	openAIContextWindows    map[string]int
	anthropic               []string
	anthropicContextWindows map[string]int
	anthropicReasoning      map[string]bool
	anthropicAdaptive       map[string]bool
}

func discoverSetupModels(
	ctx context.Context, errOut io.Writer, clients []string, targetClient string, cfg Config, anthropicPrefix string,
) ([]string, discoveredGatewayModels, error) {
	hasPi := slices.Contains(clients, "pi")
	hasVSCode := hasVSCodeClient(clients)
	// Bedrock-compat Claude Code pins each tier to the newest Bedrock model the
	// gateway advertises, so it needs the Anthropic catalogue too.
	needsBedrockTiers := cfg.Bedrock.Compat && slices.Contains(clients, claudeCodeClient)
	catalog, openErr, anthropicErr := fetchSetupModelCatalog(
		ctx, cfg, hasPi || needsBedrockTiers, hasVSCode || hasPi, anthropicPrefix)
	piMissing := hasPi && len(catalog.openAI) == 0 && len(catalog.anthropic) == 0
	// Bedrock-compat Claude Code cannot derive its tier pins when the Anthropic
	// catalogue fetch failed, or succeeded without a single Bedrock-form ID. Treat
	// that like the Pi/VS Code discovery failures: abort when the client was named
	// explicitly, skip it with a warning when it was auto-detected (so the user is
	// not left with a Claude Code that routes built-in Anthropic IDs a Bedrock-only
	// gateway rejects). --models pinning every tier needs no discovery. Lazy mode
	// never fetches the catalogue (no access token); filterLazyModelDiscoveryClients
	// already gates it on --models.
	bedrockErr := bedrockDiscoveryError(
		needsBedrockTiers && cfg.discoveryAccessToken != "", cfg.Models, catalog.anthropic, anthropicErr)
	bedrockMissing := bedrockErr != nil
	if err := explicitDiscoveryFailure(targetClient, piMissing, openErr, anthropicErr, bedrockErr); err != nil {
		return nil, catalog, err
	}
	warnModelDiscovery(errOut, hasVSCode, hasPi, piMissing, catalog, openErr, anthropicErr)
	if bedrockMissing {
		_, _ = fmt.Fprintf(errOut, "Warning: skipping auto-detected claude-code because Bedrock model discovery "+
			"failed: %v\n", bedrockErr)
	}
	return keepDiscoverableClients(clients, openErr, piMissing, bedrockMissing), catalog, nil
}

// bedrockDiscoveryError reports why bedrock-compat Claude Code cannot pin its
// tiers from the gateway catalogue, or nil when it can (or need not, because
// --models pins every tier). A catalogue with no Bedrock-form ID for any tier
// is as unusable as a failed fetch; a partial one is only warned about later.
func bedrockDiscoveryError(needsBedrockTiers bool, models, discovered []string, anthropicErr error) error {
	if !needsBedrockTiers {
		return nil
	}
	if pinned, _ := resolveBedrockModels(models, nil, false); len(pinned.emptyFamilies()) == 0 {
		return nil
	}
	if anthropicErr != nil {
		return anthropicErr
	}
	if newestBedrockTiers(discovered) == (bedrockTiers{}) {
		return errNoBedrockModels
	}
	return nil
}

// explicitDiscoveryFailure returns an error when the explicitly named client
// cannot be configured because its model discovery failed, so setup aborts
// rather than writing a half-configured client.
func explicitDiscoveryFailure(targetClient string, piMissing bool, openErr, anthropicErr, bedrockErr error) error {
	switch {
	case isVSCodeClient(targetClient) && openErr != nil:
		return fmt.Errorf("discovering gateway models: %w", openErr)
	case targetClient == "pi" && piMissing:
		return fmt.Errorf("discovering Pi models: %w", errors.Join(openErr, anthropicErr))
	case targetClient == claudeCodeClient && bedrockErr != nil:
		return fmt.Errorf("discovering Bedrock models for Claude Code: %w", bedrockErr)
	default:
		return nil
	}
}

// keepDiscoverableClients drops the auto-detected clients whose required model
// discovery failed, leaving the rest to be configured.
func keepDiscoverableClients(clients []string, openErr error, piMissing, bedrockMissing bool) []string {
	filtered := make([]string, 0, len(clients))
	for _, clientType := range clients {
		if (isVSCodeClient(clientType) && openErr != nil) || (clientType == "pi" && piMissing) ||
			(clientType == claudeCodeClient && bedrockMissing) {
			continue
		}
		filtered = append(filtered, clientType)
	}
	return filtered
}

func fetchSetupModelCatalog(
	ctx context.Context, cfg Config, wantAnthropic, wantOpenAI bool, anthropicPrefix string,
) (discoveredGatewayModels, error, error) {
	var catalog discoveredGatewayModels
	var openErr, anthropicErr error
	if wantOpenAI {
		catalog.openAI, catalog.openAIContextWindows, openErr = discoverGatewayModels(ctx, cfg)
	}
	if wantAnthropic && cfg.discoveryAccessToken != "" {
		catalog.anthropic, catalog.anthropicContextWindows, catalog.anthropicReasoning, catalog.anthropicAdaptive, anthropicErr =
			discoverAnthropicGatewayModels(ctx, cfg, anthropicPrefix)
	}
	return catalog, openErr, anthropicErr
}

func warnModelDiscovery(
	errOut io.Writer, hasVSCode, hasPi, piMissing bool, catalog discoveredGatewayModels, openErr, anthropicErr error,
) {
	if hasVSCode && openErr != nil {
		_, _ = fmt.Fprintf(errOut,
			"Warning: skipping auto-detected VS Code clients because gateway model discovery failed: %v\n", openErr)
	}
	if piMissing {
		_, _ = fmt.Fprintf(errOut,
			"Warning: skipping auto-detected Pi because gateway model discovery failed: %v\n",
			errors.Join(openErr, anthropicErr))
	}
	if hasPi && len(catalog.openAI) > 0 && anthropicErr != nil {
		_, _ = fmt.Fprintf(errOut, "Warning: skipping Pi Anthropic models: %v\n", anthropicErr)
	}
	if hasPi && len(catalog.anthropic) > 0 && openErr != nil {
		_, _ = fmt.Fprintf(errOut, "Warning: skipping Pi OpenAI models: %v\n", openErr)
	}
}

type gatewayModelsResponse struct {
	Object  string `json:"object"`
	HasMore *bool  `json:"has_more"`
	LastID  string `json:"last_id"`
	Data    []struct {
		ID             string `json:"id"`
		ContextWindow  int    `json:"context_window"`
		MaxInputTokens int    `json:"max_input_tokens"`
		Capabilities   *struct {
			Thinking struct {
				Supported bool `json:"supported"`
				Types     struct {
					Adaptive struct {
						Supported bool `json:"supported"`
					} `json:"adaptive"`
					Enabled struct {
						Supported bool `json:"supported"`
					} `json:"enabled"`
				} `json:"types"`
			} `json:"thinking"`
		} `json:"capabilities"`
	} `json:"data"`
}

// errEmptyModelCatalog reports a model list the gateway served successfully but
// with no models in it, which usually means the account has no model access.
var errEmptyModelCatalog = errors.New("gateway returned an empty model list")

// errNoBedrockModels reports an Anthropic catalogue with no Bedrock-form model
// ID for any Claude Code tier, so bedrock-compat setup has nothing to pin.
var errNoBedrockModels = errors.New("the gateway's model list has no Bedrock model IDs " +
	"(expected us.anthropic.claude-…); check that the gateway forwards to Bedrock")

// gatewayStatusError reports a non-200 response to a model-list request, so
// callers can tell a wrong path (404) from missing access (401/403) or a
// gateway fault (5xx).
type gatewayStatusError struct {
	URL        string
	StatusCode int
	Status     string
}

func (e *gatewayStatusError) Error() string {
	return fmt.Sprintf("gateway returned %s for %s", e.Status, e.URL)
}

// describeDiscoveryError rewrites a model-discovery failure into a message that
// names its likely cause and what to check, keeping err wrapped. anthropic
// selects the hints for the Anthropic model list (path prefix, Anthropic access)
// over the OpenAI one.
func describeDiscoveryError(err error, anthropic bool) error {
	list, models := "a model list", "models"
	if anthropic {
		list, models = "an Anthropic model list", "Anthropic models"
	}
	var statusErr *gatewayStatusError
	var urlErr *url.Error
	switch {
	case errors.As(err, &statusErr):
		switch code := statusErr.StatusCode; {
		case code == http.StatusNotFound && anthropic:
			return fmt.Errorf("the gateway doesn't serve %s at %s; check --anthropic-path-prefix: %w",
				list, statusErr.URL, err)
		case code == http.StatusNotFound:
			return fmt.Errorf("the gateway doesn't serve %s at %s; check the gateway URL: %w", list, statusErr.URL, err)
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return fmt.Errorf("the gateway rejected model discovery (%s); check that your account has access to %s: %w",
				statusErr.Status, models, err)
		case code >= http.StatusInternalServerError:
			return fmt.Errorf("the gateway returned %s listing models; retry, or contact the gateway operator: %w",
				statusErr.Status, err)
		}
	case errors.Is(err, errEmptyModelCatalog):
		return fmt.Errorf("the gateway returned no %s for your account; ask the gateway operator for model access: %w",
			models, err)
	case errors.As(err, &urlErr):
		return fmt.Errorf("could not reach the gateway at %s; check the URL and your network: %w", urlErr.URL, err)
	}
	return err
}

func requestGatewayModels(ctx context.Context, cfg Config, path, afterID string) (gatewayModelsResponse, error) {
	if cfg.discoveryAccessToken == "" {
		return gatewayModelsResponse{}, fmt.Errorf("discovering gateway models: authenticated setup did not provide an access token")
	}
	modelsURL, err := url.JoinPath(cfg.GatewayURL, path)
	if err != nil {
		return gatewayModelsResponse{}, fmt.Errorf("building gateway models URL: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return gatewayModelsResponse{}, fmt.Errorf("creating gateway model-discovery request: %w", err)
	}
	if afterID != "" {
		query := req.URL.Query()
		query.Set("after_id", afterID)
		req.URL.RawQuery = query.Encode()
	}
	req.Header.Set("Authorization", "Bearer "+cfg.discoveryAccessToken)
	resp, err := gatewayHTTPClient(cfg.TLSSkipVerify).Do(req)
	if err != nil {
		return gatewayModelsResponse{}, fmt.Errorf("discovering gateway models: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		// Redacted, because the gateway URL may carry a password in its userinfo.
		return gatewayModelsResponse{}, &gatewayStatusError{
			URL: req.URL.Redacted(), StatusCode: resp.StatusCode, Status: resp.Status,
		}
	}
	var payload gatewayModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return gatewayModelsResponse{}, fmt.Errorf("decoding gateway model response: %w", err)
	}
	return payload, nil
}

func discoverGatewayModels(ctx context.Context, cfg Config) ([]string, map[string]int, error) {
	payload, err := requestGatewayModels(ctx, cfg, "v1/models", "")
	if err != nil {
		return nil, nil, describeDiscoveryError(err, false)
	}
	if payload.Object != "list" {
		return nil, nil, fmt.Errorf("decoding gateway model response: expected a non-empty OpenAI model list")
	}
	if len(payload.Data) == 0 {
		return nil, nil, describeDiscoveryError(errEmptyModelCatalog, false)
	}
	seen := make(map[string]struct{}, len(payload.Data))
	models := make([]string, 0, len(payload.Data))
	contextWindows := make(map[string]int, len(payload.Data))
	for _, model := range payload.Data {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			return nil, nil, fmt.Errorf("decoding gateway model response: model ID must not be empty")
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		models = append(models, modelID)
		if model.ContextWindow > 0 {
			contextWindows[modelID] = model.ContextWindow
		}
	}
	sort.Strings(models)
	return models, contextWindows, nil
}

func discoverAnthropicGatewayModels(
	ctx context.Context, cfg Config, anthropicPrefix string,
) ([]string, map[string]int, map[string]bool, map[string]bool, error) {
	path := strings.Trim(anthropicPrefix, "/")
	if path != "" {
		path += "/"
	}
	path += "v1/models"
	seen := make(map[string]struct{})
	models := make([]string, 0)
	contextWindows := make(map[string]int)
	reasoning := make(map[string]bool)
	adaptive := make(map[string]bool)
	afterID := ""
	for range 100 {
		payload, err := requestGatewayModels(ctx, cfg, path, afterID)
		if err != nil {
			return nil, nil, nil, nil, describeDiscoveryError(err, true)
		}
		if payload.HasMore == nil {
			return nil, nil, nil, nil, fmt.Errorf("decoding gateway model response: expected a non-empty Anthropic model page")
		}
		if len(payload.Data) == 0 {
			return nil, nil, nil, nil, describeDiscoveryError(errEmptyModelCatalog, true)
		}
		for _, model := range payload.Data {
			id := strings.TrimSpace(model.ID)
			if id == "" {
				return nil, nil, nil, nil, fmt.Errorf("decoding gateway model response: model ID must not be empty")
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			models = append(models, id)
			if model.MaxInputTokens > 0 {
				contextWindows[id] = model.MaxInputTokens
			}
			if model.Capabilities != nil {
				thinking := model.Capabilities.Thinking
				reasoning[id], adaptive[id] = piThinkingModes(
					thinking.Supported, thinking.Types.Adaptive.Supported, thinking.Types.Enabled.Supported)
			}
		}
		if !*payload.HasMore {
			sort.Strings(models)
			return models, contextWindows, reasoning, adaptive, nil
		}
		if payload.LastID == "" || payload.LastID == afterID {
			return nil, nil, nil, nil, fmt.Errorf("decoding gateway model response: invalid Anthropic pagination cursor")
		}
		afterID = payload.LastID
	}
	return nil, nil, nil, nil, fmt.Errorf("decoding gateway model response: too many Anthropic model pages")
}

func piThinkingModes(supported, adaptive, enabled bool) (bool, bool) {
	return supported && (adaptive || enabled), supported && adaptive
}

// oneMSuffix is the model-ID suffix that opts Bedrock opus/sonnet into the
// 1M-token context window.
const oneMSuffix = "[1m]"

// bedrockTiers holds the model ID pinned for each Claude Code model family in
// bedrock-compat mode. An empty tier is left unpinned.
type bedrockTiers struct {
	haiku, opus, sonnet, fable string
}

// emptyFamilies lists the tiers left without a pinned model ID, in a stable
// order, so the caller can tell the user which families it could not pin.
func (t *bedrockTiers) emptyFamilies() []string {
	var empty []string
	for _, family := range bedrockFamilies {
		if *t.slot(family) == "" {
			empty = append(empty, family)
		}
	}
	return empty
}

// slot returns the field for a family name, or nil for an unknown family.
func (t *bedrockTiers) slot(family string) *string {
	switch family {
	case "haiku":
		return &t.haiku
	case "opus":
		return &t.opus
	case "sonnet":
		return &t.sonnet
	case "fable":
		return &t.fable
	default:
		return nil
	}
}

// bedrockFamilies are the Claude Code model tiers a Bedrock model ID can pin.
var bedrockFamilies = []string{"haiku", "opus", "sonnet", "fable"}

// bedrockModelCorePattern matches a Bedrock-form Anthropic Claude model ID: an
// optional cross-region prefix ("us.", "eu.", "global.", ...) and the required
// "anthropic.claude-" vendor segment, capturing the hyphenated core and dropping
// an optional "-v<rev>[:<n>]" Bedrock revision suffix. Anthropic-form IDs
// ("claude-opus-4-8", which lack the "anthropic." segment) do not match, so a
// gateway that also routes Anthropic directly never has those picked as Bedrock
// pins.
var bedrockModelCorePattern = regexp.MustCompile(
	`^(?:[a-z]+\.)?anthropic\.claude-(.+?)(?:-v\d+(?::\d+)?)?$`)

// parseBedrockModel reports whether id is a Bedrock-form Anthropic Claude model
// ID and, if so, its family and a [major, minor, releaseDate] key for ranking.
// It tokenizes the core on "-" rather than pinning one layout, so it accepts
// both the family-first form ("claude-haiku-4-5-20251001-v1:0") and the
// version-first form used by Claude 3.x on Bedrock
// ("claude-3-5-haiku-20241022-v1:0", "claude-3-haiku-20240307-v1:0"), plus a
// major-only form with a release date ("claude-sonnet-4-20250514-v1:0"). The
// 8-digit release date orders releases within a (major, minor). Any unexpected
// token (a free-form route name, a second family word) rejects the ID so only
// genuine Bedrock model IDs are pinned.
func parseBedrockModel(id string) (family string, version []int, ok bool) {
	m := bedrockModelCorePattern.FindStringSubmatch(id)
	if m == nil {
		return "", nil, false
	}
	var nums []int
	date := 0
	for _, tok := range strings.Split(m[1], "-") {
		switch {
		case slices.Contains(bedrockFamilies, tok):
			if family != "" {
				return "", nil, false // two family tokens: not a real model ID
			}
			family = tok
		case len(tok) == 8 && isAllDigits(tok):
			date, _ = strconv.Atoi(tok)
		case isAllDigits(tok):
			n, _ := strconv.Atoi(tok)
			nums = append(nums, n)
		default:
			return "", nil, false // unknown token: free-form route name, not Bedrock
		}
	}
	if family == "" {
		return "", nil, false
	}
	version = []int{0, 0, date} // [major, minor, releaseDate]
	if len(nums) > 0 {
		version[0] = nums[0]
	}
	if len(nums) > 1 {
		version[1] = nums[1]
	}
	return family, version, true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// newestBedrockTiers picks, for each family, the Bedrock-form ID in discovered
// with the highest [major, minor, releaseDate] version. Ties are broken by the
// lexicographically smallest ID so the result is deterministic. The gateway's
// created_at is not used: it reflects when a route was configured, not the
// model's release.
func newestBedrockTiers(discovered []string) bedrockTiers {
	var tiers bedrockTiers
	best := make(map[string][]int, len(bedrockFamilies))
	for _, id := range discovered {
		family, version, ok := parseBedrockModel(id)
		if !ok {
			continue
		}
		dst := tiers.slot(family)
		prev, seen := best[family]
		cmp := slices.Compare(version, prev)
		if !seen || cmp > 0 || (cmp == 0 && id < *dst) {
			best[family] = version
			*dst = id
		}
	}
	return tiers
}

// applyBedrockCompat resolves the per-tier Bedrock model IDs for Claude Code and
// writes them onto applyCfg, surfacing override diagnostics and naming any tier
// that could not be pinned. Called only for bedrock-compat Claude Code.
func applyBedrockCompat(
	errOut io.Writer, applyCfg *llmgateway.ApplyConfig, models, discoveredAnthropic []string, enable1M bool,
) {
	tiers, warnings := resolveBedrockModels(models, discoveredAnthropic, enable1M)
	applyCfg.BedrockCompat = true
	applyCfg.BedrockHaikuModel = tiers.haiku
	applyCfg.BedrockOpusModel = tiers.opus
	applyCfg.BedrockSonnetModel = tiers.sonnet
	applyCfg.BedrockFableModel = tiers.fable
	for _, w := range warnings {
		_, _ = fmt.Fprintf(errOut, "Warning: %s\n", w)
	}
	// Discovery found at least one Bedrock-form model (a failed fetch, or a
	// catalogue with none, skips claude-code upstream), but a family may still
	// have none — e.g. a gateway serving only some tiers. Name those tiers so the
	// fallback to built-in IDs is visible.
	if empty := tiers.emptyFamilies(); len(empty) > 0 {
		_, _ = fmt.Fprintf(errOut, "Warning: no Bedrock model found in the gateway catalogue for the %s "+
			"tier(s); they fall back to Claude Code's built-in model IDs.\n",
			strings.Join(empty, ", "))
	}
}

// resolveBedrockModels picks the model ID pinned for each Claude Code family.
// Each family starts from the newest Bedrock model in discovered (see
// newestBedrockTiers); override model IDs then replace a family by matching
// "haiku", "opus", "sonnet", or "fable" (case-insensitive) as a substring of
// the ID. A family with neither resolves to "" so no pin is written and Claude
// Code falls back to its built-in model ID. When enable1M is true, the "[1m]"
// suffix is appended to pinned opus and sonnet IDs (never haiku, which has a
// 200K context window) to opt into the 1M context window on Bedrock; an ID
// that already carries the suffix is left as-is so it is not doubled.
//
// warnings collects the human-readable diagnostics for overrides that had no
// (or a surprising) effect — an ID matching no tier, and a second ID clobbering
// a tier an earlier ID already set — so the caller can surface them.
func resolveBedrockModels(models, discovered []string, enable1M bool) (bedrockTiers, []string) {
	tiers := newestBedrockTiers(discovered)
	var warnings []string
	// Track which tiers an override has already claimed so a second override for
	// the same tier is reported rather than silently winning last-write.
	assigned := make(map[string]string, 4)
	for _, m := range models {
		lower := strings.ToLower(m)
		family := ""
		for _, f := range []string{"haiku", "opus", "sonnet", "fable"} {
			if strings.Contains(lower, f) {
				family = f
				break
			}
		}
		if family == "" {
			warnings = append(warnings, fmt.Sprintf(
				"--models entry %q did not match a tier (expected haiku/opus/sonnet/fable in the ID); ignored", m))
			continue
		}
		if prev, ok := assigned[family]; ok {
			warnings = append(warnings, fmt.Sprintf(
				"--models entry %q overrides %q for the %s tier; only the last is used", m, prev, family))
		}
		assigned[family] = m
		*tiers.slot(family) = m
	}
	if enable1M {
		tiers.opus = withOneMSuffix(tiers.opus)
		tiers.sonnet = withOneMSuffix(tiers.sonnet)
	}
	return tiers, warnings
}

// withOneMSuffix appends the "[1m]" suffix unless id is empty (an unpinned tier)
// or already ends with it, so a user-supplied ID that already carries the suffix
// is not doubled into "[1m][1m]".
func withOneMSuffix(id string) string {
	if id == "" || strings.HasSuffix(id, oneMSuffix) {
		return id
	}
	return id + oneMSuffix
}

// warnBedrockNoEffect warns about Bedrock flags the user passed on THIS run that
// had no effect. It keys off opts (the inline flags for this invocation), not the
// persisted config, because bedrock-compat is sticky: warning off the merged state
// would nag on every later setup that omits claude-code even though the flag was
// not passed. effectiveCompat is the merged (persisted + inline) compat state, used
// to decide whether an inline --enable-1m is inert.
//
// Two cases:
//   - --bedrock-compat passed this run but Claude Code was not among the configured
//     tools (the keys only attach to Claude Code), and
//   - --enable-1m passed this run while compat is off (the 1M suffix rides the
//     Bedrock model-ID path, so it is inert without compat) — otherwise a silent
//     no-op.
func warnBedrockNoEffect(errOut io.Writer, opts SetOptions, effectiveCompat bool, configured []ToolConfig) {
	if opts.BedrockCompat != nil && *opts.BedrockCompat && !isTarget(configured, claudeCodeClient) {
		_, _ = fmt.Fprintln(errOut,
			"Warning: --bedrock-compat was set but Claude Code was not configured; the flag had no effect.")
	}
	if opts.Enable1M != nil && *opts.Enable1M && !effectiveCompat {
		_, _ = fmt.Fprintln(errOut,
			"Warning: --enable-1m has no effect without --bedrock-compat; it is ignored.")
	}
}

func printPiLongCacheHint(out io.Writer, configured []ToolConfig, shortPromptCache bool) {
	if isTarget(configured, "pi") && !shortPromptCache {
		_, _ = fmt.Fprintln(out, "Pi uses short prompt caching by default. To enable long retention, "+
			"start Pi with PI_CACHE_RETENTION=long in its environment (Anthropic: 1h; OpenAI: 24h).")
	}
}

func warnShortPromptCacheNoEffect(errOut io.Writer, opts SetOptions, configured []ToolConfig) {
	if opts.ShortPromptCache != nil && *opts.ShortPromptCache && !isTarget(configured, claudeCodeClient) {
		_, _ = fmt.Fprintln(errOut,
			"Warning: --short-prompt-cache was set but Claude Code was not configured; the flag had no effect on client settings.")
	}
}

func warnPromptCacheBedrockCompatibility(
	errOut io.Writer, opts SetOptions, shortPromptCache bool, configured []ToolConfig,
) {
	if shortPromptCache || opts.BedrockCompat == nil || !*opts.BedrockCompat ||
		!isTarget(configured, claudeCodeClient) {
		return
	}
	_, _ = fmt.Fprintf(errOut,
		"Warning: Claude Code's one-hour prompt-cache lifetime may not take effect with Bedrock compatibility: "+
			"the gateway may reject or strip the required beta header, and Bedrock support varies by model. "+
			"Verify the effective lifetime with: %s\n", promptCacheVerificationCommand)
}

// configureDetectedTools patches each detected tool's config file and returns
// the list of successfully configured tools. An error is returned only when no
// tool was configured successfully.
func configureDetectedToolsWithDiscovery(
	out, errOut io.Writer, gm GatewayManager, detected []string,
	gatewayURL, proxyBaseURL, tokenHelperPath string, tokenHelperArgs []string,
	tlsSkipVerify bool, anthropicPathPrefix string,
	models []string, discovered discoveredGatewayModels, shortPromptCache bool, bedrock BedrockConfig,
) ([]ToolConfig, error) {
	var configured []ToolConfig
	for _, clientType := range detected {
		mode := gm.LLMGatewayModeFor(clientType)

		// Apply the Anthropic path prefix for tools that talk the Anthropic API
		// directly — direct-mode (ANTHROPIC_BASE_URL) and credential-helper mode
		// (Claude Desktop's inferenceGatewayBaseUrl). Proxy-mode tools (Cursor,
		// VS Code, Xcode) do not use it.
		anthropicBaseURL := ""
		if usesAnthropicBaseURL(mode) && anthropicPathPrefix != "" {
			// Trim any leading slash: url.JoinPath docs say elements should not
			// start with "/", and path.Join already handles the join correctly.
			if joined, err := url.JoinPath(gatewayURL, strings.TrimLeft(anthropicPathPrefix, "/")); err == nil {
				anthropicBaseURL = joined
			}
		}

		applyCfg := llmgateway.ApplyConfig{
			GatewayURL:                    gatewayURL,
			AnthropicBaseURL:              anthropicBaseURL,
			ProxyBaseURL:                  proxyBaseURL,
			TokenHelperCommand:            tokenHelperShellCommand,
			TokenHelperPath:               tokenHelperPath,
			TokenHelperArgs:               tokenHelperArgs,
			TLSSkipVerify:                 tlsSkipVerify,
			Models:                        models,
			DiscoveredModels:              discovered.openAI,
			DiscoveredModelContextWindows: discovered.openAIContextWindows,
			AnthropicModels:               discovered.anthropic,
			AnthropicModelContextWindows:  discovered.anthropicContextWindows,
			AnthropicModelReasoning:       discovered.anthropicReasoning,
			AnthropicModelAdaptive:        discovered.anthropicAdaptive,
		}

		if clientType == claudeCodeClient {
			applyCfg.ShortPromptCache = shortPromptCache
		}

		// Bedrock-compat applies only to Claude Code: it disables the experimental
		// anthropic-beta headers Bedrock rejects and pins each tier to the newest
		// discovered Bedrock model (or the --models override). Resolve tier
		// selection and the optional [1m] suffix here so pkg/client only writes
		// the resolved values.
		if bedrock.Compat && clientType == claudeCodeClient {
			applyBedrockCompat(errOut, &applyCfg, models, discovered.anthropic, bedrock.Enable1M)
		}

		configPath, err := gm.ConfigureLLMGateway(clientType, applyCfg)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "Warning: failed to configure %s: %v\n", clientType, err)
			continue
		}
		envFilePath, err := gm.ConfigureEnvFile(clientType, applyCfg)
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "Warning: failed to write .env for %s: %v\n", clientType, err)
			// Roll back the settings-file patch we just made.
			if revertErr := gm.RevertLLMGateway(clientType, configPath); revertErr != nil {
				_, _ = fmt.Fprintf(errOut, "Warning: rollback of %s failed: %v\n", clientType, revertErr)
			}
			continue
		}
		configured = append(configured, ToolConfig{
			Tool:        clientType,
			Mode:        mode,
			ConfigPath:  configPath,
			EnvFilePath: envFilePath,
		})
		_, _ = fmt.Fprintf(out, "Configured %s (%s mode)  →  %s\n", clientType, mode, configPath)
		if clientType == claudeCodeClient && !shortPromptCache {
			warnPromptCacheConflict(errOut, gm, clientType)
		}
	}
	if len(configured) == 0 {
		return nil, fmt.Errorf("failed to configure any detected tools")
	}
	return configured, nil
}

func warnPromptCacheConflict(errOut io.Writer, gm GatewayManager, clientType string) {
	conflict, err := gm.PromptCacheConflict(clientType)
	if err != nil {
		_, _ = fmt.Fprintf(errOut,
			"Warning: could not inspect %s for prompt-cache TTL conflicts: %v; "+
				"ToolHive wrote the one-hour settings anyway.\n", clientType, err)
		return
	}
	if conflict != "" {
		_, _ = fmt.Fprintf(errOut,
			"Warning: ToolHive wrote the one-hour prompt-cache settings for %s, but %s may override them. "+
				"Remove that setting to use the one-hour lifetime. Verify the effective lifetime with: %s\n",
			clientType, conflict, promptCacheVerificationCommand)
	}
}

// resolveAnthropicPrefix returns the effective Anthropic path prefix. When the
// caller explicitly set the flag (anthropicPathPrefixSet), the provided value is
// returned as-is (including empty string, which disables the prefix). Otherwise
// the gateway is auto-probed when at least one client uses its Anthropic API.
func resolveAnthropicPrefix(
	ctx context.Context, gm GatewayManager, detected []string,
	llmCfg Config, anthropicPathPrefix string, anthropicPathPrefixSet bool,
) string {
	if anthropicPathPrefixSet || !hasAnthropicBaseURLClient(gm, detected) {
		return anthropicPathPrefix
	}
	return probeAnthropicPrefix(ctx, llmCfg.GatewayURL, llmCfg.TLSSkipVerify)
}

// probeAnthropicPrefix performs a HEAD request to <gatewayURL>/anthropic/v1/messages.
// If the server responds with HTTP 401 (Unauthorized) — meaning the path exists but
// requires authentication — it returns "/anthropic" as the path prefix for
// clients using the Anthropic API.
// Any other status code (including 404 Not Found) or any network error is treated
// as "no prefix needed" and the function returns "".
func probeAnthropicPrefix(ctx context.Context, gatewayURL string, tlsSkipVerify bool) string {
	if gatewayURL == "" {
		return ""
	}
	probeURL, err := url.JoinPath(gatewayURL, "anthropic/v1/messages")
	if err != nil {
		return ""
	}

	httpClient := gatewayHTTPClient(tlsSkipVerify)

	// Use a short timeout so setup is not significantly slowed by an unreachable gateway.
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodHead, probeURL, nil)
	if err != nil {
		return ""
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return ""
	}
	_ = resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "/anthropic"
	}
	return ""
}

// gatewayHTTPClient clones the default transport so gateway requests retain
// its proxy, timeout, HTTP/2, and connection-pooling behavior.
func gatewayHTTPClient(tlsSkipVerify bool) *http.Client {
	//nolint:forcetypeassert // DefaultTransport is always *http.Transport
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsSkipVerify {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // G402: intentional for local dev with self-signed certs
	}
	return &http.Client{Transport: transport}
}

// tokenHelperShellCommand is the shell command written into direct-mode tools'
// config as their token helper — e.g. Claude Code's apiKeyHelper, which is run
// through a shell (execa with shell:true; see anthropics/claude-code#42593).
//
// It deliberately names "thv" bare rather than interpolating os.Executable().
// A bare command has nothing to escape, so it is correct in both /bin/sh and
// cmd.exe without a platform branch, and it re-resolves on every invocation —
// so upgrading, reinstalling, or relocating thv keeps working without re-running
// "thv llm setup".
//
// The trade-off is that it resolves via PATH at invocation time. A tool launched
// without the user's shell PATH (e.g. from the macOS Dock, which inherits
// launchd's environment) will not find thv, and a binary earlier on PATH can
// shadow it. Both are accepted here: direct-mode tools are terminal-oriented.
// Claude Desktop cannot accept them — it is only ever GUI-launched — so its
// credential-helper shim uses the absolute TokenHelperPath instead (see
// writeCredentialHelperShim in pkg/client).
const tokenHelperShellCommand = llmgateway.TokenHelperShellCommand

// buildTokenHelperArgv returns the argv-form of the token helper, for config
// formats that invoke an executable directly (no shell) — e.g. Codex's
// [model_providers.<id>.auth] command/args table. Since args are passed as a
// TOML array rather than interpolated into a shell string, no shell-metacharacter
// validation is needed. --skip-browser is always included since Codex has no
// interactive-context signal like Claude Desktop's credential-helper shim.
func buildTokenHelperArgv() (string, []string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("resolving thv executable path: %w", err)
	}
	return self, []string{"llm", "token", "--skip-browser"}, nil
}

// usesAnthropicBaseURL reports whether a client mode consumes the Anthropic base
// URL (gateway plus its probed or configured prefix): direct mode via
// ANTHROPIC_BASE_URL, credential-helper mode via inferenceGatewayBaseUrl,
// and Pi via its Anthropic provider.
func usesAnthropicBaseURL(mode string) bool {
	return mode == llmgateway.ModeDirect || mode == llmgateway.ModeCredentialHelper || mode == llmgateway.ModePi
}

// hasAnthropicBaseURLClient reports whether any detected client needs the
// Anthropic base URL. Proxy-mode tools ignore it entirely.
func hasAnthropicBaseURLClient(gm GatewayManager, detected []string) bool {
	for _, clientType := range detected {
		if usesAnthropicBaseURL(gm.LLMGatewayModeFor(clientType)) {
			return true
		}
	}
	return false
}

// warnCredentialHelperTools prints the follow-up notes credential-helper clients
// (Claude Desktop) need: they read config only at launch, so the user must fully
// quit and relaunch; and a managed configuration, if present, overrides
// the local config setup just wrote.
func warnCredentialHelperTools(out, errOut io.Writer, gm GatewayManager, configured []ToolConfig) {
	for _, tc := range configured {
		if tc.Mode != llmgateway.ModeCredentialHelper {
			continue
		}
		_, _ = fmt.Fprintf(out,
			"%s reads its configuration only at launch — fully quit and reopen it "+
				"for the gateway change to take effect.\n", tc.Tool)
		if gm.IsManaged(tc.Tool) {
			_, _ = fmt.Fprintf(errOut,
				"Warning: a managed configuration for %s is present; it overrides the local "+
					"configuration just written, which will be ignored. Remove the managed configuration, "+
					"configure the gateway there instead, or fix it if %s rejected it.\n", tc.Tool, tc.Tool)
		}
	}
}

// revertToolConfig reverts the settings-file and .env patches for a single
// tool. Errors are logged as warnings so the caller can continue with others.
func revertToolConfig(out, errOut io.Writer, gm GatewayManager, tc ToolConfig) {
	ok := true
	if err := gm.RevertLLMGateway(tc.Tool, tc.ConfigPath); err != nil {
		_, _ = fmt.Fprintf(errOut, "Warning: failed to revert %s settings: %v\n", tc.Tool, err)
		ok = false
	}
	if tc.EnvFilePath != "" {
		if err := gm.RevertEnvFile(tc.Tool, tc.EnvFilePath); err != nil {
			_, _ = fmt.Fprintf(errOut, "Warning: failed to revert %s .env: %v\n", tc.Tool, err)
			ok = false
		}
	}
	if ok {
		_, _ = fmt.Fprintf(out, "Reverted %s  (%s)\n", tc.Tool, tc.ConfigPath)
	}
}

// rollbackConfiguredTools reverts the settings-file and .env patches for every
// entry in configured. Errors are logged as warnings so all tools are attempted.
func rollbackConfiguredTools(errOut io.Writer, gm GatewayManager, configured []ToolConfig) {
	// Use a discard writer for the "Reverted" line — rollback is silent on success.
	for _, tc := range configured {
		revertToolConfig(io.Discard, errOut, gm, tc)
	}
}

// hasProxyMode reports whether any tool requires the localhost proxy.
func hasProxyMode(cfgs []ToolConfig) bool {
	for _, t := range cfgs {
		if t.Mode == llmgateway.ModeProxy || t.Mode == llmgateway.ModeVSCode {
			return true
		}
	}
	return false
}
