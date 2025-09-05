// Package auth provides authentication and authorization utilities.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/networking"
)

// TokenIntrospector defines the interface for token introspection providers
type TokenIntrospector interface {
	// Name returns the provider name
	Name() string

	// CanHandle returns true if this provider can handle the given introspection URL
	CanHandle(introspectURL string) bool

	// IntrospectToken introspects an opaque token and returns JWT claims
	IntrospectToken(ctx context.Context, token string) (jwt.MapClaims, error)
}

// Registry maintains a list of available token introspection providers
type Registry struct {
	providers []TokenIntrospector
}

// NewRegistry creates a new provider registry
func NewRegistry() *Registry {
	return &Registry{
		providers: []TokenIntrospector{},
	}
}

// GetIntrospector returns the appropriate provider for the given introspection URL
func (r *Registry) GetIntrospector(introspectURL string) TokenIntrospector {
	for _, provider := range r.providers {
		if provider.CanHandle(introspectURL) {
			logger.Debugf("Selected provider for introspection: %s (url: %s)", provider.Name(), introspectURL)
			return provider
		}
	}
	// Create a new fallback provider instance with the specific URL
	logger.Debugf("Using RFC7662 fallback provider for introspection: %s", introspectURL)
	return NewRFC7662Provider(introspectURL)
}

// AddProvider adds a new provider to the registry
func (r *Registry) AddProvider(provider TokenIntrospector) {
	r.providers = append(r.providers, provider)
}

// GoogleTokeninfoURL is the Google OAuth2 tokeninfo endpoint URL
const GoogleTokeninfoURL = "https://oauth2.googleapis.com/tokeninfo" //nolint:gosec

// GoogleProvider implements token introspection for Google's tokeninfo API
type GoogleProvider struct {
	client *http.Client
	url    string
}

// NewGoogleProvider creates a new Google token introspection provider
func NewGoogleProvider(introspectURL string) *GoogleProvider {
	return &GoogleProvider{
		client: http.DefaultClient,
		url:    introspectURL,
	}
}

// Name returns the provider name
func (*GoogleProvider) Name() string {
	return "google"
}

// CanHandle returns true if this provider can handle the given introspection URL
func (g *GoogleProvider) CanHandle(introspectURL string) bool {
	return introspectURL == g.url
}

