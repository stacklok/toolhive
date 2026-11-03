// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/ory/fosite"

	oauthserver "github.com/stacklok/toolhive/pkg/authserver/server"
)

// refreshGrantValidators runs the refresh-grant validators as validators of
// the refresh_token grant: the built-in upstream refresh guard, when enabled,
// followed by the embedder-supplied token endpoint handlers
// (Config.RefreshGrantHandlers).
//
// All handlers live in one registered value because fosite's
// TokenEndpointHandlers.Append drops a handler whose concrete type is already
// registered: wrapping each handler separately would register only the first.
//
// Only the handlers' request phase (HandleTokenEndpointRequest) runs. The
// response phase is never delegated: by then the built-in refresh handler has
// already rotated the refresh token, so a handler error there would leave the
// client with no tokens and a dead refresh token, and a handler could rewrite
// the issued response. The type implements only fosite.TokenEndpointHandler,
// so the handlers are never registered for revocation, introspection or the
// authorize endpoint.
type refreshGrantValidators struct {
	handlers []fosite.TokenEndpointHandler
}

var _ fosite.TokenEndpointHandler = (*refreshGrantValidators)(nil)

// CanSkipClientAuth always returns false: an embedder's handler validates a
// grant the built-in refresh handler has already accepted and must never
// relax the client authentication that grant requires.
func (*refreshGrantValidators) CanSkipClientAuth(context.Context, fosite.AccessRequester) bool {
	return false
}

// CanHandleTokenEndpointRequest reports whether the request is a refresh_token
// grant.
func (*refreshGrantValidators) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return isRefreshTokenGrant(requester)
}

// HandleTokenEndpointRequest runs each handler that accepts the request, in
// order, and returns the first refusal. It runs after the built-in refresh
// handler has authenticated the client and loaded the refresh session, and
// before any handler populates the response, so a refusal issues no tokens
// and leaves the refresh token unrotated. A handler returning
// fosite.ErrUnknownRequest is treated as having nothing to say, matching how
// fosite itself treats that error.
func (v *refreshGrantValidators) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !v.CanHandleTokenEndpointRequest(ctx, requester) {
		return fosite.ErrUnknownRequest
	}
	for _, h := range v.handlers {
		if !h.CanHandleTokenEndpointRequest(ctx, requester) {
			continue
		}
		if err := h.HandleTokenEndpointRequest(ctx, requester); err != nil && !errors.Is(err, fosite.ErrUnknownRequest) {
			return err
		}
	}
	return nil
}

// PopulateTokenEndpointResponse never delegates to the handlers, so the seam
// can only refuse a grant, never alter or break an issued response. fosite
// calls it on every registered handler; ErrUnknownRequest tells fosite this
// handler contributes nothing.
func (*refreshGrantValidators) PopulateTokenEndpointResponse(
	context.Context, fosite.AccessRequester, fosite.AccessResponder,
) error {
	return fosite.ErrUnknownRequest
}

// isRefreshTokenGrant reports whether the request's sole grant type is
// refresh_token, matching the check fosite's built-in refresh handler makes.
func isRefreshTokenGrant(requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(string(fosite.GrantTypeRefreshToken))
}

// isNilHandler reports whether h is nil, including a nil pointer (or other
// nilable value) of a concrete type, which a plain h == nil misses and which
// would panic on the first refresh grant.
func isNilHandler(h fosite.TokenEndpointHandler) bool {
	if h == nil {
		return true
	}
	v := reflect.ValueOf(h)
	nilable := []reflect.Kind{reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface}
	return slices.Contains(nilable, v.Kind()) && v.IsNil()
}

// refreshGrantHandlerFactories returns the factory that registers the
// refresh-grant validators (see refreshGrantValidatorChain) with
// createProvider, or none when there are no validators.
func refreshGrantHandlerFactories(handlers []fosite.TokenEndpointHandler) []oauthserver.Factory {
	if len(handlers) == 0 {
		return nil
	}
	validators := &refreshGrantValidators{handlers: slices.Clone(handlers)}
	return []oauthserver.Factory{
		func(*oauthserver.AuthorizationServerConfig, fosite.Storage, any) (any, error) {
			return validators, nil
		},
	}
}
