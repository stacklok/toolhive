// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package deviceflow implements the RFC 8628 OAuth 2.0 Device Authorization
// Grant's token-endpoint handler.
package deviceflow

import (
	"context"
	"errors"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	authstorage "github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Compile-time check that Handler implements fosite.TokenEndpointHandler.
var _ fosite.TokenEndpointHandler = (*Handler)(nil)

// deviceFlowConfig is the narrow config surface this handler needs. The
// embedded *fosite.Config on server.AuthorizationServerConfig satisfies this
// (its own no-context adapter methods of the same name would otherwise
// shadow the ctx-taking ones fosite's provider interfaces require — see
// tokenexchange.Factory's identical concern).
type deviceFlowConfig interface {
	fosite.AccessTokenLifespanProvider
	fosite.RefreshTokenLifespanProvider
}

// Handler implements fosite's TokenEndpointHandler for RFC 8628's
// urn:ietf:params:oauth:grant-type:device_code grant.
//
// A device_code is single-use: HandleTokenEndpointRequest deletes it from
// DeviceStorage as soon as it observes DeviceRequestStatusAuthorized, before
// PopulateTokenEndpointResponse ever issues a token, so a device_code can
// never be redeemed twice.
type Handler struct {
	DeviceStorage authstorage.DeviceCodeStorage
	CoreStorage   oauth2.CoreStorage
	Strategy      oauth2.CoreStrategy
	Config        deviceFlowConfig
	// MinInterval is the minimum time a client must wait between polls
	// (RFC 8628 Section 3.5). A poll that arrives sooner is rejected with
	// slow_down.
	MinInterval time.Duration
}

// CanHandleTokenEndpointRequest returns true if the request's grant_type is
// the RFC 8628 device_code grant type.
func (*Handler) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(oauthproto.GrantTypeDeviceCode)
}

// CanSkipClientAuth always returns false: the device_code grant does not
// exempt the client from standard authentication. The client authenticates
// with whatever method it registered with, exactly as for every other grant;
// only grants that attach a synthetic, unregistered client (see
// storage.NewSyntheticClient) skip authentication, and this is not one of
// them.
func (*Handler) CanSkipClientAuth(_ context.Context, _ fosite.AccessRequester) bool {
	return false
}

// HandleTokenEndpointRequest validates the device_code, enforces the RFC
// 8628 polling contract (slow_down, authorization_pending, access_denied,
// expired_token), and — once the device request has been authorized —
// attaches a session and consumes (deletes) the device_code so it cannot be
// redeemed twice.
func (h *Handler) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}

	client := requester.GetClient()
	if !client.GetGrantTypes().Has(oauthproto.GrantTypeDeviceCode) {
		return errorsx.WithStack(fosite.ErrUnauthorizedClient.WithHint(
			"The OAuth 2.0 Client is not allowed to use authorization grant 'urn:ietf:params:oauth:grant-type:device_code'."))
	}

	deviceCode := requester.GetRequestForm().Get("device_code")
	if deviceCode == "" {
		return errorsx.WithStack(fosite.ErrInvalidRequest.WithHint("The device_code parameter is missing."))
	}

	device, err := h.DeviceStorage.LoadDeviceRequestByDeviceCode(ctx, deviceCode)
	switch {
	case errors.Is(err, authstorage.ErrNotFound):
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The device_code is unknown or has already been redeemed."))
	case errors.Is(err, authstorage.ErrExpired):
		return errorsx.WithStack(ErrExpiredToken)
	case err != nil:
		return errorsx.WithStack(fosite.ErrServerError.WithWrap(err))
	}

	// A device_code presented by a client other than the one it was issued to
	// is treated identically to an unknown code: revealing that the code
	// exists but belongs to someone else would leak information to a client
	// guessing at codes.
	if device.ClientID != client.GetID() {
		return errorsx.WithStack(fosite.ErrInvalidGrant.WithHint(
			"The device_code is unknown or has already been redeemed."))
	}

	if err := h.enforcePollInterval(ctx, device, deviceCode); err != nil {
		return err
	}

	switch device.Status {
	case authstorage.DeviceRequestStatusPending:
		return errorsx.WithStack(ErrAuthorizationPending)
	case authstorage.DeviceRequestStatusDenied:
		return errorsx.WithStack(fosite.ErrAccessDenied)
	case authstorage.DeviceRequestStatusAuthorized:
		// Proceed below.
	default:
		return errorsx.WithStack(fosite.ErrServerError.WithHintf("unrecognized device request status %q", device.Status))
	}

	h.attachSession(ctx, requester, client, device)

	// Consume the device_code now, before any token is issued, so a second
	// concurrent or replayed request for the same code always fails at the
	// LoadDeviceRequestByDeviceCode lookup above rather than racing
	// PopulateTokenEndpointResponse into issuing two tokens for one grant.
	if err := h.DeviceStorage.DeleteDeviceRequest(ctx, deviceCode); err != nil {
		return errorsx.WithStack(fosite.ErrServerError.WithWrap(err))
	}

	return nil
}

