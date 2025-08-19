// Package discovery provides authentication discovery utilities for detecting
// authentication requirements from remote servers.
package discovery

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
	ResourceMetadata string
	Error            string
	ErrorDescription string
}

// DiscoveryConfig holds configuration for authentication discovery
type DiscoveryConfig struct {
	Timeout               time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	EnablePOSTDetection   bool // Whether to try POST requests for detection
}

// DefaultDiscoveryConfig returns a default discovery configuration
func DefaultDiscoveryConfig() *DiscoveryConfig {
	return &DiscoveryConfig{
		Timeout:               10 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		EnablePOSTDetection:   true,
	}
}

// DetectAuthenticationFromServer attempts to detect authentication requirements from the target server
func DetectAuthenticationFromServer(ctx context.Context, targetURI string, config *DiscoveryConfig) (*AuthInfo, error) {
	if config == nil {
		config = DefaultDiscoveryConfig()
	}

	// Create a context with timeout for auth detection
	detectCtx, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()

	// Make a test request to the target server to see if it returns WWW-Authenticate
	client := &http.Client{
		Timeout: config.Timeout,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
			ResponseHeaderTimeout: config.ResponseHeaderTimeout,
		},
	}

	// First try a GET request
	authInfo, err := detectAuthWithRequest(detectCtx, client, targetURI, http.MethodGet, nil)
	if err != nil {
		return nil, err
	}
	if authInfo != nil {
		return authInfo, nil
	}

	// If no auth detected with GET and POST detection is enabled, try a POST request with JSON-RPC initialize
	// Some servers only return WWW-Authenticate on specific requests
	if config.EnablePOSTDetection {
		postBody := strings.NewReader(`{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}}`)
		authInfo, err = detectAuthWithRequest(detectCtx, client, targetURI, http.MethodPost, postBody)
		if err != nil {
			return nil, err
		}
		if authInfo != nil {
			return authInfo, nil
		}
	}

	return nil, nil // No authentication required
}

// detectAuthWithRequest makes a specific HTTP request and checks for authentication requirements
func detectAuthWithRequest(ctx context.Context, client *http.Client, targetURI, method string, body *strings.Reader) (*AuthInfo, error) {
	var req *http.Request
	var err error

	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, targetURI, body)
		if err != nil {
			return nil, fmt.Errorf("failed to create %s request: %w", method, err)
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequestWithContext(ctx, method, targetURI, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create %s request: %w", method, err)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make %s request: %w", method, err)
	}
	defer resp.Body.Close()

	// Check if we got a 401 Unauthorized with WWW-Authenticate header
	if resp.StatusCode == http.StatusUnauthorized {
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		if wwwAuth != "" {
			return ParseWWWAuthenticate(wwwAuth)
		}
	}

	return nil, nil
}

// ParseWWWAuthenticate parses the WWW-Authenticate header to extract authentication information
// Supports multiple authentication schemes and complex header formats
func ParseWWWAuthenticate(header string) (*AuthInfo, error) {
	// Trim whitespace and handle empty headers
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, fmt.Errorf("empty WWW-Authenticate header")
	}

	// Split by comma to handle multiple authentication schemes
	schemes := strings.Split(header, ",")

	for _, scheme := range schemes {
		scheme = strings.TrimSpace(scheme)

		// Check for OAuth/Bearer authentication
		if strings.HasPrefix(scheme, "Bearer") {
			authInfo := &AuthInfo{Type: "OAuth"}

			// Extract parameters after "Bearer"
			params := strings.TrimSpace(strings.TrimPrefix(scheme, "Bearer"))
			if params != "" {
				// Parse parameters (realm, scope, etc.)
				realm := ExtractParameter(params, "realm")
				if realm != "" {
					authInfo.Realm = realm
				}
			}

			return authInfo, nil
		}

		// Check for OAuth-specific schemes
		if strings.HasPrefix(scheme, "OAuth") {
			authInfo := &AuthInfo{Type: "OAuth"}

			// Extract parameters after "OAuth"
			params := strings.TrimSpace(strings.TrimPrefix(scheme, "OAuth"))
			if params != "" {
				// Parse parameters (realm, scope, etc.)
				realm := ExtractParameter(params, "realm")
				if realm != "" {
					authInfo.Realm = realm
				}
			}

			return authInfo, nil
		}

		// Currently only OAuth-based authentication is supported
		// Basic and Digest authentication are not implemented
		logger.Debugf("Unsupported authentication scheme: %s", scheme)
	}

	return nil, fmt.Errorf("no supported authentication type found in header: %s", header)
}

