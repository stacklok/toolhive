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
// If authInfoHandler is nil, the returned handler responds with HTTP 404 and a
// JSON body for all /.well-known/ paths. This ensures OAuth discovery clients
// (e.g., Claude Code) receive a clean, parseable "not found" instead of falling
// through to the MCP handler, which would reject the GET with an HTTP 406
// JSON-RPC error that breaks OAuth error parsing.
//
// Usage:
//
//	authInfoHandler := auth.NewAuthInfoHandler(issuer, resourceURL, scopes)
//	wellKnownHandler := auth.NewWellKnownHandler(authInfoHandler)
//	mux.Handle("/.well-known/", wellKnownHandler)
//
// This handler matches:
//   - /.well-known/oauth-protected-resource (exact)
//   - /.well-known/oauth-protected-resource/* (subpaths)
//
// Returns 404 for other /.well-known/* paths or when auth is not configured.
func NewWellKnownHandler(authInfoHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// When auth is configured, route discovery requests to the auth handler.
		if authInfoHandler != nil &&
			strings.HasPrefix(r.URL.Path, oauthproto.WellKnownOAuthResourcePath) {
			authInfoHandler.ServeHTTP(w, r)
			return
		}

		// No auth configured, or unknown .well-known path — return JSON 404
		// so OAuth discovery clients can parse the response cleanly.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}`))
	})
}
