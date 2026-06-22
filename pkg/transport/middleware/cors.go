// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// corsAllowedMethods lists the HTTP methods the MCP proxy accepts.
	corsAllowedMethods = "GET, POST, DELETE, OPTIONS"

	// corsAllowedHeaders lists request headers MCP clients may send.
	corsAllowedHeaders = "Content-Type, Accept, Mcp-Session-Id, Authorization"

	// corsExposedHeaders lists response headers that browsers may read.
	corsExposedHeaders = "Mcp-Session-Id, Content-Type"

	// corsMaxAge is the preflight cache lifetime in seconds (24 hours).
	corsMaxAge = "86400"
)

// CORS returns a middleware that handles CORS preflight (OPTIONS) requests and
// injects Access-Control-Allow-* response headers. When allowedOrigins is empty
// the middleware is a no-op, preserving the default security posture.
//
// Origin matching rules (applied in order):
//   - "*": matches every origin; Access-Control-Allow-Origin is set to "*".
//   - Exact: "http://localhost:6274" matches only that origin.
//   - Scheme+host prefix: "http://localhost" also matches any
//     "http://localhost:<port>" (e.g. the MCP Inspector default port).
//
// All OPTIONS requests are handled directly (returning 204) when this middleware
// is active so that CORS preflights never reach the backend, which previously
// returned 405 Method Not Allowed.
func CORS(allowedOrigins []string) types.MiddlewareFunction {
	if len(allowedOrigins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	slog.Debug("CORS middleware configured", "allowed_origins", strings.Join(allowedOrigins, ", "))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			matched := matchCORSOrigin(origin, allowedOrigins)

			if matched != "" {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", matched)
				h.Set("Access-Control-Allow-Methods", corsAllowedMethods)
				h.Set("Access-Control-Allow-Headers", corsAllowedHeaders)
				h.Set("Access-Control-Expose-Headers", corsExposedHeaders)
				h.Add("Vary", "Origin")
			}

			// Intercept OPTIONS so preflight requests never reach the backend
			// (which returns 405 because it has no OPTIONS handler).
			// A matched origin gets the full preflight response; an unmatched
			// origin gets 204 without CORS headers — the browser will reject
			// the follow-up request, which is the correct security outcome.
			if r.Method == http.MethodOptions {
				if matched != "" {
					w.Header().Set("Access-Control-Max-Age", corsMaxAge)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// matchCORSOrigin returns the Access-Control-Allow-Origin value to send when
// requestOrigin matches an entry in allowed, or "" when there is no match.
//
// The returned value is the verbatim requestOrigin (a concrete origin) except
// when an allowed entry is "*", in which case "*" is returned directly.
func matchCORSOrigin(requestOrigin string, allowed []string) string {
	if requestOrigin == "" {
		return ""
	}
	for _, entry := range allowed {
		switch {
		case entry == "*":
			return "*"
		case entry == requestOrigin:
			return requestOrigin
		case strings.HasPrefix(requestOrigin, entry+":"):
			// "http://localhost" matches "http://localhost:6274", etc.
			return requestOrigin
		}
	}
	return ""
}
