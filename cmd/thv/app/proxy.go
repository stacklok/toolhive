package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/discovery"
	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

var proxyCmd = &cobra.Command{
	Use:   "proxy [flags] SERVER_NAME",
	Short: "Create a transparent proxy for an MCP server with authentication support",
	Long: `Create a transparent HTTP proxy that forwards requests to an MCP server endpoint.

This command starts a standalone proxy without creating a workload, providing:

- Transparent request forwarding to the target MCP server
- Optional OAuth/OIDC authentication to remote MCP servers
- Automatic authentication detection via WWW-Authenticate headers
- OIDC-based access control for incoming proxy requests
- Secure credential handling via files or environment variables
- Dynamic client registration (RFC 7591) for automatic OAuth client setup

#### Authentication modes

The proxy supports multiple authentication scenarios:

1. No Authentication: Simple transparent forwarding
2. Outgoing Authentication: Authenticate to remote MCP servers using OAuth/OIDC
3. Incoming Authentication: Protect the proxy endpoint with OIDC validation
4. Bidirectional: Both incoming and outgoing authentication

#### OAuth client secret sources

OAuth client secrets can be provided via (in order of precedence):

1. --remote-auth-client-secret flag (not recommended for production)
2. --remote-auth-client-secret-file flag (secure file-based approach)
3. ` + envOAuthClientSecret + ` environment variable

#### Dynamic client registration

When no client credentials are provided, the proxy automatically registers an OAuth client
with the authorization server using RFC 7591 dynamic client registration:

- No need to pre-configure client ID and secret
- Automatically discovers registration endpoint via OIDC
- Supports PKCE flow for enhanced security

#### Examples

Basic transparent proxy:

	thv proxy my-server --target-uri http://localhost:8080

Proxy with OIDC authentication to remote server:

	thv proxy my-server --target-uri https://api.example.com \
	  --remote-auth --remote-auth-issuer https://auth.example.com \
	  --remote-auth-client-id my-client-id \
	  --remote-auth-client-secret-file /path/to/secret

Proxy with non-OIDC OAuth authentication to remote server:

	thv proxy my-server --target-uri https://api.example.com \
	  --remote-auth \
	  --remote-auth-authorize-url https://auth.example.com/oauth/authorize \
	  --remote-auth-token-url https://auth.example.com/oauth/token \
	  --remote-auth-client-id my-client-id \
	  --remote-auth-client-secret-file /path/to/secret

Proxy with OIDC protection for incoming requests:

	thv proxy my-server --target-uri http://localhost:8080 \
	  --oidc-issuer https://auth.example.com \
	  --oidc-audience my-audience

Auto-detect authentication requirements:

	thv proxy my-server --target-uri https://protected-api.com \
	  --remote-auth-client-id my-client-id

Dynamic client registration (automatic OAuth client setup):

	thv proxy my-server --target-uri https://protected-api.com \
	  --remote-auth --remote-auth-issuer https://auth.example.com`,
	Args: cobra.ExactArgs(1),
	RunE: proxyCmdFunc,
}

var (
	proxyHost      string
	proxyPort      int
	proxyTargetURI string

	resourceURL string // Explicit resource URL for OAuth discovery endpoint (RFC 9728)

	// Remote server authentication flags
	remoteAuthFlags RemoteAuthFlags
)

// Environment variable names
const (
	// #nosec G101 - this is an environment variable name, not a credential
	envOAuthClientSecret = "TOOLHIVE_REMOTE_OAUTH_CLIENT_SECRET"
)

func init() {
	proxyCmd.Flags().StringVar(&proxyHost, "host", transport.LocalhostIPv4, "Host for the HTTP proxy to listen on (IP or hostname)")
	proxyCmd.Flags().IntVar(&proxyPort, "port", 0, "Port for the HTTP proxy to listen on (host port)")
	proxyCmd.Flags().StringVar(
		&proxyTargetURI,
		"target-uri",
		"",
		"URI for the target MCP server (e.g., http://localhost:8080) (required)",
	)

	// Add OIDC validation flags
	AddOIDCFlags(proxyCmd)

	proxyCmd.Flags().StringVar(&resourceURL, "resource-url", "",
		"Explicit resource URL for OAuth discovery endpoint (RFC 9728)")

	// Add remote server authentication flags
	AddRemoteAuthFlags(proxyCmd, &remoteAuthFlags)

	// Mark target-uri as required
	if err := proxyCmd.MarkFlagRequired("target-uri"); err != nil {
		logger.Warnf("Warning: Failed to mark flag as required: %v", err)
	}
	// Attach the subcommands to the main proxy command
	proxyCmd.AddCommand(proxyTunnelCmd)
	proxyCmd.AddCommand(proxyStdioCmd)

}

