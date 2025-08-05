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
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/jwx/v2/jwk"

	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/versions"
)

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

	// No need for additional caching as jwk.Cache handles it
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
	req.Header.Set("User-Agent", fmt.Sprintf("ToolHive/%s", versions.Version))
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
	cache := jwk.NewCache(ctx)

	// Register the JWKS URL with the cache using custom HTTP client
	err = cache.Register(jwksURL, jwk.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to register JWKS URL: %w", err)
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
	}, nil
}

// getKeyFromJWKS gets the key from the JWKS.
func (v *TokenValidator) getKeyFromJWKS(ctx context.Context, token *jwt.Token) (interface{}, error) {
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
	keySet, err := v.jwksClient.Get(ctx, v.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get JWKS: %w", err)
	}

	// Get the key with the matching key ID
	key, found := keySet.LookupKeyID(kid)
	if !found {
		return nil, fmt.Errorf("key ID %s not found in JWKS", kid)
	}

	// Get the raw key
	var rawKey interface{}
	if err := key.Raw(&rawKey); err != nil {
		return nil, fmt.Errorf("failed to get raw key: %w", err)
	}

	return rawKey, nil
}

// validateClaims validates the cl aims in the token.
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

func (v *TokenValidator) introspectOpaqueToken(ctx context.Context, tokenStr string) (jwt.MapClaims, error) {
	if v.introspectURL == "" {
		return nil, fmt.Errorf("no introspection endpoint available")
	}
	form := url.Values{"token": {tokenStr}}
	form.Set("token_type_hint", "access_token")

	// Build POST request with encoding and required headers
	req, err := http.NewRequestWithContext(ctx, "POST", v.introspectURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create introspection request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	// if we have client id and secret, add them to the request
	if v.clientID != "" && v.clientSecret != "" {
		req.SetBasicAuth(v.clientID, v.clientSecret)
	}

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("introspection call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("introspection unauthorized: %s", resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("introspection failed, status %d", resp.StatusCode)
	}

	claims, err := parseIntrospectionClaims(resp.Body)
	if err != nil {
		return nil, err
	}

	// Validate required claims (e.g. exp)
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

// Middleware creates an HTTP middleware that validates JWT tokens.
func (v *TokenValidator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get the token from the Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer realm=\"%s\"", v.issuer))
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}

		// Check if the Authorization header has the Bearer prefix
		if !strings.HasPrefix(authHeader, "Bearer ") {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer realm=\"%s\"", v.issuer))
			http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
			return
		}

		// Extract the token
		tokenString := strings.TrimPrefix(authHeader, "Bearer ")

		// Validate the token
		claims, err := v.ValidateToken(r.Context(), tokenString)
		if err != nil {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(
				"Bearer realm=\"%s\", error=\"invalid_token\", error_description=\"%v\"",
				v.issuer, err,
			))
			http.Error(w, fmt.Sprintf("Invalid token: %v", err), http.StatusUnauthorized)
			return
		}

		// Add the claims to the request context using a proper key type
		ctx := context.WithValue(r.Context(), ClaimsContextKey{}, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
