package auth

import (
	"context"
	"net/http"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/auth/middleware"
	"github.com/stacklok/toolhive/pkg/auth/token"
	"github.com/stacklok/toolhive/pkg/auth/token/providers"
)

// Compatibility types and constants - aliasing to new packages

// TokenValidator validates JWT or opaque tokens using OIDC configuration.
// Deprecated: Use token.Validator instead
type TokenValidator = token.Validator

// TokenValidatorConfig contains configuration for the token validator.
// Deprecated: Use token.ValidatorConfig instead
type TokenValidatorConfig = token.ValidatorConfig

// TokenIntrospector defines the interface for token introspection providers
// Deprecated: Use token.Introspector instead
type TokenIntrospector = token.Introspector

// ClaimsContextKey is the key used to store claims in the request context.
// Deprecated: Use token.ClaimsContextKey instead
type ClaimsContextKey = token.ClaimsContextKey

// RFC9728AuthInfo represents the OAuth Protected Resource metadata as defined in RFC 9728
// Deprecated: Use discovery.RFC9728AuthInfo instead
type RFC9728AuthInfo struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	JWKSURI                string   `json:"jwks_uri"`
	ScopesSupported        []string `json:"scopes_supported"`
}

// Common errors - re-exported from token package
var (
	// Deprecated: Use token.ErrNoToken instead
	ErrNoToken = token.ErrNoToken
	// Deprecated: Use token.ErrInvalidToken instead
	ErrInvalidToken = token.ErrInvalidToken
	// Deprecated: Use token.ErrTokenExpired instead
	ErrTokenExpired = token.ErrTokenExpired
	// Deprecated: Use token.ErrInvalidIssuer instead
	ErrInvalidIssuer = token.ErrInvalidIssuer
	// Deprecated: Use token.ErrInvalidAudience instead
	ErrInvalidAudience = token.ErrInvalidAudience
	// Deprecated: Use token.ErrMissingJWKSURL instead
	ErrMissingJWKSURL = token.ErrMissingJWKSURL
	// Deprecated: Use token.ErrFailedToFetchJWKS instead
	ErrFailedToFetchJWKS = token.ErrFailedToFetchJWKS
	// Deprecated: Use token.ErrFailedToDiscoverOIDC instead
	ErrFailedToDiscoverOIDC = token.ErrFailedToDiscoverOIDC
	// Deprecated: Use token.ErrMissingIssuerAndJWKSURL instead
	ErrMissingIssuerAndJWKSURL = token.ErrMissingIssuerAndJWKSURL
)

// Constants
const (
	// GoogleTokeninfoURL is the Google OAuth2 tokeninfo endpoint URL
	// Deprecated: Use providers.GoogleTokeninfoURL instead
	GoogleTokeninfoURL = providers.GoogleTokeninfoURL
)

// Compatibility functions

// NewTokenValidator creates a new token validator.
// Deprecated: Use token.NewValidator instead
func NewTokenValidator(ctx context.Context, config TokenValidatorConfig) (*TokenValidator, error) {
	return token.NewValidator(ctx, config)
}

// NewTokenValidatorConfig creates a new TokenValidatorConfig with the provided parameters
// Deprecated: Use token.NewValidatorConfig instead
func NewTokenValidatorConfig(issuer, audience, jwksURL, clientID string, clientSecret string) *TokenValidatorConfig {
	return token.NewValidatorConfig(issuer, audience, jwksURL, clientID, clientSecret)
}

// GetClaimsFromContext retrieves the claims from the request context.
// Deprecated: Use token.GetClaimsFromContext instead
func GetClaimsFromContext(ctx context.Context) (jwt.MapClaims, bool) {
	return token.GetClaimsFromContext(ctx)
}

// GetAuthenticationMiddleware returns the appropriate authentication middleware based on the configuration.
// Deprecated: Use middleware.GetAuthenticationMiddleware instead
func GetAuthenticationMiddleware(
	ctx context.Context,
	oidcConfig *TokenValidatorConfig,
) (func(http.Handler) http.Handler, http.Handler, error) {
	if oidcConfig == nil {
		return middleware.GetAuthenticationMiddleware(ctx, nil, "")
	}

	validator, err := token.NewValidator(ctx, *oidcConfig)
	if err != nil {
		return nil, nil, err
	}

	return middleware.GetAuthenticationMiddleware(ctx, validator, oidcConfig.ResourceURL)
}

// NewAuthInfoHandler creates an HTTP handler that returns RFC-9728 compliant OAuth Protected Resource metadata
// Deprecated: Use middleware.NewAuthInfoHandler instead
func NewAuthInfoHandler(_, jwksURL, resourceURL string, scopes []string) http.Handler {
	return middleware.NewAuthInfoHandler(jwksURL, resourceURL, scopes)
}

// LocalUserMiddleware creates middleware that adds local user claims to the context
// Deprecated: Use middleware.LocalUserMiddleware instead
func LocalUserMiddleware(username string) func(http.Handler) http.Handler {
	return middleware.LocalUserMiddleware(username)
}

// EscapeQuotes escapes quotes in a string for use in a quoted-string context.
func EscapeQuotes(s string) string {
	return middleware.EscapeQuotes(s)
}