// IntrospectToken introspects a Google opaque token and returns JWT claims
func (g *GoogleProvider) IntrospectToken(ctx context.Context, token string) (jwt.MapClaims, error) {
	logger.Debugf("Using Google tokeninfo provider for token introspection: %s", g.url)

	// Parse the URL and add query parameters
	u, err := url.Parse(g.url)
	if err != nil {
		return nil, fmt.Errorf("failed to parse introspection URL: %w", err)
	}

	// Add the access token as a query parameter
	query := u.Query()
	query.Set("access_token", token)
	u.RawQuery = query.Encode()

	// Create the GET request
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Google tokeninfo request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", oauth.UserAgent)

	// Make the request
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google tokeninfo request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read the response with a reasonable limit to prevent DoS attacks
	const maxResponseSize = 64 * 1024 // 64KB should be more than enough for tokeninfo response
	limitedReader := io.LimitReader(resp.Body, maxResponseSize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read Google tokeninfo response: %w", err)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("google tokeninfo request failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse the Google response and convert to JWT claims
	logger.Debugf("Successfully received Google tokeninfo response (status: %d)", resp.StatusCode)
	return g.parseGoogleResponse(body)
}

// parseGoogleResponse parses Google's tokeninfo response and converts it to JWT claims
func (*GoogleProvider) parseGoogleResponse(body []byte) (jwt.MapClaims, error) {
	// Parse Google's response format
	var googleResp struct {
		// Standard OAuth fields
		Aud   string `json:"aud,omitempty"`
		Sub   string `json:"sub,omitempty"`
		Scope string `json:"scope,omitempty"`

		// Google returns Unix timestamp as string (RFC 7662 uses numeric)
		Exp string `json:"exp,omitempty"`

		// Google-specific fields
		Azp           string `json:"azp,omitempty"`
		ExpiresIn     string `json:"expires_in,omitempty"`
		Email         string `json:"email,omitempty"`
		EmailVerified string `json:"email_verified,omitempty"`
	}

	if err := json.Unmarshal(body, &googleResp); err != nil {
		return nil, fmt.Errorf("failed to decode Google tokeninfo JSON: %w", err)
	}

	// Convert to JWT MapClaims format
	claims := jwt.MapClaims{
		"iss": "https://accounts.google.com", // Default Google issuer
	}

	// Copy standard fields
	if googleResp.Sub != "" {
		claims["sub"] = googleResp.Sub
	}
	if googleResp.Aud != "" {
		claims["aud"] = googleResp.Aud
	}
	if googleResp.Scope != "" {
		claims["scope"] = googleResp.Scope
	}

	// Handle expiration - convert string timestamp to float64
	if googleResp.Exp != "" {
		expInt, err := strconv.ParseInt(googleResp.Exp, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid exp format: %w", err)
		}
		claims["exp"] = float64(expInt) // JWT expects float64

		// Check if token is expired and return error if so (consistent with RFC 7662 behavior)
		isActive := time.Now().Unix() < expInt
		claims["active"] = isActive
		if !isActive {
			return nil, ErrInvalidToken
		}
	} else {
		return nil, fmt.Errorf("missing exp field in Google response")
	}

	// Copy Google-specific fields
	if googleResp.Azp != "" {
		claims["azp"] = googleResp.Azp
	}
	if googleResp.ExpiresIn != "" {
		claims["expires_in"] = googleResp.ExpiresIn
	}
	if googleResp.Email != "" {
		claims["email"] = googleResp.Email
	}
	if googleResp.EmailVerified != "" {
		claims["email_verified"] = googleResp.EmailVerified
	}

	return claims, nil
}

// RFC7662Provider implements standard RFC 7662 OAuth 2.0 Token Introspection
type RFC7662Provider struct {
	client       *http.Client
	clientID     string
	clientSecret string
	url          string
}

// NewRFC7662Provider creates a new RFC 7662 token introspection provider
func NewRFC7662Provider(introspectURL string) *RFC7662Provider {
	return &RFC7662Provider{
		client: http.DefaultClient,
		url:    introspectURL,
	}
}

// NewRFC7662ProviderWithAuth creates a new RFC 7662 provider with client credentials
func NewRFC7662ProviderWithAuth(
	introspectURL, clientID, clientSecret, caCertPath, authTokenFile string, allowPrivateIP bool,
) (*RFC7662Provider, error) {
	// Create HTTP client with CA bundle and auth token support
	client, err := networking.NewHttpClientBuilder().
		WithCABundle(caCertPath).
		WithTokenFromFile(authTokenFile).
		WithPrivateIPs(allowPrivateIP).
		Build()
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}

	return &RFC7662Provider{
		client:       client,
		clientID:     clientID,
		clientSecret: clientSecret,
		url:          introspectURL,
	}, nil
}

// Name returns the provider name
func (*RFC7662Provider) Name() string {
	return "rfc7662"
}

// CanHandle returns true if this provider can handle the given introspection URL
// Returns true for any URL when no specific URL was configured (fallback behavior)
// or when the URL matches the configured URL
func (r *RFC7662Provider) CanHandle(introspectURL string) bool {
	// If no URL was configured, this is a fallback provider that handles everything
	if r.url == "" {
		return true
	}
	// Otherwise, only handle the specific configured URL
	return r.url == introspectURL
}

// IntrospectToken introspects a token using RFC 7662 standard
func (r *RFC7662Provider) IntrospectToken(ctx context.Context, token string) (jwt.MapClaims, error) {
	// Prepare form data for POST request
	formData := url.Values{}
	formData.Set("token", token)
	formData.Set("token_type_hint", "access_token")

	// Create POST request with form data
	req, err := http.NewRequestWithContext(ctx, "POST", r.url, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create introspection request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", oauth.UserAgent)
	req.Header.Set("Accept", "application/json")

	// Add client authentication if configured
	if r.clientID != "" && r.clientSecret != "" {
		req.SetBasicAuth(r.clientID, r.clientSecret)
	}

	// Make the request
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introspection request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body with a reasonable limit to prevent DoS attacks
	const maxResponseSize = 64 * 1024 // 64KB should be more than enough for introspection response
	limitedReader := io.LimitReader(resp.Body, maxResponseSize)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read introspection response: %w", err)
	}

	// Check for HTTP errors
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("introspection unauthorized")
		}
		return nil, fmt.Errorf("introspection failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Parse RFC 7662 response - use the existing parseIntrospectionClaims function
	return parseIntrospectionClaims(strings.NewReader(string(body)))
}

