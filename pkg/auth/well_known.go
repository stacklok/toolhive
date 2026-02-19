// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package auth provides authentication and authorization utilities.
package auth

import (
	"net/http"
	"strings"

	oauthproto "github.com/stacklok/toolhive/pkg/oauth"
)

// NewWellKnownHandler creates an HTTP handler that routes requests under the
// /.well-known/ path space to the appropriate handler.
//
// Per RFC 9728, the /.well-known/oauth-protected-resource endpoint and any subpaths
// under it must be accessible without authentication. This handler ensures proper
// routing of discovery requests while returning 404 for unknown paths.
//
// If authInfoHandler is nil, this function returns nil (no handler registration needed).
//
// Usage:
//
//	authInfoHandler := auth.NewAuthInfoHandler(issuer, resourceURL, scopes)
//	wellKnownHandler := auth.NewWellKnownHandler(authInfoHandler)
//	if wellKnownHandler != nil {
//	    mux.Handle("/.well-known/", wellKnownHandler)
//	}
//
// This handler matches:
//   - /.well-known/oauth-protected-resource (exact)
//   - /.well-known/oauth-protected-resource/* (subpaths)
//
// Returns 404 for other /.well-known/* paths.
func NewWellKnownHandler(authInfoHandler http.Handler) http.Handler {
	if authInfoHandler == nil {
		return nil
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// RFC 9728: Match /.well-known/oauth-protected-resource and any subpaths
		// Examples:
		//   ✓ /.well-known/oauth-protected-resource
		//   ✓ /.well-known/oauth-protected-resource/mcp
		//   ✗ /.well-known/other-endpoint
		if strings.HasPrefix(r.URL.Path, oauthproto.WellKnownOAuthResourcePath) {
			authInfoHandler.ServeHTTP(w, r)
			return
		}

		// Unknown .well-known path
		http.NotFound(w, r)
	})
}
