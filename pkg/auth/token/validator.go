// Package token provides JWT and opaque token validation functionality.
package token

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"

	"github.com/stacklok/toolhive/pkg/auth/oidc"
	"github.com/stacklok/toolhive/pkg/auth/token/providers"
	"github.com/stacklok/toolhive/pkg/networking"
)

// Common errors
var (
	ErrNoToken                 = errors.New("no token provided")
	ErrInvalidToken            = providers.ErrInvalidToken // Use providers error to avoid import cycle
	ErrTokenExpired            = errors.New("token expired")
	ErrInvalidIssuer           = errors.New("invalid issuer")
	ErrInvalidAudience         = errors.New("invalid audience")
	ErrMissingJWKSURL          = errors.New("missing JWKS URL")
	ErrFailedToFetchJWKS       = errors.New("failed to fetch JWKS")
	ErrFailedToDiscoverOIDC    = errors.New("failed to discover OIDC configuration")
	ErrMissingIssuerAndJWKSURL = errors.New("either issuer or JWKS URL must be provided")
)

// Validator validates JWT or opaque tokens using OIDC configuration.
type Validator struct {
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
	registry      *IntrospectorRegistry

	// Lazy JWKS registration
	jwksRegistered      bool
	jwksRegistrationMu  sync.Mutex
	jwksRegistrationErr error
}

// ValidatorConfig contains configuration for the token validator.
type ValidatorConfig struct {
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

	// ResourceURL is the explicit resource URL for OAuth discovery (RFC 9728)
	ResourceURL string

	// httpClient stores the HTTP client with the right config
	httpClient *http.Client
}

// NewValidatorConfig creates a new ValidatorConfig with the provided parameters
func NewValidatorConfig(issuer, audience, jwksURL, clientID string, clientSecret string) *ValidatorConfig {
	// Only create a config if at least one parameter is provided
	if issuer == "" && audience == "" && jwksURL == "" && clientID == "" && clientSecret == "" {
		return nil
	}

	return &ValidatorConfig{
		Issuer:       issuer,
		Audience:     audience,
		JWKSURL:      jwksURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}
}

// NewValidator creates a new token validator.
func NewValidator(ctx context.Context, config ValidatorConfig) (*Validator, error) {
	jwksURL := config.JWKSURL

	// If JWKS URL is not provided but issuer is, try to discover it
	if jwksURL == "" && config.Issuer != "" {
		doc, err := oidc.DiscoverEndpointsWithOptions(
			ctx, config.Issuer, config.CACertPath, config.AuthTokenFile, config.AllowPrivateIP,
		)
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
	httprcClient := httprc.NewClient(httprc.WithHTTPClient(httpClient))
	cache, err := jwk.NewCache(ctx, httprcClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWKS cache: %w", err)
	}

	// Create introspector registry
	registry := NewIntrospectorRegistry()

	// Add Google provider if the introspection URL matches
	if config.IntrospectionURL == providers.GoogleTokeninfoURL {
		registry.AddIntrospector(providers.NewGoogleIntrospector(config.IntrospectionURL))
	}

	// Add RFC7662 provider with auth if configured
	if config.ClientID != "" || config.ClientSecret != "" {
		rfc7662, err := providers.NewRFC7662IntrospectorWithAuth(
			config.IntrospectionURL, config.ClientID, config.ClientSecret, config.CACertPath, config.AuthTokenFile, config.AllowPrivateIP,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create RFC7662 introspector: %w", err)
		}
		registry.AddIntrospector(rfc7662)
	}

	return &Validator{
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
func (v *Validator) ensureJWKSRegistered(ctx context.Context) error {
	v.jwksRegistrationMu.Lock()
	defer v.jwksRegistrationMu.Unlock()

	if v.jwksRegistered {
		return v.jwksRegistrationErr
	}

	registrationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

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
func (v *Validator) getKeyFromJWKS(ctx context.Context, token *jwt.Token) (interface{}, error) {
	if err := v.ensureJWKSRegistered(ctx); err != nil {
		return nil, fmt.Errorf("JWKS registration failed: %w", err)
	}

	if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
		return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
	}

	kid, ok := token.Header["kid"].(string)
	if !ok {
		return nil, fmt.Errorf("token header missing kid")
	}

	keySet, err := v.jwksClient.Lookup(ctx, v.jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup JWKS: %w", err)
	}

	key, found := keySet.LookupKeyID(kid)
	if !found {
		return nil, fmt.Errorf("key ID %s not found in JWKS", kid)
	}

	var rawKey interface{}
	if err := jwk.Export(key, &rawKey); err != nil {
		return nil, fmt.Errorf("failed to export raw key: %w", err)
	}

	return rawKey, nil
}

// validateClaims validates the claims in the token.
func (v *Validator) validateClaims(claims jwt.MapClaims) error {
	if v.issuer != "" {
		issuerClaim, err := claims.GetIssuer()
		if err != nil {
			return fmt.Errorf("failed to get issuer from claims: %w", err)
		}
		if strings.TrimSpace(issuerClaim) != strings.TrimSpace(v.issuer) {
			return ErrInvalidIssuer
		}
	}

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

	expirationTime, err := claims.GetExpirationTime()
	if err != nil || expirationTime == nil || expirationTime.Before(time.Now()) {
		return ErrTokenExpired
	}

	return nil
}

// introspectOpaqueToken uses the provider pattern to introspect opaque tokens
func (v *Validator) introspectOpaqueToken(ctx context.Context, tokenStr string) (jwt.MapClaims, error) {
	if v.introspectURL == "" {
		return nil, fmt.Errorf("no introspection endpoint available")
	}

	introspector := v.registry.GetIntrospector(v.introspectURL)
	if introspector == nil {
		return nil, fmt.Errorf("no introspector available for URL: %s", v.introspectURL)
	}

	claims, err := introspector.IntrospectToken(ctx, tokenStr)
	if err != nil {
		return nil, fmt.Errorf("%s introspection failed: %w", introspector.Name(), err)
	}

	if err := v.validateClaims(claims); err != nil {
		return nil, err
	}

	return claims, nil
}

// ValidateToken validates a token.
func (v *Validator) ValidateToken(ctx context.Context, tokenString string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
		return v.getKeyFromJWKS(ctx, token)
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenMalformed) {
			claims, err := v.introspectOpaqueToken(ctx, tokenString)
			if err != nil {
				return nil, fmt.Errorf("failed to introspect opaque token: %w", err)
			}
			return claims, nil
		}
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	if !token.Valid {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("failed to get claims from token")
	}

	if err := v.validateClaims(claims); err != nil {
		return nil, err
	}

	return claims, nil
}

// JWKSURL returns the JWKS URL used by the validator
func (v *Validator) JWKSURL() string {
	return v.jwksURL
}
