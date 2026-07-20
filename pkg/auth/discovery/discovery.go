// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Default timeout constants for authentication operations
const (
	DefaultOAuthTimeout      = 5 * time.Minute
	DefaultHTTPTimeout       = 30 * time.Second
	DefaultAuthDetectTimeout = 10 * time.Second
	MaxRetryAttempts         = 3
	RetryBaseDelay           = 2 * time.Second
	MaxResponseBodyDrain     = 1 * 1024 * 1024 // 1 MB - limit response body draining to prevent resource exhaustion
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
	Issuer                            string
	AuthorizationURL                  string
	TokenURL                          string
	RegistrationEndpoint              string
	ClientIDMetadataDocumentSupported bool
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

	// Make a test request to the target server to see if it returns WWW-Authenticate.
	// The remote MCP server is untrusted, so refuse cross-host / scheme-downgrade
	// redirects to prevent it driving the host into an SSRF (CWE-918).
	client := &http.Client{
		Timeout: config.Timeout,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
			ResponseHeaderTimeout: config.ResponseHeaderTimeout,
		},
		CheckRedirect: networking.SameHostRedirectPolicy(),
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

	// NEW: Well-known URI fallback per MCP specification
	// When no WWW-Authenticate header found, try well-known URIs
	slog.Debug("No WWW-Authenticate header found, attempting well-known URI discovery")

	wellKnownAuthInfo, err := tryWellKnownDiscovery(detectCtx, client, targetURI)
	if err != nil {
		slog.Debug("Well-known URI discovery failed", "error", err)
		return nil, nil // Not an error, just no auth detected
	}

	if wellKnownAuthInfo != nil {
		slog.Debug("Discovered authentication via well-known URI")
		return wellKnownAuthInfo, nil
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

	// #nosec G704 -- targetURI is server-controlled; the client refuses cross-host and
	// HTTPS->HTTP redirects (networking.SameHostRedirectPolicy) to contain SSRF.
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make %s request: %w", method, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("Failed to close response body", "error", err)
		}
	}()

	// Check if we got a 401 Unauthorized with WWW-Authenticate header
	if resp.StatusCode == http.StatusUnauthorized {
		wwwAuth := resp.Header.Get("WWW-Authenticate")
		if wwwAuth != "" {
			return ParseWWWAuthenticate(wwwAuth)
		}
	}

	return nil, nil
}

// buildWellKnownURI constructs a well-known URI for OAuth Protected Resource metadata
// per RFC 9728 Section 3.1 and MCP specification
func buildWellKnownURI(parsedURL *url.URL, endpointSpecific bool) string {
	baseURL := url.URL{
		Scheme: parsedURL.Scheme,
		Host:   parsedURL.Host,
	}

	if endpointSpecific && parsedURL.Path != "" && parsedURL.Path != "/" {
		// Endpoint-specific: /.well-known/oauth-protected-resource/<original-path>
		// Remove leading slash from original path to avoid double slashes
		cleanPath := strings.TrimPrefix(parsedURL.Path, "/")
		baseURL.Path = path.Join(oauthproto.WellKnownOAuthResourcePath, cleanPath)
	} else {
		// Root-level: /.well-known/oauth-protected-resource
		baseURL.Path = oauthproto.WellKnownOAuthResourcePath
	}

	return baseURL.String()
}

