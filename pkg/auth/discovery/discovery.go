// Package discovery provides authentication discovery utilities for detecting
// authentication requirements from remote servers.
//
// Supported Authentication Types:
// - OAuth 2.0 with PKCE (Proof Key for Code Exchange)
// - OIDC (OpenID Connect) discovery
// - Manual OAuth endpoint configuration
// - RFC 9728 Protected Resource Metadata
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
)

// Default timeout constants for authentication operations
const (
	DefaultOAuthTimeout      = 5 * time.Minute
	DefaultHTTPTimeout       = 30 * time.Second
	DefaultAuthDetectTimeout = 10 * time.Second
	MaxRetryAttempts         = 3
	RetryBaseDelay           = 2 * time.Second
)

// AuthInfo contains authentication information extracted from WWW-Authenticate header
type AuthInfo struct {
	Realm            string
	Type             string
	ResourceMetadata string
	Error            string
	ErrorDescription string
}

// AuthServerInfo contains information about a validated authorization server
type AuthServerInfo struct {
	Issuer               string
	AuthorizationURL     string
	TokenURL             string
	RegistrationEndpoint string
}

// Config holds configuration for authentication discovery
type Config struct {
	Timeout               time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	EnablePOSTDetection   bool // Whether to try POST requests for detection
}

// DefaultDiscoveryConfig returns a default discovery configuration
func DefaultDiscoveryConfig() *Config {
	return &Config{
		Timeout:               DefaultAuthDetectTimeout,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		EnablePOSTDetection:   true,
	}
}

