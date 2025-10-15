package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	// #nosec G101 - this is an environment variable name, not a credential
	envTokenExchangeClientSecret = "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET"
)

// readSecretFromFile reads a secret from a file, cleaning the path and trimming whitespace
func readSecretFromFile(filePath string) (string, error) {
	// Clean the file path to prevent path traversal
	cleanPath := filepath.Clean(filePath)
	logger.Debugf("Reading secret from file: %s", cleanPath)
	// #nosec G304 - file path is cleaned above
	secretBytes, err := os.ReadFile(cleanPath)
	if err != nil {
		return "", fmt.Errorf("failed to read secret file %s: %w", cleanPath, err)
	}
	secret := strings.TrimSpace(string(secretBytes))
	if secret == "" {
		return "", fmt.Errorf("secret file %s is empty", cleanPath)
	}
	return secret, nil
}

// resolveSecret resolves a secret from multiple sources following a standard priority order.
// Priority: 1. Flag value, 2. File, 3. Environment variable
// Returns empty string (not an error) if no secret is found - this is acceptable for public client/PKCE flows.
func resolveSecret(flagValue, filePath, envVarName string) (string, error) {
	// 1. Check if provided directly via flag
	if flagValue != "" {
		logger.Debug("Using secret from command-line flag")
		return flagValue, nil
	}

	// 2. Check if provided via file
	if filePath != "" {
		return readSecretFromFile(filePath)
	}

	// 3. Check environment variable
	if secret := os.Getenv(envVarName); secret != "" {
		logger.Debugf("Using secret from %s environment variable", envVarName)
		return secret, nil
	}

	// No secret found - this is acceptable for PKCE flows
	logger.Debug("No secret provided - using public client mode")
	return "", nil
}

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

// BuildTokenExchangeConfig creates a TokenExchangeConfig from the RemoteAuthFlags.
// Returns nil if TokenExchangeURL is empty (token exchange is not configured).
// Returns error if there is a configuration error (e.g., file read failure).
func (f *RemoteAuthFlags) BuildTokenExchangeConfig() (*tokenexchange.Config, error) {
	// Only create config if token exchange URL is provided
	if f.TokenExchangeURL == "" {
		return nil, nil
	}

	// Resolve token exchange client secret using the same mechanism as remote-auth-client-secret
	clientSecret, err := resolveSecret(
		f.TokenExchangeClientSecret,
		f.TokenExchangeClientSecretFile,
		envTokenExchangeClientSecret,
	)
	if err != nil {
		return nil, err
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

	// Normalize token type from user input (allows short forms like "access_token")
	normalizedTokenType := f.TokenExchangeSubjectTokenType
	if normalizedTokenType != "" {
		var err error
		normalizedTokenType, err = tokenexchange.NormalizeTokenType(normalizedTokenType)
		if err != nil {
			return nil, fmt.Errorf("invalid subject token type: %w", err)
		}
	}

	return &tokenexchange.Config{
		TokenURL:                f.TokenExchangeURL,
		ClientID:                f.TokenExchangeClientID,
		ClientSecret:            clientSecret,
		Audience:                f.TokenExchangeAudience,
		Scopes:                  f.TokenExchangeScopes,
		SubjectTokenType:        normalizedTokenType,
		HeaderStrategy:          headerStrategy,
		ExternalTokenHeaderName: externalTokenHeaderName,
	}, nil
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
		"Type of subject token to exchange. Accepts: access_token (default), id_token (required for Google STS)")
	cmd.Flags().StringVar(&config.TokenExchangeHeaderName, "token-exchange-header-name", "",
		"Custom header name for injecting exchanged token (default: replaces Authorization header)")
}
