// Package middleware provides HTTP authentication middleware.
package middleware

import (
	"context"
	"fmt"
	"net/http"
	"os/user"
	"strings"

	"github.com/stacklok/toolhive/pkg/auth/token"
	"github.com/stacklok/toolhive/pkg/logger"
)

// TokenMiddleware creates an HTTP middleware that validates JWT tokens.
func TokenMiddleware(validator *token.Validator, resourceURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get the token from the Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				w.Header().Set("WWW-Authenticate", buildWWWAuthenticate(validator, resourceURL, false, ""))
				http.Error(w, "Authorization header required", http.StatusUnauthorized)
				return
			}

			// Check if the Authorization header has the Bearer prefix
			if !strings.HasPrefix(authHeader, "Bearer ") {
				w.Header().Set("WWW-Authenticate", buildWWWAuthenticate(validator, resourceURL, false, ""))
				http.Error(w, "Invalid Authorization header format", http.StatusUnauthorized)
				return
			}

			// Extract the token
			tokenString := strings.TrimPrefix(authHeader, "Bearer ")

			// Validate the token
			claims, err := validator.ValidateToken(r.Context(), tokenString)
			if err != nil {
				w.Header().Set("WWW-Authenticate", buildWWWAuthenticate(validator, resourceURL, true, err.Error()))
				http.Error(w, fmt.Sprintf("Invalid token: %v", err), http.StatusUnauthorized)
				return
			}

			// Add the claims to the request context
			ctx := context.WithValue(r.Context(), token.ClaimsContextKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// buildWWWAuthenticate builds a RFC 6750 / RFC 9728 compliant value for the
// WWW-Authenticate header. It always includes realm and, if set, resource_metadata.
// If includeError is true, it appends error="invalid_token" and an optional description.
func buildWWWAuthenticate(validator *token.Validator, resourceURL string, includeError bool, errDescription string) string {
	var parts []string

	// realm (RFC 6750) - this could be the issuer
	parts = append(parts, fmt.Sprintf(`realm="%s"`, escapeQuotes(validator.JWKSURL())))

	// resource_metadata (RFC 9728)
	if resourceURL != "" {
		parts = append(parts, fmt.Sprintf(`resource_metadata="%s"`, escapeQuotes(resourceURL)))
	}

	// error fields (RFC 6750 §3)
	if includeError {
		parts = append(parts, `error="invalid_token"`)
		if errDescription != "" {
			parts = append(parts, fmt.Sprintf(`error_description="%s"`, escapeQuotes(errDescription)))
		}
	}
	return "Bearer " + strings.Join(parts, ", ")
}

// EscapeQuotes escapes quotes in a string for use in a quoted-string context.
func EscapeQuotes(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// escapeQuotes is a private wrapper
func escapeQuotes(s string) string {
	return EscapeQuotes(s)
}

// GetAuthenticationMiddleware returns the appropriate authentication middleware based on the configuration.
// If validator is provided, it returns JWT middleware. Otherwise, it returns local user middleware.
func GetAuthenticationMiddleware(
	_ context.Context,
	validator *token.Validator,
	resourceURL string,
) (func(http.Handler) http.Handler, http.Handler, error) {
	if validator != nil {
		logger.Info("Token validation enabled")
		authInfoHandler := NewAuthInfoHandler(validator.JWKSURL(), resourceURL, nil)
		return TokenMiddleware(validator, resourceURL), authInfoHandler, nil
	}

	logger.Info("Token validation disabled, using local user authentication")

	// Get current OS user
	currentUser, err := user.Current()
	if err != nil {
		logger.Warnf("Failed to get current user, using 'local' as default: %v", err)
		return LocalUserMiddleware("local"), nil, nil
	}

	logger.Infof("Using local user authentication for user: %s", currentUser.Username)
	return LocalUserMiddleware(currentUser.Username), nil, nil
}
