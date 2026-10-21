// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"

	"github.com/ory/fosite"

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
)

// refreshGrantOnlyHandler confines an embedder-supplied token endpoint handler
// (Config.RefreshGrantHandlers) to validating the refresh_token grant.
//
// Only the inner handler's request phase (HandleTokenEndpointRequest) runs.
// The response phase is never delegated: by then the built-in refresh handler
// has already rotated the refresh token, so an inner error there would leave
// the client with no tokens and a dead refresh token, and an inner handler
// could rewrite the issued response. The type implements only
// fosite.TokenEndpointHandler, so the handler is never registered for
// revocation, introspection or the authorize endpoint.
type refreshGrantOnlyHandler struct {
	inner fosite.TokenEndpointHandler
}

var _ fosite.TokenEndpointHandler = (*refreshGrantOnlyHandler)(nil)

// CanSkipClientAuth always returns false: an embedder's handler validates a
// grant the built-in refresh handler has already accepted and must never
// relax the client authentication that grant requires.
func (*refreshGrantOnlyHandler) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return false
}

// CanHandleTokenEndpointRequest reports whether the request is a refresh_token
// grant the inner handler accepts.
func (h *refreshGrantOnlyHandler) CanHandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) bool {
	return isRefreshTokenGrant(requester) && h.inner.CanHandleTokenEndpointRequest(ctx, requester)
}

// HandleTokenEndpointRequest delegates to the inner handler for a refresh_token
// grant. It runs after the built-in refresh handler has authenticated the
// client and loaded the refresh session, and before any handler populates the
// response, so an error here issues no tokens and leaves the refresh token
// unrotated.
func (h *refreshGrantOnlyHandler) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return fosite.ErrUnknownRequest
	}
	return h.inner.HandleTokenEndpointRequest(ctx, requester)
}

// PopulateTokenEndpointResponse never delegates to the inner handler, so the
// seam can only refuse a grant, never alter or break an issued response.
// fosite calls it on every registered handler; ErrUnknownRequest tells fosite
// this handler contributes nothing.
func (*refreshGrantOnlyHandler) PopulateTokenEndpointResponse(
	context.Context, fosite.AccessRequester, fosite.AccessResponder,
) error {
	return fosite.ErrUnknownRequest
}

// isRefreshTokenGrant reports whether the request's sole grant type is
// refresh_token, matching the check fosite's built-in refresh handler makes.
func isRefreshTokenGrant(requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(string(fosite.GrantTypeRefreshToken))
}

// refreshGrantHandlerFactories wraps each embedder handler so it runs only on
// the refresh_token grant, as a factory for createProvider.
func refreshGrantHandlerFactories(handlers []fosite.TokenEndpointHandler) []oauthserver.Factory {
	factories := make([]oauthserver.Factory, 0, len(handlers))
	for _, h := range handlers {
		wrapped := &refreshGrantOnlyHandler{inner: h}
		factories = append(factories, func(*oauthserver.AuthorizationServerConfig, fosite.Storage, any) (any, error) {
			return wrapped, nil
		})
	}
	return factories
}
