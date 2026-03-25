// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/registry/auth"
)

var (
	authIssuer   string
	authClientID string
	authAudience string
	authScopes   []string
)

var setRegistryAuthCmd = &cobra.Command{
	Use:        "set-registry-auth",
	Short:      "Configure OIDC authentication for the registry",
	Deprecated: "use 'thv config set-registry' with --issuer and --client-id flags instead",
	Long: `Configure OIDC authentication for the remote MCP server registry.
PKCE (S256) is always enforced for security.

The issuer URL is validated via OIDC discovery before saving.

Examples:
  thv config set-registry-auth --issuer https://auth.company.com --client-id toolhive-cli
  thv config set-registry-auth \
    --issuer https://auth.company.com --client-id toolhive-cli \
    --audience api://my-registry --scopes profile`,
	RunE: setRegistryAuthCmdFunc,
}

var unsetRegistryAuthCmd = &cobra.Command{
	Use:   "unset-registry-auth",
	Short: "Remove registry authentication configuration",
	Deprecated: "use 'thv config unset-registry' to clear the registry configuration, or 'thv config set-registry' to" +
		" reconfigure the registry without auth flags",
	Long: "Remove the OIDC authentication configuration for the registry.",
	RunE: unsetRegistryAuthCmdFunc,
}

func init() {
	setRegistryAuthCmd.Flags().StringVar(&authIssuer, "issuer", "", "OIDC issuer URL (required)")
	setRegistryAuthCmd.Flags().StringVar(&authClientID, "client-id", "", "OAuth client ID (required)")
	setRegistryAuthCmd.Flags().StringVar(&authAudience, "audience", "", "OAuth audience parameter")
	setRegistryAuthCmd.Flags().StringSliceVar(
		&authScopes, "scopes", auth.DefaultOAuthScopes(), "OAuth scopes",
	)

	_ = setRegistryAuthCmd.MarkFlagRequired("issuer")
	_ = setRegistryAuthCmd.MarkFlagRequired("client-id")

	configCmd.AddCommand(setRegistryAuthCmd)
	configCmd.AddCommand(unsetRegistryAuthCmd)
}

func setRegistryAuthCmdFunc(cmd *cobra.Command, _ []string) error {
	provider := config.NewDefaultProvider()

	// Enforce the coupling invariant: auth requires a registry URL.
	cfg := provider.GetConfig()
	if cfg.RegistryApiUrl == "" && cfg.RegistryUrl == "" && cfg.LocalRegistryPath == "" {
		return fmt.Errorf("no registry URL is configured; use 'thv config set-registry' with --issuer and --client-id flags instead")
	}

	authManager := registry.NewAuthManager(provider)

	if err := authManager.SetOAuthAuth(cmd.Context(), authIssuer, authClientID, authAudience, authScopes); err != nil {
		return fmt.Errorf("failed to configure registry auth: %w", err)
	}

	return nil
}

func unsetRegistryAuthCmdFunc(_ *cobra.Command, _ []string) error {
	authManager := registry.NewAuthManager(config.NewDefaultProvider())

	if err := authManager.UnsetAuth(); err != nil {
		return fmt.Errorf("failed to remove registry auth: %w", err)
	}

	return nil
}
