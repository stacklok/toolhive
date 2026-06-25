// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// upstreamTokenCheckMiddleware returns HTTP middleware that verifies all
// upstream provider tokens required by the configured backends are present in
// the authenticated identity. It runs after the auth middleware (so the identity
// and its UpstreamTokens map are already populated) and before the mcp-go SDK
// handler (so a 401 can still be written as an HTTP response).
//
// When GetAllValidTokens drops a provider during identity enrichment (because
// the access token expired and the refresh token is missing or revoked), the
// provider key is absent from identity.UpstreamTokens. Without this middleware
// the missing token surfaces only when the outgoing auth strategy fires inside
// a tool call — at that point the response has already committed to HTTP 200 and
// the error reaches the MCP client as a generic JSON-RPC error with no re-auth
// signal. This middleware catches the condition at the HTTP boundary and returns
// HTTP 401 + WWW-Authenticate so the MCP client knows to re-authenticate.
//
// The registry is consulted on every request so that dynamic registry updates
// (backends added or removed at runtime) are reflected immediately.
//
// If no identity is found in the context (unauthenticated route, health probe)
// the middleware passes the request through unchanged.
func upstreamTokenCheckMiddleware(registry vmcp.BackendRegistry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := auth.IdentityFromContext(r.Context())
			if !ok {
				// No identity — unauthenticated route or health probe. Pass through.
				next.ServeHTTP(w, r)
				return
			}

			if missing := firstMissingProvider(r.Context(), registry, identity); missing != "" {
				writeUpstreamTokenRequired(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// firstMissingProvider scans all backends in the registry and returns the first
// provider name whose token is absent from the identity. Returns "" when all
// required provider tokens are present.
func firstMissingProvider(ctx context.Context, registry vmcp.BackendRegistry, identity *auth.Identity) string {
	for _, backend := range registry.List(ctx) {
		provider := upstreamProviderName(backend.AuthConfig)
		if provider == "" {
			continue
		}
		if identity.UpstreamTokens[provider] == "" {
			return provider
		}
	}
	return ""
}

// upstreamProviderName extracts the upstream provider name from an auth config
// for strategies that require an upstream IDP token. Returns "" for strategies
// that do not depend on an upstream provider (unauthenticated, header injection).
func upstreamProviderName(cfg *authtypes.BackendAuthStrategy) string {
	if cfg == nil {
		return ""
	}
	switch cfg.Type {
	case authtypes.StrategyTypeUpstreamInject:
		if cfg.UpstreamInject != nil {
			return cfg.UpstreamInject.ProviderName
		}
	case authtypes.StrategyTypeTokenExchange:
		if cfg.TokenExchange != nil {
			return cfg.TokenExchange.SubjectProviderName
		}
	case authtypes.StrategyTypeAwsSts:
		if cfg.AwsSts != nil {
			return cfg.AwsSts.SubjectProviderName
		}
	case authtypes.StrategyTypeOBO:
		if cfg.OBO != nil {
			return cfg.OBO.SubjectTokenProviderName
		}
	}
	return ""
}

// writeUpstreamTokenRequired writes an HTTP 401 response with a WWW-Authenticate
// Bearer challenge (RFC 6750 §3.1) signalling that the upstream IDP credential
// is no longer valid and the client must re-authenticate.
func writeUpstreamTokenRequired(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate",
		`Bearer error="invalid_token", error_description="upstream token is no longer valid; re-authentication required"`)
	http.Error(w, "upstream authentication required", http.StatusUnauthorized)
}
