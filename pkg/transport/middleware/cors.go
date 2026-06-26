// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// defaultCORSAllowedMethods is the fallback preflight method list used when
	// the caller does not supply an explicit set. The proxy passes a set derived
	// from the server's actual capabilities (see the stateless/stateful method
	// sources of truth in the transparent proxy) so the preflight never
	// advertises a method the backend will reject.
	defaultCORSAllowedMethods = "GET, POST, DELETE, OPTIONS"

	// corsAllowedHeaders lists request headers MCP clients may send. MCP-Protocol-Version
	// must be allow-listed: ToolHive reads and validates it on the request path
	// (an unsupported value yields 400), so a browser MCP client cannot send it
	// through CORS unless it is listed here.
	corsAllowedHeaders = "Content-Type, Accept, Mcp-Session-Id, MCP-Protocol-Version, Authorization"

	// corsExposedHeaders lists response headers that browsers may read. MCP-Protocol-Version
	// is exposed so a browser client can read the negotiated protocol version back.
	// Content-Type is omitted because it is already a CORS-safelisted response
	// header and does not need to be exposed explicitly.
	corsExposedHeaders = "Mcp-Session-Id, MCP-Protocol-Version"

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
//
// allowedMethods is the value advertised in Access-Control-Allow-Methods. It
// should reflect the methods the backend actually accepts so a preflight never
// succeeds for a method the real request would reject. When empty,
// defaultCORSAllowedMethods is used.
func CORS(allowedOrigins []string, allowedMethods string) types.MiddlewareFunction {
	if len(allowedOrigins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	if allowedMethods == "" {
		allowedMethods = defaultCORSAllowedMethods
	}

	slog.Debug("CORS middleware configured",
		"allowed_origins", strings.Join(allowedOrigins, ", "), "allowed_methods", allowedMethods)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			matched := matchCORSOrigin(origin, allowedOrigins)

			if matched != "" {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", matched)
				h.Set("Access-Control-Allow-Methods", allowedMethods)
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
			// The trailing ":" boundary is load-bearing: it ensures the entry
			// matches only "<entry>:<port>" and never a longer host. Without it,
			// the entry "http://localhost" would also match
			// "http://localhost.evil.com". See cors_test.go for the invariant.
			return requestOrigin
		}
	}
	return ""
}

// ValidateAndNormalizeOrigins validates configured CORS origins and returns a
// normalized copy. It surfaces misconfiguration at startup instead of letting an
// origin silently never match (which produces a broken browser experience with
// no signal):
//
//   - "*" (wildcard) is passed through unchanged.
//   - A trailing slash (e.g. "http://localhost:6274/") is stripped — a browser
//     Origin header never carries one — with a warning, so the entry still matches.
//   - An entry without a scheme (e.g. "localhost:6274") can never match a browser
//     Origin header (always scheme://host[:port]) and is rejected with an error.
func ValidateAndNormalizeOrigins(origins []string) ([]string, error) {
	normalized := make([]string, 0, len(origins))
	for _, origin := range origins {
		entry := strings.TrimSpace(origin)

		if entry == "*" {
			normalized = append(normalized, entry)
			continue
		}

		if strings.HasSuffix(entry, "/") {
			stripped := strings.TrimRight(entry, "/")
			slog.Warn("CORS origin has a trailing slash that browsers never send; normalizing",
				"origin", origin, "normalized", stripped)
			entry = stripped
		}

		// A browser Origin is scheme://host[:port]; without a scheme the entry
		// can never match an incoming Origin header.
		if !strings.Contains(entry, "://") {
			return nil, fmt.Errorf(
				"invalid CORS origin %q: missing scheme (expected e.g. %q)", origin, "http://localhost:6274")
		}

		normalized = append(normalized, entry)
	}
	return normalized, nil
}
