// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth/secrets"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/llm"
	pkgsecrets "github.com/stacklok/toolhive/pkg/secrets"
)

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

Run "thv llm setup" to get started.`,
	}

	cmd.AddCommand(newLLMConfigCommand())
	cmd.AddCommand(newLLMSetupCommand())
	cmd.AddCommand(newLLMTeardownCommand())
	cmd.AddCommand(newLLMProxyCommand())
	cmd.AddCommand(newLLMTokenCommand())

	return cmd
}

// ── config subcommand group ───────────────────────────────────────────────────

func newLLMConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage LLM gateway configuration",
		Long:  "The config command provides subcommands to manage LLM gateway connection settings.",
	}

	cmd.AddCommand(newLLMConfigSetCommand())
	cmd.AddCommand(newLLMConfigShowCommand())
	cmd.AddCommand(newLLMConfigResetCommand())

	return cmd
}

func newLLMConfigSetCommand() *cobra.Command {
	var (
		gatewayURL   string
		issuer       string
		clientID     string
		audience     string
		proxyPort    int
		callbackPort int
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
		RunE: func(_ *cobra.Command, _ []string) error {
			return config.UpdateConfig(func(c *config.Config) error {
				if gatewayURL != "" {
					c.LLM.GatewayURL = gatewayURL
				}
				if issuer != "" {
					c.LLM.OIDC.Issuer = issuer
				}
				if clientID != "" {
					c.LLM.OIDC.ClientID = clientID
				}
				if audience != "" {
					c.LLM.OIDC.Audience = audience
				}
				if proxyPort != 0 {
					c.LLM.Proxy.ListenPort = proxyPort
				}
				if callbackPort != 0 {
					c.LLM.OIDC.CallbackPort = callbackPort
				}
				return c.LLM.Validate()
			})
		},
	}

	cmd.Flags().StringVar(&gatewayURL, "gateway-url", "", "LLM gateway base URL (must use HTTPS)")
	cmd.Flags().StringVar(&issuer, "issuer", "", "OIDC issuer URL")
	cmd.Flags().StringVar(&clientID, "client-id", "", "OIDC client ID")
	cmd.Flags().StringVar(&audience, "audience", "", "OIDC audience (optional)")
	cmd.Flags().IntVar(&proxyPort, "proxy-port", 0, "Localhost proxy listen port (default 14000)")
	cmd.Flags().IntVar(&callbackPort, "callback-port", 0, "OIDC callback port (default: ephemeral)")

	return cmd
}

func newLLMConfigShowCommand() *cobra.Command {
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Display current LLM gateway configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			provider := config.NewDefaultProvider()
			llmCfg := provider.GetConfig().LLM

			if outputFormat == "json" {
				enc, err := json.MarshalIndent(llmCfg, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to encode config as JSON: %w", err)
				}
				fmt.Println(string(enc))
				return nil
			}

			if !llmCfg.IsConfigured() {
				fmt.Println("LLM gateway is not configured. Run \"thv llm config set\" to configure it.")
				return nil
			}

			fmt.Printf("Gateway URL:   %s\n", llmCfg.GatewayURL)
			fmt.Printf("OIDC Issuer:   %s\n", llmCfg.OIDC.Issuer)
			fmt.Printf("OIDC Client:   %s\n", llmCfg.OIDC.ClientID)
			if llmCfg.OIDC.Audience != "" {
				fmt.Printf("Audience:      %s\n", llmCfg.OIDC.Audience)
			}
			fmt.Printf("Proxy Port:    %d\n", llmCfg.EffectiveProxyPort())
			fmt.Printf("Scopes:        %v\n", llmCfg.OIDC.EffectiveScopes())
			if len(llmCfg.ConfiguredTools) > 0 {
				fmt.Println("Configured tools:")
				for _, t := range llmCfg.ConfiguredTools {
					fmt.Printf("  - %s (%s)  %s\n", t.Tool, t.Mode, t.ConfigPath)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFormat, "output", "o", "", "Output format (json)")

	return cmd
}

func newLLMConfigResetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reset",
		Short: "Clear all LLM gateway configuration and cached tokens",
		Long: `Remove all LLM gateway settings from config.yaml and delete cached OIDC
tokens from the secrets provider.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Delete cached tokens from the secrets provider first.
			if err := deleteLLMSecrets(cmd.Context()); err != nil {
				// Non-fatal: log and continue so the config is still cleared.
				fmt.Printf("Warning: could not remove cached LLM tokens: %v\n", err)
			}

			return config.UpdateConfig(func(c *config.Config) error {
				c.LLM = llm.LLMConfig{}
				return nil
			})
		},
	}
}

// deleteLLMSecrets removes all secrets stored under the LLM scope.
func deleteLLMSecrets(_ context.Context) error {
	provider, err := secrets.GetSystemSecretsProvider()
	if err != nil {
		return fmt.Errorf("failed to get secrets provider: %w", err)
	}
	scoped := pkgsecrets.NewScopedProvider(provider, pkgsecrets.ScopeLLM)
	return scoped.Cleanup()
}

// ── setup / teardown stubs ────────────────────────────────────────────────────

func newLLMSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Detect installed AI tools, configure them, and trigger OIDC login",
		Long: `Detect installed AI coding tools, configure each to use the LLM gateway,
start the background proxy for proxy-mode tools, and trigger an OIDC browser login.

Run "thv llm config set" first to set the gateway URL, issuer, and client ID.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented: coming in a future release")
		},
	}
}

func newLLMTeardownCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "teardown [tool-name]",
		Short: "Remove LLM gateway configuration from all tools and stop the proxy",
		Long: `Remove LLM gateway configuration from all configured AI tools and stop the
background proxy. Optionally target a single tool by name.

Use --purge-tokens to also delete cached OIDC tokens from the secrets provider.`,
		Args: cobra.MaximumNArgs(1),
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
		Short: "Start the LLM proxy in the foreground (for debugging)",
		Long: `Start the localhost reverse proxy in the foreground with full log output.
This is a debugging aid — use "thv llm setup" to start the proxy in the background.`,
		Args: cobra.NoArgs,
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
		RunE: func(_ *cobra.Command, _ []string) error {
			return fmt.Errorf("not implemented: coming in a future release")
		},
	}

	return cmd
}
