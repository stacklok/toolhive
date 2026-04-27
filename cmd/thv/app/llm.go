// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth/secrets"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/llm"
	llmproxy "github.com/stacklok/toolhive/pkg/llm/proxy"
	llmtools "github.com/stacklok/toolhive/pkg/llm/tools"
	pkgsecrets "github.com/stacklok/toolhive/pkg/secrets"
)

func newLLMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "llm",
		Hidden: true,
		Short:  "Manage LLM gateway authentication",
		Long: `Configure and manage authentication for OIDC-protected LLM gateways.

The llm command bridges AI coding tools to LLM gateways by handling OIDC
authentication transparently. Two modes are planned:

  Proxy mode    — a localhost reverse proxy injects fresh tokens for tools
                  that only accept static API keys (e.g. Cursor).
  Token helper  — "thv llm token" prints a fresh JWT suitable for use as
                  apiKeyHelper or auth.command in OIDC-capable tools
                  (e.g. Claude Code).

To configure the gateway connection settings, use:

  thv llm config set --gateway-url https://llm.example.com \
                     --issuer https://auth.example.com \
                     --client-id my-client-id

Use "thv llm config show" to view the current configuration.`,
	}

	cmd.AddCommand(newConfigCommand())
	cmd.AddCommand(newLLMSetupCommand())
	cmd.AddCommand(newLLMTeardownCommand())
	cmd.AddCommand(newLLMProxyCommand())
	cmd.AddCommand(newLLMTokenCommand())

	return cmd
}

// ── config subcommand group ───────────────────────────────────────────────────

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage LLM gateway configuration",
		Long:  "The config command provides subcommands to manage LLM gateway connection settings.",
	}

	cmd.AddCommand(newConfigSetCommand())
	cmd.AddCommand(newConfigShowCommand())
	cmd.AddCommand(newConfigResetCommand())

	return cmd
}

func newConfigSetCommand() *cobra.Command {
	var opts llm.SetOptions

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set LLM gateway connection settings",
		Long: `Persist LLM gateway connection settings to config.yaml.

Example:
  thv llm config set \
    --gateway-url https://llm.example.com \
    --issuer https://auth.example.com \
    --client-id my-client-id`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return config.UpdateConfig(func(c *config.Config) error {
				return c.LLM.SetFields(opts)
			})
		},
	}

	cmd.Flags().StringVar(&opts.GatewayURL, "gateway-url", "", "LLM gateway base URL (must use HTTPS)")
	cmd.Flags().StringVar(&opts.Issuer, "issuer", "", "OIDC issuer URL")
	cmd.Flags().StringVar(&opts.ClientID, "client-id", "", "OIDC client ID")
	cmd.Flags().StringVar(&opts.Audience, "audience", "", "OIDC audience (optional)")
	cmd.Flags().IntVar(&opts.ProxyPort, "proxy-port", 0, "Localhost proxy listen port (default 14000)")
	cmd.Flags().IntVar(&opts.CallbackPort, "callback-port", 0, "OIDC callback port (default: ephemeral)")

	return cmd
}

func newConfigShowCommand() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:     "show",
		Short:   "Display current LLM gateway configuration",
		Args:    cobra.NoArgs,
		PreRunE: ValidateFormat(&outputFormat, FormatJSON, FormatText),
		RunE: func(_ *cobra.Command, _ []string) error {
			provider := config.NewDefaultProvider()
			llmCfg := provider.GetConfig().LLM

			if outputFormat == FormatJSON {
				enc, err := json.MarshalIndent(llmCfg, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to encode config as JSON: %w", err)
				}
				fmt.Println(string(enc))
				return nil
			}

			return llmCfg.Show(os.Stdout)
		},
	}

	AddFormatFlag(cmd, &outputFormat, FormatJSON, FormatText)

	return cmd
}

func newConfigResetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Clear all LLM gateway configuration and cached tokens",
		Long: `Remove all LLM gateway settings from config.yaml and delete cached OIDC
tokens from the secrets provider.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Delete cached tokens from the secrets provider first.
			provider, err := secrets.GetSystemSecretsProvider()
			if err != nil {
				// Non-fatal: log and continue so the config is still cleared.
				fmt.Fprintf(os.Stderr, "Warning: could not get secrets provider: %v\n", err)
			} else if err := llm.DeleteCachedTokens(cmd.Context(), provider); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not remove cached LLM tokens: %v\n", err)
			}

			return config.UpdateConfig(func(c *config.Config) error {
				c.LLM = llm.Config{}
				return nil
			})
		},
	}
}

// runLLMToken prints a fresh LLM gateway access token to stdout.
// All diagnostic output goes to stderr so the caller can capture the token
// cleanly (e.g. apiKeyHelper or auth.command in Claude Code / Cursor).
func runLLMToken(ctx context.Context) error {
	provider := config.NewDefaultProvider()
	llmCfg := provider.GetConfig().LLM

	if !llmCfg.IsConfigured() {
		return fmt.Errorf("LLM gateway is not configured — run \"thv llm config set\" first")
	}

	ts, err := buildLLMTokenSource(&llmCfg, false /* non-interactive */)
	if err != nil {
		return err
	}
	token, err := ts.Token(ctx)
	if err != nil {
		return err
	}

	fmt.Println(token)
	return nil
}

// buildLLMTokenSource constructs the standard LLM token-source pipeline:
// system secrets provider → ScopeLLM scoped provider → config-persisting updater.
// This is the single place that wires ScopeLLM and the refresh-token persistence
// logic; runLLMToken, runLLMProxyForeground, and future callers all use it.
func buildLLMTokenSource(cfg *llm.Config, interactive bool) (*llm.TokenSource, error) {
	secretsProvider, err := secrets.GetSystemSecretsProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get secrets provider: %w", err)
	}
	scoped := pkgsecrets.NewScopedProvider(secretsProvider, pkgsecrets.ScopeLLM)

	updater := func(key string, expiry time.Time) {
		if updateErr := config.UpdateConfig(func(c *config.Config) error {
			c.LLM.OIDC.CachedRefreshTokenRef = key
			c.LLM.OIDC.CachedTokenExpiry = expiry
			return nil
		}); updateErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to persist LLM token reference: %v\n", updateErr)
		}
	}

	return llm.NewTokenSource(cfg, scoped, interactive, updater), nil
}

// ── setup / teardown ─────────────────────────────────────────────────────────

func newLLMSetupCommand() *cobra.Command {
	var opts llm.SetOptions

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure all detected AI tools to use the LLM gateway",
		Long: `Detect installed AI coding tools (Claude Code, Gemini CLI, Cursor, VS Code,
Xcode) and patch each tool's configuration to route through the LLM gateway.

Token-helper tools (Claude Code, Gemini CLI) are configured to call
"thv llm token" to obtain a fresh OIDC token on demand.

Proxy-mode tools (Cursor, VS Code, Xcode) are configured to send requests to
the localhost reverse proxy started by "thv llm proxy start".

Inline flags (--gateway-url, --issuer, --client-id, etc.) are merged into the
persisted configuration before setup runs, so you can combine "config set" and
"setup" in a single command.

Run "thv llm teardown" to revert all changes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLLMSetup(cmd, opts)
		},
	}

	cmd.Flags().StringVar(&opts.GatewayURL, "gateway-url", "", "LLM gateway base URL (must use HTTPS)")
	cmd.Flags().StringVar(&opts.Issuer, "issuer", "", "OIDC issuer URL")
	cmd.Flags().StringVar(&opts.ClientID, "client-id", "", "OIDC client ID")
	cmd.Flags().StringVar(&opts.Audience, "audience", "", "OIDC audience (optional)")
	cmd.Flags().IntVar(&opts.ProxyPort, "proxy-port", 0, "Localhost proxy listen port (default 14000)")
	cmd.Flags().IntVar(&opts.CallbackPort, "callback-port", 0, "OIDC callback port (default: ephemeral)")

	return cmd
}