// Common errors
var (
	ErrNoToken                 = errors.New("no token provided")
	ErrInvalidToken            = errors.New("invalid token")
	ErrTokenExpired            = errors.New("token expired")
	ErrInvalidIssuer           = errors.New("invalid issuer")
	ErrInvalidAudience         = errors.New("invalid audience")
	ErrMissingJWKSURL          = errors.New("missing JWKS URL")
	ErrFailedToFetchJWKS       = errors.New("failed to fetch JWKS")
	ErrFailedToDiscoverOIDC    = errors.New("failed to discover OIDC configuration")
	ErrMissingIssuerAndJWKSURL = errors.New("either issuer or JWKS URL must be provided")
)

// OIDCDiscoveryDocument represents the OIDC discovery document structure
type OIDCDiscoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	IntrospectionEndpoint string `json:"introspection_endpoint"`
	// Add other fields as needed
}

// TokenValidator validates JWT or opaque tokens using OIDC configuration.
type TokenValidator struct {
	// OIDC configuration
	issuer        string
	audience      string
	jwksURL       string
	clientID      string
	clientSecret  string // Optional client secret for introspection
	jwksClient    *jwk.Cache
	introspectURL string       // Optional introspection endpoint
	client        *http.Client // HTTP client for making requests
	resourceURL   string       // (RFC 9728)
	registry      *Registry    // Token introspection providers

	// Lazy JWKS registration
	jwksRegistered      bool
	jwksRegistrationMu  sync.Mutex
	jwksRegistrationErr error
}

// TokenValidatorConfig contains configuration for the token validator.
type TokenValidatorConfig struct {
	// Issuer is the OIDC issuer URL (e.g., https://accounts.google.com)
	Issuer string

	// Audience is the expected audience for the token
	Audience string

	// JWKSURL is the URL to fetch the JWKS from
	JWKSURL string

	// ClientID is the OIDC client ID
	ClientID string

	// ClientSecret is the optional OIDC client secret for introspection
	ClientSecret string

	// CACertPath is the path to the CA certificate bundle for HTTPS requests
	CACertPath string

	// AuthTokenFile is the path to file containing bearer token for authentication
	AuthTokenFile string

	// AllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses
	AllowPrivateIP bool

	// IntrospectionURL is the optional introspection endpoint for validating tokens
	IntrospectionURL string

	// Store http client with the right config
	httpClient *http.Client

	// ResourceURL is the explicit resource URL for OAuth discovery (RFC 9728)
	ResourceURL string
}

// discoverOIDCConfiguration discovers OIDC configuration from the issuer's well-known endpoint
func discoverOIDCConfiguration(
	ctx context.Context,
	issuer, caCertPath, authTokenFile string,
	allowPrivateIP bool,
) (*OIDCDiscoveryDocument, error) {
	// Construct the well-known endpoint URL
	wellKnownURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnownURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set User-Agent header
	req.Header.Set("User-Agent", oauth.UserAgent)
	req.Header.Set("Accept", "application/json")

	// Create HTTP client with CA bundle and auth token support
	client, err := networking.NewHttpClientBuilder().
		WithCABundle(caCertPath).
		WithTokenFromFile(authTokenFile).
		WithPrivateIPs(allowPrivateIP).
		Build()
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OIDC configuration: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery endpoint returned status %d", resp.StatusCode)
	}

	// Parse the response
	var doc OIDCDiscoveryDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to decode OIDC configuration: %w", err)
	}

	// Validate that we got the required fields
	if doc.JWKSURI == "" {
		return nil, fmt.Errorf("OIDC configuration missing jwks_uri")
	}

	return &doc, nil
}

// NewTokenValidatorConfig creates a new TokenValidatorConfig with the provided parameters
func NewTokenValidatorConfig(issuer, audience, jwksURL, clientID string, clientSecret string) *TokenValidatorConfig {
	// Only create a config if at least one parameter is provided
	if issuer == "" && audience == "" && jwksURL == "" && clientID == "" && clientSecret == "" {
		return nil
	}

	return &TokenValidatorConfig{
		Issuer:       issuer,
		Audience:     audience,
		JWKSURL:      jwksURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}
}

