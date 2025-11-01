// Package middleware provides middleware functions for the transport package.
package middleware

import (
	"fmt"
	"net/http"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// CreateTokenInjectionMiddleware returns a middleware that injects a Bearer token
// from the provided oauth2.TokenSource. It returns 401 when the workload is unauthenticated.
func CreateTokenInjectionMiddleware(tokenSource oauth2.TokenSource) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tokenSource != nil {
				token, err := tokenSource.Token()
				if err != nil {
					logger.Warnf("Unable to retrieve OAuth token: %v", err)
					// The token source (AuthenticatedTokenSource) handles marking
					// the workload as unauthenticated in its Token() method
					http.Error(w, "Authentication required", http.StatusUnauthorized)
					return
				}

				r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
			}
			next.ServeHTTP(w, r)
		})
	}
}
