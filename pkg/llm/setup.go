// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"fmt"
	"io"
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
func Setup(
	ctx context.Context, out, errOut io.Writer,
	gm GatewayManager, provider ConfigUpdater, login LoginFunc,
	inlineOpts SetOptions, targetClient string,
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

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving thv executable path: %w", err)
	}
	// Reject paths that contain shell metacharacters: the token-helper command
	// is written verbatim into long-lived tool config files and re-executed by
	// the shell inside Claude Code / Gemini CLI.  A path with '"', '\', ';',
	// '$', '`', newline, or carriage-return would silently produce a broken or
	// exploitable command.  '$' and '`' are included because they trigger
	// variable and command substitution even inside double-quoted strings.
	//
	// Note: backslashes are Windows path separators, so this check effectively
	// makes "thv llm setup" unsupported on Windows — consistent with the rest
	// of the LLM gateway feature (token-helper tools use POSIX-style shells).
	const shellUnsafe = `"\;$` + "`\n\r"
	if strings.ContainsAny(self, shellUnsafe) {
		return fmt.Errorf(
			"executable path %q contains shell-unsafe characters; "+
				"move thv to a path without quotes, backslashes, semicolons, "+
				"dollar signs, or backticks "+
				"(Windows paths are not supported by thv llm setup)", self)
	}

	proxyBaseURL := fmt.Sprintf("http://localhost:%d/v1", llmCfg.EffectiveProxyPort())
	tokenHelperCommand := fmt.Sprintf(`"%s" llm token`, self)

	// Detect tools before login so we skip the interactive browser flow when
	// there is nothing to configure. Login still runs before any files are
	// patched, preserving the guarantee that a failed login leaves no state.
	detected, err := filterDetectedClients(gm.DetectedLLMGatewayClients(), targetClient)
	if err != nil {
		return err
	}
	if len(detected) == 0 {
		_, _ = fmt.Fprintln(out, "No supported AI tools detected.")
		return nil
	}

	_, _ = fmt.Fprintln(out, "Ensuring you are logged in to the LLM gateway…")
	if err := login(ctx, &llmCfg); err != nil {
		return fmt.Errorf("OIDC login failed: %s", SanitizeTokenError(err))
	}
	_, _ = fmt.Fprintln(out, "Login successful.")

	configured, err := configureDetectedTools(
		out, errOut, gm, detected,
		llmCfg.GatewayURL, llmCfg.EffectiveAnthropicPathPrefix(), proxyBaseURL, tokenHelperCommand,
		llmCfg.TLSSkipVerify,
	)
	if err != nil {
		return err
	}

	warnTLSSkipVerify(errOut, llmCfg.TLSSkipVerify, configured)

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
		for _, tc := range configured {
			if revertErr := gm.RevertLLMGateway(tc.Tool, tc.ConfigPath); revertErr != nil {
				_, _ = fmt.Fprintf(errOut,
					"Warning: rollback of %s failed: %v\n", tc.Tool, revertErr)
			}
		}
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
		if err := gm.RevertLLMGateway(tc.Tool, tc.ConfigPath); err != nil {
			_, _ = fmt.Fprintf(errOut, "Warning: failed to revert %s: %v\n", tc.Tool, err)
			continue
		}
		_, _ = fmt.Fprintf(out, "Reverted %s  (%s)\n", tc.Tool, tc.ConfigPath)
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
		case "direct":
			_, _ = fmt.Fprintf(errOut,
				"Warning: %s uses direct mode — NODE_TLS_REJECT_UNAUTHORIZED=0 has been written to its "+
					"settings, disabling TLS certificate verification for ALL of %s's outbound connections "+
					"(LLM provider APIs, MCP registry, etc.), not just the LLM gateway. "+
					"Use only in isolated local environments.\n", tc.Tool, tc.Tool)
		case "proxy":
			_, _ = fmt.Fprintf(errOut,
				"Warning: %s uses proxy mode — TLS certificate verification is disabled for the "+
					"proxy's upstream gateway connection only. Use only in isolated local environments.\n", tc.Tool)
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

// configureDetectedTools patches each detected tool's config file and returns
// the list of successfully configured tools. An error is returned only when no
// tool was configured successfully.
func configureDetectedTools(
	out, errOut io.Writer,
	gm GatewayManager,
	detected []string,
	gatewayURL, anthropicPathPrefix, proxyBaseURL, tokenHelperCommand string,
	tlsSkipVerify bool,
) ([]ToolConfig, error) {
	var configured []ToolConfig
	for _, clientType := range detected {
		configPath, err := gm.ConfigureLLMGateway(clientType, llmgateway.ApplyConfig{
			GatewayURL:          gatewayURL,
			AnthropicPathPrefix: anthropicPathPrefix,
			ProxyBaseURL:        proxyBaseURL,
			TokenHelperCommand:  tokenHelperCommand,
			TLSSkipVerify:       tlsSkipVerify,
		})
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "Warning: failed to configure %s: %v\n", clientType, err)
			continue
		}
		mode := gm.LLMGatewayModeFor(clientType)
		configured = append(configured, ToolConfig{
			Tool:       clientType,
			Mode:       mode,
			ConfigPath: configPath,
		})
		_, _ = fmt.Fprintf(out, "Configured %s (%s mode)  →  %s\n", clientType, mode, configPath)
	}
	if len(configured) == 0 {
		return nil, fmt.Errorf("failed to configure any detected tools")
	}
	return configured, nil
}

// hasProxyMode reports whether any of the given tool configs uses proxy mode.
func hasProxyMode(cfgs []ToolConfig) bool {
	for _, t := range cfgs {
		if t.Mode == "proxy" {
			return true
		}
	}
	return false
}
