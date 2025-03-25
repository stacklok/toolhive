package main

import (
	"github.com/spf13/cobra"
)

// OIDC validation flags
var (
	proxyOIDCIssuer   string
	proxyOIDCAudience string
	proxyOIDCJWKSURL  string
	proxyOIDCClientID string
)

// AddOIDCFlags adds OIDC validation flags to the provided command.
func AddOIDCFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&proxyOIDCIssuer, "oidc-issuer", "", "OIDC issuer URL (e.g., https://accounts.google.com)")
	cmd.Flags().StringVar(&proxyOIDCAudience, "oidc-audience", "", "Expected audience for the token")
	cmd.Flags().StringVar(&proxyOIDCJWKSURL, "oidc-jwks-url", "", "URL to fetch the JWKS from")
	cmd.Flags().StringVar(&proxyOIDCClientID, "oidc-client-id", "", "OIDC client ID")
}
