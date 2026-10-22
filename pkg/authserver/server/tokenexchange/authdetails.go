// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const authorizationDetailsParam = "authorization_details"

// Reason codes logged when authorization_details is rejected. They are fixed
// strings: neither the payload nor a hook's error text is ever logged at WARN.
const (
	rejectMalformed    = "malformed"
	rejectRepeated     = "repeated"
	rejectHookRejected = "hook_rejected"
	rejectHookFailed   = "hook_failed"
)

// parseAuthorizationDetails runs the early, cheap shape checks. It returns a
// nil Pending when the parameter is ignored: no registry is configured, or the
// parameter is absent or empty.
func (h *Handler) parseAuthorizationDetails(form url.Values, clientID string) (*authorizationdetails.Pending, error) {
	if h.authorizationDetailsRegistry == nil {
		return nil, nil
	}
	values := form[authorizationDetailsParam]
	if len(values) == 0 || (len(values) == 1 && values[0] == "") {
		return nil, nil
	}
	if len(values) > 1 {
		warnAuthorizationDetailsRejected(clientID, rejectRepeated)
		return nil, &fosite.RFC6749Error{
			ErrorField:       string(authorizationdetails.CodeInvalidRequest),
			DescriptionField: "The authorization_details parameter must not be repeated.",
			CodeField:        http.StatusBadRequest,
		}
	}
	pending, err := h.authorizationDetailsRegistry.Parse(values[0], oauthproto.GrantTypeTokenExchange)
	if err != nil {
		warnAuthorizationDetailsRejected(clientID, rejectMalformed)
		return nil, invalidAuthorizationDetailsError()
	}
	return pending, nil
}

// grantAuthorizationDetails asks the registered hooks what to grant and stores
// the granted JSON in the request form for PopulateTokenEndpointResponse.
func (h *Handler) grantAuthorizationDetails(
	ctx context.Context,
	pending *authorizationdetails.Pending,
	requester fosite.AccessRequester,
	params *formParams,
	claims *ValidatedClaims,
	actorSub string,
) error {
	form := requester.GetRequestForm()
	clientID := requester.GetClient().GetID()

	granted, err := pending.Grant(ctx, authorizationdetails.Request{
		ClientID:           clientID,
		Actor:              actorSub,
		ActorTokenPresent:  params.actorToken != "",
		RequestedTokenType: form.Get("requested_token_type"),
		RequestedScopes:    slices.Clone([]string(requester.GetRequestedScopes())),
		GrantedAudience:    slices.Clone([]string(requester.GetGrantedAudience())),
		Subject: authorizationdetails.Subject{
			Issuer:   claims.Issuer,
			Subject:  claims.Subject,
			ClientID: claims.ClientID,
			JWTID:    claims.JWTID,
			Audience: slices.Clone(claims.Audience),
			IssuedAt: claims.IssuedAt,
			Expiry:   claims.Expiry,
			External: claims.ExternalIssuer != "",
			Claims:   deepCopyClaims(claims.Claims),
		},
	})
	if err != nil {
		return authorizationDetailsGrantError(clientID, err)
	}

	// The form is dropped by fosite's Sanitize before the request is stored,
	// so the granted JSON reaches only this request's response.
	form.Set(authorizationDetailsParam, string(granted))
	return nil
}

// echoAuthorizationDetails adds the granted authorization_details to the
// response. The value is the granted JSON that grantAuthorizationDetails
// stored in the form; without a registry the parameter is never echoed.
func (h *Handler) echoAuthorizationDetails(requester fosite.AccessRequester, responder fosite.AccessResponder) {
	if h.authorizationDetailsRegistry == nil {
		return
	}
	if granted := requester.GetRequestForm().Get(authorizationDetailsParam); granted != "" {
		responder.SetExtra(authorizationDetailsParam, json.RawMessage(granted))
	}
}

// authorizationDetailsGrantError maps a Grant failure to an OAuth error and
// logs the rejection once at WARN.
func authorizationDetailsGrantError(clientID string, err error) error {
	var typed authorizationdetails.Error
	switch {
	case errors.As(err, &typed):
		slog.Warn("authorization_details rejected",
			"client_id", clientID, "reason", rejectHookRejected,
			"code", string(typed.Code), "hook_reason", typed.Reason)
		return &fosite.RFC6749Error{
			ErrorField:       string(typed.Code),
			DescriptionField: typed.Reason,
			CodeField:        http.StatusBadRequest,
		}
	default:
		warnAuthorizationDetailsRejected(clientID, rejectHookFailed)
		// The error text may hold internal data, so it is never logged or
		// returned to the client.
		return errorsx.WithStack(fosite.ErrServerError.WithDescription("The authorization_details could not be processed."))
	}
}

func warnAuthorizationDetailsRejected(clientID, reason string) {
	slog.Warn("authorization_details rejected", "client_id", clientID, "reason", reason)
}

func invalidAuthorizationDetailsError() *fosite.RFC6749Error {
	return &fosite.RFC6749Error{
		ErrorField:       string(authorizationdetails.CodeInvalidAuthorizationDetails),
		DescriptionField: "The authorization_details parameter is invalid.",
		CodeField:        http.StatusBadRequest,
	}
}

// deepCopyClaims copies a JSON-decoded claim map so a hook cannot modify the
// validated claims. Scalars are immutable; only maps and slices are cloned.
func deepCopyClaims(claims map[string]any) map[string]any {
	if claims == nil {
		return nil
	}
	out := make(map[string]any, len(claims))
	for key, value := range claims {
		out[key] = deepCopyValue(value)
	}
	return out
}

func deepCopyValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return deepCopyClaims(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = deepCopyValue(item)
		}
		return out
	default:
		return value
	}
}