// attachSession builds and attaches the session for an authorized device
// request: identity, granted scopes/audience, and access/refresh token
// expiry (mirroring fosite's own authorization-code grant handler).
func (h *Handler) attachSession(
	ctx context.Context, requester fosite.AccessRequester, client fosite.Client, device *authstorage.DeviceRequest,
) {
	sess := session.New(device.ResolvedUserID, device.SessionID, device.ClientID, session.UserClaims{
		Name:  device.ResolvedUserName,
		Email: device.ResolvedUserEmail,
	})
	requester.SetSession(sess)

	for _, scope := range device.Scopes {
		requester.GrantScope(scope)
	}
	for _, aud := range device.Audience {
		requester.GrantAudience(aud)
	}

	deviceGrant := fosite.GrantType(oauthproto.GrantTypeDeviceCode)
	atLifespan := fosite.GetEffectiveLifespan(client, deviceGrant, fosite.AccessToken, h.Config.GetAccessTokenLifespan(ctx))
	sess.SetExpiresAt(fosite.AccessToken, time.Now().UTC().Add(atLifespan).Round(time.Second))

	if client.GetGrantTypes().Has(oauthproto.GrantTypeRefreshToken) {
		rtLifespan := fosite.GetEffectiveLifespan(client, deviceGrant, fosite.RefreshToken, h.Config.GetRefreshTokenLifespan(ctx))
		sess.SetExpiresAt(fosite.RefreshToken, time.Now().UTC().Add(rtLifespan).Round(time.Second))
	}
}

// enforcePollInterval applies RFC 8628 Section 3.5's minimum polling
// interval. It records this poll's timestamp unconditionally — including on
// the slow_down path — so a client polling faster than MinInterval cannot
// reset its own window by polling again before the interval elapses.
func (h *Handler) enforcePollInterval(ctx context.Context, device *authstorage.DeviceRequest, deviceCode string) error {
	tooSoon := !device.LastPolledAt.IsZero() && time.Since(device.LastPolledAt) < h.MinInterval
	if err := h.DeviceStorage.UpdateDeviceRequestLastPolledAt(ctx, deviceCode, time.Now()); err != nil {
		return errorsx.WithStack(fosite.ErrServerError.WithWrap(err))
	}
	if tooSoon {
		return errorsx.WithStack(ErrSlowDown)
	}
	return nil
}

// PopulateTokenEndpointResponse issues the access token and, when the client
// is registered for the refresh_token grant, a refresh token.
func (h *Handler) PopulateTokenEndpointResponse(
	ctx context.Context, requester fosite.AccessRequester, responder fosite.AccessResponder,
) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}

	access, accessSignature, err := h.Strategy.GenerateAccessToken(ctx, requester)
	if err != nil {
		return errorsx.WithStack(fosite.ErrServerError.WithWrap(err))
	}

	var refresh, refreshSignature string
	if requester.GetClient().GetGrantTypes().Has(oauthproto.GrantTypeRefreshToken) {
		refresh, refreshSignature, err = h.Strategy.GenerateRefreshToken(ctx, requester)
		if err != nil {
			return errorsx.WithStack(fosite.ErrServerError.WithWrap(err))
		}
	}

	if err := h.CoreStorage.CreateAccessTokenSession(ctx, accessSignature, requester.Sanitize([]string{})); err != nil {
		return errorsx.WithStack(fosite.ErrServerError.WithWrap(err))
	}
	if refreshSignature != "" {
		refreshReq := requester.Sanitize([]string{})
		if err := h.CoreStorage.CreateRefreshTokenSession(ctx, refreshSignature, accessSignature, refreshReq); err != nil {
			return errorsx.WithStack(fosite.ErrServerError.WithWrap(err))
		}
	}

	deviceGrant := fosite.GrantType(oauthproto.GrantTypeDeviceCode)
	atLifespan := fosite.GetEffectiveLifespan(
		requester.GetClient(), deviceGrant, fosite.AccessToken, h.Config.GetAccessTokenLifespan(ctx))
	responder.SetAccessToken(access)
	responder.SetTokenType("bearer")
	responder.SetExpiresIn(expiresIn(requester, fosite.AccessToken, atLifespan))
	responder.SetScopes(requester.GetGrantedScopes())
	if refresh != "" {
		responder.SetExtra("refresh_token", refresh)
	}

	return nil
}

// expiresIn mirrors fosite's own unexported helper of the same purpose
// (handler/oauth2/helper.go): it reports the session's actual expiry when
// one was set (HandleTokenEndpointRequest always sets one for AccessToken),
// falling back to defaultLifespan otherwise.
func expiresIn(r fosite.Requester, tokenType fosite.TokenType, defaultLifespan time.Duration) time.Duration {
	expiresAt := r.GetSession().GetExpiresAt(tokenType)
	if expiresAt.IsZero() {
		return defaultLifespan
	}
	return time.Until(expiresAt)
}