func proxyCmdFunc(cmd *cobra.Command, args []string) error {
	ctx, stopSignal := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignal()
	// Get the server name
	serverName := args[0]

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(proxyHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", proxyHost)
	}
	proxyHost = validatedHost

	err = validateProxyTargetURI(proxyTargetURI)
	if err != nil {
		return fmt.Errorf("invalid target URI: %w", err)
	}

	// Select a port for the HTTP proxy (host port)
	port, err := networking.FindOrUsePort(proxyPort)
	if err != nil {
		return err
	}
	logger.Infof("Using host port: %d", port)

	// Handle OAuth authentication to the remote server if needed
	var tokenSource *oauth2.TokenSource
	var oauthConfig *oauth.Config
	var introspectionURL string

	if remoteAuthFlags.EnableRemoteAuth || shouldDetectAuth() {
		var result *discovery.OAuthFlowResult
		result, err = handleOutgoingAuthentication(ctx)
		if err != nil {
			return fmt.Errorf("failed to authenticate to remote server: %w", err)
		}
		if result != nil {
			tokenSource = result.TokenSource
			oauthConfig = result.Config

			if oauthConfig != nil {
				introspectionURL = oauthConfig.IntrospectionEndpoint
				logger.Infof("Using OAuth config with introspection URL: %s", introspectionURL)
			}
		} else {
			logger.Info("No OAuth configuration available, proceeding without outgoing authentication")
		}
	}

	// Create middlewares slice for incoming request authentication
	var middlewares []types.MiddlewareFunction

	// Get OIDC configuration if enabled (for protecting the proxy endpoint)
	var oidcConfig *auth.TokenValidatorConfig
	if IsOIDCEnabled(cmd) {
		// Get OIDC flag values
		issuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
		audience := GetStringFlagOrEmpty(cmd, "oidc-audience")
		jwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
		introspectionURL := GetStringFlagOrEmpty(cmd, "oidc-introspection-url")
		clientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")
		clientSecret := GetStringFlagOrEmpty(cmd, "oidc-client-secret")

		oidcConfig = &auth.TokenValidatorConfig{
			Issuer:           issuer,
			Audience:         audience,
			JWKSURL:          jwksURL,
			IntrospectionURL: introspectionURL,
			ClientID:         clientID,
			ClientSecret:     clientSecret,
			ResourceURL:      resourceURL,
		}
	}

	// Get authentication middleware for incoming requests
	authMiddleware, authInfoHandler, err := auth.GetAuthenticationMiddleware(ctx, oidcConfig)
	if err != nil {
		return fmt.Errorf("failed to create authentication middleware: %v", err)
	}
	middlewares = append(middlewares, authMiddleware)

	// Add OAuth token injection or token exchange middleware for outgoing requests
	if err := addExternalTokenMiddleware(&middlewares, tokenSource); err != nil {
		return err
	}

	// Create the transparent proxy
	logger.Infof("Setting up transparent proxy to forward from host port %d to %s",
		port, proxyTargetURI)

	// Create the transparent proxy with middlewares
	proxy := transparent.NewTransparentProxy(
		proxyHost, port, serverName, proxyTargetURI,
		nil, authInfoHandler,
		false,
		false, // isRemote
		"",
		middlewares...)
	if err := proxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start proxy: %v", err)
	}

	logger.Infof("Transparent proxy started for server %s on port %d -> %s",
		serverName, port, proxyTargetURI)
	logger.Info("Press Ctrl+C to stop")

	<-ctx.Done()
	logger.Infof("Interrupt received, proxy is shutting down. Please wait for connections to close...")

	if err := proxy.CloseListener(); err != nil {
		logger.Warnf("Error closing proxy listener: %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return proxy.Stop(shutdownCtx)
}

// shouldDetectAuth determines if we should try to detect authentication requirements
func shouldDetectAuth() bool {
	// Only try to detect auth if OAuth client ID is provided
	// This prevents unnecessary requests when no OAuth config is available
	return remoteAuthFlags.RemoteAuthClientID != ""
}

// handleOutgoingAuthentication handles authentication to the remote MCP server
func handleOutgoingAuthentication(ctx context.Context) (*discovery.OAuthFlowResult, error) {
	// Resolve client secret from multiple sources
	clientSecret, err := resolveClientSecret()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve client secret: %w", err)
	}

	if remoteAuthFlags.EnableRemoteAuth {

		// Check if we have either OIDC issuer or manual OAuth endpoints
		hasOIDCConfig := remoteAuthFlags.RemoteAuthIssuer != ""
		hasManualConfig := remoteAuthFlags.RemoteAuthAuthorizeURL != "" && remoteAuthFlags.RemoteAuthTokenURL != ""

		if !hasOIDCConfig && !hasManualConfig {
			return nil, fmt.Errorf("either --remote-auth-issuer (for OIDC) or both --remote-auth-authorize-url " +
				"and --remote-auth-token-url (for OAuth) are required")
		}

		if hasOIDCConfig && hasManualConfig {
			return nil, fmt.Errorf("cannot specify both OIDC issuer and manual OAuth endpoints - choose one approach")
		}

		flowConfig := &discovery.OAuthFlowConfig{
			ClientID:     remoteAuthFlags.RemoteAuthClientID,
			ClientSecret: clientSecret,
			AuthorizeURL: remoteAuthFlags.RemoteAuthAuthorizeURL,
			TokenURL:     remoteAuthFlags.RemoteAuthTokenURL,
			Scopes:       remoteAuthFlags.RemoteAuthScopes,
			CallbackPort: remoteAuthFlags.RemoteAuthCallbackPort,
			Timeout:      remoteAuthFlags.RemoteAuthTimeout,
			SkipBrowser:  remoteAuthFlags.RemoteAuthSkipBrowser,
		}

		result, err := discovery.PerformOAuthFlow(ctx, remoteAuthFlags.RemoteAuthIssuer, flowConfig)
		if err != nil {
			return nil, err
		}

		return result, nil
	}

	// Try to detect authentication requirements from WWW-Authenticate header
	authInfo, err := discovery.DetectAuthenticationFromServer(ctx, proxyTargetURI, nil)
	if err != nil {
		logger.Debugf("Could not detect authentication from server: %v", err)
		return nil, nil // Not an error, just no auth detected
	}

	if authInfo != nil {
		logger.Infof("Detected authentication requirement from server: %s", authInfo.Realm)

		// Perform OAuth flow with discovered configuration
		flowConfig := &discovery.OAuthFlowConfig{
			ClientID:     remoteAuthFlags.RemoteAuthClientID,
			ClientSecret: clientSecret,
			AuthorizeURL: remoteAuthFlags.RemoteAuthAuthorizeURL,
			TokenURL:     remoteAuthFlags.RemoteAuthTokenURL,
			Scopes:       remoteAuthFlags.RemoteAuthScopes,
			CallbackPort: remoteAuthFlags.RemoteAuthCallbackPort,
			Timeout:      remoteAuthFlags.RemoteAuthTimeout,
			SkipBrowser:  remoteAuthFlags.RemoteAuthSkipBrowser,
		}

		result, err := discovery.PerformOAuthFlow(ctx, authInfo.Realm, flowConfig)
		if err != nil {
			return nil, err
		}

		return result, nil
	}

	return nil, nil // No authentication required
}

