package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/logger"
)

// AuthInfo contains authentication information extracted from WWW-Authenticate header
type AuthInfo struct {
	Realm            string
	Type             string
	ResourceMetadata string // For Stripe-style authentication
	Error            string
	ErrorDescription string
}

// RemoteAuthHandler handles authentication for remote MCP servers
type RemoteAuthHandler struct {
	config *RemoteAuthConfig
}

// NewRemoteAuthHandler creates a new remote authentication handler
func NewRemoteAuthHandler(config *RemoteAuthConfig) *RemoteAuthHandler {
	return &RemoteAuthHandler{
		config: config,
	}
}

// Authenticate is the main entry point for remote MCP server authentication
func (h *RemoteAuthHandler) Authenticate(ctx context.Context, remoteURL string) (*oauth2.TokenSource, error) {
	logger.Debugf("Authenticate called for remote URL: %s", remoteURL)
	logger.Debugf("RemoteAuthConfig - enableRemoteAuth: %v, hasBearerToken: %v",
		h.config.EnableRemoteAuth, h.config.BearerToken != "")

	if h.config != nil {
		logger.Debugf("OAuth configuration - clientID: %s, hasAuthorizeURL: %v, hasTokenURL: %v, hasIssuer: %v, scopes: %v",
			h.config.ClientID,
			h.config.AuthorizeURL != "",
			h.config.TokenURL != "",
			h.config.Issuer != "",
			h.config.Scopes)
	}

	// If we have a Bearer token configured, use it regardless of server authentication requirements
	if h.config != nil && h.config.BearerToken != "" {
		logger.Info("Using configured Bearer token for authentication")
		return h.createBearerTokenSource(h.config.BearerToken), nil
	}

	// First, try to detect if authentication is required
	authInfo, err := h.detectAuthenticationFromServer(ctx, remoteURL)
	if err != nil {
		logger.Debugf("Could not detect authentication from server: %v", err)
		return nil, nil // Not an error, just no auth detected
	}

	if authInfo != nil {
		logger.Infof("Detected authentication requirement from server - type: %s, realm: %s, resource_metadata: %s",
			authInfo.Type, authInfo.Realm, authInfo.ResourceMetadata)

		// Handle different authentication types
		switch authInfo.Type {
		case "Bearer":
			return h.handleBearerAuthentication(ctx, authInfo, remoteURL)
		case "Basic":
			return h.handleBasicAuthentication(ctx, authInfo)
		case "Digest":
			return h.handleDigestAuthentication(ctx, authInfo)
		default:
			logger.Infof("Unsupported authentication type: %s", authInfo.Type)
			return nil, nil
		}
	}

	return nil, nil // No authentication required
}

// detectAuthenticationFromServer attempts to detect authentication requirements from the target server
func (h *RemoteAuthHandler) detectAuthenticationFromServer(ctx context.Context, targetURI string) (*AuthInfo, error) {
	// Create a context with timeout for auth detection
	detectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Make a test request to the target server to see if it returns WWW-Authenticate
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}

	// First try a GET request
	req, err := http.NewRequestWithContext(detectCtx, http.MethodGet, targetURI, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create GET request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make GET request: %w", err)
	}
	defer resp.Body.Close()

	// Check if we got a 401 Unauthorized with WWW-Authenticate header
	if resp.StatusCode == http.StatusUnauthorized {
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		if wwwAuth != "" {
			return h.parseWWWAuthenticate(wwwAuth)
		}
	}

	// If no auth detected with GET, try a POST request with JSON-RPC initialize
	// Some servers (like Stripe) only return WWW-Authenticate on specific requests
	postBody := strings.NewReader(`{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}}`)
	postReq, err := http.NewRequestWithContext(detectCtx, http.MethodPost, targetURI, postBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create POST request: %w", err)
	}
	postReq.Header.Set("Content-Type", "application/json")

	postResp, err := client.Do(postReq)
	if err != nil {
		return nil, fmt.Errorf("failed to make POST request: %w", err)
	}
	defer postResp.Body.Close()

	// Check if we got a 401 Unauthorized with WWW-Authenticate header
	if postResp.StatusCode == http.StatusUnauthorized {
		wwwAuth := postResp.Header.Get("WWW-Authenticate")
		if wwwAuth != "" {
			return h.parseWWWAuthenticate(wwwAuth)
		}
	}

	return nil, nil // No authentication required
}

