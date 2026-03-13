// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry/auth"
	"github.com/stacklok/toolhive/pkg/secrets"
)

var (
	loginRegistry string
	loginIssuer   string
	loginClientID string
	loginAudience string
	loginScopes   []string
)

var registryLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the configured registry",
	Long: `Perform an interactive OAuth login against the configured registry.

If the registry URL or OAuth configuration (issuer, client-id) are not yet
saved in config, you can supply them as flags and they will be persisted
before the login flow begins.

Examples:
  thv registry login
  thv registry login --registry https://registry.example.com/api --issuer https://auth.example.com --client-id my-app`,
	RunE: registryLoginCmdFunc,
}

func init() {
	registryCmd.AddCommand(registryLoginCmd)

	registryLoginCmd.Flags().StringVar(&loginRegistry, "registry", "", "Registry URL to save if not already configured")
	registryLoginCmd.Flags().StringVar(&loginIssuer, "issuer", "", "OIDC issuer URL to save if OAuth is not configured")
	registryLoginCmd.Flags().StringVar(&loginClientID, "client-id", "", "OAuth client ID to save if OAuth is not configured")
	registryLoginCmd.Flags().StringVar(&loginAudience, "audience", "", "OAuth audience (optional)")
	registryLoginCmd.Flags().StringSliceVar(&loginScopes, "scopes", nil, "OAuth scopes (defaults to openid,offline_access)")
}

func registryLoginCmdFunc(cmd *cobra.Command, _ []string) error {
	configProvider := config.NewDefaultProvider()
	secretsProvider, err := newSecretsProvider(configProvider)
	if err != nil {
		return err
	}

	opts := auth.LoginOptions{
		RegistryURL: loginRegistry,
		Issuer:      loginIssuer,
		ClientID:    loginClientID,
		Audience:    loginAudience,
		Scopes:      loginScopes,
	}

	return auth.Login(cmd.Context(), configProvider, secretsProvider, opts)
}

// newSecretsProvider creates a secrets provider from the given config provider.
func newSecretsProvider(configProvider config.Provider) (secrets.Provider, error) {
	cfg, err := configProvider.LoadOrCreateConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	providerType, err := cfg.Secrets.GetProviderType()
	if err != nil {
		return nil, fmt.Errorf("getting secrets provider type: %w", err)
	}
	return secrets.CreateSecretProvider(providerType)
}
