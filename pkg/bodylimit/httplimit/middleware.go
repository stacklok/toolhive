// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package httplimit provides generic HTTP request-body limiting middleware.
package httplimit

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
)

// DefaultMaxRequestBodySize is the fail-safe cap applied when no limit is configured.
const DefaultMaxRequestBodySize int64 = 8 << 20

// Middleware returns middleware that rejects request bodies larger than maxBytes.
// Non-positive limits fall back to DefaultMaxRequestBodySize.
func Middleware(maxBytes int64) func(http.Handler) http.Handler {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxRequestBodySize
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				slog.Warn("request body size exceeds limit", //nolint:gosec // G706: request metadata for diagnostics
					"content_length", r.ContentLength, "limit", maxBytes, "method", r.Method, "path", r.URL.Path)
				http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}

			limitExceeded := false
			wrappedWriter := &bodySizeResponseWriter{ResponseWriter: w, limitExceeded: &limitExceeded}
			limitedBody := http.MaxBytesReader(wrappedWriter, r.Body, maxBytes)
			r.Body = &maxBytesTracker{ReadCloser: limitedBody, limitExceeded: &limitExceeded}
			next.ServeHTTP(wrappedWriter, r)
		})
	}
}

// IsRequestTooLarge reports whether err is an http.MaxBytesReader overflow.
func IsRequestTooLarge(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

type maxBytesTracker struct {
	io.ReadCloser
	limitExceeded *bool
}

func (t *maxBytesTracker) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		*t.limitExceeded = true
	}
	return n, err
}

type bodySizeResponseWriter struct {
	http.ResponseWriter
	limitExceeded *bool
	written       bool
}

func (w *bodySizeResponseWriter) WriteHeader(statusCode int) {
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

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *bodySizeResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush forwards to the underlying writer when it supports http.Flusher.
func (w *bodySizeResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
