// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/stacklok/toolhive/pkg/transport/middleware/origin"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// corsAllowedHeaders lists request headers MCP clients may send. The
	// CORS-unsafelisted MCP-Protocol-Version must be allow-listed: ToolHive
	// reads and validates it on the request path, so a browser MCP client
	// cannot send it through CORS unless it is listed here. Last-Event-ID is
	// needed for SSE stream resumption (also not CORS-safelisted).
	corsAllowedHeaders = "Content-Type, Accept, Authorization, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID"

	// corsExposedHeaders lists response headers that browsers may read.
	// MCP-Protocol-Version is exposed so a browser client can read the
	// negotiated protocol version back.
	corsExposedHeaders = "Mcp-Session-Id, MCP-Protocol-Version"

	// corsMaxAge is the preflight cache lifetime in seconds (24 hours).
	corsMaxAge = "86400"
)

// CORS returns a middleware that handles CORS preflight (OPTIONS) requests and
// injects Access-Control-Allow-* response headers on responses whose Origin
// header matches an allowed entry. When allowedOrigins is empty the middleware
// is a no-op, preserving the default security posture.
//
// Origin matching is exact and uses the same canonicalization as the origin
// middleware (origin.CanonicalizeOrigin): scheme and host are lowercased per
// RFC 6454 §4, and there is no wildcard and no scheme+host prefix matching.
// The allowlist is canonicalized once at construction; the request Origin is
// canonicalized per request. The Access-Control-Allow-Origin value echoes the
// matched canonicalized origin, which is always what the browser compares
// against (browsers serialize Origin with a lowercase scheme+host).
//
// All OPTIONS requests are handled directly (returning 204) when this
// middleware is active so that CORS preflights never reach the backend, which
// previously returned 405 Method Not Allowed. An unmatched origin gets 204
// without CORS headers — the browser will reject the follow-up request, which
// is the correct fail-closed outcome.
//
// Preflight is intentionally unauthenticated: it runs outermost (before the
// method gate, auth middleware, and the backend) and its response leaks only
// static allowlist headers.
//
// allowedMethods is the value advertised in Access-Control-Allow-Methods. It
// should reflect the methods the backend actually accepts so a preflight never
// succeeds for a method the real request would reject.
func CORS(allowedOrigins []string, allowedMethods string) types.MiddlewareFunction {
	origins := slices.Clone(allowedOrigins)
	origins = slices.DeleteFunc(origins, func(o string) bool { return strings.TrimSpace(o) == "" })
	if len(origins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	allowedSet := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowedSet[origin.CanonicalizeOrigin(o)] = struct{}{}
	}
	slog.Debug("CORS middleware configured",
		"allowed_origin_count", len(allowedSet), "allowed_methods", allowedMethods)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var matched string
			if rawOrigin := r.Header.Get("Origin"); rawOrigin != "" {
				canonical := origin.CanonicalizeOrigin(rawOrigin)
				if _, ok := allowedSet[canonical]; ok {
					matched = canonical
				}
			}

			if matched != "" {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", matched)
				h.Set("Access-Control-Allow-Methods", allowedMethods)
				h.Set("Access-Control-Allow-Headers", corsAllowedHeaders)
				h.Set("Access-Control-Expose-Headers", corsExposedHeaders)
				h.Add("Vary", "Origin")
			}

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

// ValidateAllowedOrigins validates configured CORS origins and returns a
// canonicalized copy. It surfaces misconfiguration at startup instead of
// letting an origin silently never match (which produces a broken browser
// experience with no signal):
//
//   - Empty entries (after trimming whitespace) are dropped.
//   - Entries that cannot parse as a scheme://host[:port] origin — e.g. a
//     missing scheme ("localhost:6274"), a wildcard ("*"), or garbage — are
//     rejected with an error.
//   - A trailing slash (e.g. "http://localhost:6274/") is normalized away, as
//     a browser Origin header never carries one.
//
// Canonicalization matches origin.CanonicalizeOrigin, so the returned entries
// behave identically in both the origin validator and the CORS middleware.
func ValidateAllowedOrigins(origins []string) ([]string, error) {
	validated := make([]string, 0, len(origins))
	for _, raw := range origins {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		canonical := origin.CanonicalizeOrigin(entry)
		if strings.HasPrefix(canonical, "\x00") {
			return nil, fmt.Errorf(
				"invalid CORS origin %q: must be a scheme://host[:port] URL (e.g. %q)", raw, "http://localhost:6274")
		}
		validated = append(validated, canonical)
	}
	return validated, nil
}
