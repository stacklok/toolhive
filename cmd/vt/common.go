package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/secrets"
)

// AddOIDCFlags adds OIDC validation flags to the provided command.
func AddOIDCFlags(cmd *cobra.Command) {
	cmd.Flags().String("oidc-issuer", "", "OIDC issuer URL (e.g., https://accounts.google.com)")
	cmd.Flags().String("oidc-audience", "", "Expected audience for the token")
	cmd.Flags().String("oidc-jwks-url", "", "URL to fetch the JWKS from")
	cmd.Flags().String("oidc-client-id", "", "OIDC client ID")
}

// GetStringFlagOrEmpty tries to get the string value of the given flag.
// If the flag doesn't exist or there's an error, it returns an empty string.
func GetStringFlagOrEmpty(cmd *cobra.Command, flagName string) string {
	value, err := cmd.Flags().GetString(flagName)
	if err != nil {
		return ""
	}
	return value
}

// IsOIDCEnabled returns true if OIDC validation is enabled for the given command.
// OIDC validation is considered enabled if either the OIDC issuer or the JWKS URL flag is provided.
func IsOIDCEnabled(cmd *cobra.Command) bool {
	jwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
	issuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")

	return jwksURL != "" || issuer != ""
}

// GetSecretsProviderType returns the secrets provider type from the command flags
func GetSecretsProviderType(cmd *cobra.Command) (secrets.ManagerType, error) {
	provider, err := cmd.Flags().GetString("secrets-provider")
	if err != nil {
		return "", fmt.Errorf("failed to get secrets-provider flag: %w", err)
	}

	switch provider {
	case string(secrets.BasicType):
		return secrets.BasicType, nil
	case string(secrets.EncryptedType):
		return secrets.EncryptedType, nil
	default:
		// TODO: auto-generate the set of valid values.
		return "", fmt.Errorf("invalid secrets provider type: %s (valid types: basic, encrypted)", provider)
	}
}
