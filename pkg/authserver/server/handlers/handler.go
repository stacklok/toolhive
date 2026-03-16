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
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// Handler provides HTTP handlers for the OAuth authorization server endpoints.
type Handler struct {
	provider      fosite.OAuth2Provider
	config        *server.AuthorizationServerConfig
	storage       storage.Storage
	upstreams     map[string]upstream.OAuth2Provider
	upstreamOrder []string
	userResolver  *UserResolver
}

// NewHandler creates a new Handler with the given dependencies.
// upstreams maps logical provider names to their OAuth2Provider implementations.
// upstreamOrder defines the sequence in which upstream providers are consulted
// during multi-upstream authorization flows (e.g., sequential token acquisition).
//
// Panics if upstreamOrder is empty, or if any name in upstreamOrder does not
// exist in the upstreams map. These are programming errors caught at construction
// time; configuration validation must happen before calling NewHandler.
func NewHandler(
	provider fosite.OAuth2Provider,
	config *server.AuthorizationServerConfig,
	stor storage.Storage,
	upstreams map[string]upstream.OAuth2Provider,
	upstreamOrder []string,
) *Handler {
	if len(upstreamOrder) == 0 {
		panic("handlers: upstreamOrder must not be empty")
	}
	if len(upstreamOrder) != len(upstreams) {
		panic(fmt.Sprintf(
			"handlers: upstreamOrder length (%d) does not match upstreams map length (%d)",
			len(upstreamOrder), len(upstreams),
		))
	}
	for _, name := range upstreamOrder {
		p, ok := upstreams[name]
		if !ok {
			panic(fmt.Sprintf("handlers: upstream %q in upstreamOrder not found in upstreams map", name))
		}
		if p == nil {
			panic(fmt.Sprintf("handlers: upstream %q has nil provider", name))
		}
	}
	return &Handler{
		provider:      provider,
		config:        config,
		storage:       stor,
		upstreams:     upstreams,
		upstreamOrder: upstreamOrder,
		userResolver:  NewUserResolver(stor),
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
	r.Post("/oauth/token", h.TokenHandler)
	r.Post("/oauth/register", h.RegisterClientHandler)
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

// nextMissingUpstream returns the name of the next upstream provider in the
// authorization chain that does not yet have stored tokens for this session.
// Returns empty string if all upstreams are satisfied.
// Returns an error if the storage lookup fails.
func (h *Handler) nextMissingUpstream(ctx context.Context, sessionID string) (string, error) {
	stored, err := h.storage.GetAllUpstreamTokens(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to check upstream token state: %w", err)
	}
	for _, name := range h.upstreamOrder {
		if _, ok := stored[name]; !ok {
			return name, nil
		}
	}
	return "", nil
}
