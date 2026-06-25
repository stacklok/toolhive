// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package middleware

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
)

const (
	// HeaderUserSub is the HTTP header name for forwarding the authenticated user's subject claim.
	// Backend MCP servers can read this header to identify the user without calling /introspect.
	HeaderUserSub = "X-User-Sub"
	// HeaderUserEmail is the HTTP header name for forwarding the authenticated user's email claim.
	HeaderUserEmail = "X-User-Email"
	// HeaderUserName is the HTTP header name for forwarding the authenticated user's name claim.
	HeaderUserName = "X-User-Name"
)

// NewClaimInjectionMiddleware returns a middleware that extracts user identity from the
// request context (populated by auth middleware) and injects it as HTTP headers into the
// forwarded request. This allows backend MCP servers to receive user identity without
// needing to implement their own OAuth token validation or /introspect calls.
//
// Headers injected (when identity is present):
//   - X-User-Sub:   the 'sub' claim (Google/OIDC user ID, always present)
//   - X-User-Email: the 'email' claim (if available in token)
//   - X-User-Name:  the 'name' claim (if available in token)
//
// This middleware is safe to add unconditionally: if no identity is present in context
// (e.g., anonymous request), no headers are injected.
func NewClaimInjectionMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := auth.IdentityFromContext(r.Context())
			if ok && identity != nil {
				// Clone request to avoid modifying the original
				r = r.Clone(r.Context())
				if identity.Subject != "" {
					r.Header.Set(HeaderUserSub, identity.Subject)
				}
				if identity.Email != "" {
					r.Header.Set(HeaderUserEmail, identity.Email)
				}
				if identity.Name != "" {
					r.Header.Set(HeaderUserName, identity.Name)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
