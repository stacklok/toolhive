// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultResponseWriteTimeout = 30 * time.Second

type postResponseWriter struct {
	http.ResponseWriter
	writeTimeout time.Duration
	armOnce      sync.Once
}

func (w *postResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *postResponseWriter) armWriteDeadline() {
	w.armOnce.Do(func() {
		// A streamable-HTTP POST may legitimately negotiate an SSE response.
		// Like a qualifying GET stream, its lifetime is governed by request
		// cancellation rather than a socket write deadline.
		if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
			return
		}
		if err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(w.writeTimeout)); err != nil {
			slog.Warn("failed to arm MCP response write deadline", "error", err)
		}
	})
}

func (w *postResponseWriter) WriteHeader(statusCode int) {
	w.armWriteDeadline()
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *postResponseWriter) Write(p []byte) (int, error) {
	w.armWriteDeadline()
	return w.ResponseWriter.Write(p)
}

func (w *postResponseWriter) Flush() {
	w.armWriteDeadline()
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		slog.Debug("failed to flush MCP response", "error", err)
	}
}

// WriteTimeout clears the server-level write deadline while an MCP POST is
// computing, then arms a fresh deadline when a non-streaming response begins.
// Qualifying SSE requests remain unbounded and are canceled through request
// contexts. Other requests are left untouched. The optional duration controls
// the response-write allowance and defaults to 30 seconds.
func WriteTimeout(endpointPath string, responseWriteTimeout ...time.Duration) func(http.Handler) http.Handler {
	writeTimeout := defaultResponseWriteTimeout
	if len(responseWriteTimeout) > 0 && responseWriteTimeout[0] > 0 {
		writeTimeout = responseWriteTimeout[0]
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isMCPPost := r.Method == http.MethodPost && r.URL.Path == endpointPath
			isMCPStream := r.Method == http.MethodGet &&
				strings.Contains(r.Header.Get("Accept"), "text/event-stream") &&
				r.URL.Path == endpointPath
			if isMCPPost || isMCPStream {
				rc := http.NewResponseController(w)
				if err := rc.SetWriteDeadline(time.Time{}); err != nil {
					slog.Warn("failed to clear write deadline for MCP request; request may be killed by server WriteTimeout",
						"error", err,
						"method", r.Method,
						"path", r.URL.Path,
						"remote", r.RemoteAddr,
					)
				}
			}
			if isMCPPost {
				w = &postResponseWriter{ResponseWriter: w, writeTimeout: writeTimeout}
			}
			next.ServeHTTP(w, r)
		})
	}
}
