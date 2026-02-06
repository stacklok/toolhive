// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
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
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/middleware"
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

	// Header forwarding flags
	remoteForwardHeaders       []string
	remoteForwardHeadersSecret []string
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

	// Add header forwarding flags
	// Using StringArrayVar (not StringSliceVar) to avoid comma-splitting in header values
	proxyCmd.Flags().StringArrayVar(&remoteForwardHeaders, "remote-forward-headers", []string{},
		"Headers to inject into requests to remote server (format: Name=Value, can be repeated)")
	proxyCmd.Flags().StringArrayVar(&remoteForwardHeadersSecret, "remote-forward-headers-secret", []string{},
		"Headers with secret values from ToolHive secrets manager (format: Name=secret-name, can be repeated)")

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

	// Validate OAuth callback port availability
	if err := networking.ValidateCallbackPort(
		remoteAuthFlags.RemoteAuthCallbackPort,
		remoteAuthFlags.RemoteAuthClientID,
	); err != nil {
		return err
	}

	// Select a port for the HTTP proxy (host port)
	port, err := networking.FindOrUsePort(proxyPort)
	if err != nil {
		return err
	}
	logger.Debugf("Using host port: %d", port)

	// Handle OAuth authentication to the remote server if needed
	var tokenSource oauth2.TokenSource
	var oauthConfig *oauth.Config
	var introspectionURL string

	if shouldHandleOutgoingAuth() {
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
				logger.Debugf("Using OAuth config with introspection URL: %s", introspectionURL)
			}
		} else {
			logger.Debug("No OAuth configuration available, proceeding without outgoing authentication")
		}
	}

	// Create middlewares slice for incoming request authentication
	var middlewares []types.NamedMiddleware

	// Get OIDC configuration if enabled (for protecting the proxy endpoint)
	oidcConfig := getProxyOIDCConfig(cmd)

	// Get authentication middleware for incoming requests
	authMiddleware, authInfoHandler, err := auth.GetAuthenticationMiddleware(ctx, oidcConfig)
	if err != nil {
		return fmt.Errorf("failed to create authentication middleware: %w", err)
	}
	middlewares = append(middlewares, types.NamedMiddleware{
		Name:     "auth",
		Function: authMiddleware,
	})

	// Add OAuth token injection or token exchange middleware for outgoing requests
	if err := addExternalTokenMiddleware(&middlewares, tokenSource); err != nil {
		return err
	}

	// Add header forward middleware if headers are configured
	if err := addHeaderForwardMiddleware(
		&middlewares, remoteForwardHeaders, remoteForwardHeadersSecret,
	); err != nil {
		return err
	}

	// Create the transparent proxy
	logger.Debugf("Setting up transparent proxy to forward from host port %d to %s",
		port, proxyTargetURI)

	// Create the transparent proxy with middlewares
	proxy := transparent.NewTransparentProxy(
		proxyHost,
		port,
		proxyTargetURI,
		nil,
		authInfoHandler,
		nil, // prefixHandlers - not configured for proxy command
		false,
		false, // isRemote
		"",
		nil,   // onHealthCheckFailed - not needed for local proxies
		nil,   // onUnauthorizedResponse - not needed for local proxies
		"",    // endpointPrefix - not configured for proxy command
		false, // trustProxyHeaders - not configured for proxy command
		middlewares...)
	if err := proxy.Start(ctx); err != nil {
		return fmt.Errorf("failed to start proxy: %w", err)
	}

	fmt.Printf("Transparent proxy started for server %s on port %d -> %s\n",
		serverName, port, proxyTargetURI)

	<-ctx.Done()
	fmt.Println("Interrupt received, proxy is shutting down. Please wait for connections to close...")

	if err := proxy.CloseListener(); err != nil {
		logger.Warnf("Error closing proxy listener: %v", err)
	}
	// Use Background context for proxy shutdown. The parent context is already cancelled
	// at this point, so we need a fresh context with its own timeout to ensure the
	// shutdown operation completes successfully.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return proxy.Stop(shutdownCtx)
}

// getProxyOIDCConfig returns the OIDC token validator config from CLI flags, or nil if OIDC is not enabled.
func getProxyOIDCConfig(cmd *cobra.Command) *auth.TokenValidatorConfig {
	if !IsOIDCEnabled(cmd) {
		return nil
	}
	return &auth.TokenValidatorConfig{
		Issuer:           GetStringFlagOrEmpty(cmd, "oidc-issuer"),
		Audience:         GetStringFlagOrEmpty(cmd, "oidc-audience"),
		JWKSURL:          GetStringFlagOrEmpty(cmd, "oidc-jwks-url"),
		IntrospectionURL: GetStringFlagOrEmpty(cmd, "oidc-introspection-url"),
		ClientID:         GetStringFlagOrEmpty(cmd, "oidc-client-id"),
		ClientSecret:     GetStringFlagOrEmpty(cmd, "oidc-client-secret"),
		ResourceURL:      resourceURL,
	}
}