// checkWellKnownURIExists returns true if a well-known URI is accessible and returns application/json
// Per RFC 9728, protected resource metadata MUST be queried using HTTP GET and MUST return application/json
func checkWellKnownURIExists(ctx context.Context, client *http.Client, uri string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		//nolint:gosec // G706: uri is from server endpoint discovery
		slog.Debug("Failed to create GET request", "uri", uri, "error", err)
		return false
	}

	req.Header.Set("Accept", "application/json")

	// #nosec G704 -- uri is server-controlled; the client refuses cross-host and
	// HTTPS->HTTP redirects (networking.SameHostRedirectPolicy) to contain SSRF.
	resp, err := client.Do(req)
	if err != nil {
		//nolint:gosec // G706: uri is from server endpoint discovery
		slog.Debug("Failed to check well-known URI", "uri", uri, "error", err)
		return false
	}
	defer func() {
		// Drain and close response body to enable connection reuse
		// Limit draining to MaxResponseBodyDrain to prevent resource exhaustion from large responses
		_, _ = io.CopyN(io.Discard, resp.Body, MaxResponseBodyDrain)
		_ = resp.Body.Close()
	}()

	// RFC 9728 requires 200 OK status code - metadata endpoints must be publicly accessible
	if resp.StatusCode != http.StatusOK {
		return false
	}

	// RFC 9728 requires Content-Type to be application/json
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "application/json") {
		//nolint:gosec // G706: content type from server response is safe to log
		slog.Debug("Well-known URI returned unexpected content type",
			"uri", uri, "content_type", contentType)
		return false
	}

	return true
}

// tryWellKnownDiscovery attempts to discover authentication requirements via well-known URIs
// per MCP specification Section: Protected Resource Metadata Discovery Requirements.
// Tries endpoint-specific path first, then root-level path.
func tryWellKnownDiscovery(ctx context.Context, client *http.Client, targetURI string) (*AuthInfo, error) {
	parsedURL, err := url.Parse(targetURI)
	if err != nil {
		return nil, fmt.Errorf("invalid target URI: %w", err)
	}

	// Build well-known URIs to try (in priority order per MCP spec)
	wellKnownURIs := []string{
		// 1. Endpoint-specific: /.well-known/oauth-protected-resource/<path>
		buildWellKnownURI(parsedURL, true),
		// 2. Root-level: /.well-known/oauth-protected-resource
		buildWellKnownURI(parsedURL, false),
	}

	// Try each well-known URI in order
	for _, wellKnownURI := range wellKnownURIs {
		//nolint:gosec // G706: well-known URIs are built from server endpoint
		slog.Debug("Trying well-known URI", "uri", wellKnownURI)

		// Check if the URI exists before attempting to fetch
		if !checkWellKnownURIExists(ctx, client, wellKnownURI) {
			//nolint:gosec // G706: well-known URIs are built from server endpoint
			slog.Debug("Well-known URI not found", "uri", wellKnownURI)
			continue
		}

		// URI exists - return AuthInfo with ResourceMetadata set
		// Downstream handler will use FetchResourceMetadata to get the actual metadata
		//nolint:gosec // G706: well-known URIs are built from server endpoint
		slog.Debug("Found well-known URI", "uri", wellKnownURI)
		return &AuthInfo{
			Type:             "OAuth",
			ResourceMetadata: wellKnownURI,
		}, nil
	}

	return nil, nil // No well-known metadata found
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
		authInfo := &AuthInfo{Type: "Bearer"}

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
		//nolint:gosec // G706: auth scheme name (Basic/Digest) is safe to log
		slog.Debug("Unsupported authentication scheme", "header", header)
		return nil, fmt.Errorf("unsupported authentication scheme: %s", strings.Split(header, " ")[0])
	}

	return nil, fmt.Errorf("no supported authentication type found in header: %s", header)
}

// DeriveIssuerFromURL attempts to derive the OAuth issuer from the remote URL using general patterns
func DeriveIssuerFromURL(remoteURL string) string {
	// Parse the URL to extract the domain
	parsedURL, err := url.Parse(remoteURL)
	if err != nil {
		slog.Debug("Failed to parse remote URL", "error", err)
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

	//nolint:gosec // G706: derived issuer URL is from server configuration
	slog.Debug("Derived issuer from URL", "remote_url", remoteURL, "issuer", issuer)
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
		slog.Debug("Realm is not a valid URL", "error", err)
		return ""
	}

	// RFC 8414: The issuer identifier MUST be a URL using the "https" scheme
	// with no query or fragment components
	if parsedURL.Scheme != "https" && !networking.IsLocalhost(parsedURL.Host) {
		slog.Debug("Realm is not using HTTPS scheme", "realm", realm)
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
		slog.Debug("Realm contains query or fragment components", "realm", realm)
		// Remove query and fragment to make it a valid issuer
		parsedURL.RawQuery = ""
		parsedURL.Fragment = ""
	}

	issuer := parsedURL.String()
	//nolint:gosec // G706: realm is from WWW-Authenticate header of configured remote
	slog.Debug("Derived issuer from realm", "realm", realm, "issuer", issuer)
	return issuer
}