// DetectAuthenticationFromServer attempts to detect authentication requirements from the target server
func DetectAuthenticationFromServer(ctx context.Context, targetURI string, config *Config) (*AuthInfo, error) {
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
func detectAuthWithRequest(
	ctx context.Context,
	client *http.Client,
	targetURI string,
	method string,
	body *strings.Reader,
) (*AuthInfo, error) {
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

	// Check for OAuth/Bearer authentication
	// Note: We don't split by comma because Bearer parameters can contain commas in quoted values
	if strings.HasPrefix(header, "Bearer") {
		authInfo := &AuthInfo{Type: "OAuth"}

		// Extract parameters after "Bearer"
		params := strings.TrimSpace(strings.TrimPrefix(header, "Bearer"))
		if params != "" {
			// Parse parameters (realm, scope, resource_metadata, etc.)
			realm := ExtractParameter(params, "realm")
			if realm != "" {
				authInfo.Realm = realm
			}

			// RFC 9728: Check for resource_metadata parameter
			resourceMetadata := ExtractParameter(params, "resource_metadata")
			if resourceMetadata != "" {
				authInfo.ResourceMetadata = resourceMetadata
			}

			// Extract error information if present
			errorParam := ExtractParameter(params, "error")
			if errorParam != "" {
				authInfo.Error = errorParam
			}

			errorDesc := ExtractParameter(params, "error_description")
			if errorDesc != "" {
				authInfo.ErrorDescription = errorDesc
			}
		}

		return authInfo, nil
	}

	// Check for OAuth-specific schemes
	if strings.HasPrefix(header, "OAuth") {
		authInfo := &AuthInfo{Type: "OAuth"}

		// Extract parameters after "OAuth"
		params := strings.TrimSpace(strings.TrimPrefix(header, "OAuth"))
		if params != "" {
			// Parse parameters (realm, scope, etc.)
			realm := ExtractParameter(params, "realm")
			if realm != "" {
				authInfo.Realm = realm
			}

			// RFC 9728: Check for resource_metadata parameter
			resourceMetadata := ExtractParameter(params, "resource_metadata")
			if resourceMetadata != "" {
				authInfo.ResourceMetadata = resourceMetadata
			}
		}

		return authInfo, nil
	}

	// Currently only OAuth-based authentication is supported
	// Basic and Digest authentication are not implemented
	if strings.HasPrefix(header, "Basic") || strings.HasPrefix(header, "Digest") {
		logger.Debugf("Unsupported authentication scheme: %s", header)
		return nil, fmt.Errorf("unsupported authentication scheme: %s", strings.Split(header, " ")[0])
	}

	return nil, fmt.Errorf("no supported authentication type found in header: %s", header)
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

	// Append port if explicitly present in the original URL
	port := parsedURL.Port()
	if port != "" {
		host = fmt.Sprintf("%s:%s", host, port)
	}

	// For localhost, preserve the original scheme (HTTP or HTTPS)
	// This supports local development and testing scenarios
	scheme := networking.HttpsScheme
	if networking.IsLocalhost(host) && parsedURL.Scheme != "" {
		scheme = parsedURL.Scheme
	}

	// General pattern: use the domain as the issuer
	// This works for most OAuth providers that use their domain as the issuer
	issuer := fmt.Sprintf("%s://%s", scheme, host)

	logger.Debugf("Derived issuer from URL - remoteURL: %s, issuer: %s", remoteURL, issuer)
	return issuer
}

// ExtractParameter extracts a parameter value from an authentication header
// Handles both quoted and unquoted values according to RFC 2617 and RFC 6750
func ExtractParameter(params, paramName string) string {
	// Parameters can be separated by comma or space
	// Handle both paramName=value and paramName="value" formats

	// First try to find the parameter with equals sign
	searchStr := paramName + "="
	idx := strings.Index(params, searchStr)
	if idx == -1 {
		return ""
	}

	// Extract the value after the equals sign
	valueStart := idx + len(searchStr)
	if valueStart >= len(params) {
		return ""
	}

	remainder := params[valueStart:]

	// Check if the value is quoted
	if strings.HasPrefix(remainder, `"`) {
		// Find the closing quote
		endIdx := 1
		for endIdx < len(remainder) {
			if remainder[endIdx] == '"' && (endIdx == 1 || remainder[endIdx-1] != '\\') {
				// Found unescaped closing quote
				value := remainder[1:endIdx]
				// Unescape any escaped quotes
				value = strings.ReplaceAll(value, `\"`, `"`)
				return value
			}
			endIdx++
		}
		// No closing quote found, return empty
		return ""
	}

	// Unquoted value - find the end (comma, space, or end of string)
	endIdx := 0
	for endIdx < len(remainder) {
		if remainder[endIdx] == ',' || remainder[endIdx] == ' ' {
			break
		}
		endIdx++
	}

	return strings.TrimSpace(remainder[:endIdx])
}

// DeriveIssuerFromRealm attempts to derive the OAuth issuer from the realm parameter
// According to RFC 8414, the issuer MUST be a URL using the "https" scheme with no query or fragment
func DeriveIssuerFromRealm(realm string) string {
	if realm == "" {
		return ""
	}

	// Check if realm is already a valid HTTPS URL
	parsedURL, err := url.Parse(realm)
	if err != nil {
		logger.Debugf("Realm is not a valid URL: %v", err)
		return ""
	}

	// RFC 8414: The issuer identifier MUST be a URL using the "https" scheme
	// with no query or fragment components
	if parsedURL.Scheme != "https" && !networking.IsLocalhost(parsedURL.Host) {
		logger.Debugf("Realm is not using HTTPS scheme: %s", realm)
		return ""
	}

	// Normalize the path to prevent path traversal attacks
	if parsedURL.Path != "" {
		// Clean the path to resolve . and .. elements
		cleanPath := path.Clean(parsedURL.Path)
		// Ensure the path doesn't escape the root
		if !strings.HasPrefix(cleanPath, "/") {
			cleanPath = "/" + cleanPath
		}
		parsedURL.Path = cleanPath
	}

	if parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		logger.Debugf("Realm contains query or fragment components: %s", realm)
		// Remove query and fragment to make it a valid issuer
		parsedURL.RawQuery = ""
		parsedURL.Fragment = ""
	}

	issuer := parsedURL.String()
	logger.Debugf("Derived issuer from realm - realm: %s, issuer: %s", realm, issuer)
	return issuer
}

// OAuthFlowConfig contains configuration for performing OAuth flows
type OAuthFlowConfig struct {
	ClientID             string
	ClientSecret         string
	AuthorizeURL         string // Manual OAuth endpoint (optional)
	TokenURL             string // Manual OAuth endpoint (optional)
	RegistrationEndpoint string // Manual registration endpoint (optional)
	Scopes               []string
	CallbackPort         int
	Timeout              time.Duration
	SkipBrowser          bool
	OAuthParams          map[string]string
}