// shouldHandleOutgoingAuth determines if outgoing authentication should be attempted.
// This is true when:
// - Remote auth is explicitly enabled via --remote-auth flag
// - OAuth client ID is provided (allows auto-detection of auth requirements)
// - Bearer token is configured via flag, file, or environment variable
func shouldHandleOutgoingAuth() bool {
	return remoteAuthFlags.EnableRemoteAuth ||
		remoteAuthFlags.RemoteAuthClientID != "" ||
		remoteAuthFlags.RemoteAuthBearerToken != "" ||
		remoteAuthFlags.RemoteAuthBearerTokenFile != "" ||
		os.Getenv(remote.BearerTokenEnvVarName) != ""
}

// handleOutgoingAuthentication handles authentication to the remote MCP server
func handleOutgoingAuthentication(ctx context.Context) (*discovery.OAuthFlowResult, error) {
	bearerToken, err := resolveSecret(
		remoteAuthFlags.RemoteAuthBearerToken,
		remoteAuthFlags.RemoteAuthBearerTokenFile,
		remote.BearerTokenEnvVarName,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve bearer token: %w", err)
	}
	if bearerToken != "" {
		logger.Debug("Using bearer token authentication for remote server")
		return &discovery.OAuthFlowResult{
			TokenSource: remote.NewBearerTokenSource(bearerToken),
		}, nil
	}

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
		logger.Debugf("Detected authentication requirement from server: %s", authInfo.Realm)

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
	return resolveSecret(
		remoteAuthFlags.RemoteAuthClientSecret,
		remoteAuthFlags.RemoteAuthClientSecretFile,
		envOAuthClientSecret,
	)
}

// createTokenInjectionMiddleware creates a middleware that injects the OAuth token into requests
func createTokenInjectionMiddleware(tokenSource oauth2.TokenSource) types.MiddlewareFunction {
	return middleware.CreateTokenInjectionMiddleware(tokenSource)
}

// addExternalTokenMiddleware adds token exchange or token injection middleware to the middleware chain
func addExternalTokenMiddleware(middlewares *[]types.NamedMiddleware, tokenSource oauth2.TokenSource) error {
	if remoteAuthFlags.TokenExchangeURL != "" {
		// Use token exchange middleware when token exchange is configured
		tokenExchangeConfig, err := remoteAuthFlags.BuildTokenExchangeConfig()
		if err != nil {
			return fmt.Errorf("invalid token exchange configuration: %w", err)
		}
		if tokenExchangeConfig == nil {
			logger.Warn("Token exchange URL provided but configuration could not be built")
			return nil
		}

		var tokenExchangeMiddleware types.MiddlewareFunction
		if tokenSource != nil {
			// Create middleware using TokenSource - middleware handles token selection
			tokenExchangeMiddleware, err = tokenexchange.CreateMiddlewareFromTokenSource(*tokenExchangeConfig, tokenSource)
			if err != nil {
				return fmt.Errorf("failed to create token exchange middleware: %w", err)
			}
		} else {
			// Create middleware that extracts token from Authorization header
			tokenExchangeMiddleware, err = tokenexchange.CreateMiddlewareFromHeader(*tokenExchangeConfig)
			if err != nil {
				return fmt.Errorf("failed to create token exchange middleware: %w", err)
			}
		}
		*middlewares = append(*middlewares, types.NamedMiddleware{
			Name:     tokenexchange.MiddlewareType,
			Function: tokenExchangeMiddleware,
		})
	} else if tokenSource != nil {
		// Fallback to direct token injection when no token exchange is configured
		tokenMiddleware := createTokenInjectionMiddleware(tokenSource)
		*middlewares = append(*middlewares, types.NamedMiddleware{
			Name:     "token-injection",
			Function: tokenMiddleware,
		})
	}
	return nil
}

// addHeaderForwardMiddleware adds header forward middleware to the middleware chain if headers are configured.
// Secret references are resolved immediately via the secrets manager.
func addHeaderForwardMiddleware(
	middlewares *[]types.NamedMiddleware, headers []string, secretHeaders []string,
) error {
	// Parse plaintext headers from flags
	addHeaders, err := parseHeaderForwardFlags(headers)
	if err != nil {
		return fmt.Errorf("failed to parse header forward flags: %w", err)
	}

	// Resolve secret-backed headers
	if len(secretHeaders) > 0 {
		secretMap, err := parseHeaderSecretFlags(secretHeaders)
		if err != nil {
			return err
		}
		resolved, err := resolveHeaderSecrets(secretMap)
		if err != nil {
			return err
		}
		for name, value := range resolved {
			addHeaders[name] = value
		}
	}

	// Skip if no headers configured
	if len(addHeaders) == 0 {
		return nil
	}

	// Create the header forward middleware
	mwFunc, err := middleware.CreateHeaderForwardMiddleware(addHeaders)
	if err != nil {
		return fmt.Errorf("failed to create header forward middleware: %w", err)
	}
	*middlewares = append(*middlewares, types.NamedMiddleware{
		Name:     middleware.HeaderForwardMiddlewareName,
		Function: mwFunc,
	})

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
