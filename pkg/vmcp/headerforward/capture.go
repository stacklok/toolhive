// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package headerforward

import (
	"context"
	"net/http"
)

// forwardedHeadersKey is the unexported context key under which the allowlisted
// incoming request headers are carried from the capture middleware to the
// per-session backend client. Keeping it unexported makes the context value an
// implementation detail of this package: callers use WithForwardedHeaders /
// ForwardedHeadersFromContext rather than the raw key.
type forwardedHeadersKey struct{}

// WithForwardedHeaders returns a child context carrying the allowlisted forwarded
// headers (canonical header name → value) for the current request. This is
// request-scoped plumbing — the headers are read back by the per-session backend
// client when it builds the outbound transport.
func WithForwardedHeaders(ctx context.Context, headers map[string]string) context.Context {
	return context.WithValue(ctx, forwardedHeadersKey{}, headers)
}

// ForwardedHeadersFromContext returns the forwarded headers captured for the
// current request, or nil if none were captured.
func ForwardedHeadersFromContext(ctx context.Context) map[string]string {
	h, _ := ctx.Value(forwardedHeadersKey{}).(map[string]string)
	return h
}

// CaptureMiddleware returns HTTP middleware that copies the allowlisted incoming
// request headers into the request context (via WithForwardedHeaders) so the
// per-session backend client can forward them to backends. Header names are
// matched case-insensitively and stored in canonical form.
//
// The capture is pure plumbing: it does not depend on the request identity and
// carries the values as an explicit, request-scoped context value rather than on
// any business-logic type. If allow is empty the function returns a no-op wrapper
// to avoid allocations on the hot path.
func CaptureMiddleware(allow []string) func(http.Handler) http.Handler {
	if len(allow) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	// Precompute the canonical header names once at construction time so the
	// per-request handler does only cheap lookups.
	canonical := make([]string, len(allow))
	for i, name := range allow {
		canonical[i] = http.CanonicalHeaderKey(name)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Collect the values of allowlisted headers that are actually present.
			fwd := make(map[string]string, len(canonical))
			for _, name := range canonical {
				if v := r.Header.Get(name); v != "" {
					fwd[name] = v
				}
			}

			if len(fwd) == 0 {
				// Nothing to attach — skip the context allocation on the fast path.
				next.ServeHTTP(w, r)
				return
			}

			ctx := WithForwardedHeaders(r.Context(), fwd)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
