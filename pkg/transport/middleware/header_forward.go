// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// HeaderForwardMiddlewareName is the type constant for the header forward middleware.
const HeaderForwardMiddlewareName = "header-forward"

// RestrictedHeaders is the set of headers that cannot be configured for forwarding.
// Keys are in canonical form (http.CanonicalHeaderKey).
var RestrictedHeaders = map[string]bool{
	// Routing manipulation
	"Host": true,
	// Hop-by-hop headers (RFC 7230, RFC 7540)
	"Connection":     true,
	"Keep-Alive":     true,
	"Te":             true,
	"Trailer":        true,
	"Upgrade":        true,
	"Http2-Settings": true, // RFC 7540 Section 3.2.1
	// Hop-by-hop proxy headers
	"Proxy-Authorization": true,
	"Proxy-Authenticate":  true,
	"Proxy-Connection":    true,
	// Request smuggling vectors
	"Transfer-Encoding": true,
	"Content-Length":    true,
	// Identity spoofing
	"Forwarded":         true, // RFC 7239 (standardized X-Forwarded-*)
	"X-Forwarded-For":   true,
	"X-Forwarded-Host":  true,
	"X-Forwarded-Proto": true,
	"X-Real-Ip":         true,
}

// HeaderForwardMiddlewareParams holds the parameters for the header forward middleware factory.
type HeaderForwardMiddlewareParams struct {
	// AddHeaders is a map of header names to values to inject into requests.
	AddHeaders map[string]string `json:"add_headers"`
}

// HeaderForwardFactoryMiddleware wraps header forward functionality for the factory pattern.
type HeaderForwardFactoryMiddleware struct {
	handler types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *HeaderForwardFactoryMiddleware) Handler() types.MiddlewareFunction {
	return m.handler
}

// Close cleans up any resources used by the middleware.
func (*HeaderForwardFactoryMiddleware) Close() error {
	return nil
}

// CreateMiddleware is the factory function for header forward middleware.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params HeaderForwardMiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal header forward middleware parameters: %w", err)
	}

	handler, err := createHeaderForwardHandler(params.AddHeaders)
	if err != nil {
		return err
	}

	mw := &HeaderForwardFactoryMiddleware{
		handler: handler,
	}
	runner.AddMiddleware(HeaderForwardMiddlewareName, mw)
	return nil
}

// CreateHeaderForwardMiddleware returns a middleware function that injects configured headers
// into requests before they are forwarded to remote MCP servers.
// This is a convenience function for use outside the factory pattern (e.g., thv proxy).
// It returns an error if any header name is in the restricted set.
func CreateHeaderForwardMiddleware(addHeaders map[string]string) (types.MiddlewareFunction, error) {
	return createHeaderForwardHandler(addHeaders)
}

// createHeaderForwardHandler returns a middleware that injects configured headers
// into requests before they are forwarded to remote MCP servers.
// Header names are pre-canonicalized at creation time.
// Returns an error if any configured header is in the RestrictedHeaders blocklist.
func createHeaderForwardHandler(addHeaders map[string]string) (types.MiddlewareFunction, error) {
	// Return no-op middleware if no headers configured
	if len(addHeaders) == 0 {
		return func(next http.Handler) http.Handler {
			return next
		}, nil
	}

	// Pre-canonicalize header names and validate against blocklist
	canonicalHeaders := make(map[string]string, len(addHeaders))
	for name, value := range addHeaders {
		canonical := http.CanonicalHeaderKey(name)

		if RestrictedHeaders[canonical] {
			return nil, fmt.Errorf("header %q is restricted and cannot be configured for forwarding", canonical)
		}

		if canonical == "Authorization" {
			slog.Warn("authorization header is configured for forwarding; ensure the value is appropriate for the target server")
		}

		canonicalHeaders[canonical] = value
	}

	// Log configured header names once at startup (never log values)
	headerNames := slices.Sorted(maps.Keys(canonicalHeaders))
	slog.Debug("header forward middleware configured",
		"headers", strings.Join(headerNames, ", "))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for name, value := range canonicalHeaders {
				r.Header.Set(name, value)
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}
