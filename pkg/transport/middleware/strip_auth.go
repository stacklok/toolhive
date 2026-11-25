// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// StripAuthMiddlewareName is the type constant for the credential stripping middleware.
const StripAuthMiddlewareName = "strip-auth"

// clientCredentialHeaders are the request headers removed by the strip-auth
// middleware before a request is forwarded to the backend. Authorization
// carries the ToolHive JWT; Cookie and Proxy-Authorization are the other
// headers a client can use to carry credentials. The set mirrors the headers
// net/http refuses to copy across cross-host redirects.
var clientCredentialHeaders = []string{"Authorization", "Cookie", "Proxy-Authorization"}

// StripAuthMiddleware removes client credential headers from requests so they
// never reach the backend. It is used when clients are authenticated by the
// proxy but the backend itself is public (DisableUpstreamTokenInjection): by
// the time this middleware runs, the auth middleware has already validated
// the client JWT and stored the identity in the request context, so the
// backend receives an unauthenticated request. Credentials injected by
// middlewares that run closer to the backend (e.g. header-forward) are
// unaffected.
type StripAuthMiddleware struct{}

// Handler returns the middleware function.
func (*StripAuthMiddleware) Handler() types.MiddlewareFunction {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, h := range clientCredentialHeaders {
				r.Header.Del(h)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Close cleans up resources. The middleware holds none.
func (*StripAuthMiddleware) Close() error { return nil }

// CreateStripAuthMiddleware is the factory function for the strip-auth
// middleware. It takes no parameters.
func CreateStripAuthMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	runner.AddMiddleware(config.Type, &StripAuthMiddleware{})
	return nil
}
