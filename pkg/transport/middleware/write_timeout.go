// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// WriteTimeout clears the write deadline for qualifying SSE connections
// (GET + Accept: text/event-stream + matching path) so http.Server.WriteTimeout
// does not kill long-lived streams (golang/go#16100). All other requests are
// left untouched.
func WriteTimeout(endpointPath string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet &&
				strings.Contains(r.Header.Get("Accept"), "text/event-stream") &&
				r.URL.Path == endpointPath {
				rc := http.NewResponseController(w)
				if err := rc.SetWriteDeadline(time.Time{}); err != nil {
					slog.Warn("failed to clear write deadline for SSE connection; stream may be killed by server WriteTimeout",
						"error", err,
						"method", r.Method,
						"path", r.URL.Path,
						"remote", r.RemoteAddr,
					)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