// OAuthFlowConfig contains configuration for performing OAuth flows
type OAuthFlowConfig struct {
	ClientID             string
	ClientSecret         string //nolint:gosec // G117: field legitimately holds sensitive data
	AuthorizeURL         string // Manual OAuth endpoint (optional)
	TokenURL             string // Manual OAuth endpoint (optional)
	RegistrationEndpoint string // Manual registration endpoint (optional)
	Scopes               []string
	CallbackPort         int
	Timeout              time.Duration
	SkipBrowser          bool
	Resource             string // RFC 8707 resource indicator (optional)
	OAuthParams          map[string]string
	ScopeParamName       string // Override scope query parameter name (e.g., "user_scope" for Slack)

	// AllowPrivateIPs permits the Dynamic Client Registration calls (discovery
	// fetch and registration POST) to reach private/loopback/link-local
	// addresses. Callers set it from networking.TargetIsPrivate on the remote
	// target: when ToolHive is pointed at a private remote the upstream IdP may
	// legitimately be private too, so its DCR calls must be allowed; otherwise
	// they are guarded to contain SSRF (CWE-918). This mirrors the
	// blockPrivateIPs decision the flow already applies to its other discovery
	// fetches. Defaults to false (guarded).
	AllowPrivateIPs bool

	// DCR renewal metadata — populated by handleDynamicRegistration and threaded
	// into OAuthFlowResult so callers can persist the data for RFC 7592 operations.
	SecretExpiry            time.Time // zero means the secret never expires
	RegistrationAccessToken string    //nolint:gosec // G117: field legitimately holds sensitive data
	RegistrationClientURI   string
	TokenEndpointAuthMethod string
	RegisteredCallbackPort  int
}

// OAuthFlowResult contains the result of an OAuth flow
type OAuthFlowResult struct {
	TokenSource oauth2.TokenSource
	Config      *oauth.Config

	// Token details for persistence across restarts
	AccessToken  string //nolint:gosec // G117: field legitimately holds sensitive data
	RefreshToken string //nolint:gosec // G117: field legitimately holds sensitive data
	Expiry       time.Time

	// DCR client credentials for persistence (obtained during Dynamic Client Registration)
	ClientID     string
	ClientSecret string //nolint:gosec // G117: field legitimately holds sensitive data

	// DCR renewal metadata (RFC 7591 §3.2.1 / RFC 7592).
	// SecretExpiry is zero when the provider did not issue an expiring secret.
	// RegistrationAccessToken and RegistrationClientURI are empty when the
	// provider does not support RFC 7592 management operations.
	SecretExpiry            time.Time
	RegistrationAccessToken string //nolint:gosec // G117: field legitimately holds sensitive data
	RegistrationClientURI   string
	TokenEndpointAuthMethod string
	RegisteredCallbackPort  int
}

func shouldDynamicallyRegisterClient(config *OAuthFlowConfig) bool {
	return config.ClientID == "" && config.ClientSecret == ""
}

