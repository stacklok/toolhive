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

package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// Handler provides HTTP handlers for the OAuth authorization server endpoints.
type Handler struct {
	provider fosite.OAuth2Provider
	config   *server.AuthorizationServerConfig
	storage  storage.Storage
	upstream upstream.OAuth2Provider
}

// NewHandler creates a new Handler with the given dependencies.
// The upstream IDP provider is required for the auth server to function.
func NewHandler(
	provider fosite.OAuth2Provider,
	config *server.AuthorizationServerConfig,
	stor storage.Storage,
	upstreamIDP upstream.OAuth2Provider,
) *Handler {
	return &Handler{
		provider: provider,
		config:   config,
		storage:  stor,
		upstream: upstreamIDP,
	}
}

// Routes returns a router with all OAuth/OIDC endpoints registered.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	h.OAuthRoutes(r)
	h.WellKnownRoutes(r)
	return r
}

// OAuthRoutes registers OAuth endpoints (authorize, callback, token, register) on the provided router.
func (*Handler) OAuthRoutes(_ chi.Router) {
	// TODO: Register OAuth endpoint handlers here once implemented:
	// - GET /oauth/authorize  -> h.AuthorizeHandler (initiates OAuth flow)
	// - GET /oauth/callback   -> h.CallbackHandler (receives upstream IDP callback)
	// - POST /oauth/token     -> h.TokenHandler (token endpoint)
	// - POST /oauth/register -> h.RegisterClientHandler (RFC 7591 dynamic client registration)
}

// WellKnownRoutes registers well-known endpoints (JWKS, OIDC discovery) on the provided router.
func (h *Handler) WellKnownRoutes(r chi.Router) {
	r.Get("/.well-known/jwks.json", h.JWKSHandler)
	r.Get("/.well-known/openid-configuration", h.OIDCDiscoveryHandler)
}