// parseWWWAuthenticate parses the WWW-Authenticate header to extract authentication information
// Supports multiple authentication schemes and complex header formats
func (h *RemoteAuthHandler) parseWWWAuthenticate(header string) (*AuthInfo, error) {
	// Trim whitespace and handle empty headers
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, fmt.Errorf("empty WWW-Authenticate header")
	}

	// Split by comma to handle multiple authentication schemes
	schemes := strings.Split(header, ",")

	for _, scheme := range schemes {
		scheme = strings.TrimSpace(scheme)

		// Check for Bearer authentication
		if strings.HasPrefix(scheme, "Bearer") {
			authInfo := &AuthInfo{Type: "Bearer"}

			// Extract parameters after "Bearer"
			params := strings.TrimSpace(strings.TrimPrefix(scheme, "Bearer"))
			if params == "" {
				// Simple "Bearer" without parameters
				return authInfo, nil
			}

			// Parse parameters (realm, scope, resource_metadata, error, etc.)
			authInfo.Realm = h.extractParameter(params, "realm")
			authInfo.ResourceMetadata = h.extractParameter(params, "resource_metadata")
			authInfo.Error = h.extractParameter(params, "error")
			authInfo.ErrorDescription = h.extractParameter(params, "error_description")

			return authInfo, nil
		}

		// Check for other authentication types (Basic, Digest, etc.)
		if strings.HasPrefix(scheme, "Basic") {
			return &AuthInfo{Type: "Basic"}, nil
		}

		if strings.HasPrefix(scheme, "Digest") {
			authInfo := &AuthInfo{Type: "Digest"}
			authInfo.Realm = h.extractParameter(scheme, "realm")
			return authInfo, nil
		}
	}

	return nil, fmt.Errorf("no supported authentication type found in header: %s", header)
}

// extractParameter extracts a parameter value from an authentication header
func (*RemoteAuthHandler) extractParameter(params, paramName string) string {
	// Look for paramName=value or paramName="value"
	parts := strings.Split(params, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, paramName+"=") {
			value := strings.TrimPrefix(part, paramName+"=")
			// Remove quotes if present
			value = strings.Trim(value, `"`)
			return value
		}
	}
	return ""
}

// handleBearerAuthentication handles Bearer token authentication
func (h *RemoteAuthHandler) handleBearerAuthentication(
	ctx context.Context, authInfo *AuthInfo, remoteURL string) (*oauth2.TokenSource, error) {
	// If we have a client ID configured, try OAuth flow
	if h.config != nil && h.config.ClientID != "" {
		logger.Info("Attempting OAuth authentication flow")

		// Check if we have manual OAuth endpoints from registry
		if h.config.AuthorizeURL != "" && h.config.TokenURL != "" {
			logger.Info("Using manual OAuth endpoints from registry, no issuer needed")
			return h.performOAuthFlow(ctx, "") // Empty issuer for manual endpoints
		}

		// Determine the issuer/realm for OAuth discovery
		issuer := authInfo.Realm
		if authInfo.ResourceMetadata != "" {
			// For Stripe-style authentication, use resource_metadata as the realm
			issuer = authInfo.ResourceMetadata
		}

		if issuer == "" {
			// If no realm or resource_metadata, try to derive from the remote URL
			issuer = h.deriveIssuerFromURL(remoteURL)
		}

		if issuer != "" {
			return h.performOAuthFlow(ctx, issuer)
		}
	}

	// If no OAuth configuration or issuer, return error
	return nil, fmt.Errorf("bearer authentication required but no OAuth configuration available")
}

