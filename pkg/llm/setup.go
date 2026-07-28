// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/llmgateway"
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
	// IsManaged reports whether a managed-preferences profile overrides the
	// client's local config (so the config setup writes would be ignored).
	IsManaged(clientType string) bool
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
func Setup(
	ctx context.Context, out, errOut io.Writer,
	gm GatewayManager, provider ConfigUpdater, login LoginFunc,
	inlineOpts SetOptions, anthropicPathPrefix string, anthropicPathPrefixSet bool, targetClient string,
	lazy bool,
) error {
	llmCfg := provider.GetLLMConfig()

	// Apply inline flags in-memory so login and tool detection use the merged
	// config without touching disk. Persistence happens below, only after login
	// and tool patching succeed, so a failed login leaves no persisted state.
	if err := llmCfg.SetFields(inlineOpts); err != nil {
		return fmt.Errorf("invalid inline flag values: %w", err)
	}

	if !llmCfg.IsConfigured() {
		return fmt.Errorf("LLM gateway is not configured — run \"thv llm config set\" first")
	}

	proxyBaseURL := fmt.Sprintf("http://localhost:%d/v1", llmCfg.EffectiveProxyPort())

	// Detect tools before login so we skip the interactive browser flow when
	// there is nothing to configure. In non-lazy mode login still runs before any
	// files are patched, preserving the guarantee that a failed login leaves no
	// state.
	detected, err := filterDetectedClients(gm.DetectedLLMGatewayClients(), targetClient)
	if err != nil {
		return err
	}
	if len(detected) == 0 {
		_, _ = fmt.Fprintln(out, "No supported AI tools detected.")
		return nil
	}

	// Only build the shell-string token helper if a detected tool actually
	// consumes it — its shell-safety check on the thv executable path would
	// otherwise fail setup for e.g. a Codex-only run, which never uses it.
	var tokenHelperCommand string
	if tokenHelperCommandNeeded(gm, detected) {
		tokenHelperCommand, err = buildTokenHelperCommand()
		if err != nil {
			return err
		}
	}
	tokenHelperPath, tokenHelperArgs, err := buildTokenHelperArgv()
	if err != nil {
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
			return fmt.Errorf("OIDC login failed: %s", SanitizeTokenError(err))
		}
		_, _ = fmt.Fprintln(out, "Login successful.")
	}

	// Resolve the effective path prefix for ANTHROPIC_BASE_URL.
	// If the caller supplied --anthropic-path-prefix, use it directly.
	// Otherwise auto-probe: a HEAD request to <gateway>/anthropic/v1/messages
	// that returns 401 (rather than 404) indicates the gateway uses the
	// /anthropic prefix, so we apply it automatically.
	// Only probe if at least one detected tool uses direct mode; proxy-mode
	// tools ignore the Anthropic prefix entirely.
	anthropicPrefix := resolveAnthropicPrefix(ctx, gm, detected, llmCfg, anthropicPathPrefix, anthropicPathPrefixSet)

	configured, err := configureDetectedTools(
		out, errOut, gm, detected, llmCfg.GatewayURL, proxyBaseURL, tokenHelperCommand,
		tokenHelperPath, tokenHelperArgs, llmCfg.TLSSkipVerify, anthropicPrefix, llmCfg.Models, llmCfg.Bedrock,
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

	if hasProxyMode(configured) {
		_, _ = fmt.Fprintln(out, "One or more tools use proxy mode. Start the proxy with: thv llm proxy start")
	}

	return nil
}