// NewTokenValidator creates a new token validator.
func NewTokenValidator(ctx context.Context, config TokenValidatorConfig) (*TokenValidator, error) {
	jwksURL := config.JWKSURL

	// If JWKS URL is not provided but issuer is, try to discover it
	if jwksURL == "" && config.Issuer != "" {
		doc, err := discoverOIDCConfiguration(ctx, config.Issuer, config.CACertPath, config.AuthTokenFile, config.AllowPrivateIP)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrFailedToDiscoverOIDC, err)
		}
		jwksURL = doc.JWKSURI
	}

	// Ensure we have a JWKS URL either provided or discovered
	if jwksURL == "" {
		return nil, ErrMissingIssuerAndJWKSURL
	}

	// Create HTTP client with CA bundle and auth token support for JWKS
	httpClient, err := networking.NewHttpClientBuilder().
		WithCABundle(config.CACertPath).
		WithPrivateIPs(config.AllowPrivateIP).
		WithTokenFromFile(config.AuthTokenFile).
		Build()
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP client: %w", err)
	}
	config.httpClient = httpClient

	// Create a new JWKS client with auto-refresh
	// In jwx v3, NewCache requires an httprc.Client
	httprcClient := httprc.NewClient(httprc.WithHTTPClient(httpClient))
	cache, err := jwk.NewCache(ctx, httprcClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS cache: %w", err)
	}

	// Skip synchronous JWKS registration - will be done lazily on first use

	// Create provider registry with RFC7662 fallback
	registry := NewRegistry()

	// Add Google provider if the introspection URL matches
	if config.IntrospectionURL == GoogleTokeninfoURL {
		logger.Debugf("Registering Google tokeninfo provider: %s", config.IntrospectionURL)
		registry.AddProvider(NewGoogleProvider(config.IntrospectionURL))
	}

	// Add RFC7662 provider with auth if configured
	if config.ClientID != "" || config.ClientSecret != "" {
		rfc7662Provider, err := NewRFC7662ProviderWithAuth(
			config.IntrospectionURL, config.ClientID, config.ClientSecret, config.CACertPath, config.AuthTokenFile, config.AllowPrivateIP,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create RFC7662 provider: %w", err)
		}
		registry.AddProvider(rfc7662Provider)
	}

	return &TokenValidator{
		issuer:        config.Issuer,
		audience:      config.Audience,
		jwksURL:       jwksURL,
		introspectURL: config.IntrospectionURL,
		clientID:      config.ClientID,
		clientSecret:  config.ClientSecret,
		jwksClient:    cache,
		client:        config.httpClient,
		resourceURL:   config.ResourceURL,
		registry:      registry,
	}, nil
}

// ensureJWKSRegistered ensures that the JWKS URL is registered with the cache.
// This is called lazily on first use to avoid blocking startup.
func (v *TokenValidator) ensureJWKSRegistered(ctx context.Context) error {
	v.jwksRegistrationMu.Lock()
	defer v.jwksRegistrationMu.Unlock()

	// Check if already registered or failed
	if v.jwksRegistered {
		return v.jwksRegistrationErr
	}

	// Create context with 5-second timeout for JWKS registration
	registrationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Attempt registration
	err := v.jwksClient.Register(registrationCtx, v.jwksURL)
	if err != nil {
		v.jwksRegistrationErr = fmt.Errorf("failed to register JWKS URL: %w", err)
	} else {
		v.jwksRegistrationErr = nil
	}

	v.jwksRegistered = true
	return v.jwksRegistrationErr
}

