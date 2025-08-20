package app

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/discovery"
	"github.com/stacklok/toolhive/pkg/auth/oauth"
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
	  --remote-auth-client-id my-client-id`,
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
		tokenSource, oauthConfig, err = handleOutgoingAuthentication(ctx)
		if err != nil {
			return fmt.Errorf("failed to authenticate to remote server: %w", err)
		}
		if oauthConfig != nil {
			introspectionURL = oauthConfig.IntrospectionEndpoint
			logger.Infof("Using OAuth config with introspection URL: %s", introspectionURL)
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

	// Add OAuth token injection middleware for outgoing requests if we have an access token
	if tokenSource != nil {
		tokenMiddleware := createTokenInjectionMiddleware(tokenSource)
		middlewares = append(middlewares, tokenMiddleware)
	}

	// Create the transparent proxy
	logger.Infof("Setting up transparent proxy to forward from host port %d to %s",
		port, proxyTargetURI)

	// Create the transparent proxy with middlewares
	proxy := transparent.NewTransparentProxy(
		proxyHost, port, serverName, proxyTargetURI,
		nil, authInfoHandler,
		false,
		true, // isRemote
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
func handleOutgoingAuthentication(ctx context.Context) (*oauth2.TokenSource, *oauth.Config, error) {
	// Resolve client secret from multiple sources
	clientSecret, err := resolveClientSecret()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve client secret: %w", err)
	}

	if remoteAuthFlags.EnableRemoteAuth {
		// If OAuth is explicitly enabled, validate configuration
		if remoteAuthFlags.RemoteAuthClientID == "" {
			return nil, nil, fmt.Errorf("remote-auth-client-id is required when remote authentication is enabled")
		}

		// Check if we have either OIDC issuer or manual OAuth endpoints
		hasOIDCConfig := remoteAuthFlags.RemoteAuthIssuer != ""
		hasManualConfig := remoteAuthFlags.RemoteAuthAuthorizeURL != "" && remoteAuthFlags.RemoteAuthTokenURL != ""

		if !hasOIDCConfig && !hasManualConfig {
			return nil, nil, fmt.Errorf("either --remote-auth-issuer (for OIDC) or both --remote-auth-authorize-url " +
				"and --remote-auth-token-url (for OAuth) are required")
		}

		if hasOIDCConfig && hasManualConfig {
			return nil, nil, fmt.Errorf("cannot specify both OIDC issuer and manual OAuth endpoints - choose one approach")
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
			return nil, nil, err
		}

		return result.TokenSource, result.Config, nil
	}

	// Try to detect authentication requirements from WWW-Authenticate header
	authInfo, err := discovery.DetectAuthenticationFromServer(ctx, proxyTargetURI, nil)
	if err != nil {
		logger.Debugf("Could not detect authentication from server: %v", err)
		return nil, nil, nil // Not an error, just no auth detected
	}

	if authInfo != nil {
		logger.Infof("Detected authentication requirement from server: %s", authInfo.Realm)

		if remoteAuthFlags.RemoteAuthClientID == "" {
			return nil, nil, fmt.Errorf("detected OAuth requirement but no remote-auth-client-id provided")
		}

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
			return nil, nil, err
		}

		return result.TokenSource, result.Config, nil
	}

	return nil, nil, nil // No authentication required
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
		// Clean the file path to prevent path traversal
		cleanPath := filepath.Clean(remoteAuthFlags.RemoteAuthClientSecretFile)
		logger.Debugf("Reading client secret from file: %s", cleanPath)
		// #nosec G304 - file path is cleaned above
		secretBytes, err := os.ReadFile(cleanPath)
		if err != nil {
			return "", fmt.Errorf("failed to read client secret file %s: %w", cleanPath, err)
		}
		secret := strings.TrimSpace(string(secretBytes))
		if secret == "" {
			return "", fmt.Errorf("client secret file %s is empty", cleanPath)
		}
		return secret, nil
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
