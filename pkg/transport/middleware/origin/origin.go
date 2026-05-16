// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package origin provides HTTP middleware that enforces MCP Origin header
// validation (DNS-rebinding protection) per MCP 2025-11-25 §"Security Warning"
// (https://modelcontextprotocol.io/specification/2025-11-25/basic/transports#security-warning).
//
// When the Origin header is present on an inbound request, it MUST exactly
// match one of the configured allowed origins. Otherwise the middleware
// responds with HTTP 403 and a JSON-RPC error body. Requests without an
// Origin header (typical for non-browser clients) are permitted through.
package origin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// MiddlewareType is the type identifier registered in the middleware factory map.
	MiddlewareType = "origin"

	// jsonRPCCodeInvalidRequest is the JSON-RPC 2.0 error code for an invalid
	// request. We reuse it for rejected Origin values because the request is
	// not well-formed from the server's security policy perspective.
	jsonRPCCodeInvalidRequest int64 = -32600

	// forbiddenBodyFallback is returned if JSON marshalling of the error body
	// fails (should never happen with simple map types).
	forbiddenBodyFallback = `{"jsonrpc":"2.0","error":{"code":-32600,"message":"Origin not allowed"},"id":null}`
)

// MiddlewareParams holds the parameters for the origin middleware factory.
type MiddlewareParams struct {
	// AllowedOrigins is the exact-match allowlist of acceptable Origin values.
	// An empty list disables the middleware (requests pass through unchanged).
	AllowedOrigins []string `json:"allowed_origins"`
}

// FactoryMiddleware wraps origin-validation as a factory-pattern middleware.
type FactoryMiddleware struct {
	handler types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *FactoryMiddleware) Handler() types.MiddlewareFunction {
	return m.handler
}

// Close releases any resources held by the middleware.
func (*FactoryMiddleware) Close() error {
	return nil
}

// CreateMiddleware is the factory function registered in
// runner.GetSupportedMiddlewareFactories.
//
// If params.AllowedOrigins is empty the factory still registers a pass-through
// handler so the middleware slot is occupied, but logs at Warn level to make
// the security-disabled state visible in operator logs. Callers that want to
// avoid registration entirely should skip calling this factory (see
// pkg/runner.addOriginMiddleware).
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal origin middleware parameters: %w", err)
	}

	if len(params.AllowedOrigins) == 0 {
		slog.Warn("origin middleware registered with empty allowlist; Origin validation disabled")
	}

	handler := createOriginHandler(params.AllowedOrigins)
	runner.AddMiddleware(MiddlewareType, &FactoryMiddleware{handler: handler})
	return nil
}

// CreateOriginMiddleware returns a middleware function that enforces Origin
// header validation against the provided allowlist. Intended for callers that
// build their middleware chain directly (e.g. `thv proxy`) and do not go
// through the factory registry.
//
// What this solves: DNS-rebinding protection per MCP 2025-11-25 §"Security
// Warning" — requests whose Origin header is present and not in allowedOrigins
// receive HTTP 403 with a JSON-RPC error body.
//
// What this does NOT solve: CORS, CSRF token validation, authentication, or
// Origin-header injection via trusted reverse proxies (the caller's reverse
// proxy must deduplicate Origin headers upstream).
//
// An empty allowedOrigins slice produces a pass-through handler — the caller
// is responsible for deciding whether that is acceptable (e.g. when bind is
// loopback-only and the caller derived an allowlist via ResolveAllowedOrigins).
//
// Matching rules: exact match on byte representation except that the scheme
// and host portions of the Origin value are lowercased (RFC 6454 §4: scheme
// and host are ASCII-case-insensitive). Configured allowlist entries are
// lowercased once at construction time.
func CreateOriginMiddleware(allowedOrigins []string) types.MiddlewareFunction {
	return createOriginHandler(allowedOrigins)
}

