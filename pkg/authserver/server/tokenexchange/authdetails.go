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

// errRepeatedDetailsParam reports that the authorization_details parameter was
// sent more than once. Callers map it to their own error and log.
var errRepeatedDetailsParam = errors.New("authorization_details parameter repeated")

// readDetailsParam reads the shape of the authorization_details form parameter.
// present is false when the parameter is absent or is one empty value. More
// than one value, empty ones included, is errRepeatedDetailsParam.
func readDetailsParam(form url.Values) (raw string, present bool, err error) {
	values := form[authorizationDetailsParam]
	switch {
	case len(values) > 1:
		return "", false, errRepeatedDetailsParam
	case len(values) == 0, values[0] == "":
		return "", false, nil
	}
	return values[0], true, nil
}

// detailsSubject builds the hook Subject from validated claims. Subject is
// always the token's sub; resolvedUser is the local user the request resolved
// it to, empty when it resolved none. Audience and Claims are copied, so a
// hook cannot change the validated claims. The authorization_details claim is
// left out of Claims: its decoded copy has lost number precision, and a hook
// reads the entries from Request.Entries.
func detailsSubject(claims *ValidatedClaims, resolvedUser string) authorizationdetails.Subject {
	copied := deepCopyClaims(claims.Claims)
	delete(copied, authorizationDetailsParam)
	return authorizationdetails.Subject{
		Issuer:       claims.Issuer,
		Subject:      claims.Subject,
		ResolvedUser: resolvedUser,
		ClientID:     claims.ClientID,
		JWTID:        claims.JWTID,
		Audience:     slices.Clone(claims.Audience),
		IssuedAt:     claims.IssuedAt,
		Expiry:       claims.Expiry,
		External:     claims.ExternalIssuer != "",
		Claims:       copied,
	}
}

// parseAuthorizationDetails runs the early, cheap shape checks. It returns a
// nil Pending when the parameter is ignored: no registry is configured, or the
// parameter is absent or empty.
func (h *Handler) parseAuthorizationDetails(form url.Values, clientID string) (*authorizationdetails.Pending, error) {
	if h.authorizationDetailsRegistry == nil {
		return nil, nil
	}
	raw, present, err := readDetailsParam(form)
	// readDetailsParam's only error is errRepeatedDetailsParam.
	if err != nil {
		warnAuthorizationDetailsRejected(clientID, rejectRepeated)
		return nil, repeatedAuthorizationDetailsError()
	}
	if !present {
		return nil, nil
	}
	pending, err := h.authorizationDetailsRegistry.Parse(raw, oauthproto.GrantTypeTokenExchange)
	if err != nil {
		warnAuthorizationDetailsRejected(clientID, rejectMalformed)
		return nil, invalidAuthorizationDetailsError()
	}
	return pending, nil
}

// grantAuthorizationDetails asks the registered hooks what to grant and stores
// the granted JSON in the request form for PopulateTokenEndpointResponse.
func (*Handler) grantAuthorizationDetails(
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
		Subject:            detailsSubject(claims, ""),
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
	return grantFailure(context.Background(), clientID, err, func(string) slog.Level { return slog.LevelWarn })
}

// grantFailure maps a Grant or GrantSubset failure to an OAuth error and logs
// the rejection once, at the level that level returns for the logged reason.
func grantFailure(ctx context.Context, clientID string, err error, level func(reason string) slog.Level) error {
	var typed authorizationdetails.Error
	if errors.As(err, &typed) {
		slog.Log(ctx, level(rejectHookRejected), "authorization_details rejected",
			"client_id", clientID, "reason", rejectHookRejected,
			"code", string(typed.Code), "hook_reason", typed.Reason)
		return &fosite.RFC6749Error{
			ErrorField:       string(typed.Code),
			DescriptionField: typed.Reason,
			CodeField:        http.StatusBadRequest,
		}
	}
	slog.Log(ctx, level(rejectHookFailed), "authorization_details rejected",
		"client_id", clientID, "reason", rejectHookFailed)
	// The error text may hold internal data, so it is never logged or
	// returned to the client.
	return errorsx.WithStack(fosite.ErrServerError.WithDescription("The authorization_details could not be processed."))
}

func warnAuthorizationDetailsRejected(clientID, reason string) {
	slog.Warn("authorization_details rejected", "client_id", clientID, "reason", reason)
}

// repeatedAuthorizationDetailsError is returned when the authorization_details
// parameter appears more than once.
func repeatedAuthorizationDetailsError() *fosite.RFC6749Error {
	return &fosite.RFC6749Error{
		ErrorField:       string(authorizationdetails.CodeInvalidRequest),
		DescriptionField: "The authorization_details parameter must not be repeated.",
		CodeField:        http.StatusBadRequest,
	}
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
