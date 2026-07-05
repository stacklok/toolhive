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
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

// NamedUpstream pairs a logical provider name with its OAuth2Provider implementation.
// The name is used as the storage key and must be unique within the upstream slice.
type NamedUpstream struct {
	Name     string
	Provider upstream.OAuth2Provider
}

// Handler provides HTTP handlers for the OAuth authorization server endpoints.
type Handler struct {
	provider     fosite.OAuth2Provider
	config       *server.AuthorizationServerConfig
	storage      storage.Storage
	upstreams    []NamedUpstream
	userResolver *UserResolver
	// refresher, when set, lets nextMissingUpstream transparently refresh an
	// expired upstream leg during chain evaluation instead of re-prompting. Nil
	// when no refresher is configured; an expired leg is then treated as missing.
	refresher storage.UpstreamTokenRefresher
	// filter, when set, narrows the authorization chain to a subset of the
	// configured upstreams once the first leg resolves. Nil when no filter is
	// configured; the chain then walks all configured upstreams as before.
	filter UpstreamFilter
}

// UpstreamFilter narrows the authorization chain to a subset of the configured
// upstreams. It is consulted once in the callback handler, after the first
// upstream (upstreams[0]) resolves. The first upstream is always required: it is
// never passed to the filter and cannot be removed by it.
//
// FilterUpstreams receives the request context of the first leg's callback and
// the names of the non-first configured upstreams, in configured order. It
// returns the subset to keep. The handler preserves configured order and ignores
// any returned name that is not one of the non-first configured upstreams, so the
// filter cannot reorder the chain or introduce unknown providers. A returned
// error fails the authorization with a server error — the handler never falls
// back to walking every upstream on error.
type UpstreamFilter interface {
	FilterUpstreams(ctx context.Context, configured []string) ([]string, error)
}

// Option configures optional Handler behavior at construction time.
type Option func(*Handler)

// WithUpstreamRefresher injects a refresher used by nextMissingUpstream to
// transparently refresh expired upstream tokens while evaluating the
// authorization chain. When unset, an expired leg is treated as missing and the
// user is re-prompted — the behavior before this option existed.
func WithUpstreamRefresher(r storage.UpstreamTokenRefresher) Option {
	return func(h *Handler) {
		h.refresher = r
	}
}

// WithUpstreamFilter injects a filter that narrows the authorization chain to a
// subset of the configured upstreams once the first leg resolves. When unset, the
// handler walks all configured upstreams — the behavior before this option
// existed. See UpstreamFilter for the contract.
func WithUpstreamFilter(f UpstreamFilter) Option {
	return func(h *Handler) {
		h.filter = f
	}
}

