// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package bodylimit provides HTTP middleware that caps the size of request
// bodies, rejecting oversized requests with 413 Request Entity Too Large.
//
// It is used both directly as a net/http middleware (the management API and the
// vMCP server) and via the runner middleware registry (the MCP proxies), so
// every inbound listener can bound the memory a single request may consume
// before it is buffered by handlers that call io.ReadAll.
package bodylimit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// MiddlewareType is the type constant for the body limit middleware in the
	// runner middleware registry.
	MiddlewareType = "bodylimit"

	// DefaultMaxRequestBodySize is the fail-safe cap applied when no limit is
	// configured. Zero must never mean "unlimited" (see go-style rules), so
	// callers passing a non-positive limit fall back to this value.
	DefaultMaxRequestBodySize int64 = 1 << 20 // 1 MB
)

// MiddlewareParams holds the parameters for the body limit middleware factory.
type MiddlewareParams struct {
	// MaxBytes is the maximum request body size in bytes. Values <= 0 are
	// treated as DefaultMaxRequestBodySize (zero never means "unlimited").
	MaxBytes int64 `json:"max_bytes"`
}

// bodyLimitMiddleware adapts the body-limit handler to the types.Middleware
// interface expected by the runner middleware registry.
type bodyLimitMiddleware struct {
	handler types.MiddlewareFunction
}

// Handler returns the middleware function used by the proxy.
func (m *bodyLimitMiddleware) Handler() types.MiddlewareFunction {
	return m.handler
}

// Close releases resources held by the middleware. The body-limit middleware
// holds none, so this is a no-op.
func (*bodyLimitMiddleware) Close() error {
	return nil
}

// Middleware returns a net/http middleware that rejects requests whose body
// exceeds maxBytes with 413 Request Entity Too Large. It rejects early based on
// the Content-Length header, and wraps the body in http.MaxBytesReader as a
// safety net for requests that omit (or understate) Content-Length. A
// non-positive maxBytes falls back to DefaultMaxRequestBodySize.
func Middleware(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxRequestBodySize
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check Content-Length header first for early rejection.
			if r.ContentLength > maxBytes {
				slog.Warn("request body size exceeds limit", //nolint:gosec // G706: request metadata for diagnostics
					"content_length", r.ContentLength, "limit", maxBytes, "method", r.Method, "path", r.URL.Path)
				http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}

			// Track if MaxBytesReader's limit is exceeded.
			limitExceeded := false
			bytesRead := int64(0)

			// Wrap ResponseWriter to intercept only MaxBytesReader errors.
			wrappedWriter := &bodySizeResponseWriter{
				ResponseWriter: w,
				limitExceeded:  &limitExceeded,
				written:        false,
			}

			// Set MaxBytesReader as a safety net for requests without Content-Length.
			limitedBody := http.MaxBytesReader(wrappedWriter, r.Body, maxBytes)

			// Wrap the limited body to detect when the size limit is exceeded.
			tracker := &maxBytesTracker{
				ReadCloser:    limitedBody,
				bytesRead:     &bytesRead,
				limit:         maxBytes,
				limitExceeded: &limitExceeded,
			}
			r.Body = tracker

			next.ServeHTTP(wrappedWriter, r)
		})
	}
}

// CreateMiddleware is the types.MiddlewareFactory registered in the runner's
// GetSupportedMiddlewareFactories. It unmarshals MiddlewareParams, builds the
// handler via Middleware, and registers it with the runner.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	var params MiddlewareParams
	if err := json.Unmarshal(config.Parameters, &params); err != nil {
		return fmt.Errorf("failed to unmarshal body limit middleware parameters: %w", err)
	}

	mw := &bodyLimitMiddleware{
		handler: Middleware(params.MaxBytes),
	}
	runner.AddMiddleware(MiddlewareType, mw)
	return nil
}

// maxBytesTracker wraps an io.ReadCloser to track bytes read and detect size
// limit violations from http.MaxBytesReader.
type maxBytesTracker struct {
	io.ReadCloser
	bytesRead     *int64
	limit         int64
	limitExceeded *bool
}

func (t *maxBytesTracker) Read(p []byte) (n int, err error) {
	n, err = t.ReadCloser.Read(p)
	*t.bytesRead += int64(n)

	// Check if we've reached/exceeded the limit or if this is a MaxBytesError.
	// Use >= because MaxBytesReader stops AT the limit, not after it.
	if *t.bytesRead >= t.limit {
		*t.limitExceeded = true
	}

	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			*t.limitExceeded = true
		}
	}

	return n, err
}

// bodySizeResponseWriter wraps http.ResponseWriter to convert 400 to 413 only
// when MaxBytesReader's limit was exceeded (not for validation errors).
type bodySizeResponseWriter struct {
	http.ResponseWriter
	limitExceeded *bool
	written       bool
}

func (w *bodySizeResponseWriter) WriteHeader(statusCode int) {
	// Only convert 400 to 413 if MaxBytesReader's limit was actually exceeded.
	if statusCode == http.StatusBadRequest && !w.written && *w.limitExceeded {
		statusCode = http.StatusRequestEntityTooLarge
	}
	w.written = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *bodySizeResponseWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying ResponseWriter so http.ResponseController (and
// other unwrap-aware callers) can reach optional capabilities such as
// SetWriteDeadline. Without this, wrapping the writer would break long-lived
// SSE streams whose transport clears the server write deadline.
func (w *bodySizeResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Flush forwards to the underlying ResponseWriter when it supports flushing,
// preserving streaming (SSE) responses for callers that assert http.Flusher
// directly rather than going through http.ResponseController. http.Hijacker is
// intentionally not forwarded: the transports this middleware guards stream over
// SSE and do not hijack the connection.
func (w *bodySizeResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
