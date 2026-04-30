// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth/secrets"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/llm"
	llmproxy "github.com/stacklok/toolhive/pkg/llm/proxy"
	"github.com/stacklok/toolhive/pkg/llmgateway"
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
	var (
		opts          llm.SetOptions
		tlsSkipVerify bool
	)

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
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("tls-skip-verify") {
				opts.TLSSkipVerify = &tlsSkipVerify
			}
			return config.UpdateConfig(func(c *config.Config) error {
				return c.LLM.SetFields(opts)
			})
		},
	}

	cmd.Flags().StringVar(&opts.GatewayURL, "gateway-url", "", "LLM gateway base URL (must use HTTPS)")
	cmd.Flags().StringVar(&opts.Issuer, "issuer", "", "OIDC issuer URL")
	cmd.Flags().StringVar(&opts.ClientID, "client-id", "", "OIDC client ID")
	cmd.Flags().StringVar(&opts.Audience, "audience", "", "OIDC audience (optional)")
	cmd.Flags().IntVar(&opts.ProxyPort, "proxy-port", 0, "Localhost proxy listen port (omit to keep current; default: 14000)")
	cmd.Flags().IntVar(&opts.CallbackPort, "callback-port", 0, "OIDC callback port (omit to keep current; default: ephemeral)")
	cmd.Flags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false,
		"Skip TLS certificate verification for the upstream gateway (local dev only; use --tls-skip-verify=false to clear)")

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
			if sp, err := secrets.GetSystemSecretsProvider(); err == nil {
				llm.PurgeTokens(cmd.Context(), cmd.ErrOrStderr(), sp)
			} else {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not get secrets provider: %v\n", err)
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
	var (
		opts          llm.SetOptions
		tlsSkipVerify bool
	)

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure all detected AI tools to use the LLM gateway",
		Long: `Detect installed AI coding tools (Claude Code, Gemini CLI, Cursor, VS Code,
Xcode) and patch each tool's configuration to route through the LLM gateway.

Token-helper tools (Claude Code, Gemini CLI) are configured to call
"thv llm token" to obtain a fresh OIDC token on demand.

Proxy-mode tools (Cursor, VS Code, Xcode) are configured to send requests to
the localhost reverse proxy started by "thv llm proxy start".

Inline flags (--gateway-url, --issuer, --client-id, etc.) are applied for this
run and persisted to config only after login and tool patching succeed. This
lets you combine "config set" and "setup" into a single command.

Run "thv llm teardown" to revert all changes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("tls-skip-verify") {
				opts.TLSSkipVerify = &tlsSkipVerify
			}
			cm, err := client.NewClientManager()
			if err != nil {
				return fmt.Errorf("initializing client manager: %w", err)
			}
			return runLLMSetup(
				cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				cm, config.NewDefaultProvider(), oidcLogin, opts,
			)
		},
	}

	cmd.Flags().StringVar(&opts.GatewayURL, "gateway-url", "", "LLM gateway base URL (must use HTTPS)")
	cmd.Flags().StringVar(&opts.Issuer, "issuer", "", "OIDC issuer URL")
	cmd.Flags().StringVar(&opts.ClientID, "client-id", "", "OIDC client ID")
	cmd.Flags().StringVar(&opts.Audience, "audience", "", "OIDC audience (optional)")
	cmd.Flags().IntVar(&opts.ProxyPort, "proxy-port", 0, "Localhost proxy listen port (omit to keep current; default: 14000)")
	cmd.Flags().IntVar(&opts.CallbackPort, "callback-port", 0, "OIDC callback port (omit to keep current; default: ephemeral)")
	cmd.Flags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false,
		"Skip TLS certificate verification for the upstream gateway (local dev only). "+
			"For direct-mode tools (Claude Code, Gemini CLI) this sets NODE_TLS_REJECT_UNAUTHORIZED=0, "+
			"disabling TLS for ALL of that tool's outbound connections. "+
			"For proxy-mode tools only the proxy-to-gateway connection is affected.")

	return cmd
}

func oidcLogin(ctx context.Context, cfg *llm.Config) error {
	ts, err := buildLLMTokenSource(cfg, true /* interactive */)
	if err != nil {
		return fmt.Errorf("building token source: %w", err)
	}
	_, err = ts.Token(ctx)
	return err
}

// runLLMSetup is a thin CLI wrapper: it adapts concrete CLI types to the
// interfaces expected by llm.Setup and delegates all orchestration there.
func runLLMSetup(
	ctx context.Context, out, errOut io.Writer,
	cm *client.ClientManager, provider config.Provider, login llm.LoginFunc,
	inlineOpts llm.SetOptions,
) error {
	return llm.Setup(ctx, out, errOut, &clientManagerAdapter{cm}, &configUpdaterAdapter{provider}, login, inlineOpts)
}

func newLLMTeardownCommand() *cobra.Command {
	var purgeTokens bool

	cmd := &cobra.Command{
		Use:   "teardown [tool-name]",
		Short: "Remove LLM gateway configuration from all (or one) configured tools",
		Long: `Revert the configuration changes made by "thv llm setup" for all configured
tools, or for a single tool when tool-name is provided.

Use --purge-tokens to also remove cached OIDC tokens from the secrets provider.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cm, err := client.NewClientManager()
			if err != nil {
				return fmt.Errorf("initializing client manager: %w", err)
			}
			return runLLMTeardown(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), cm, args, purgeTokens, config.NewDefaultProvider())
		},
	}

	cmd.Flags().BoolVar(&purgeTokens, "purge-tokens", false, "Also delete cached OIDC tokens from the secrets provider")

	return cmd
}

