// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package authserver provides a centralized OAuth 2.0 Authorization Server
// implementation using ory/fosite for issuing JWTs to clients.
//
// The auth server supports:
//   - OAuth 2.0 Authorization Code flow with PKCE (RFC 7636)
//   - Dynamic Client Registration (RFC 7591)
//   - Upstream IDP delegation (authenticates users via external IdP like Google, Okta)
//   - JWT access tokens with configurable lifespans
//   - OIDC discovery (/.well-known/openid-configuration)
//
// # Usage
//
// The primary entry point is CreateHandlersWithResult, which creates HTTP handlers
// for OAuth and well-known endpoints:
//
//	result, err := authserver.CreateHandlersWithResult(ctx, cfg, proxyPort)
//	if err != nil {
//	    return err
//	}
//	// Mount handlers on your HTTP server
//	mux.Handle("/oauth/", result.OAuthMux)
//	mux.Handle("/.well-known/", result.WellKnownMux)
//
// # Storage
//
// The auth server supports pluggable storage backends:
//   - In-memory storage (default, suitable for single-instance deployments)
//   - Redis storage (for distributed deployments)
//
// # IDP Token Storage
//
// When using upstream IDP delegation, tokens from the external IdP are stored
// and can be retrieved via the IDPTokenStorage interface for use by middleware
// (e.g., token swap middleware that replaces JWT auth with upstream tokens).
package authserver

import "net/http"

// HandlerResult contains the handlers and resources created by CreateHandlersWithResult.
type HandlerResult struct {
	// OAuthMux handles OAuth endpoints (/oauth/authorize, /oauth/token, /oauth/callback)
	OAuthMux http.Handler

	// WellKnownMux handles well-known endpoints (/.well-known/openid-configuration, /.well-known/jwks.json)
	WellKnownMux http.Handler

	// Storage is the storage instance (implements IDPTokenStorage)
	Storage Storage
}

// IDPTokenStorage returns the IDP token storage interface.
// This allows callers to access IDP token storage without coupling to the concrete Storage type.
func (r *HandlerResult) IDPTokenStorage() IDPTokenStorage {
	return r.Storage
}
