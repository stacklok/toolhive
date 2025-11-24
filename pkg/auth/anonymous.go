// Package auth provides authentication and authorization utilities.
package auth

import (
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AnonymousMiddleware creates an HTTP middleware that sets up anonymous identity.
// This is useful for testing and local environments where authorization policies
// need to work without requiring actual authentication.
//
// The middleware sets up basic anonymous identity that can be used by authorization
// policies, allowing them to function even when authentication is disabled.
// This is heavily discouraged in production settings but is handy for testing
// and local development environments.
func AnonymousMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Create anonymous claims with basic information
		claims := jwt.MapClaims{
			"sub":   "anonymous",
			"iss":   "toolhive-local",
			"aud":   "toolhive",
			"exp":   time.Now().Add(24 * time.Hour).Unix(), // Valid for 24 hours
			"iat":   time.Now().Unix(),
			"nbf":   time.Now().Unix(),
			"email": "anonymous@localhost",
			"name":  "Anonymous User",
		}

		// Create Identity from claims
		identity := &Identity{
			Subject:   "anonymous",
			Name:      "Anonymous User",
			Email:     "anonymous@localhost",
			Claims:    claims,
			Token:     "", // No token for anonymous auth
			TokenType: "Bearer",
		}

		// Add the Identity to the request context
		ctx := WithIdentity(r.Context(), identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