// NewHandler creates a new Handler with the given dependencies.
// upstreams defines the ordered sequence of upstream providers consulted
// during multi-upstream authorization flows (e.g., sequential token acquisition).
//
// Returns an error if config is nil, if config's embedded *fosite.Config is
// nil, if upstreams is empty, or if any entry has an empty name or nil
// provider. Catching misconfiguration here is far easier to diagnose than
// a nil-deref panic deep inside an HTTP handler at request time.
func NewHandler(
	provider fosite.OAuth2Provider,
	config *server.AuthorizationServerConfig,
	stor storage.Storage,
	upstreams []NamedUpstream,
	opts ...Option,
) (*Handler, error) {
	if config == nil || config.Config == nil {
		return nil, fmt.Errorf(
			"handlers: AuthorizationServerConfig with embedded *fosite.Config must be non-nil")
	}
	if len(upstreams) == 0 {
		return nil, fmt.Errorf("handlers: upstreams must not be empty")
	}
	for _, u := range upstreams {
		if u.Name == "" {
			return nil, fmt.Errorf("handlers: upstream entry has empty name")
		}
		if u.Provider == nil {
			return nil, fmt.Errorf("handlers: upstream %q has nil provider", u.Name)
		}
	}
	h := &Handler{
		provider:     provider,
		config:       config,
		storage:      stor,
		upstreams:    upstreams,
		userResolver: NewUserResolver(stor),
	}
	for _, o := range opts {
		o(h)
	}
	return h, nil
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
//
// The wildcard variants (/.well-known/oauth-authorization-server/*) handle RFC 8414
// Section 3.1 path-based issuers, where clients insert /.well-known/ before the
// issuer's path component (e.g., /.well-known/oauth-authorization-server/inject-test
// for issuer https://example.com/inject-test).
func (h *Handler) WellKnownRoutes(r chi.Router) {
	r.Get("/.well-known/jwks.json", h.JWKSHandler)
	r.Get("/.well-known/oauth-authorization-server", h.OAuthDiscoveryHandler)
	r.Get("/.well-known/oauth-authorization-server/*", h.OAuthDiscoveryHandler)
	r.Get("/.well-known/openid-configuration", h.OIDCDiscoveryHandler)
	r.Get("/.well-known/openid-configuration/*", h.OIDCDiscoveryHandler)
}

// nextMissingUpstream returns the name of the next upstream provider in the
// authorization chain that the user must (re-)authenticate with for this session.
// It walks the provided chain — the effective, possibly filtered, ordered set of
// upstream names for this authorization (see computeChain) — rather than the raw
// configured list, so a leg the filter dropped is never prompted for. Returns
// empty string if all legs in the chain are satisfied, or an error if the storage
// lookup fails.
//
// A leg is satisfied when it has a stored token that is live (or asserts no
// expiry). A present-but-expired token is NOT treated as satisfied by presence
// alone: the leg is refreshed transparently (mirroring upstreamtoken
// InProcessService.GetAllUpstreamCredentials) and only counts as satisfied if the refresh
// succeeds. If refresh is impossible or fails, the leg is reported as missing so
// the user is re-prompted up front, rather than the stale token surfacing as a
// runtime auth error later at MCP-request token-swap time.
func (h *Handler) nextMissingUpstream(ctx context.Context, sessionID string, chain []string) (string, error) {
	stored, err := h.storage.GetAllUpstreamTokens(ctx, sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to check upstream token state: %w", err)
	}
	for _, name := range chain {
		tokens, ok := stored[name]
		if !ok || tokens == nil {
			// No token stored for this leg — prompt.
			return name, nil
		}
		// A live token (or one with no asserted expiry) satisfies the leg.
		if tokens.ExpiresAt.IsZero() || !tokens.IsExpired(time.Now()) {
			continue
		}
		// Expired — attempt a transparent refresh; prompt now if it can't be done.
		if !h.refreshExpiredLeg(ctx, sessionID, name, tokens) {
			return name, nil
		}
	}
	return "", nil
}

// computeChain returns the ordered, effective set of upstream names this
// authorization must walk. The first configured upstream always leads the chain
// and is never filtered out. When no filter is configured, the chain is the full
// configured list in order (the behavior before the filter hook existed). When a
// filter is configured, it is consulted with the names of the non-first upstreams
// (in configured order) and the chain becomes the first upstream plus the kept
// subset. Configured order is preserved and any returned name that is not a
// non-first configured upstream is ignored, so the filter can only narrow — never
// reorder or extend — the chain.
//
// A filter error is returned to the caller so the authorization fails cleanly; it
// never silently falls back to walking every upstream.
func (h *Handler) computeChain(ctx context.Context) ([]string, error) {
	chain := []string{h.upstreams[0].Name}
	rest := h.upstreams[1:]
	if len(rest) == 0 {
		return chain, nil
	}

	restNames := make([]string, len(rest))
	for i := range rest {
		restNames[i] = rest[i].Name
	}

	if h.filter == nil {
		return append(chain, restNames...), nil
	}

	keep, err := h.filter.FilterUpstreams(ctx, restNames)
	if err != nil {
		return nil, fmt.Errorf("upstream filter failed: %w", err)
	}

	keepSet := make(map[string]struct{}, len(keep))
	for _, name := range keep {
		keepSet[name] = struct{}{}
	}
	// Iterate configured order (not the filter's return order) so the chain
	// preserves the operator-defined sequence and silently drops any name the
	// filter returned that is not a non-first configured upstream.
	for i := range rest {
		if _, ok := keepSet[rest[i].Name]; ok {
			chain = append(chain, rest[i].Name)
		}
	}
	return chain, nil
}

// refreshExpiredLeg attempts to refresh an expired upstream token for one chain
// leg. It returns true when the leg can be treated as authenticated (refresh
// succeeded) and false when the user must be re-prompted: no refresher configured,
// no refresh token on the row, or the refresh itself failed (expired/revoked
// refresh token, provider error). Mirrors the refresh-then-classify behavior in
// upstreamtoken.InProcessService.GetAllUpstreamCredentials.
func (h *Handler) refreshExpiredLeg(
	ctx context.Context,
	sessionID, providerName string,
	expired *storage.UpstreamTokens,
) bool {
	if h.refresher == nil || expired.RefreshToken == "" {
		return false
	}
	// RefreshAndStore persists the refreshed token to storage as its side effect;
	// the token-swap path re-reads it from storage at MCP-request time, so the
	// returned *UpstreamTokens is intentionally discarded here.
	if _, err := h.refresher.RefreshAndStore(ctx, sessionID, expired); err != nil {
		slog.WarnContext(ctx, "upstream token refresh failed during chain evaluation; re-prompting",
			"session_id", sessionID,
			"provider", providerName,
			"error", err,
		)
		return false
	}
	slog.DebugContext(ctx, "refreshed expired upstream token during chain evaluation",
		"session_id", sessionID,
		"provider", providerName,
	)
	return true
}

// upstreamByName returns the upstream provider with the given name.
// It follows the (value, bool) convention: the second return value is false
// if no upstream with that name exists.
func (h *Handler) upstreamByName(name string) (upstream.OAuth2Provider, bool) {
	for i := range h.upstreams {
		if h.upstreams[i].Name == name {
			return h.upstreams[i].Provider, true
		}
	}
	return nil, false
}

// issuer returns the authorization-server issuer URL. NewHandler enforces
// that h.config and h.config.Config are non-nil, so this method does not
// re-validate.
func (h *Handler) issuer() string {
	return h.config.AccessTokenIssuer
}
