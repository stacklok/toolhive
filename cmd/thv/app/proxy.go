package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/proxy/transparent"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

var proxyCmd = &cobra.Command{
	Use:   "proxy [flags] SERVER_NAME",
	Short: "Spawn a transparent proxy for an MCP server",
	Long: `Spawn a transparent proxy that will redirect to an MCP server endpoint.
This command creates a standalone proxy without starting a container.`,
	Args: cobra.ExactArgs(1),
	RunE: proxyCmdFunc,
}

var (
	proxyHost      string
	proxyPort      int
	proxyTargetURI string

	// Remote server authentication flags
	remoteAuthIssuer       string
	remoteAuthClientID     string
	remoteAuthClientSecret string
	remoteAuthScopes       []string
	remoteAuthSkipBrowser  bool
	remoteAuthTimeout      time.Duration
	enableRemoteAuth       bool
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

	// Add remote server authentication flags
	proxyCmd.Flags().BoolVar(&enableRemoteAuth, "remote-auth", false, "Enable OAuth authentication to remote MCP server")
	proxyCmd.Flags().StringVar(&remoteAuthIssuer, "remote-auth-issuer", "",
		"OAuth/OIDC issuer URL for remote server authentication (e.g., https://accounts.google.com)")
	proxyCmd.Flags().StringVar(&remoteAuthClientID, "remote-auth-client-id", "",
		"OAuth client ID for remote server authentication")
	proxyCmd.Flags().StringVar(&remoteAuthClientSecret, "remote-auth-client-secret", "",
		"OAuth client secret for remote server authentication (optional for PKCE)")
	proxyCmd.Flags().StringSliceVar(&remoteAuthScopes, "remote-auth-scopes",
		[]string{"openid", "profile", "email"}, "OAuth scopes to request for remote server authentication")
	proxyCmd.Flags().BoolVar(&remoteAuthSkipBrowser, "remote-auth-skip-browser", false,
		"Skip opening browser for remote server OAuth flow")
	proxyCmd.Flags().DurationVar(&remoteAuthTimeout, "remote-auth-timeout", 30*time.Second,
		"Timeout for OAuth authentication flow (e.g., 30s, 1m, 2m30s)")

	// Mark target-uri as required
	if err := proxyCmd.MarkFlagRequired("target-uri"); err != nil {
		logger.Warnf("Warning: Failed to mark flag as required: %v", err)
	}
}

func proxyCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	// Get the server name
	serverName := args[0]

	// Validate the host flag and default resolving to IP in case hostname is provided
	validatedHost, err := ValidateAndNormaliseHostFlag(proxyHost)
	if err != nil {
		return fmt.Errorf("invalid host: %s", proxyHost)
	}
	proxyHost = validatedHost

	// Select a port for the HTTP proxy (host port)
	port, err := networking.FindOrUsePort(proxyPort)
	if err != nil {
		return err
	}
	logger.Infof("Using host port: %d", port)

	// Handle OAuth authentication to the remote server if needed
	var accessToken string
	if enableRemoteAuth || shouldDetectAuth() {
		token, err := handleOutgoingAuthentication(ctx)
		if err != nil {
			return fmt.Errorf("failed to authenticate to remote server: %w", err)
		}
		accessToken = token
	}

	// Create middlewares slice for incoming request authentication
	var middlewares []types.Middleware

	// Get OIDC configuration if enabled (for protecting the proxy endpoint)
	var oidcConfig *auth.JWTValidatorConfig
	if IsOIDCEnabled(cmd) {
		// Get OIDC flag values
		issuer := GetStringFlagOrEmpty(cmd, "oidc-issuer")
		audience := GetStringFlagOrEmpty(cmd, "oidc-audience")
		jwksURL := GetStringFlagOrEmpty(cmd, "oidc-jwks-url")
		clientID := GetStringFlagOrEmpty(cmd, "oidc-client-id")

		oidcConfig = &auth.JWTValidatorConfig{
			Issuer:   issuer,
			Audience: audience,
			JWKSURL:  jwksURL,
			ClientID: clientID,
		}
	}

	// Get authentication middleware for incoming requests
	authMiddleware, err := auth.GetAuthenticationMiddleware(ctx, oidcConfig)
	if err != nil {
		return fmt.Errorf("failed to create authentication middleware: %v", err)
	}
	middlewares = append(middlewares, authMiddleware)

	// Add OAuth token injection middleware for outgoing requests if we have an access token
	if accessToken != "" {
		tokenMiddleware := createTokenInjectionMiddleware(accessToken)
		middlewares = append(middlewares, tokenMiddleware)
	}

	// Create the transparent proxy
	logger.Infof("Setting up transparent proxy to forward from host port %d to %s",
		port, proxyTargetURI)

	// Create the transparent proxy with middlewares
	proxy := transparent.NewTransparentProxy(proxyHost, port, serverName, proxyTargetURI, middlewares...)
	if err := proxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start proxy: %v", err)
	}

	logger.Infof("Transparent proxy started for server %s on port %d -> %s",
		serverName, port, proxyTargetURI)
	logger.Info("Press Ctrl+C to stop")

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigCh
	logger.Infof("Received signal %s, stopping proxy...", sig)

	// Stop the proxy
	if err := proxy.Stop(ctx); err != nil {
		logger.Warnf("Warning: Failed to stop proxy: %v", err)
	}

	logger.Infof("Proxy for server %s stopped", serverName)
	return nil
}