// PerformOAuthFlow performs an OAuth authentication flow with the given configuration
func PerformOAuthFlow(ctx context.Context, issuer string, config *OAuthFlowConfig) (*OAuthFlowResult, error) {
	slog.Debug("Starting OAuth authentication flow", "issuer", issuer)

	if config == nil {
		return nil, fmt.Errorf("OAuth flow config cannot be nil")
	}

	// Resolve port availability before registration. DCR clients allow port fallback
	// because the actual port is registered after selection. Pre-registered and CIMD
	// clients require the configured port to be available as-is — it is already
	// published in their IdP application or metadata document redirect URI.
	if shouldDynamicallyRegisterClient(config) {
		// For dynamic registration, we can allow fallback to alternative ports
		// since we can register the client with the actual port we'll use
		port, err := networking.FindOrUsePort(config.CallbackPort)
		if err != nil {
			return nil, fmt.Errorf("failed to find available port: %w", err)
		}

		if port != config.CallbackPort {
			slog.Warn("Specified auth callback port is unavailable", "requested_port", config.CallbackPort, "actual_port", port)
		}
		config.CallbackPort = port
	} else {
		// For pre-registered clients and CIMD, use strict port checking.
		// The port is either configured in the IdP app or baked into the
		// redirect URI in the hosted metadata document.
		if !networking.IsAvailable(config.CallbackPort) {
			return nil, fmt.Errorf(
				"specified auth callback port %d is not available - please choose a different port or ensure it's not in use",
				config.CallbackPort,
			)
		}
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

// handleDynamicRegistration handles the dynamic client registration process.
//
// Persistence model (option (b) from #5219): the resolver runs against an
// in-memory dcr.CredentialStore for the duration of a single CLI
// invocation, so within one PerformOAuthFlow call concurrent goroutines
// share the singleflight (proving the inherited "singleflight
// deduplication" property). Cross-invocation persistence is handled
// outside the resolver by pkg/auth/remote/handler.go, which reads
// CachedClientID / CachedClientSecretRef before this code path runs and
// short-circuits to a refresh-token flow when a usable cached client
// exists.
//
// One consequence of option (b) is that the resolver's RFC 7591 §3.2.1
// expiry-driven refetch does NOT participate in the CLI's cross-invocation
// persistence loop: each PerformOAuthFlow call builds a fresh in-memory store,
// so a cached entry from a previous invocation never reaches the resolver.
// Cross-invocation client-secret expiry is handled instead by the remote
// handler, which consults CachedSecretExpiry and renews through RFC 7592 before
// cached credentials are used.
//
// Wrapping the remote handler's secretProvider into a dcr.CredentialStore
// adapter (option (a)) would close that loop and is the natural follow-up;
// it was rejected here as out-of-scope churn for sub-issue 4b.
func handleDynamicRegistration(ctx context.Context, issuer string, config *OAuthFlowConfig) error {
	discoveredDoc, err := getDiscoveryDocument(ctx, issuer, config)
	if err != nil {
		return fmt.Errorf("failed to discover registration endpoint: %w", err)
	}

	// Check if the provider supports Dynamic Client Registration before
	// invoking the resolver. The CLI-flag hint below is intentional: this
	// function is CLI-facing (pkg/auth/discovery is not a protocol-level
	// package) and the flags named here are the correct fallback for
	// operators who need to supply credentials manually. The
	// protocol-neutral version of this message lives in
	// pkg/oauthproto.handleHTTPResponse for the HTTP 404/405/501 paths.
	if discoveredDoc.RegistrationEndpoint == "" {
		return fmt.Errorf("this provider does not support Dynamic Client Registration (DCR). " +
			"Please configure OAuth client credentials using --remote-auth-client-id and --remote-auth-client-secret flags, " +
			"or register a client manually with the provider")
	}

	resolution, err := resolveDCRCredentials(ctx, issuer, config, discoveredDoc)
	if err != nil {
		return err
	}

	// Update config with registered client credentials. The remote handler
	// at pkg/auth/remote/handler.go reads ClientID / ClientSecret off
	// OAuthFlowResult and persists them into CachedClientID /
	// CachedClientSecretRef for the next invocation.
	config.ClientID = resolution.ClientID
	config.ClientSecret = resolution.ClientSecret

	// Surface the resolved authorization / token endpoints to the OAuth
	// flow. The resolver returns the endpoints it used for registration
	// (caller-supplied if specified, discovered otherwise), so
	// downstream OAuth-config construction can rely on these fields
	// being populated.
	if resolution.AuthorizationEndpoint != "" {
		config.AuthorizeURL = resolution.AuthorizationEndpoint
	}
	if resolution.TokenEndpoint != "" {
		config.TokenURL = resolution.TokenEndpoint
	}

	// Store DCR renewal metadata for RFC 7592 operations.
	// A zero ClientSecretExpiresAt means the secret never expires (RFC 7591 §3.2.1).
	config.SecretExpiry = resolution.ClientSecretExpiresAt
	config.RegistrationAccessToken = resolution.RegistrationAccessToken
	config.RegistrationClientURI = resolution.RegistrationClientURI
	config.TokenEndpointAuthMethod = resolution.TokenEndpointAuthMethod
	config.RegisteredCallbackPort = config.CallbackPort

	if resolution.RegistrationAccessToken != "" {
		slog.Debug("DCR response includes registration access token for RFC 7592 operations")
	}

	return nil
}

// resolveDCRCredentials routes the CLI-flow DCR registration through the
// shared pkg/auth/dcr resolver, inheriting its singleflight deduplication,
// S256 PKCE gating, RFC 7591 §3.2.1 expiry-driven refetch, bearer-token
// transport with redirect refusal, and panic recovery.
//
// The redirect URI is a loopback per RFC 8252 §7.3 — this is the CLI's
// existing public-client model, and PublicClient=true tells the resolver
// to register with token_endpoint_auth_method=none and to refuse when
// S256 PKCE is not advertised (rather than silently downgrading).
//
// Discovery fetch — multi-URL fallback. oauthproto.FetchAuthorizationServerMetadata
// tries the three well-known URL forms defined by RFC 8414 §3.1 and OIDC
// Discovery in priority order: RFC 8414 path-insertion
// (scheme://host/.well-known/oauth-authorization-server/{path}), OIDC
// issuer-suffix ({issuer}/.well-known/openid-configuration), and bare
// RFC 8414 (scheme://host/.well-known/oauth-authorization-server). Using
// this function rather than constructing a single OIDC URL ensures that
// authorization servers with non-root issuer paths whose metadata lives
// at the path-insertion URL are correctly reached (fixes #5356).
// The fetched code_challenge_methods_supported is forwarded to the
// resolver via dcr.Request so the S256 PKCE gate fires without a
// second discovery round-trip inside the resolver.
//
// Threading code_challenge_methods_supported through AuthServerInfo (and
// updating every caller of ValidateAndDiscoverAuthServer) would eliminate
// this fetch on the pre-discovered path and is a natural follow-up.
func resolveDCRCredentials(
	ctx context.Context,
	issuer string,
	config *OAuthFlowConfig,
	discoveredDoc *oauthproto.OIDCDiscoveryDocument,
) (*dcr.Resolution, error) {
	redirectURI := fmt.Sprintf("http://localhost:%d/callback", config.CallbackPort)

	// dcr.Request.Issuer carries the caller's logical scope for cache
	// keying. The CLI flow has no separate logical issuer of its own, so
	// it deliberately reuses the upstream's issuer URL here. This is
	// safe because the resolver's cache key is (Issuer, UpstreamID,
	// RedirectURI, ScopesHash) and the CLI always supplies an explicit
	// loopback RedirectURI per RFC 8252 §7.3 — even if a future
	// embedded-authserver upstream happened to share Issuer with a CLI
	// invocation, the distinct RedirectURI keeps the cache keys apart.
	// UpstreamID (derived by the resolver from the RegistrationEndpoint set
	// below, or a DiscoveryURL) further distinguishes distinct upstreams.
	// See the Issuer field doc on dcr.Request for the wider semantics.
	req := &dcr.Request{
		Issuer:                issuer,
		RedirectURI:           redirectURI,
		Scopes:                config.Scopes,
		AuthorizationEndpoint: discoveredDoc.AuthorizationEndpoint,
		TokenEndpoint:         discoveredDoc.TokenEndpoint,
		PublicClient:          true,
		// Carry the flow's private-IP decision so DCR shares the same SSRF
		// posture as the flow's other discovery fetches; without this the DCR
		// calls would always be guarded and a legitimately private upstream
		// would be refused. See OAuthFlowConfig.AllowPrivateIPs.
		AllowPrivateIPs: config.AllowPrivateIPs,
	}

	// Fetch AS metadata using the multi-URL fallback so non-root issuers
	// are correctly reached (see function-level doc). discoveredDoc.Issuer
	// is non-empty for every reachable caller; the else branch is
	// defence-in-depth for a future refactor that produces an empty-issuer
	// doc and preserves the pre-existing RegistrationEndpoint-direct path.
	if discoveredDoc.Issuer != "" {
		// Guard this fetch the same way pkg/auth/dcr's own outbound calls are
		// guarded (CWE-918): discoveredDoc.Issuer can be untrusted server input
		// on some discovery branches (see discoverIssuerAndScopes in
		// pkg/auth/remote/handler.go), so a nil client here would reopen the
		// discovery-indirection and DNS-rebinding vectors this PR closes
		// elsewhere. See networking.NewHostScopedClientBuilder for the guard
		// policy and pkg/auth/dcr's newGuardedDCRClient for the same pattern.
		metaHost, parseErr := url.Parse(discoveredDoc.Issuer)
		if parseErr != nil {
			return nil, fmt.Errorf("dynamic client registration failed: parse issuer for http client: %w", parseErr)
		}
		metaClient, clientErr := networking.NewHostScopedClientBuilder(metaHost.Host, config.AllowPrivateIPs, false).
			WithDisableKeepAlives(true).
			Build()
		if clientErr != nil {
			return nil, fmt.Errorf("dynamic client registration failed: build metadata http client: %w", clientErr)
		}
		metaClient.CheckRedirect = networking.SameHostRedirectPolicy()

		fullMeta, metaErr := oauthproto.FetchAuthorizationServerMetadata(ctx, discoveredDoc.Issuer, metaClient)
		if metaErr != nil && !errors.Is(metaErr, oauthproto.ErrRegistrationEndpointMissing) {
			return nil, fmt.Errorf("dynamic client registration failed: discover authorization server metadata: %w", metaErr)
		}
		if fullMeta == nil {
			// Contract violation guard — FetchAuthorizationServerMetadata
			// guarantees non-nil metadata alongside ErrRegistrationEndpointMissing.
			return nil, fmt.Errorf("dynamic client registration failed: authorization server metadata unexpectedly nil")
		}
		regEndpoint := fullMeta.RegistrationEndpoint
		if regEndpoint == "" {
			// Synthesise per nanobot/Hydra convention, mirroring the resolver's
			// own synthesiseRegistrationEndpoint for the DiscoveryURL branch.
			u, parseErr := url.Parse(discoveredDoc.Issuer)
			if parseErr != nil {
				return nil, fmt.Errorf("dynamic client registration failed: parse issuer for endpoint synthesis: %w", parseErr)
			}
			regEndpoint = (&url.URL{
				Scheme: u.Scheme,
				Host:   u.Host,
				Path:   strings.TrimRight(u.Path, "/") + "/register",
			}).String()
		}
		req.RegistrationEndpoint = regEndpoint
		req.CodeChallengeMethodsSupported = fullMeta.CodeChallengeMethodsSupported
	} else {
		req.RegistrationEndpoint = discoveredDoc.RegistrationEndpoint
	}

	store := dcr.NewInMemoryStore()
	defer func() {
		// Close returns nil today (inMemoryStore.Close is sync.Once-guarded
		// over an in-memory map), but a future change to the underlying
		// MemoryStorage.Close — e.g., timeout-aware cleanup goroutine
		// teardown — could surface a real error. Log it at debug rather
		// than dropping it so a regression is visible without elevating
		// every CLI-flow shutdown to WARN.
		if err := store.Close(); err != nil {
			slog.Debug("dcr: in-memory store close failed", "error", err)
		}
	}()

	resolution, err := dcr.ResolveCredentials(ctx, req, store)
	if err != nil {
		// Surface the structured error to the boundary log so operators
		// see one slog.Error record with the step / issuer / redirect_uri
		// attributes, then wrap with the CLI-facing prefix.
		dcr.LogStepError(issuer, err)
		return nil, fmt.Errorf("dynamic client registration failed: %w", err)
	}
	return resolution, nil
}

// getDiscoveryDocument retrieves the OIDC discovery document.
//
// The "pre-discovered" short-circuit synthesises a document from
// OAuthFlowConfig fields populated by earlier discovery in the remote
// handler (pkg/auth/remote/handler.go via ValidateAndDiscoverAuthServer).
// On that path the synthesised document carries only the endpoint URLs,
// NOT the server-capability fields (code_challenge_methods_supported,
// token_endpoint_auth_methods_supported, scopes_supported) — those
// remain at their zero values. resolveDCRCredentials handles this by
// re-issuing a discovery fetch through the resolver (see its doc for
// the rationale and trade-off).
func getDiscoveryDocument(
	ctx context.Context,
	issuer string,
	config *OAuthFlowConfig,
) (*oauthproto.OIDCDiscoveryDocument, error) {
	// If we already have the registration endpoint from earlier discovery, use it
	if config.RegistrationEndpoint != "" && config.AuthorizeURL != "" && config.TokenURL != "" {
		slog.Debug("Using pre-discovered OAuth endpoints for dynamic registration")
		return &oauthproto.OIDCDiscoveryDocument{
			AuthorizationServerMetadata: oauthproto.AuthorizationServerMetadata{
				Issuer:                issuer,
				AuthorizationEndpoint: config.AuthorizeURL,
				TokenEndpoint:         config.TokenURL,
				RegistrationEndpoint:  config.RegistrationEndpoint,
			},
		}, nil
	}

	// Fall back to discovering endpoints. This fetch precedes the DCR
	// registration this function's callers are about to perform, so it shares
	// the same private-IP policy (CWE-918) rather than defaulting to unguarded.
	return oauth.DiscoverOIDCEndpoints(ctx, issuer, !config.AllowPrivateIPs)
}

// createOAuthConfig creates the OAuth configuration based on available endpoints
func createOAuthConfig(ctx context.Context, issuer string, config *OAuthFlowConfig) (*oauth.Config, error) {
	// Check if we have OAuth endpoints configured
	if config.AuthorizeURL != "" && config.TokenURL != "" {
		slog.Debug("Using OAuth endpoints",
			"authorize_url", config.AuthorizeURL, "token_url", config.TokenURL)

		return oauth.CreateOAuthConfigManual(
			config.ClientID,
			config.ClientSecret,
			config.AuthorizeURL,
			config.TokenURL,
			config.Scopes,
			true, // Enable PKCE by default for security
			config.CallbackPort,
			config.Resource,
			config.OAuthParams,
			config.ScopeParamName,
		)
	}

	// Fall back to OIDC discovery
	slog.Debug("Using OIDC discovery")
	cfg, err := oauth.CreateOAuthConfigFromOIDC(
		ctx,
		issuer,
		config.ClientID,
		config.ClientSecret,
		config.Scopes,
		true, // Enable PKCE by default for security
		config.CallbackPort,
		config.Resource,
	)
	if err != nil {
		return nil, err
	}
	cfg.ScopeParamName = config.ScopeParamName
	return cfg, nil
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
		if errors.Is(oauthCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("OAuth flow timed out after %v - user did not complete authentication", oauthTimeout)
		}
		return nil, fmt.Errorf("OAuth flow failed: %w", err)
	}

	slog.Debug("OAuth authentication successful")

	// Log token info (without exposing the actual token)
	if tokenResult.Claims != nil {
		if sub, ok := tokenResult.Claims["sub"].(string); ok {
			slog.Debug("Authenticated as subject", "sub", sub)
		}
		if email, ok := tokenResult.Claims["email"].(string); ok {
			slog.Debug("Authenticated with email", "email", email)
		}
	}

	source := flow.TokenSource()
	return buildOAuthFlowResult(source, oauthConfig, tokenResult, config), nil
}

func buildOAuthFlowResult(
	tokenSource oauth2.TokenSource,
	oauthConfig *oauth.Config,
	tokenResult *oauth.TokenResult,
	config *OAuthFlowConfig,
) *OAuthFlowResult {
	return &OAuthFlowResult{
		TokenSource:  tokenSource,
		Config:       oauthConfig,
		AccessToken:  tokenResult.AccessToken,
		RefreshToken: tokenResult.RefreshToken,
		Expiry:       tokenResult.Expiry,
		ClientID:     oauthConfig.ClientID,
		ClientSecret: oauthConfig.ClientSecret,
		// DCR renewal metadata — populated only when dynamic registration was performed.
		SecretExpiry:            config.SecretExpiry,
		RegistrationAccessToken: config.RegistrationAccessToken,
		RegistrationClientURI:   config.RegistrationClientURI,
		TokenEndpointAuthMethod: config.TokenEndpointAuthMethod,
		RegisteredCallbackPort:  config.RegisteredCallbackPort,
	}
}

// FetchResourceMetadata fetches RFC 9728 protected-resource metadata from a
// server-supplied URL.
//
// The metadataURL originates from untrusted remote-server discovery (the
// WWW-Authenticate resource_metadata parameter), so the client refuses
// cross-host and HTTPS->HTTP redirects (CWE-918). When blockPrivateIPs is true
// it additionally refuses to dial private/loopback/link-local addresses on
// every hop. Callers set blockPrivateIPs when the operator-configured target is
// public; when the operator deliberately targets an internal network it is
// false so legitimately-internal auth metadata stays reachable.
func FetchResourceMetadata(ctx context.Context, metadataURL string, blockPrivateIPs bool) (*auth.RFC9728AuthInfo, error) {
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

	// The HTTPS check above runs once on the initial URL only, so refuse
	// cross-host and scheme-downgrade redirects to stop a 30x reaching an
	// internal address; optionally block private dials on every hop.
	transport := &http.Transport{
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}
	if blockPrivateIPs {
		transport.DialContext = networking.NewPrivateIPBlockingDialContext()
		transport.DisableKeepAlives = true
	}
	client := &http.Client{
		Timeout:       DefaultHTTPTimeout,
		Transport:     transport,
		CheckRedirect: networking.SameHostRedirectPolicy(),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	// #nosec G704 -- metadataURL is server-controlled; the client refuses cross-host and
	// HTTPS->HTTP redirects (networking.SameHostRedirectPolicy) to contain SSRF.
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch metadata: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Debug("Failed to close response body", "error", err)
		}
	}()

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
//
// When blockPrivateIPs is true the underlying discovery refuses to dial
// private/loopback/link-local addresses (SSRF guard for server-supplied issuer
// URLs); callers pass false when the issuer is operator-configured or the
// configured target is itself internal.
func ValidateAndDiscoverAuthServer(ctx context.Context, potentialIssuer string, blockPrivateIPs bool) (*AuthServerInfo, error) {
	// Use DiscoverActualIssuer which doesn't validate issuer match
	// This allows us to discover the real issuer even when it differs from the metadata URL
	doc, err := oauth.DiscoverActualIssuer(ctx, potentialIssuer, blockPrivateIPs)
	if err == nil && doc != nil && doc.Issuer != "" {
		// Found valid authorization server metadata, return the actual issuer and endpoints
		if doc.Issuer != potentialIssuer {
			slog.Debug("Discovered actual issuer", "issuer", doc.Issuer, "metadata_url", potentialIssuer)
		} else {
			slog.Debug("Validated authorization server", "issuer", potentialIssuer)
		}

		return &AuthServerInfo{
			Issuer:                            doc.Issuer,
			AuthorizationURL:                  doc.AuthorizationEndpoint,
			TokenURL:                          doc.TokenEndpoint,
			RegistrationEndpoint:              doc.RegistrationEndpoint,
			ClientIDMetadataDocumentSupported: doc.ClientIDMetadataDocumentSupported,
		}, nil
	}

	// If that fails, the URL might not be a valid authorization server
	return nil, fmt.Errorf("could not validate %s as an authorization server: %w", potentialIssuer, err)
}