// runLLMTeardown is a thin CLI wrapper: it adapts concrete CLI types to the
// interfaces expected by llm.Teardown and delegates all orchestration there.
func runLLMTeardown(
	ctx context.Context,
	out, errOut io.Writer,
	cm *client.ClientManager,
	args []string,
	purgeTokens bool,
	provider config.Provider,
) error {
	var sp pkgsecrets.Provider
	if purgeTokens {
		var err error
		sp, err = secrets.GetSystemSecretsProvider()
		if err != nil {
			_, _ = fmt.Fprintf(errOut, "Warning: could not get secrets provider: %v\n", err)
		}
	}
	var targetTool string
	if len(args) == 1 {
		targetTool = args[0]
	}
	return llm.Teardown(ctx, out, errOut, &clientManagerAdapter{cm}, targetTool, purgeTokens, &configUpdaterAdapter{provider}, sp)
}

// ── CLI adapters ──────────────────────────────────────────────────────────────

// clientManagerAdapter adapts *client.ClientManager to llm.GatewayManager.
type clientManagerAdapter struct{ cm *client.ClientManager }

func (a *clientManagerAdapter) DetectedLLMGatewayClients() []string {
	apps := a.cm.DetectedLLMGatewayClients()
	result := make([]string, len(apps))
	for i, app := range apps {
		result[i] = string(app)
	}
	return result
}

func (a *clientManagerAdapter) ConfigureLLMGateway(clientType string, cfg llmgateway.ApplyConfig) (string, error) {
	return a.cm.ConfigureLLMGateway(client.ClientApp(clientType), cfg)
}

func (a *clientManagerAdapter) LLMGatewayModeFor(clientType string) string {
	return a.cm.LLMGatewayModeFor(client.ClientApp(clientType))
}

func (a *clientManagerAdapter) RevertLLMGateway(clientType, configPath string) error {
	return a.cm.RevertLLMGateway(client.ClientApp(clientType), configPath)
}

// configUpdaterAdapter adapts config.Provider to llm.ConfigUpdater.
type configUpdaterAdapter struct{ p config.Provider }

func (a *configUpdaterAdapter) GetLLMConfig() llm.Config {
	return a.p.GetConfig().LLM
}

func (a *configUpdaterAdapter) UpdateLLMConfig(fn func(*llm.Config) error) error {
	return a.p.UpdateConfig(func(c *config.Config) error {
		return fn(&c.LLM)
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
	var tlsSkipVerify bool

	cmd := &cobra.Command{
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

			// --tls-skip-verify overrides the stored config; if not provided, fall
			// back to whatever was persisted by "thv llm setup" or "config set".
			if cmd.Flags().Changed("tls-skip-verify") {
				llmCfg.TLSSkipVerify = tlsSkipVerify
			}

			return runLLMProxyForeground(cmd.Context(), &llmCfg)
		},
	}

	cmd.Flags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false,
		"Skip TLS certificate verification for the upstream gateway (overrides stored config; local dev only)")

	return cmd
}

// runLLMProxyForeground builds a TokenSource and starts the proxy in this process.
func runLLMProxyForeground(ctx context.Context, llmCfg *llm.Config) error {
	ts, err := buildLLMTokenSource(llmCfg, true /* interactive: proxy is foreground, browser flow is acceptable */)
	if err != nil {
		return err
	}
	p, err := llmproxy.New(llmCfg, ts, llmproxy.WithTLSSkipVerify(llmCfg.TLSSkipVerify))
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