// OAuthFlowResult contains the result of an OAuth flow
type OAuthFlowResult struct {
	TokenSource oauth2.TokenSource
	Config      *oauth.Config
}

func shouldDynamicallyRegisterClient(config *OAuthFlowConfig) bool {
	return config.ClientID == "" && config.ClientSecret == ""
}

// PerformOAuthFlow performs an OAuth authentication flow with the given configuration
func PerformOAuthFlow(ctx context.Context, issuer string, config *OAuthFlowConfig) (*OAuthFlowResult, error) {
	logger.Infof("Starting OAuth authentication flow for issuer: %s", issuer)

	if config == nil {
		return nil, fmt.Errorf("OAuth flow config cannot be nil")
	}

	// Handle dynamic client registration if needed
	if shouldDynamicallyRegisterClient(config) {
		if err := handleDynamicRegistration(ctx, issuer, config); err != nil {
			return nil, err
		}
	}

	// Create OAuth configuration
	oauthConfig, err := createOAuthConfig(ctx, issuer, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth config: %w", err)
	}

	// Create and execute OAuth flow
	return newOAuthFlow(ctx, oauthConfig, config)
}

// handleDynamicRegistration handles the dynamic client registration process
func handleDynamicRegistration(ctx context.Context, issuer string, config *OAuthFlowConfig) error {
	discoveredDoc, err := getDiscoveryDocument(ctx, issuer, config)
	if err != nil {
		return fmt.Errorf("failed to discover registration endpoint: %w", err)
	}

	registrationResponse, err := registerDynamicClient(ctx, config, discoveredDoc)
	if err != nil {
		return err
	}

	// Update config with registered client credentials
	config.ClientID = registrationResponse.ClientID
	config.ClientSecret = registrationResponse.ClientSecret

	if discoveredDoc.RegistrationEndpoint != "" {
		config.AuthorizeURL = discoveredDoc.AuthorizationEndpoint
		config.TokenURL = discoveredDoc.TokenEndpoint
	}

	return nil
}

// getDiscoveryDocument retrieves the OIDC discovery document
func getDiscoveryDocument(ctx context.Context, issuer string, config *OAuthFlowConfig) (*oauth.OIDCDiscoveryDocument, error) {
	// If we already have the registration endpoint from earlier discovery, use it
	if config.RegistrationEndpoint != "" && config.AuthorizeURL != "" && config.TokenURL != "" {
		logger.Debugf("Using pre-discovered OAuth endpoints for dynamic registration")
		return &oauth.OIDCDiscoveryDocument{
			Issuer:                issuer,
			AuthorizationEndpoint: config.AuthorizeURL,
			TokenEndpoint:         config.TokenURL,
			RegistrationEndpoint:  config.RegistrationEndpoint,
		}, nil
	}

	// Fall back to discovering endpoints
	return oauth.DiscoverOIDCEndpoints(ctx, issuer)
}

// createOAuthConfig creates the OAuth configuration based on available endpoints
func createOAuthConfig(ctx context.Context, issuer string, config *OAuthFlowConfig) (*oauth.Config, error) {
	// Check if we have OAuth endpoints configured
	if config.AuthorizeURL != "" && config.TokenURL != "" {
		logger.Infof("Using OAuth endpoints - authorize_url: %s, token_url: %s",
			config.AuthorizeURL, config.TokenURL)

		return oauth.CreateOAuthConfigManual(
			config.ClientID,
			config.ClientSecret,
			config.AuthorizeURL,
			config.TokenURL,
			config.Scopes,
			true, // Enable PKCE by default for security
			config.CallbackPort,
			config.OAuthParams,
		)
	}

	// Fall back to OIDC discovery
	logger.Info("Using OIDC discovery")
	return oauth.CreateOAuthConfigFromOIDC(
		ctx,
		issuer,
		config.ClientID,
		config.ClientSecret,
		config.Scopes,
		true, // Enable PKCE by default for security
		config.CallbackPort,
	)
}