// createOriginHandler builds the actual middleware function. An empty
// allowlist short-circuits to a no-op so that callers can safely pass a
// possibly-empty slice.
func createOriginHandler(allowedOrigins []string) types.MiddlewareFunction {
	if len(allowedOrigins) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	// Build a set for O(1) lookups. Entries are canonicalized so that
	// case-variant Origin values (RFC 6454 §4 makes scheme + host case-
	// insensitive) match predictably. Preserve the sorted list for logging.
	allowedSet := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowedSet[canonicalizeOrigin(o)] = struct{}{}
	}
	slog.Debug("origin middleware configured",
		"allowed_origin_count", len(allowedSet),
		"allowed_origins", slices.Sorted(maps.Keys(allowedSet)),
	)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Reject requests with multiple Origin headers outright — the
			// Fetch spec defines Origin as a single-value header and browsers
			// never legitimately send more than one. Splitting / merging at an
			// upstream proxy is the only way this fires.
			if values := r.Header.Values("Origin"); len(values) > 1 {
				slog.Warn("rejecting request with multiple Origin headers",
					"count", len(values),
					"method", r.Method,
					"path", r.URL.Path,
					"remote", r.RemoteAddr,
				)
				writeForbidden(w)
				return
			}

			origin := r.Header.Get("Origin")
			if origin == "" {
				// MCP spec §"Security Warning" only mandates validation when
				// the header is present. Non-browser clients (stdio bridges,
				// SDK clients) typically omit Origin entirely.
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := allowedSet[canonicalizeOrigin(origin)]; !ok {
				slog.Warn("rejecting request with disallowed Origin",
					"origin", origin,
					"method", r.Method,
					"path", r.URL.Path,
					"remote", r.RemoteAddr,
				)
				writeForbidden(w)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// canonicalizeOrigin lowercases the scheme and host portions of an Origin
// value while preserving the port verbatim. RFC 6454 §4 makes the scheme and
// host ASCII-case-insensitive; the port is a decimal integer and has no case.
// Malformed inputs (no "://" separator) are returned lowercased in full on the
// assumption that they will simply not match any legitimate allowlist entry.
func canonicalizeOrigin(raw string) string {
	if raw == "" {
		return raw
	}
	schemeEnd := strings.Index(raw, "://")
	if schemeEnd < 0 {
		return strings.ToLower(raw)
	}
	scheme := strings.ToLower(raw[:schemeEnd])
	rest := raw[schemeEnd+3:]
	// rest is "host[:port]"; port starts at the LAST ":" to correctly handle
	// IPv6 literals that the spec requires wrapped in brackets (e.g. "[::1]:8080").
	if portIdx := strings.LastIndex(rest, ":"); portIdx > 0 && !strings.Contains(rest[portIdx+1:], "]") {
		host := strings.ToLower(rest[:portIdx])
		return scheme + "://" + host + rest[portIdx:]
	}
	return scheme + "://" + strings.ToLower(rest)
}

// ResolveAllowedOrigins picks the effective Origin allowlist for a proxy
// listener. Resolution order:
//  1. If explicit is non-empty, use it verbatim.
//  2. Otherwise, if host is a loopback IP or the string "localhost", and port
//     is valid, return loopback-only defaults
//     (http://localhost:PORT, http://127.0.0.1:PORT, http://[::1]:PORT).
//  3. Otherwise, return nil — operators exposing the proxy publicly must
//     configure an explicit allowlist.
//
// Shared by the runner middleware-config helper (pkg/runner) and the
// standalone `thv proxy` command to keep the default-derivation logic in one
// place; exported because the `thv proxy` call site is outside the runner
// package and cannot reach an internal helper.
//
// What this does NOT solve: it does not validate that `explicit` entries are
// well-formed Origin values. Callers that pass operator-supplied slices must
// rely on the middleware's canonical matching to either accept or reject
// malformed entries at request time (they will simply fail to match).
func ResolveAllowedOrigins(host string, port int, explicit []string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	if port <= 0 {
		return nil
	}
	if !isLoopbackHost(host) {
		return nil
	}
	return []string{
		fmt.Sprintf("http://localhost:%d", port),
		fmt.Sprintf("http://127.0.0.1:%d", port),
		fmt.Sprintf("http://[::1]:%d", port),
	}
}

// isLoopbackHost reports whether host refers to a loopback address. Accepts
// the literal string "localhost" plus any IP literal that net.ParseIP
// classifies as loopback (e.g. 127.0.0.0/8, ::1). IPv6 is currently rejected
// by cmd/thv/app/run.go:ValidateAndNormaliseHostFlag; this helper nevertheless
// handles it so future IPv6 support does not silently lose default Origin
// protection.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	// Strip bracket form for IPv6 literals: "[::1]" → "::1".
	trimmed := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if ip := net.ParseIP(trimmed); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// writeForbidden emits a 403 response with a JSON-RPC error body (id: null).
func writeForbidden(w http.ResponseWriter) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    jsonRPCCodeInvalidRequest,
			"message": "Origin not allowed",
		},
		"id": nil,
	})
	if err != nil {
		// Marshal of a static map should never fail; fall back to a literal.
		body = []byte(forbiddenBodyFallback)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	//nolint:gosec // G104: writing a static JSON error response to an HTTP client
	_, _ = w.Write(body)
}
