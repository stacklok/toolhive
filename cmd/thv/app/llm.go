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

// skipBrowserFlagUsage is the shared help text for the --skip-browser flag on
// the login-capable llm subcommands (setup, token, proxy start).
const skipBrowserFlagUsage = "Print the OIDC authorization URL instead of opening a browser, then wait for the " +
	"callback. Use in headless/SSH/CI environments where no system browser is available."

func newLLMCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "llm",
		Short: "Manage LLM gateway authentication",
		Long: `Configure and manage authentication for OIDC-protected LLM gateways.

The llm command bridges AI coding tools to LLM gateways by handling OIDC
authentication transparently. Two modes are supported:

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

// addLLMConnectionFlags registers the gateway connection flags shared by
// "config set" and "setup" so the two commands stay in sync. It binds directly
// into the caller's SetOptions.
func addLLMConnectionFlags(cmd *cobra.Command, opts *llm.SetOptions) {
	cmd.Flags().StringVar(&opts.GatewayURL, "gateway-url", "", "LLM gateway base URL (must use HTTPS)")
	cmd.Flags().StringVar(&opts.Issuer, "issuer", "", "OIDC issuer URL")
	cmd.Flags().StringVar(&opts.ClientID, "client-id", "", "OIDC client ID")
	cmd.Flags().StringVar(&opts.Audience, "audience", "", "OIDC audience (optional)")
	cmd.Flags().IntVar(&opts.ProxyPort, "proxy-port", 0, "Localhost proxy listen port (omit to keep current; default: 14000)")
	cmd.Flags().IntVar(&opts.CallbackPort, "callback-port", 0, "OIDC callback port (omit to keep current; default: ephemeral)")
}

// applyChangedLLMFlags copies the *bool / []string flag values into opts only
// when the user actually set them, so an unset flag leaves the persisted config
// field unchanged (nil pointer = "not provided"). Shared by "config set" and
// "setup" so both commands treat these flags identically.
func applyChangedLLMFlags(
	cmd *cobra.Command, opts *llm.SetOptions, tlsSkipVerify, bedrockCompat, enable1M bool, models []string,
) {
	if cmd.Flags().Changed("tls-skip-verify") {
		opts.TLSSkipVerify = &tlsSkipVerify
	}
	if cmd.Flags().Changed("bedrock-compat") {
		opts.BedrockCompat = &bedrockCompat
	}
	if cmd.Flags().Changed("enable-1m") {
		opts.Enable1M = &enable1M
	}
	if cmd.Flags().Changed("models") {
		opts.Models = models
	}
}

func newConfigSetCommand() *cobra.Command {
	var (
		opts          llm.SetOptions
		tlsSkipVerify bool
		bedrockCompat bool
		enable1M      bool
		models        []string
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
			applyChangedLLMFlags(cmd, &opts, tlsSkipVerify, bedrockCompat, enable1M, models)
			return config.UpdateConfig(func(c *config.Config) error {
				return c.LLM.SetFields(opts)
			})
		},
	}

	addLLMConnectionFlags(cmd, &opts)
	cmd.Flags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false,
		"Skip TLS certificate verification for the upstream gateway (local dev only; use --tls-skip-verify=false to clear)")
	cmd.Flags().BoolVar(&bedrockCompat, "bedrock-compat", false,
		"Persist Bedrock compatibility for Claude Code (CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1 + per-tier "+
			"Bedrock model IDs). Applied by \"thv llm setup\". Use --bedrock-compat=false to clear.")
	cmd.Flags().BoolVar(&enable1M, "enable-1m", false,
		"With Bedrock compat, opt into the 1M context window by appending [1m] to opus/sonnet model IDs.")
	cmd.Flags().StringSliceVar(&models, "models", nil,
		"Model IDs to persist and apply during \"thv llm setup\", comma-separated or by repeating the flag, e.g. "+
			"--models=us.anthropic.claude-opus-4-8,us.anthropic.claude-sonnet-5. Credential-helper clients "+
			"(Claude Desktop) write these as inferenceModels; with Bedrock compat, each ID is also mapped to a "+
			"Claude Code tier by matching 'haiku', 'opus', or 'sonnet' in the ID.")

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
func runLLMToken(ctx context.Context, skipBrowser bool) error {
	provider := config.NewDefaultProvider()
	llmCfg := provider.GetConfig().LLM

	if !llmCfg.IsConfigured() {
		return fmt.Errorf("LLM gateway is not configured — run \"thv llm config set\" first")
	}

	// Interactive: on a genuine cache miss (no cached or refreshable token) this
	// launches the OIDC browser flow — the same flow "thv llm setup" runs — so a
	// prior "thv llm setup --lazy" signs the user in transparently on first use.
	// A cached or refreshable token is served without any browser prompt.
	ts, err := buildLLMTokenSource(&llmCfg, true /* interactive */, skipBrowser)
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
func buildLLMTokenSource(cfg *llm.Config, interactive, skipBrowser bool) (*llm.TokenSource, error) {
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

	return llm.NewTokenSource(cfg, scoped, interactive, skipBrowser, updater), nil
}

// ── setup / teardown ─────────────────────────────────────────────────────────

func newLLMSetupCommand() *cobra.Command {
	var (
		opts                llm.SetOptions
		tlsSkipVerify       bool
		bedrockCompat       bool
		enable1M            bool
		targetClient        string
		anthropicPathPrefix string
		lazy                bool
		skipBrowser         bool
		models              []string
	)

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure detected AI tools to use the LLM gateway",
		Long: `Detect installed AI tools (Claude Code, Gemini CLI, Cursor, VS Code, Xcode,
Claude Desktop, Codex) and patch each tool's configuration to route through the
LLM gateway.