// handleBasicAuthentication handles Basic authentication
func (*RemoteAuthHandler) handleBasicAuthentication(_ context.Context, _ *AuthInfo) (*oauth2.TokenSource, error) {
	// Basic authentication is not supported in this implementation
	// Could be extended to support username/password authentication
	return nil, fmt.Errorf("basic authentication not supported")
}

// handleDigestAuthentication handles Digest authentication
func (*RemoteAuthHandler) handleDigestAuthentication(_ context.Context, _ *AuthInfo) (*oauth2.TokenSource, error) {
	// Digest authentication is not supported in this implementation
	return nil, fmt.Errorf("digest authentication not supported")
}

// deriveIssuerFromURL attempts to derive the OAuth issuer from the remote URL using general patterns
func (*RemoteAuthHandler) deriveIssuerFromURL(remoteURL string) string {
	// Parse the URL to extract the domain
	parsedURL, err := url.Parse(remoteURL)
	if err != nil {
		logger.Debugf("Failed to parse remote URL: %v", err)
		return ""
	}

	host := parsedURL.Hostname()
	if host == "" {
		return ""
	}

	// General pattern: use the domain as the issuer
	// This works for most OAuth providers that use their domain as the issuer
	issuer := fmt.Sprintf("https://%s", host)

	logger.Debugf("Derived issuer from URL - remoteURL: %s, issuer: %s", remoteURL, issuer)
	return issuer
}

// performOAuthFlow performs the OAuth authentication flow
func (h *RemoteAuthHandler) performOAuthFlow(ctx context.Context, issuer string) (*oauth2.TokenSource, error) {
	logger.Infof("Starting OAuth authentication flow for issuer: %s", issuer)

	var oauthConfig *oauth.Config
	var err error

	// Check if we have manual OAuth endpoints configured from registry
	if h.config != nil && h.config.AuthorizeURL != "" && h.config.TokenURL != "" {
		logger.Infof("Using manual OAuth endpoints from registry - authorize_url: %s, token_url: %s",
			h.config.AuthorizeURL, h.config.TokenURL)

		oauthConfig, err = oauth.CreateOAuthConfigManual(
			h.config.ClientID,
			h.config.ClientSecret,
			h.config.AuthorizeURL,
			h.config.TokenURL,
			h.config.Scopes,
			true, // Enable PKCE
			h.config.CallbackPort,
			h.config.OAuthParams,
		)
	} else if h.config != nil && h.config.ClientSecret != "" {
		// Use OIDC discovery with client secret
		logger.Info("Using OIDC discovery with client secret")
		oauthConfig, err = oauth.CreateOAuthConfigFromOIDC(
			ctx,
			issuer,
			h.config.ClientID,
			h.config.ClientSecret,
			h.config.Scopes,
			true, // Enable PKCE by default for security
			h.config.CallbackPort,
		)
	} else {
		// Try OIDC discovery without client secret (PKCE flow)
		logger.Info("Using OIDC discovery with PKCE")
		oauthConfig, err = oauth.CreateOAuthConfigFromOIDC(
			ctx,
			issuer,
			h.config.ClientID,
			"", // No client secret for PKCE
			h.config.Scopes,
			true, // Enable PKCE
			h.config.CallbackPort,
		)
	}

	if err != nil {
		// If OIDC discovery fails, try fallback to known OAuth endpoints
		logger.Infof("OIDC discovery failed, trying fallback: %v", err)
		return h.performOAuthFlowFallback(ctx, issuer)
	}

	// Create OAuth flow
	flow, err := oauth.NewFlow(oauthConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth flow: %w", err)
	}

	// Create a context with timeout for the OAuth flow
	oauthTimeout := h.config.Timeout
	if oauthTimeout <= 0 {
		oauthTimeout = 5 * time.Minute
	}

	oauthCtx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()

	// Start OAuth flow
	_, err = flow.Start(oauthCtx, h.config.SkipBrowser)
	if err != nil {
		if oauthCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("OAuth flow timed out after %v - user did not complete authentication", oauthTimeout)
		}
		return nil, fmt.Errorf("OAuth flow failed: %w", err)
	}

	logger.Info("OAuth authentication successful")

	source := flow.TokenSource()
	return &source, nil
}

