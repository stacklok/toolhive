// Package auth provides authentication and authorization utilities.
package auth

import (
	"context"
	"errors"
	"net/http"
	"os/user"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/stacklok/toolhive/pkg/logger"
)

// bearerTokenType defines the expected token type for Bearer authentication.
const bearerTokenType = "Bearer"

// Common Bearer token extraction errors
var (
	ErrAuthHeaderMissing       = errors.New("authorization header required")
	ErrInvalidAuthHeaderFormat = errors.New("invalid authorization header format, expected 'Bearer <token>'")
	ErrEmptyBearerToken        = errors.New("empty token in authorization header")
)

// ExtractBearerToken extracts and validates a Bearer token from the Authorization header.
// It performs the following validations:
//  1. Verifies the Authorization header is present
//  2. Checks for the "Bearer " prefix (case-sensitive per RFC 6750)
//  3. Ensures the token is not empty after removing the prefix
//
// The function returns the token string (without "Bearer " prefix) and any validation error.
// Callers are responsible for further token validation (JWT parsing, introspection, etc.)
// and for converting errors to appropriate HTTP responses.
//
// This function implements RFC 6750 Section 2.1 (Bearer Token Authorization Header).
// See: https://datatracker.ietf.org/doc/html/rfc6750#section-2.1
func ExtractBearerToken(r *http.Request) (string, error) {
	// Get the Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", ErrAuthHeaderMissing
	}

	// Check for the Bearer prefix (case-sensitive per RFC 6750)
	bearerPrefix := bearerTokenType + " "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		return "", ErrInvalidAuthHeaderFormat
	}

	// Extract the token
	tokenString := strings.TrimPrefix(authHeader, bearerPrefix)

	// Check for empty token (handles "Bearer " with no token or only whitespace)
	if strings.TrimSpace(tokenString) == "" {
		return "", ErrEmptyBearerToken
	}

	return tokenString, nil
}

// GetClaimsFromContext retrieves the claims from the request context.
// This is a helper function that can be used by authorization policies
// to access the claims regardless of which middleware was used (JWT, anonymous, or local).
//
// Returns the claims and a boolean indicating whether claims were found.
func GetClaimsFromContext(ctx context.Context) (jwt.MapClaims, bool) {
	if ctx == nil {
		return nil, false
	}
	claims, ok := ctx.Value(ClaimsContextKey{}).(jwt.MapClaims)
	return claims, ok
}

// GetAuthenticationMiddleware returns the appropriate authentication middleware based on the configuration.
// If OIDC config is provided, it returns JWT middleware. Otherwise, it returns local user middleware.
func GetAuthenticationMiddleware(ctx context.Context, oidcConfig *TokenValidatorConfig,
) (func(http.Handler) http.Handler, http.Handler, error) {
	if oidcConfig != nil {
		logger.Info("OIDC validation enabled")

		// Create JWT validator
		jwtValidator, err := NewTokenValidator(ctx, *oidcConfig)
		if err != nil {
			return nil, nil, err
		}

		authInfoHandler := NewAuthInfoHandler(oidcConfig.Issuer, jwtValidator.jwksURL, oidcConfig.ResourceURL, nil)
		return jwtValidator.Middleware, authInfoHandler, nil
	}

	logger.Info("OIDC validation disabled, using local user authentication")

	// Get current OS user
	currentUser, err := user.Current()
	if err != nil {
		logger.Warnf("Failed to get current user, using 'local' as default: %v", err)
		return LocalUserMiddleware("local"), nil, nil
	}

	logger.Infof("Using local user authentication for user: %s", currentUser.Username)
	return LocalUserMiddleware(currentUser.Username), nil, nil
}

// EscapeQuotes escapes quotes in a string for use in a quoted-string context.
func EscapeQuotes(s string) string {
	// Simple escape of backslashes and quotes is sufficient for quoted-string.
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}
