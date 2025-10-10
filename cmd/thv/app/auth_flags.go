package app

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/runner"
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

	// Token Exchange Configuration
	TokenExchangeURL              string
	TokenExchangeClientID         string
	TokenExchangeClientSecret     string
	TokenExchangeClientSecretFile string
	TokenExchangeAudience         string
	TokenExchangeScopes           []string
	TokenExchangeSubjectTokenType string
	TokenExchangeHeaderName       string
}

// BuildTokenExchangeConfig creates a TokenExchangeConfig from the RemoteAuthFlags
// Returns nil if TokenExchangeURL is empty (token exchange is not configured)
func (f *RemoteAuthFlags) BuildTokenExchangeConfig() *tokenexchange.Config {
	// Only create config if token exchange URL is provided
	if f.TokenExchangeURL == "" {
		return nil
	}

	// Resolve token exchange client secret from multiple sources
	clientSecret, err := resolveSecretFromSources(
		f.TokenExchangeClientSecret,
		f.TokenExchangeClientSecretFile,
		envTokenExchangeClientSecret,
		"token exchange client secret",
	)
	if err != nil {
		logger.Warnf("Failed to resolve token exchange client secret: %v", err)
		clientSecret = ""
	}

	// Determine header strategy based on whether custom header name is provided
	var headerStrategy string
	var externalTokenHeaderName string
	if f.TokenExchangeHeaderName != "" {
		headerStrategy = tokenexchange.HeaderStrategyCustom
		externalTokenHeaderName = f.TokenExchangeHeaderName
	} else {
		headerStrategy = tokenexchange.HeaderStrategyReplace
	}

	return &tokenexchange.Config{
		TokenURL:                f.TokenExchangeURL,
		ClientID:                f.TokenExchangeClientID,
		ClientSecret:            clientSecret,
		Audience:                f.TokenExchangeAudience,
		Scopes:                  f.TokenExchangeScopes,
		SubjectTokenType:        f.TokenExchangeSubjectTokenType,
		HeaderStrategy:          headerStrategy,
		ExternalTokenHeaderName: externalTokenHeaderName,
	}
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
	cmd.Flags().IntVar(&config.RemoteAuthCallbackPort, "remote-auth-callback-port", runner.DefaultCallbackPort,
		"Port for OAuth callback server during remote authentication")
	cmd.Flags().StringVar(&config.RemoteAuthAuthorizeURL, "remote-auth-authorize-url", "",
		"OAuth authorization endpoint URL (alternative to --remote-auth-issuer for non-OIDC OAuth)")
	cmd.Flags().StringVar(&config.RemoteAuthTokenURL, "remote-auth-token-url", "",
		"OAuth token endpoint URL (alternative to --remote-auth-issuer for non-OIDC OAuth)")

	// Token Exchange flags
	cmd.Flags().StringVar(&config.TokenExchangeURL, "token-exchange-url", "",
		"OAuth 2.0 token exchange endpoint URL (enables token exchange when provided)")
	cmd.Flags().StringVar(&config.TokenExchangeClientID, "token-exchange-client-id", "",
		"OAuth client ID for token exchange operations")
	cmd.Flags().StringVar(&config.TokenExchangeClientSecret, "token-exchange-client-secret", "",
		"OAuth client secret for token exchange operations")
	cmd.Flags().StringVar(&config.TokenExchangeClientSecretFile, "token-exchange-client-secret-file", "",
		"Path to file containing OAuth client secret for token exchange (alternative to --token-exchange-client-secret)")
	cmd.Flags().StringVar(&config.TokenExchangeAudience, "token-exchange-audience", "",
		"Target audience for exchanged tokens")
	cmd.Flags().StringSliceVar(&config.TokenExchangeScopes, "token-exchange-scopes", []string{},
		"Scopes to request for exchanged tokens")
	cmd.Flags().StringVar(&config.TokenExchangeSubjectTokenType, "token-exchange-subject-token-type", "",
		"Type of subject token to exchange (default: urn:ietf:params:oauth:token-type:access_token, Google STS requires: urn:ietf:params:oauth:token-type:id_token)")
	cmd.Flags().StringVar(&config.TokenExchangeHeaderName, "token-exchange-header-name", "",
		"Custom header name for injecting exchanged token (default: replaces Authorization header)")
}