// getKeyFromJWKS gets the key from the JWKS.
func (v *TokenValidator) getKeyFromJWKS(ctx context.Context, token *jwt.Token) (interface{}, error) {
	// Ensure JWKS is registered before attempting to use it
	if err := v.ensureJWKSRegistered(ctx); err != nil {
		return nil, fmt.Errorf("JWKS registration failed: %w", err)
	}

	// Validate the signing method
	if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
		return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
	}

	// Get the key ID from the token header
	kid, ok := token.Header["kid"].(string)
	if !ok {
		return nil, fmt.Errorf("token header missing kid")
	}

	// Get the key set from the JWKS
	// In jwx v3, Get is replaced with Lookup
	keySet, err := v.jwksClient.Lookup(ctx, v.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup JWKS: %w", err)
	}

	// Get the key with the matching key ID
	key, found := keySet.LookupKeyID(kid)
	if !found {
		return nil, fmt.Errorf("key ID %s not found in JWKS", kid)
	}

	// Get the raw key
	// In jwx v3, Raw method is replaced with Export function
	var rawKey interface{}
	if err := jwk.Export(key, &rawKey); err != nil {
		return nil, fmt.Errorf("failed to export raw key: %w", err)
	}

	return rawKey, nil
}

// validateClaims validates the claims in the token.
func (v *TokenValidator) validateClaims(claims jwt.MapClaims) error {
	// Validate the issuer if provided
	if v.issuer != "" {
		issuerClaim, err := claims.GetIssuer()
		if err != nil {
			return fmt.Errorf("failed to get issuer from claims: %w", err)
		}
		if strings.TrimSpace(issuerClaim) != strings.TrimSpace(v.issuer) {
			return ErrInvalidIssuer
		}
	}
	// Validate the audience if provided
	if v.audience != "" {
		audiences, err := claims.GetAudience()
		if err != nil {
			return ErrInvalidAudience
		}

		found := false
		for _, aud := range audiences {
			if aud == v.audience {
				found = true
				break
			}
		}

		if !found {
			return ErrInvalidAudience
		}
	}

	// Validate the expiration time
	expirationTime, err := claims.GetExpirationTime()
	if err != nil || expirationTime == nil || expirationTime.Before(time.Now()) {
		return ErrTokenExpired
	}

	return nil
}

func parseIntrospectionClaims(r io.Reader) (jwt.MapClaims, error) {
	var j struct {
		Active bool                   `json:"active"`
		Exp    *float64               `json:"exp,omitempty"`
		Sub    string                 `json:"sub,omitempty"`
		Aud    interface{}            `json:"aud,omitempty"`
		Scope  string                 `json:"scope,omitempty"`
		Iss    string                 `json:"iss,omitempty"`
		Extra  map[string]interface{} `json:"-"`
	}

	if err := json.NewDecoder(r).Decode(&j); err != nil {
		return nil, fmt.Errorf("failed to decode introspection JSON: %w", err)
	}
	if !j.Active {
		return nil, ErrInvalidToken
	}

	claims := jwt.MapClaims{}
	if j.Exp != nil {
		claims["exp"] = *j.Exp
	}
	if j.Sub != "" {
		claims["sub"] = strings.TrimSpace(j.Sub)
	}
	if j.Aud != nil {
		claims["aud"] = j.Aud
	}
	if j.Scope != "" {
		claims["scope"] = strings.TrimSpace(j.Scope)
	}
	if j.Iss != "" {
		claims["iss"] = strings.TrimSpace(j.Iss)
	}
	for k, v := range j.Extra {
		claims[k] = v
	}

	return claims, nil
}

// introspectOpaqueToken uses the provider pattern to introspect opaque tokens
func (v *TokenValidator) introspectOpaqueToken(ctx context.Context, tokenStr string) (jwt.MapClaims, error) {
	if v.introspectURL == "" {
		return nil, fmt.Errorf("no introspection endpoint available")
	}

	// Find appropriate provider for the introspection URL
	provider := v.registry.GetIntrospector(v.introspectURL)
	if provider == nil {
		return nil, fmt.Errorf("no provider available for introspection URL: %s", v.introspectURL)
	}

	// Use provider to introspect the token
	claims, err := provider.IntrospectToken(ctx, tokenStr)
	if err != nil {
		return nil, fmt.Errorf("%s introspection failed: %w", provider.Name(), err)
	}

	// Validate required claims (exp, iss, aud if configured)
	if err := v.validateClaims(claims); err != nil {
		return nil, err
	}

	return claims, nil
}