// ExtractParameter extracts a parameter value from an authentication header
func ExtractParameter(params, paramName string) string {
	// Look for paramName=value or paramName="value"
	// Split by comma first, then by space to handle multiple parameters
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

// DeriveIssuerFromURL attempts to derive the OAuth issuer from the remote URL using general patterns
func DeriveIssuerFromURL(remoteURL string) string {
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

// OAuthFlowConfig contains configuration for performing OAuth flows
type OAuthFlowConfig struct {
	ClientID     string
	ClientSecret string
	AuthorizeURL string // Manual OAuth endpoint (optional)
	TokenURL     string // Manual OAuth endpoint (optional)
	Scopes       []string
	CallbackPort int
	Timeout      time.Duration
	SkipBrowser  bool
	OAuthParams  map[string]string
}

// OAuthFlowResult contains the result of an OAuth flow
type OAuthFlowResult struct {
	TokenSource *oauth2.TokenSource
	Config      *oauth.Config
}

// PerformOAuthFlow performs an OAuth authentication flow with the given configuration
func PerformOAuthFlow(ctx context.Context, issuer string, config *OAuthFlowConfig) (*OAuthFlowResult, error) {
	logger.Infof("Starting OAuth authentication flow for issuer: %s", issuer)

	if config == nil {
		return nil, fmt.Errorf("OAuth flow config cannot be nil")
	}

	var oauthConfig *oauth.Config
	var err error

	// Check if we have manual OAuth endpoints configured
	if config.AuthorizeURL != "" && config.TokenURL != "" {
		logger.Infof("Using manual OAuth endpoints - authorize_url: %s, token_url: %s",
			config.AuthorizeURL, config.TokenURL)

		oauthConfig, err = oauth.CreateOAuthConfigManual(
			config.ClientID,
			config.ClientSecret,
			config.AuthorizeURL,
			config.TokenURL,
			config.Scopes,
			true, // Enable PKCE by default for security
			config.CallbackPort,
			config.OAuthParams,
		)
	} else {
		// Fall back to OIDC discovery
		logger.Info("Using OIDC discovery")
		oauthConfig, err = oauth.CreateOAuthConfigFromOIDC(
			ctx,
			issuer,
			config.ClientID,
			config.ClientSecret,
			config.Scopes,
			true, // Enable PKCE by default for security
			config.CallbackPort,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth config: %w", err)
	}

	// Create OAuth flow
	flow, err := oauth.NewFlow(oauthConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth flow: %w", err)
	}

	// Create a context with timeout for the OAuth flow
	oauthTimeout := config.Timeout
	if oauthTimeout <= 0 {
		oauthTimeout = 5 * time.Minute // Default timeout
	}

	oauthCtx, cancel := context.WithTimeout(ctx, oauthTimeout)
	defer cancel()

	// Start OAuth flow
	tokenResult, err := flow.Start(oauthCtx, config.SkipBrowser)
	if err != nil {
		if oauthCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("OAuth flow timed out after %v - user did not complete authentication", oauthTimeout)
		}
		return nil, fmt.Errorf("OAuth flow failed: %w", err)
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

	source := flow.TokenSource()
	return &OAuthFlowResult{
		TokenSource: &source,
		Config:      oauthConfig,
	}, nil
}