// Teardown removes LLM gateway configuration from all (or one) configured tools.
//
// targetTool selects which tool to revert; pass an empty string to revert all
// configured tools. An error is returned when targetTool is non-empty but not
// found in the configured tool list.
//
// If secretsProvider is non-nil and purgeTokens is true, cached OIDC tokens
// are deleted after the config update succeeds.
func Teardown(
	ctx context.Context,
	out, errOut io.Writer,
	gm GatewayManager,
	targetTool string,
	purgeTokens bool,
	provider ConfigUpdater,
	secretsProvider pkgsecrets.Provider,
) error {
	llmCfg := provider.GetLLMConfig()

	var targets []ToolConfig
	if targetTool != "" {
		for _, tc := range llmCfg.ConfiguredTools {
			if tc.Tool == targetTool {
				targets = append(targets, tc)
				break
			}
		}
		if len(targets) == 0 {
			return fmt.Errorf("tool %q is not configured", targetTool)
		}
	} else {
		targets = llmCfg.ConfiguredTools
	}

	if len(targets) == 0 {
		_, _ = fmt.Fprintln(out, "No tools are currently configured.")
		return nil
	}

	// Separate tools into those to revert and those to keep, without touching
	// any files yet. We persist the new config first so that if UpdateLLMConfig
	// fails, the tool files are left intact and the state stays consistent.
	var toRevert, remaining []ToolConfig
	for _, tc := range llmCfg.ConfiguredTools {
		if isTarget(targets, tc.Tool) {
			toRevert = append(toRevert, tc)
		} else {
			remaining = append(remaining, tc)
		}
	}

	// Persist the updated tool list (and clear token metadata if purging) in a
	// single write before mutating any tool config files. If this fails,
	// nothing on disk has changed and the caller can retry.
	if err := provider.UpdateLLMConfig(func(c *Config) error {
		c.ConfiguredTools = remaining
		if purgeTokens {
			c.OIDC.CachedRefreshTokenRef = ""
			c.OIDC.CachedTokenExpiry = time.Time{}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("persisting tool configuration: %w", err)
	}

	// Revert tool config files best-effort; warn on failure but do not undo
	// the config update above (the user can re-run setup+teardown to reconcile).
	for _, tc := range toRevert {
		revertToolConfig(out, errOut, gm, tc)
	}

	if purgeTokens && secretsProvider != nil {
		// Delete secrets after config refs are cleared so there is no window
		// where secrets are gone but the config still points at them.
		PurgeTokens(ctx, errOut, secretsProvider)
	}

	return nil
}

// PurgeTokens deletes all cached OIDC tokens from the provided secrets
// provider. Errors are logged as warnings rather than returned.
func PurgeTokens(ctx context.Context, errOut io.Writer, provider pkgsecrets.Provider) {
	if err := DeleteCachedTokens(ctx, provider); err != nil {
		_, _ = fmt.Fprintf(errOut, "Warning: could not remove cached LLM tokens: %v\n", err)
	}
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
//   - proxy: only the proxy's upstream connection to the gateway has TLS verification
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
		case llmgateway.ModeProxy:
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
func filterDetectedClients(detected []string, targetClient string) ([]string, error) {
	if targetClient == "" {
		return detected, nil
	}
	for _, c := range detected {
		if c == targetClient {
			return []string{targetClient}, nil
		}
	}
	return nil, fmt.Errorf("client %q is not installed or not detected", targetClient)
}

// claudeCodeClient is the canonical client identifier for Claude Code. Declared
// here as a string literal because pkg/llm does not import pkg/client (which
// owns the ClientApp constant) to avoid an import cycle.
const claudeCodeClient = "claude-code"

// Default Bedrock inference-profile model IDs written for Claude Code in
// bedrock-compat mode when --models does not override a tier. These track the
// current generation and are expected to be bumped periodically; users override
// per tier via --models.
const (
	defaultBedrockHaikuModel  = "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	defaultBedrockOpusModel   = "us.anthropic.claude-opus-4-8"
	defaultBedrockSonnetModel = "us.anthropic.claude-sonnet-5"
)

// oneMSuffix is the model-ID suffix that opts Bedrock opus/sonnet into the
// 1M-token context window.
const oneMSuffix = "[1m]"

// resolveBedrockModels maps optional override model IDs onto Claude Code's three
// tiers, starting from the built-in defaults. Each override is assigned to a tier
// by matching "haiku", "opus", or "sonnet" (case-insensitive) as a substring of
// the ID. When enable1M is true, the "[1m]" suffix is appended to the opus and
// sonnet IDs (never haiku, which has a 200K context window) to opt into the 1M
// context window on Bedrock; an override that already carries the suffix is left
// as-is so it is not doubled.
//
// warnings collects the human-readable diagnostics for overrides that had no
// (or a surprising) effect — an ID matching no tier, and a second ID clobbering
// a tier an earlier ID already set — so the caller can surface them.
func resolveBedrockModels(models []string, enable1M bool) (haiku, opus, sonnet string, warnings []string) {
	haiku, opus, sonnet = defaultBedrockHaikuModel, defaultBedrockOpusModel, defaultBedrockSonnetModel
	// Track which tiers an override has already claimed so a second override for
	// the same tier is reported rather than silently winning last-write.
	assigned := make(map[string]string, 3)
	assign := func(tier string, dst *string, id string) {
		if prev, ok := assigned[tier]; ok {
			warnings = append(warnings, fmt.Sprintf(
				"--models entry %q overrides %q for the %s tier; only the last is used", id, prev, tier))
		}
		assigned[tier] = id
		*dst = id
	}
	for _, m := range models {
		switch lower := strings.ToLower(m); {
		case strings.Contains(lower, "haiku"):
			assign("haiku", &haiku, m)
		case strings.Contains(lower, "opus"):
			assign("opus", &opus, m)
		case strings.Contains(lower, "sonnet"):
			assign("sonnet", &sonnet, m)
		default:
			warnings = append(warnings, fmt.Sprintf(
				"--models entry %q did not match a tier (expected haiku/opus/sonnet in the ID); ignored", m))
		}
	}
	if enable1M {
		opus = withOneMSuffix(opus)
		sonnet = withOneMSuffix(sonnet)
	}
	return haiku, opus, sonnet, warnings
}

// withOneMSuffix appends the "[1m]" suffix unless id already ends with it, so a
// user-supplied ID that already carries the suffix is not doubled into "[1m][1m]".
func withOneMSuffix(id string) string {
	if strings.HasSuffix(id, oneMSuffix) {
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

// configureDetectedTools patches each detected tool's config file and returns
// the list of successfully configured tools. An error is returned only when no
// tool was configured successfully.
func configureDetectedTools(
	out, errOut io.Writer,
	gm GatewayManager,
	detected []string,
	gatewayURL, proxyBaseURL, tokenHelperCommand string,
	tokenHelperPath string, tokenHelperArgs []string,
	tlsSkipVerify bool,
	anthropicPathPrefix string,
	models []string,
	bedrock BedrockConfig,
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
			GatewayURL:         gatewayURL,
			AnthropicBaseURL:   anthropicBaseURL,
			ProxyBaseURL:       proxyBaseURL,
			TokenHelperCommand: tokenHelperCommand,
			TokenHelperPath:    tokenHelperPath,
			TokenHelperArgs:    tokenHelperArgs,
			TLSSkipVerify:      tlsSkipVerify,
			Models:             models,
		}

		// Bedrock-compat applies only to Claude Code: it disables the experimental
		// anthropic-beta headers Bedrock rejects and pins per-tier Bedrock model
		// IDs. Resolve defaults, tier mapping, and the optional [1m] suffix here so
		// pkg/client only writes the resolved values.
		if bedrock.Compat && clientType == claudeCodeClient {
			haiku, opus, sonnet, warnings := resolveBedrockModels(models, bedrock.Enable1M)
			applyCfg.BedrockCompat = true
			applyCfg.BedrockHaikuModel = haiku
			applyCfg.BedrockOpusModel = opus
			applyCfg.BedrockSonnetModel = sonnet
			for _, w := range warnings {
				_, _ = fmt.Fprintf(errOut, "Warning: %s\n", w)
			}
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
	}
	if len(configured) == 0 {
		return nil, fmt.Errorf("failed to configure any detected tools")
	}
	return configured, nil
}

// resolveAnthropicPrefix returns the effective Anthropic path prefix. When the
// caller explicitly set the flag (anthropicPathPrefixSet), the provided value is
// returned as-is (including empty string, which disables the prefix). Otherwise
// the gateway is auto-probed when at least one direct-mode client is present.
func resolveAnthropicPrefix(
	ctx context.Context, gm GatewayManager, detected []string,
	llmCfg Config, anthropicPathPrefix string, anthropicPathPrefixSet bool,
) string {
	if anthropicPathPrefixSet || !hasDirectModeClient(gm, detected) {
		return anthropicPathPrefix
	}
	return probeAnthropicPrefix(ctx, llmCfg.GatewayURL, llmCfg.TLSSkipVerify)
}

// probeAnthropicPrefix performs a HEAD request to <gatewayURL>/anthropic/v1/messages.
// If the server responds with HTTP 401 (Unauthorized) — meaning the path exists but
// requires authentication — it returns "/anthropic" as the path prefix so that
// ANTHROPIC_BASE_URL is constructed as <gateway>/anthropic rather than <gateway>.
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

	// Build an http.Client that honours --tls-skip-verify so the probe works
	// against gateways with self-signed certificates (local dev). Clone
	// http.DefaultTransport to preserve all production defaults (timeouts,
	// ProxyFromEnvironment, HTTP/2, connection pooling) and only toggle
	// InsecureSkipVerify.
	//nolint:forcetypeassert // DefaultTransport is always *http.Transport
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsSkipVerify {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		transport.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // G402: intentional for local dev with self-signed certs
	}
	httpClient := &http.Client{Transport: transport}

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

// tokenHelperCommandNeeded reports whether any detected client's mode consumes
// the shell-string token helper (TokenHelperCommand) — direct-mode's
// apiKeyHelper-style JSON-Pointer clients and Claude Desktop's credential
// helper. Proxy-mode tools and Codex (argv-based auth) never use it.
func tokenHelperCommandNeeded(gm GatewayManager, detected []string) bool {
	for _, clientType := range detected {
		switch gm.LLMGatewayModeFor(clientType) {
		case llmgateway.ModeDirect, llmgateway.ModeCredentialHelper:
			return true
		}
	}
	return false
}

// buildTokenHelperCommand returns the shell command string used as the
// token-helper for direct-mode tools. It rejects executable paths that contain
// shell metacharacters, since the command is written verbatim into long-lived
// tool config files and re-executed by the shell inside Claude Code / Gemini CLI.
// A path with '"', '\', ';', '$', '`', newline, or carriage-return would
// silently produce a broken or exploitable command. '$' and '`' are included
// because they trigger variable/command substitution inside double-quoted strings.
//
// Note: backslashes are Windows path separators, so this effectively makes
// "thv llm setup" unsupported on Windows — consistent with the rest of the LLM
// gateway feature (token-helper tools use POSIX-style shells).
func buildTokenHelperCommand() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving thv executable path: %w", err)
	}
	const shellUnsafe = `"\;$` + "`\n\r"
	if strings.ContainsAny(self, shellUnsafe) {
		return "", fmt.Errorf(
			"executable path %q contains shell-unsafe characters; "+
				"move thv to a path without quotes, backslashes, semicolons, "+
				"dollar signs, or backticks "+
				"(Windows paths are not supported by thv llm setup)", self)
	}
	return fmt.Sprintf(`"%s" llm token`, self), nil
}

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
// URL (gateway + /anthropic prefix): direct mode via ANTHROPIC_BASE_URL, and
// credential-helper mode (Claude Desktop) via inferenceGatewayBaseUrl.
func usesAnthropicBaseURL(mode string) bool {
	return mode == llmgateway.ModeDirect || mode == llmgateway.ModeCredentialHelper
}

// hasDirectModeClient reports whether any client in the detected list uses a
// mode that needs the Anthropic base URL. Used to skip the Anthropic-prefix
// probe when no such tools are present (proxy-mode tools ignore it entirely).
func hasDirectModeClient(gm GatewayManager, detected []string) bool {
	for _, clientType := range detected {
		if usesAnthropicBaseURL(gm.LLMGatewayModeFor(clientType)) {
			return true
		}
	}
	return false
}

// warnCredentialHelperTools prints the follow-up notes credential-helper clients
// (Claude Desktop) need: they read config only at launch, so the user must fully
// quit and relaunch; and a managed-preferences profile, if present, overrides
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
				"Warning: a managed-preferences profile for %s is present; it overrides the local "+
					"configuration just written, which will be ignored. Remove the managed profile or "+
					"configure the gateway there instead.\n", tc.Tool)
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

// hasProxyMode reports whether any of the given tool configs uses proxy mode.
func hasProxyMode(cfgs []ToolConfig) bool {
	for _, t := range cfgs {
		if t.Mode == llmgateway.ModeProxy {
			return true
		}
	}
	return false
}