// AuthInfo contains authentication information extracted from WWW-Authenticate header
type AuthInfo struct {
	Realm string
	Type  string
}

// detectAuthenticationFromServer attempts to detect authentication requirements from the target server
func detectAuthenticationFromServer(ctx context.Context, targetURI string) (*AuthInfo, error) {
	// Make a test request to the target server to see if it returns WWW-Authenticate
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURI, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	// Check if we got a 401 Unauthorized with WWW-Authenticate header
	if resp.StatusCode == http.StatusUnauthorized {
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		if wwwAuth != "" {
			return parseWWWAuthenticate(wwwAuth)
		}
	}

	return nil, nil
}

// parseWWWAuthenticate parses the WWW-Authenticate header to extract realm and type
func parseWWWAuthenticate(header string) (*AuthInfo, error) {
	// Example: Bearer realm="https://accounts.google.com"
	if !strings.HasPrefix(header, "Bearer ") {
		return nil, fmt.Errorf("unsupported authentication type: %s", header)
	}

	header = strings.TrimPrefix(header, "Bearer ")

	// Extract the realm from the header
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "realm=") {
			realm := strings.Trim(strings.TrimPrefix(part, "realm="), `"`)
			return &AuthInfo{
				Realm: realm,
				Type:  "Bearer",
			}, nil
		}
	}

	return nil, fmt.Errorf("no realm found in WWW-Authenticate header")
}

// performOAuthFlow performs the OAuth authentication flow
func performOAuthFlow(ctx context.Context, issuer, clientID, clientSecret string, scopes []string) (string, error) {
	logger.Info("Starting OAuth authentication flow...")

	// Create OAuth config from OIDC discovery
	oauthConfig, err := oauth.CreateOAuthConfigFromOIDC(
		ctx,
		issuer,
		clientID,
		clientSecret,
		scopes,
		true, // Enable PKCE by default for security
	)
	if err != nil {
		return "", fmt.Errorf("failed to create OAuth config: %w", err)
	}

	// Create OAuth flow
	flow, err := oauth.NewFlow(oauthConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create OAuth flow: %w", err)
	}

	// Create a context with timeout for the OAuth flow
	// Use the configured timeout, defaulting to 30 seconds if not set
	oauthTimeout := remoteAuthTimeout
	if oauthTimeout <= 0 {
		oauthTimeout = 30 * time.Second
	}

	oauthCtx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()

	// Start OAuth flow
	tokenResult, err := flow.Start(oauthCtx, remoteAuthSkipBrowser)
	if err != nil {
		if oauthCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("OAuth flow timed out after %v - user did not complete authentication", oauthTimeout)
		}
		return "", fmt.Errorf("OAuth flow failed: %w", err)
	}

	logger.Info("OAuth authentication successful")

	// Log token info (without exposing the actual token)
	if tokenResult.Claims != nil {
		if sub, ok := tokenResult.Claims["sub"].(string); ok {
			logger.Infof("Authenticated as subject: %s", sub)
		}
		if email, ok := tokenResult.Claims["email"].(string); ok {
			logger.Infof("Authenticated email: %s", email)
		}
	}

	return tokenResult.AccessToken, nil
}

// shouldDetectAuth determines if we should try to detect authentication requirements
func shouldDetectAuth() bool {
	// Only try to detect auth if OAuth client ID is provided
	// This prevents unnecessary requests when no OAuth config is available
	return remoteAuthClientID != ""
}

// handleOutgoingAuthentication handles authentication to the remote MCP server
func handleOutgoingAuthentication(ctx context.Context) (string, error) {
	if enableRemoteAuth {
		// If OAuth is explicitly enabled, use provided configuration
		if remoteAuthIssuer == "" {
			return "", fmt.Errorf("remote-auth-issuer is required when remote authentication is enabled")
		}
		if remoteAuthClientID == "" {
			return "", fmt.Errorf("remote-auth-client-id is required when remote authentication is enabled")
		}

		return performOAuthFlow(ctx, remoteAuthIssuer, remoteAuthClientID, remoteAuthClientSecret, remoteAuthScopes)
	}

	// Try to detect authentication requirements from WWW-Authenticate header
	authInfo, err := detectAuthenticationFromServer(ctx, proxyTargetURI)
	if err != nil {
		logger.Debugf("Could not detect authentication from server: %v", err)
		return "", nil // Not an error, just no auth detected
	}

	if authInfo != nil {
		logger.Infof("Detected authentication requirement from server: %s", authInfo.Realm)

		if remoteAuthClientID == "" {
			return "", fmt.Errorf("detected OAuth requirement but no remote-auth-client-id provided")
		}

		// Perform OAuth flow with discovered configuration
		return performOAuthFlow(ctx, authInfo.Realm, remoteAuthClientID, remoteAuthClientSecret, remoteAuthScopes)
	}

	return "", nil // No authentication required
}

// createTokenInjectionMiddleware creates a middleware that injects the OAuth token into requests
func createTokenInjectionMiddleware(accessToken string) types.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Add the Authorization header with the Bearer token
			r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
			next.ServeHTTP(w, r)
		})
	}
}