// resolveClientSecret resolves the OAuth client secret from multiple sources
// Priority: 1. Flag value, 2. File, 3. Environment variable
func resolveClientSecret() (string, error) {
	// 1. Check if provided directly via flag
	if remoteAuthFlags.RemoteAuthClientSecret != "" {
		logger.Debug("Using client secret from command-line flag")
		return remoteAuthFlags.RemoteAuthClientSecret, nil
	}

	// 2. Check if provided via file
	if remoteAuthFlags.RemoteAuthClientSecretFile != "" {
		return readSecretFromFile(remoteAuthFlags.RemoteAuthClientSecretFile, "client secret")
	}

	// 3. Check environment variable
	if secret := os.Getenv(envOAuthClientSecret); secret != "" {
		logger.Debugf("Using client secret from %s environment variable", envOAuthClientSecret)
		return secret, nil
	}

	// No client secret found - this is acceptable for PKCE flows
	logger.Debug("No client secret provided - using PKCE flow")
	return "", nil
}

// createTokenInjectionMiddleware creates a middleware that injects the OAuth token into requests
func createTokenInjectionMiddleware(tokenSource *oauth2.TokenSource) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := (*tokenSource).Token()
			if err != nil {
				http.Error(w, "Unable to retrieve OAuth token", http.StatusUnauthorized)
				return
			}

			r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
			next.ServeHTTP(w, r)
		})
	}
}

// addExternalTokenMiddleware adds token exchange or token injection middleware to the middleware chain
func addExternalTokenMiddleware(middlewares *[]types.MiddlewareFunction, tokenSource *oauth2.TokenSource) error {
	if remoteAuthFlags.TokenExchangeURL != "" {
		// Use token exchange middleware when token exchange is configured
		tokenExchangeConfig, err := remoteAuthFlags.BuildTokenExchangeConfig()
		if err != nil {
			return fmt.Errorf("invalid token exchange configuration: %w", err)
		}
		if tokenExchangeConfig != nil && tokenSource != nil {
			// Create middleware using TokenSource - middleware handles token selection
			tokenExchangeMiddleware, err := tokenexchange.CreateMiddlewareFromTokenSource(*tokenExchangeConfig, *tokenSource)
			if err != nil {
				return fmt.Errorf("failed to create token exchange middleware: %v", err)
			}
			*middlewares = append(*middlewares, tokenExchangeMiddleware)
		}
	} else if tokenSource != nil {
		// Fallback to direct token injection when no token exchange is configured
		tokenMiddleware := createTokenInjectionMiddleware(tokenSource)
		*middlewares = append(*middlewares, tokenMiddleware)
	}
	return nil
}

// validateProxyTargetURI validates that the target URI for the proxy is valid and does not contain a path
func validateProxyTargetURI(targetURI string) error {
	// Parse the target URI
	targetURL, err := url.Parse(targetURI)
	if err != nil {
		return fmt.Errorf("invalid target URI: %w", err)
	}

	// Check if the path is empty or just "/"
	if targetURL.Path != "" && targetURL.Path != "/" {
		return fmt.Errorf("target URI should not contain a path, got: %s", proxyTargetURI)
	}

	return nil
}