Token-helper tools (Claude Code, Gemini CLI) are configured to call
"thv llm token" to obtain a fresh OIDC token on demand.

Claude Desktop is configured via its third-party inference credential helper,
which also calls "thv llm token". It reads its configuration only at launch, so
fully quit and relaunch it after setup. Pass --models to list the models it
should offer until the gateway serves model discovery itself.

Codex CLI and the ChatGPT desktop app use the same user configuration:
~/.codex/config.toml by default, or %USERPROFILE%\.codex\config.toml on Windows.
ToolHive adds a custom model_provider whose auth command invokes "thv llm token"
directly (no shell), keeping existing model_providers and mcp_servers entries
untouched. Desktop app detection is supported on macOS and Windows; the canonical
--client target remains "codex". If the desktop app is running, fully quit and
reopen it after setup.

Proxy-mode tools (Cursor, VS Code, Xcode) are configured to send requests to
the localhost reverse proxy started by "thv llm proxy start".

Use --client to configure only a single named tool instead of all detected
tools. An error is returned if the named client is not installed.

Inline flags (--gateway-url, --issuer, --client-id, etc.) are applied for this
run and persisted to config only after login and tool patching succeed. This
lets you combine "config set" and "setup" into a single command.

For a gateway that forwards to AWS Bedrock, add --bedrock-compat to configure
Claude Code with CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1 and per-tier Bedrock
model IDs (override with --models; add --enable-1m for the 1M context window on
opus/sonnet). The setting is persisted and only affects Claude Code. To add it to
an already-configured Claude Code, re-run:

  thv llm setup --client claude-code --bedrock-compat

Re-running is idempotent and uses the cached token (no browser prompt).

Run "thv llm teardown" to revert all changes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			applyChangedLLMFlags(cmd, &opts, tlsSkipVerify, bedrockCompat, enable1M, models)
			cm, err := client.NewClientManager()
			if err != nil {
				return fmt.Errorf("initializing client manager: %w", err)
			}
			login := func(ctx context.Context, cfg *llm.Config) error {
				return oidcLogin(ctx, cfg, skipBrowser)
			}
			return runLLMSetup(
				cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				cm, config.NewDefaultProvider(), login, opts,
				anthropicPathPrefix, cmd.Flags().Changed("anthropic-path-prefix"), targetClient, lazy,
			)
		},
	}

	addLLMConnectionFlags(cmd, &opts)
	cmd.Flags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false,
		"Skip TLS certificate verification for the upstream gateway (local dev only). "+
			"For direct-mode tools (Claude Code, Gemini CLI) this sets NODE_TLS_REJECT_UNAUTHORIZED=0, "+
			"disabling TLS for ALL of that tool's outbound connections. "+
			"For proxy-mode tools only the proxy-to-gateway connection is affected.")
	cmd.Flags().StringVar(&anthropicPathPrefix, "anthropic-path-prefix", "",
		"Path prefix appended to the gateway URL when writing ANTHROPIC_BASE_URL for direct-mode tools "+
			"(e.g. /anthropic). When omitted, the gateway is probed automatically.")
	cmd.Flags().StringVar(&targetClient, "client", "",
		"Configure only this AI tool by name (e.g. claude-code, cursor, codex). Omit to configure all detected tools.")
	cmd.Flags().BoolVar(&lazy, "lazy", false,
		"Skip the interactive OIDC login and defer it until the first time a configured tool "+
			"accesses the gateway. Tool config and persisted settings are written normally. "+
			"Useful for unattended provisioning (e.g. an MDM profile).")
	cmd.Flags().BoolVar(&skipBrowser, "skip-browser", false, skipBrowserFlagUsage)
	cmd.Flags().StringSliceVar(&models, "models", nil,
		"Model IDs to configure, comma-separated or by repeating the flag, e.g. "+
			"--models=us.anthropic.claude-opus-4-8,us.anthropic.claude-sonnet-5. "+
			"For credential-helper clients (Claude Desktop) these become inferenceModels. "+
			"With --bedrock-compat, each ID is also mapped to a Claude Code tier by matching "+
			"'haiku', 'opus', or 'sonnet' in the ID (IDs matching no tier are ignored with a "+
			"warning). Omit to use the built-in Bedrock defaults / gateway model discovery.")
	cmd.Flags().BoolVar(&bedrockCompat, "bedrock-compat", false,
		"Configure Claude Code for a gateway that forwards to AWS Bedrock: write "+
			"CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1 (Bedrock rejects the experimental anthropic-beta headers) "+
			"and pin per-tier Bedrock model IDs (override with --models). Persisted, so a later plain "+
			"\"thv llm setup\" keeps it; clear with --bedrock-compat=false. Only affects Claude Code.")
	cmd.Flags().BoolVar(&enable1M, "enable-1m", false,
		"With --bedrock-compat, append the [1m] suffix to the opus and sonnet model IDs to opt into the "+
			"1M-token context window on Bedrock (never haiku, which is 200K). Off by default.")

	return cmd
}