// performOAuthFlowFallback performs OAuth flow with common endpoint patterns
func (h *RemoteAuthHandler) performOAuthFlowFallback(ctx context.Context, issuer string) (*oauth2.TokenSource, error) {
	logger.Infof("Using fallback OAuth configuration for issuer: %s", issuer)

	// Try common OAuth endpoint patterns
	commonPatterns := []struct {
		name     string
		authURL  string
		tokenURL string
	}{
		{
			name:     "Standard OAuth endpoints",
			authURL:  issuer + "/oauth/authorize",
			tokenURL: issuer + "/oauth/token",
		},
		{
			name:     "OIDC endpoints",
			authURL:  issuer + "/connect",
			tokenURL: issuer + "/oauth/token",
		},
		{
			name:     "API OAuth endpoints",
			authURL:  issuer + "/connect",
			tokenURL: issuer + "/api/oauth/token",
		},
		{
			name:     "Identity endpoints",
			authURL:  issuer + "/connect",
			tokenURL: issuer + "/v1/identity/openidconnect/tokenservice",
		},
	}

	for _, pattern := range commonPatterns {
		logger.Debugf("Trying OAuth pattern: %s", pattern.name)

		oauthConfig, err := oauth.CreateOAuthConfigManual(
			h.config.ClientID,
			h.config.ClientSecret,
			pattern.authURL,
			pattern.tokenURL,
			h.config.Scopes,
			true, // Enable PKCE
			h.config.CallbackPort,
			h.config.OAuthParams,
		)

		if err != nil {
			logger.Debugf("Pattern failed - pattern: %s, error: %v", pattern.name, err)
			continue
		}

		// Try to create and start the OAuth flow
		flow, err := oauth.NewFlow(oauthConfig)
		if err != nil {
			logger.Debugf("Failed to create OAuth flow for pattern - pattern: %s, error: %v", pattern.name, err)
			continue
		}

		// Create a context with timeout for the OAuth flow
		oauthTimeout := h.config.Timeout
		if oauthTimeout <= 0 {
			oauthTimeout = 5 * time.Minute
		}

		oauthCtx, cancel := context.WithTimeout(ctx, oauthTimeout)
		defer cancel()

		// Start OAuth flow
		_, err = flow.Start(oauthCtx, h.config.SkipBrowser)
		if err != nil {
			if oauthCtx.Err() == context.DeadlineExceeded {
				return nil, fmt.Errorf("OAuth flow timed out after %v - user did not complete authentication", oauthTimeout)
			}
			logger.Debugf("OAuth flow failed for pattern - pattern: %s, error: %v", pattern.name, err)
			continue
		}

		logger.Infof("OAuth authentication successful using pattern: %s", pattern.name)
		source := flow.TokenSource()
		return &source, nil
	}

	return nil, fmt.Errorf("no working OAuth configuration found for issuer: %s. Please provide manual OAuth endpoints", issuer)
}

// createBearerTokenSource creates a static oauth2.TokenSource from a provided Bearer token
func (*RemoteAuthHandler) createBearerTokenSource(token string) *oauth2.TokenSource {
	logger.Debugf("Creating Bearer token source with token prefix: %s...", token[:10])

	// Create a static token source that always returns the same token
	staticToken := &oauth2.Token{
		AccessToken: token,
		TokenType:   "Bearer",
	}
	tokenSource := oauth2.StaticTokenSource(staticToken)
	logger.Debug("Bearer token source created successfully")
	return &tokenSource
}