func newOAuthFlow(ctx context.Context, oauthConfig *oauth.Config, config *OAuthFlowConfig) (*OAuthFlowResult, error) {
	flow, err := oauth.NewFlow(oauthConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth flow: %w", err)
	}

	// Create a context with timeout for the OAuth flow
	oauthTimeout := config.Timeout
	if oauthTimeout <= 0 {
		oauthTimeout = DefaultOAuthTimeout
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
		TokenSource: source,
		Config:      oauthConfig,
	}, nil
}

func registerDynamicClient(
	ctx context.Context,
	config *OAuthFlowConfig,
	discoveredDoc *oauth.OIDCDiscoveryDocument,
) (*oauth.DynamicClientRegistrationResponse, error) {

	// Use default client name if not provided
	registrationRequest := oauth.NewDynamicClientRegistrationRequest(config.Scopes, config.CallbackPort)

	// Perform dynamic client registration
	registrationResponse, err := oauth.RegisterClientDynamically(ctx, discoveredDoc.RegistrationEndpoint, registrationRequest)
	if err != nil {
		return nil, fmt.Errorf("dynamic client registration failed: %w", err)
	}

	return registrationResponse, nil
}

// FetchResourceMetadata as specified in RFC 9728
func FetchResourceMetadata(ctx context.Context, metadataURL string) (*auth.RFC9728AuthInfo, error) {
	if metadataURL == "" {
		return nil, fmt.Errorf("metadata URL is empty")
	}

	// Validate URL
	parsedURL, err := url.Parse(metadataURL)
	if err != nil {
		return nil, fmt.Errorf("invalid metadata URL: %w", err)
	}

	// RFC 9728: Must use HTTPS (except for localhost in development)
	if parsedURL.Scheme != "https" && parsedURL.Hostname() != "localhost" && parsedURL.Hostname() != "127.0.0.1" {
		return nil, fmt.Errorf("metadata URL must use HTTPS: %s", metadataURL)
	}

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: DefaultHTTPTimeout,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata request failed with status %d", resp.StatusCode)
	}

	// Check content type
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "application/json") {
		return nil, fmt.Errorf("unexpected content type: %s", contentType)
	}

	// Parse the metadata
	const maxResponseSize = 1024 * 1024 // 1MB limit
	var metadata auth.RFC9728AuthInfo
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseSize)).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}

	// RFC 9728 Section 3.3: Validate that the resource value matches
	// For now we just check it's not empty
	if metadata.Resource == "" {
		return nil, fmt.Errorf("metadata missing required 'resource' field")
	}

	return &metadata, nil
}

// ValidateAndDiscoverAuthServer attempts to validate if a URL is an authorization server
// and discover its actual issuer by fetching its metadata.
// This handles the case where the URL used to fetch metadata differs from the actual issuer
// (e.g., Stripe's case where https://mcp.stripe.com hosts metadata for https://marketplace.stripe.com)
func ValidateAndDiscoverAuthServer(ctx context.Context, potentialIssuer string) (*AuthServerInfo, error) {
	// Use DiscoverActualIssuer which doesn't validate issuer match
	// This allows us to discover the real issuer even when it differs from the metadata URL
	doc, err := oauth.DiscoverActualIssuer(ctx, potentialIssuer)
	if err == nil && doc != nil && doc.Issuer != "" {
		// Found valid authorization server metadata, return the actual issuer and endpoints
		if doc.Issuer != potentialIssuer {
			logger.Infof("Discovered actual issuer: %s (from metadata URL: %s)", doc.Issuer, potentialIssuer)
		} else {
			logger.Debugf("Validated authorization server: %s", potentialIssuer)
		}

		return &AuthServerInfo{
			Issuer:               doc.Issuer,
			AuthorizationURL:     doc.AuthorizationEndpoint,
			TokenURL:             doc.TokenEndpoint,
			RegistrationEndpoint: doc.RegistrationEndpoint,
		}, nil
	}

	// If that fails, the URL might not be a valid authorization server
	return nil, fmt.Errorf("could not validate %s as an authorization server: %w", potentialIssuer, err)
}
