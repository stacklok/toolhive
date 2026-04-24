// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth/secrets"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/llm"
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

			llmCfg.Show(os.Stdout)
			return nil
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

	secretsProvider, err := secrets.GetSystemSecretsProvider()
	if err != nil {
		return fmt.Errorf("failed to get secrets provider: %w", err)
	}
	scoped := pkgsecrets.NewScopedProvider(secretsProvider, pkgsecrets.ScopeLLM)

	updater := func(key string, expiry time.Time) {
		if err := config.UpdateConfig(func(c *config.Config) error {
			c.LLM.OIDC.CachedRefreshTokenRef = key
			c.LLM.OIDC.CachedTokenExpiry = expiry
			return nil
		}); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to persist LLM token reference: %v\n", err)
		}
	}

	ts := llm.NewTokenSource(&llmCfg, scoped, false /* non-interactive */, updater)
	token, err := ts.Token(ctx)
	if err != nil {
		return err
	}

	fmt.Println(token)
	return nil
}


// ── setup / teardown stubs ────────────────────────────────────────────────────

func newLLMSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Detect installed AI tools, configure them, and trigger OIDC login (coming soon)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented: coming in a future release")
		},
	}
}

func newLLMTeardownCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teardown [tool-name]",
		Short: "Remove LLM gateway configuration from all tools and stop the proxy (coming soon)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented: coming in a future release")
		},
	}

	cmd.Flags().Bool("purge-tokens", false, "Also delete cached OIDC tokens from the secrets provider")

	return cmd
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
		Short: "Start the LLM proxy in the foreground (coming soon)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented: coming in a future release")
		},
	}
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
