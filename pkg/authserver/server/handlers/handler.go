// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	provider     fosite.OAuth2Provider
	config       *server.AuthorizationServerConfig
	storage      storage.Storage
	upstream     upstream.OAuth2Provider
	userResolver *UserResolver
}

// NewHandler creates a new Handler with the given dependencies.
func NewHandler(
	provider fosite.OAuth2Provider,
	config *server.AuthorizationServerConfig,
	stor storage.Storage,
	upstreamIDP upstream.OAuth2Provider,
) *Handler {
	return &Handler{
		provider:     provider,
		config:       config,
		storage:      stor,
		upstream:     upstreamIDP,
		userResolver: NewUserResolver(stor),
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
func (h *Handler) OAuthRoutes(r chi.Router) {
	r.Get("/oauth/authorize", h.AuthorizeHandler)
	r.Get("/oauth/callback", h.CallbackHandler)
	// TODO: Register remaining OAuth endpoint handlers here once implemented:
	// - POST /oauth/token     -> h.TokenHandler (token endpoint)
	// - POST /oauth/register -> h.RegisterClientHandler (RFC 7591 dynamic client registration)
}

// WellKnownRoutes registers well-known endpoints (JWKS, OAuth/OIDC discovery) on the provided router.
// Both discovery endpoints are registered per the MCP specification requirement to provide
// at least one discovery mechanism, with both supported for maximum interoperability:
// - /.well-known/oauth-authorization-server (RFC 8414) for OAuth-only clients
// - /.well-known/openid-configuration (OIDC Discovery 1.0) for OIDC clients
func (h *Handler) WellKnownRoutes(r chi.Router) {
	r.Get("/.well-known/jwks.json", h.JWKSHandler)
	r.Get("/.well-known/oauth-authorization-server", h.OAuthDiscoveryHandler)
	r.Get("/.well-known/openid-configuration", h.OIDCDiscoveryHandler)
}
