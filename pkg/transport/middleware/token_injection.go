// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package middleware provides middleware functions for the transport package.
package middleware

import (
	"fmt"
	"log/slog"
	"net/http"

	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// retryAfterSecs tells MCP clients how long to wait before retrying.
// Matches the initial MonitoredTokenSource backoff interval so that clients
// retry around the same time the next token refresh attempt happens.
const retryAfterSecs = "10"

// CreateTokenInjectionMiddleware returns a middleware that injects a Bearer token
// from the provided oauth2.TokenSource. It returns 503 Service Unavailable with a
// Retry-After header when the token cannot be retrieved, so that MCP clients treat
// the failure as transient rather than initiating an OAuth discovery flow.
func CreateTokenInjectionMiddleware(tokenSource oauth2.TokenSource) types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tokenSource != nil {
				token, err := tokenSource.Token()
				if err != nil {
					slog.Warn("unable to retrieve OAuth token", "error", err)
					// The token source (AuthenticatedTokenSource) handles marking
					// the workload as unauthenticated in its Token() method.
					// Return 503 instead of 401 so MCP clients do not mistake this
					// for a server that requires client-side OAuth authentication.
					w.Header().Set("Retry-After", retryAfterSecs)
					http.Error(w, "Token temporarily unavailable", http.StatusServiceUnavailable)
					return
				}

				r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
			}
			next.ServeHTTP(w, r)
		})
	}
}
