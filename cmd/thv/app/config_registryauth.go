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
	Use:   "set-registry-auth",
	Short: "Configure OAuth/OIDC authentication for the registry",
	Long: `Configure OAuth/OIDC authentication for the remote MCP server registry.
PKCE (S256) is always enforced for security.

The issuer URL is validated via OIDC discovery before saving.

Examples:
  thv config set-registry-auth --issuer https://auth.company.com --client-id toolhive-cli
  thv config set-registry-auth \
    --issuer https://auth.company.com --client-id toolhive-cli \
    --audience api://my-registry --scopes openid,profile`,
	RunE: setRegistryAuthCmdFunc,
}

var unsetRegistryAuthCmd = &cobra.Command{
	Use:   "unset-registry-auth",
	Short: "Remove registry authentication configuration",
	Long:  "Remove the OAuth/OIDC authentication configuration for the registry.",
	RunE:  unsetRegistryAuthCmdFunc,
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

func setRegistryAuthCmdFunc(_ *cobra.Command, _ []string) error {
	authManager := registry.NewAuthManager(config.NewDefaultProvider())

	if err := authManager.SetOAuthAuth(authIssuer, authClientID, authAudience, authScopes); err != nil {
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
