package app

import (
	"time"

	"github.com/spf13/cobra"
)

// RemoteAuthFlags holds the common remote authentication configuration
type RemoteAuthFlags struct {
	EnableRemoteAuth           bool
	RemoteAuthClientID         string
	RemoteAuthClientSecret     string
	RemoteAuthClientSecretFile string
	RemoteAuthScopes           []string
	RemoteAuthSkipBrowser      bool
	RemoteAuthTimeout          time.Duration
	RemoteAuthCallbackPort     int
	RemoteAuthIssuer           string
	RemoteAuthAuthorizeURL     string
	RemoteAuthTokenURL         string
}

// AddRemoteAuthFlags adds the common remote authentication flags to a command
func AddRemoteAuthFlags(cmd *cobra.Command, config *RemoteAuthFlags) {
	cmd.Flags().BoolVar(&config.EnableRemoteAuth, "remote-auth", false,
		"Enable OAuth/OIDC authentication to remote MCP server")
	cmd.Flags().StringVar(&config.RemoteAuthIssuer, "remote-auth-issuer", "",
		"OAuth/OIDC issuer URL for remote server authentication (e.g., https://accounts.google.com)")
	cmd.Flags().StringVar(&config.RemoteAuthClientID, "remote-auth-client-id", "",
		"OAuth client ID for remote server authentication")
	cmd.Flags().StringVar(&config.RemoteAuthClientSecret, "remote-auth-client-secret", "",
		"OAuth client secret for remote server authentication (optional for PKCE)")
	cmd.Flags().StringVar(&config.RemoteAuthClientSecretFile, "remote-auth-client-secret-file", "",
		"Path to file containing OAuth client secret (alternative to --remote-auth-client-secret)")
	cmd.Flags().StringSliceVar(&config.RemoteAuthScopes, "remote-auth-scopes", []string{},
		"OAuth scopes to request for remote server authentication (defaults: OIDC uses 'openid,profile,email')")
	cmd.Flags().BoolVar(&config.RemoteAuthSkipBrowser, "remote-auth-skip-browser", false,
		"Skip opening browser for remote server OAuth flow")
	cmd.Flags().DurationVar(&config.RemoteAuthTimeout, "remote-auth-timeout", 30*time.Second,
		"Timeout for OAuth authentication flow (e.g., 30s, 1m, 2m30s)")
	cmd.Flags().IntVar(&config.RemoteAuthCallbackPort, "remote-auth-callback-port", 8666,
		"Port for OAuth callback server during remote authentication (default: 8666)")
	cmd.Flags().StringVar(&config.RemoteAuthAuthorizeURL, "remote-auth-authorize-url", "",
		"OAuth authorization endpoint URL (alternative to --remote-auth-issuer for non-OIDC OAuth)")
	cmd.Flags().StringVar(&config.RemoteAuthTokenURL, "remote-auth-token-url", "",
		"OAuth token endpoint URL (alternative to --remote-auth-issuer for non-OIDC OAuth)")
}