// ValidateToken validates a token.
func (v *TokenValidator) ValidateToken(ctx context.Context, tokenString string) (jwt.MapClaims, error) {
	// Parse the token
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		return v.getKeyFromJWKS(ctx, token)
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenMalformed) {
			// check against introspection endpoint if available
			claims, err := v.introspectOpaqueToken(ctx, tokenString)
			if err != nil {
				return nil, fmt.Errorf("failed to introspect opaque token: %w", err)
			}
			return claims, nil
		}
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	// it is a jwt token
	// Check if the token is valid
	if !token.Valid {
		return nil, ErrInvalidToken
	}

	// Get the claims
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("failed to get claims from token")
	}

	// Validate the claims
	if err := v.validateClaims(claims); err != nil {
		return nil, err
	}

	return claims, nil
}

// ClaimsContextKey is the key used to store claims in the request context.
type ClaimsContextKey struct{}

// buildWWWAuthenticate builds a RFC 6750 / RFC 9728 compliant value for the
// WWW-Authenticate header. It always includes realm and, if set, resource_metadata.
// If includeError is true, it appends error="invalid_token" and an optional description.
func (v *TokenValidator) buildWWWAuthenticate(includeError bool, errDescription string) string {
	var parts []string

	// realm (RFC 6750)
	if v.issuer != "" {
		parts = append(parts, fmt.Sprintf(`realm="%s"`, EscapeQuotes(v.issuer)))
	}

	// resource_metadata (RFC 9728)
	if v.resourceURL != "" {
		parts = append(parts, fmt.Sprintf(`resource_metadata="%s"`, EscapeQuotes(v.resourceURL)))
	}

	// error fields (RFC 6750 §3)
	if includeError {
		parts = append(parts, `error="invalid_token"`)
		if errDescription != "" {
			parts = append(parts, fmt.Sprintf(`error_description="%s"`, EscapeQuotes(errDescription)))
		}
	}
	return "Bearer " + strings.Join(parts, ", ")
}

// Middleware creates an HTTP middleware that validates JWT tokens.
func (v *TokenValidator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get the token from the Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			w.Header().Set("WWW-Authenticate", v.buildWWWAuthenticate(false, ""))
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		// Check if the Authorization header has the Bearer prefix
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.Header().Set("WWW-Authenticate", v.buildWWWAuthenticate(false, ""))
			http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		// Extract the token
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")

		// Validate the token
		claims, err := v.ValidateToken(r.Context(), tokenString)
		if err != nil {
			w.Header().Set("WWW-Authenticate", v.buildWWWAuthenticate(true, err.Error()))
			http.Error(w, fmt.Sprintf("Invalid token: %v", err), http.StatusUnauthorized)
			return
		}

		// Add the claims to the request context using a proper key type
		ctx := context.WithValue(r.Context(), ClaimsContextKey{}, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RFC9728AuthInfo represents the OAuth Protected Resource metadata as defined in RFC 9728
type RFC9728AuthInfo struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	JWKSURI                string   `json:"jwks_uri"`
	ScopesSupported        []string `json:"scopes_supported"`
}

// NewAuthInfoHandler creates an HTTP handler that returns RFC-9728 compliant OAuth Protected Resource metadata
func NewAuthInfoHandler(issuer, jwksURL, resourceURL string, scopes []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers for all requests
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Allow all origins if none specified. This should be fine because this is a discovery endpoint.
			origin = "*"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		// At least mcp-inspector requires these headers to be set for CORS. It seems to be a little
		// off since this is a discovery endpoint, but let's make the inspector happy.
		w.Header().Set("Access-Control-Allow-Headers", "mcp-protocol-version, Content-Type, Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400") // 24 hours

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// if resourceURL is not set, return 404 as we shouldn't presume a resource URL
		if resourceURL == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Use provided scopes or default to 'openid'
		supportedScopes := scopes
		if len(supportedScopes) == 0 {
			supportedScopes = []string{"openid"}
		}

		authInfo := RFC9728AuthInfo{
			Resource:               resourceURL,
			AuthorizationServers:   []string{issuer},
			BearerMethodsSupported: []string{"header"},
			JWKSURI:                jwksURL,
			ScopesSupported:        supportedScopes,
		}

		// Set content type
		w.Header().Set("Content-Type", "application/json")

		// Encode and send the response
		if err := json.NewEncoder(w).Encode(authInfo); err != nil {
			logger.Errorf("Failed to encode OAuth discovery response: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	})
}