func runLLMSetup(cmd *cobra.Command, opts llm.SetOptions) error {
	// If any inline flag was provided, persist it to config before reading.
	hasInlineFlags := opts.GatewayURL != "" || opts.Issuer != "" || opts.ClientID != "" ||
		opts.Audience != "" || opts.ProxyPort != 0 || opts.CallbackPort != 0
	if hasInlineFlags {
		if err := config.UpdateConfig(func(c *config.Config) error {
			return c.LLM.SetFields(opts)
		}); err != nil {
			return fmt.Errorf("persisting inline flags: %w", err)
		}
	}

	provider := config.NewDefaultProvider()
	llmCfg := provider.GetConfig().LLM

	if !llmCfg.IsConfigured() {
		return fmt.Errorf("LLM gateway is not configured — run \"thv llm config set\" first")
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving thv executable path: %w", err)
	}

	proxyBaseURL := fmt.Sprintf("http://localhost:%d/v1", llmCfg.EffectiveProxyPort())
	applyCfg := llmtools.ApplyConfig{
		GatewayURL:         llmCfg.GatewayURL,
		ProxyBaseURL:       proxyBaseURL,
		TokenHelperCommand: fmt.Sprintf(`"%s" llm token`, self),
	}

	detected := llmtools.Default().Detected()
	if len(detected) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No supported AI tools detected.")
		return nil
	}

	var configured []llm.ToolConfig
	for _, a := range detected {
		configPath, err := a.Apply(applyCfg)
		if err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to configure %s: %v\n", a.Name(), err)
			continue
		}
		configured = append(configured, llm.ToolConfig{
			Tool:       a.Name(),
			Mode:       a.Mode(),
			ConfigPath: configPath,
		})
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Configured %s (%s mode)  →  %s\n", a.Name(), a.Mode(), configPath)
	}

	if len(configured) == 0 {
		return fmt.Errorf("failed to configure any detected tools")
	}

	if err := config.UpdateConfig(func(c *config.Config) error {
		c.LLM.ConfiguredTools = mergeToolConfigs(c.LLM.ConfiguredTools, configured)
		return nil
	}); err != nil {
		// Roll back every adapter we successfully patched so the tool config
		// files are not left in a modified state without a persisted record of
		// what was changed (which would make teardown unable to revert them).
		reg := llmtools.Default()
		for _, tc := range configured {
			a := reg.Get(tc.Tool)
			if a == nil {
				continue
			}
			if revertErr := a.Revert(tc.ConfigPath); revertErr != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"Warning: rollback of %s failed: %v\n", tc.Tool, revertErr)
			}
		}
		return fmt.Errorf("persisting tool configuration: %w", err)
	}

	// Trigger OIDC browser login so the user is authenticated before using the tools.
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Opening browser for OIDC login…")
	ts, err := buildLLMTokenSource(&llmCfg, true /* interactive */)
	if err != nil {
		return fmt.Errorf("building token source: %w", err)
	}
	if _, err := ts.Token(cmd.Context()); err != nil {
		return fmt.Errorf("OIDC login failed: %w", err)
	}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Login successful.")

	// If any proxy-mode tool was configured, start the background proxy and
	// persist its PID so teardown can stop it.
	if hasProxyMode(configured) {
		pid, err := startBackgroundProxy(self)
		if err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not start LLM proxy: %v\n", err)
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Started LLM proxy (PID %d)\n", pid)
			if updateErr := config.UpdateConfig(func(c *config.Config) error {
				c.LLM.Proxy.PID = pid
				return nil
			}); updateErr != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not persist proxy PID: %v\n", updateErr)
			}
		}
	}

	return nil
}

// isTarget reports whether toolName appears in the targets slice.
func isTarget(targets []llm.ToolConfig, toolName string) bool {
	for _, t := range targets {
		if t.Tool == toolName {
			return true
		}
	}
	return false
}

// mergeToolConfigs merges newly configured tools into the existing list,
// replacing any entry with the same tool name.
func mergeToolConfigs(existing, incoming []llm.ToolConfig) []llm.ToolConfig {
	index := make(map[string]int, len(existing))
	result := make([]llm.ToolConfig, len(existing))
	copy(result, existing)
	for i, tc := range result {
		index[tc.Tool] = i
	}
	for _, tc := range incoming {
		if i, ok := index[tc.Tool]; ok {
			result[i] = tc
		} else {
			result = append(result, tc)
		}
	}
	return result
}

func newLLMTeardownCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teardown [tool-name]",
		Short: "Remove LLM gateway configuration from all (or one) configured tools",
		Long: `Revert the configuration changes made by "thv llm setup" for all configured
tools, or for a single tool when tool-name is provided.

Use --purge-tokens to also remove cached OIDC tokens from the secrets provider.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			purge, _ := cmd.Flags().GetBool("purge-tokens")
			return runLLMTeardown(cmd, args, purge)
		},
	}

	cmd.Flags().Bool("purge-tokens", false, "Also delete cached OIDC tokens from the secrets provider")

	return cmd
}

func runLLMTeardown(cmd *cobra.Command, args []string, purgeTokens bool) error {
	provider := config.NewDefaultProvider()
	llmCfg := provider.GetConfig().LLM

	var targets []llm.ToolConfig
	if len(args) == 1 {
		for _, tc := range llmCfg.ConfiguredTools {
			if tc.Tool == args[0] {
				targets = append(targets, tc)
				break
			}
		}
		if len(targets) == 0 {
			return fmt.Errorf("tool %q is not configured", args[0])
		}
	} else {
		targets = llmCfg.ConfiguredTools
	}

	if len(targets) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No tools are currently configured.")
		return nil
	}

	reg := llmtools.Default()
	var remaining []llm.ToolConfig
	for _, tc := range llmCfg.ConfiguredTools {
		if !isTarget(targets, tc.Tool) {
			remaining = append(remaining, tc)
			continue
		}
		a := reg.Get(tc.Tool)
		if a == nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: no adapter found for %q — skipping revert\n", tc.Tool)
			remaining = append(remaining, tc)
			continue
		}
		if err := a.Revert(tc.ConfigPath); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to revert %s: %v\n", tc.Tool, err)
			remaining = append(remaining, tc)
			continue
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Reverted %s  (%s)\n", tc.Tool, tc.ConfigPath)
	}

	// Stop the background proxy if one was started by setup.
	if llmCfg.Proxy.PID > 0 {
		if proc, err := os.FindProcess(llmCfg.Proxy.PID); err == nil {
			if err := proc.Signal(os.Interrupt); err != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not stop proxy (PID %d): %v\n", llmCfg.Proxy.PID, err)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Stopped LLM proxy (PID %d)\n", llmCfg.Proxy.PID)
			}
		}
	}

	if purgeTokens {
		secretsProvider, err := secrets.GetSystemSecretsProvider()
		if err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not get secrets provider: %v\n", err)
		} else if err := llm.DeleteCachedTokens(cmd.Context(), secretsProvider); err != nil {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not remove cached LLM tokens: %v\n", err)
		}
	}

	return config.UpdateConfig(func(c *config.Config) error {
		c.LLM.ConfiguredTools = remaining
		c.LLM.Proxy.PID = 0
		return nil
	})
}

// ── proxy subcommand group ────────────────────────────────────────────────────

func newLLMProxyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Manage the LLM gateway localhost proxy",
	}
	cmd.AddCommand(newLLMProxyStartCommand())
	return cmd
}

func newLLMProxyStartCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the LLM gateway localhost proxy",
		Long: `Start a localhost reverse proxy that injects fresh OIDC tokens for AI tools
that only accept static API keys (e.g. Cursor).

The proxy runs in the foreground and blocks until interrupted (Ctrl+C).
To run it in the background, use your shell or a process manager:

  thv llm proxy start &`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider := config.NewDefaultProvider()
			llmCfg := provider.GetConfig().LLM

			if !llmCfg.IsConfigured() {
				return fmt.Errorf("LLM gateway is not configured — run \"thv llm config set\" first")
			}
			if err := llmCfg.Validate(); err != nil {
				return fmt.Errorf("LLM gateway configuration is invalid: %w", err)
			}

			return runLLMProxyForeground(cmd.Context(), &llmCfg)
		},
	}
}

// runLLMProxyForeground builds a TokenSource and starts the proxy in this process.
func runLLMProxyForeground(ctx context.Context, llmCfg *llm.Config) error {
	ts, err := buildLLMTokenSource(llmCfg, true /* interactive: proxy is foreground, browser flow is acceptable */)
	if err != nil {
		return err
	}
	p, err := llmproxy.New(llmCfg, ts)
	if err != nil {
		return err
	}

	fmt.Printf("LLM proxy listening on http://%s/v1\n", p.Addr())
	return p.Start(ctx)
}

// ── token helper (hidden) ─────────────────────────────────────────────────────

func newLLMTokenCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "token",
		Hidden: true,
		Short:  "Print a fresh LLM gateway access token to stdout",
		Long: `Print a fresh OIDC access token to stdout (all other output on stderr).
Intended for use as apiKeyHelper or auth.command in OIDC-capable AI tools.
Runs non-interactively — will not launch a browser flow.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLLMToken(cmd.Context())
		},
	}

	return cmd
}

// ── helpers ───────────────────────────────────────────────────────────────────

// startBackgroundProxy spawns "thv llm proxy start" as a detached background
// process and returns its PID. The child outlives the parent process.
func startBackgroundProxy(self string) (int, error) {
	//nolint:gosec // self is the path to the current executable, not user input
	cmd := exec.Command(self, "llm", "proxy", "start")
	// Redirect output so the child does not inherit the parent terminal.
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("starting LLM proxy: %w", err)
	}
	return cmd.Process.Pid, nil
}

// hasProxyMode reports whether any of the given tool configs uses proxy mode.
func hasProxyMode(cfgs []llm.ToolConfig) bool {
	for _, t := range cfgs {
		if t.Mode == llmtools.ModeProxy {
			return true
		}
	}
	return false
}