func oidcLogin(ctx context.Context, cfg *llm.Config, skipBrowser bool) error {
	ts, err := buildLLMTokenSource(cfg, true /* interactive */, skipBrowser)
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
	inlineOpts llm.SetOptions, anthropicPathPrefix string, anthropicPathPrefixSet bool, targetClient string,
	lazy bool,
) error {
	return llm.Setup(
		ctx, out, errOut,
		&clientManagerAdapter{cm}, &configUpdaterAdapter{provider}, login,
		inlineOpts, anthropicPathPrefix, anthropicPathPrefixSet, targetClient, lazy,
	)
}

func newLLMTeardownCommand() *cobra.Command {
	var (
		purgeTokens  bool
		targetClient string
	)

	cmd := &cobra.Command{
		Use:   "teardown [tool-name]",
		Short: "Remove LLM gateway configuration from all (or one) configured tools",
		Long: `Revert the configuration changes made by "thv llm setup" for all configured
tools, or for a single tool when tool-name is provided as a positional argument
or via --client.

Use --purge-tokens to also remove cached OIDC tokens from the secrets provider.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if targetClient != "" && len(args) > 0 {
				return fmt.Errorf("cannot use --client and a positional tool-name argument at the same time")
			}
			if targetClient != "" {
				args = []string{targetClient}
			}
			cm, err := client.NewClientManager()
			if err != nil {
				return fmt.Errorf("initializing client manager: %w", err)
			}
			return runLLMTeardown(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), cm, args, purgeTokens, config.NewDefaultProvider())
		},
	}

	cmd.Flags().BoolVar(&purgeTokens, "purge-tokens", false, "Also delete cached OIDC tokens from the secrets provider")
	cmd.Flags().StringVar(&targetClient, "client", "",
		"Remove configuration for only this AI tool by name (e.g. claude-code, cursor). Omit to revert all configured tools.")

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

func (a *clientManagerAdapter) IsManaged(clientType string) bool {
	return a.cm.IsManaged(client.ClientApp(clientType))
}

func (a *clientManagerAdapter) ConfigureEnvFile(clientType string, cfg llmgateway.ApplyConfig) (string, error) {
	return a.cm.ConfigureEnvFile(client.ClientApp(clientType), cfg)
}

func (a *clientManagerAdapter) RevertEnvFile(clientType, envFilePath string) error {
	return a.cm.RevertEnvFile(client.ClientApp(clientType), envFilePath)
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
	var (
		tlsSkipVerify bool
		skipBrowser   bool
	)

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

			return runLLMProxyForeground(cmd.Context(), &llmCfg, skipBrowser)
		},
	}

	cmd.Flags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false,
		"Skip TLS certificate verification for the upstream gateway (overrides stored config; local dev only)")
	cmd.Flags().BoolVar(&skipBrowser, "skip-browser", false, skipBrowserFlagUsage)

	return cmd
}

// runLLMProxyForeground builds a TokenSource and starts the proxy in this process.
func runLLMProxyForeground(ctx context.Context, llmCfg *llm.Config, skipBrowser bool) error {
	ts, err := buildLLMTokenSource(llmCfg, true /* interactive: proxy is foreground, browser flow is acceptable */, skipBrowser)
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

// ── token helper ──────────────────────────────────────────────────────────────

func newLLMTokenCommand() *cobra.Command {
	var skipBrowser bool

	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print a fresh LLM gateway access token to stdout",
		Long: `Print a fresh OIDC access token to stdout (all other output on stderr).
Intended for use as apiKeyHelper or auth.command in OIDC-capable AI tools.

A cached or refreshable token is printed without prompting. If none exists
(for example after "thv llm setup --lazy"), the OIDC browser login flow is
launched automatically and the resulting token is printed once login completes.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLLMToken(cmd.Context(), skipBrowser)
		},
	}

	cmd.Flags().BoolVar(&skipBrowser, "skip-browser", false, skipBrowserFlagUsage)

	return cmd
}
