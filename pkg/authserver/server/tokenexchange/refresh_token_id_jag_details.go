// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// parseIDJAGDetails reads the authorization_details parameter of a request for
// an ID-JAG and runs the registry's shape checks. It returns a nil Pending when
// the parameter is absent or is one empty value. It reads nothing from the
// store, so decideIDJAG calls it before the grant is read.
//
// The registry's generic rules (one JSON array of objects, each with a string
// "type", within 64 KiB, no repeated member names) apply with or without a
// registry: without one the value is parsed by a registry that knows no type.
// A value that breaks them gets malformed_entry. An ID-JAG carries the entries
// it is minted with, so a type this server has no hook for is refused, never
// ignored: with no registry, or with a type not registered for token exchange,
// the request gets unknown_type. Both are invalid_authorization_details with
// the reason as the whole description, the codes ID-JAG redemption uses for
// the same causes. "[]" is left to grantIDJAGDetails, which refuses it as
// empty_grant. A repeated parameter, empty values included, is
// invalid_request.
func (h *RefreshTokenHandler) parseIDJAGDetails(
	ctx context.Context, form url.Values, log *slog.Logger,
) (*authorizationdetails.Pending, error) {
	raw, present, err := readDetailsParam(form)
	switch {
	case err != nil:
		// readDetailsParam's only error is errRepeatedDetailsParam.
		return nil, refuse(ctx, log, rejectRepeated, errorsx.WithStack(repeatedAuthorizationDetailsError()))
	case !present:
		return nil, nil
	}
	registry := h.detailsRegistry
	if registry == nil {
		registry = noDetailsTypes
	}
	pending, err := registry.Parse(raw, oauthproto.GrantTypeTokenExchange)
	switch {
	case errors.Is(err, authorizationdetails.ErrUnknownType):
		// Parse accepted the shape, so the value is a bounded array of objects.
		// The types tell the operator which registration is missing; the
		// entries themselves are never logged.
		var entries []json.RawMessage
		_ = json.Unmarshal([]byte(raw), &entries)
		return nil, refuse(ctx, log.With("types", entryTypes(entries)), reasonDetailsUnknownType,
			idJAGDetailsRefusal(reasonDetailsUnknownType))
	case err != nil:
		return nil, refuse(ctx, log, reasonDetailsMalformedEntry, idJAGDetailsRefusal(reasonDetailsMalformedEntry))
	}
	return pending, nil
}

// noDetailsTypes is the registry used when the embedder gave none: it knows no
// type, so it applies the generic shape rules and answers ErrUnknownType for
// any entry. New never fails for an empty map.
var noDetailsTypes, _ = authorizationdetails.New(nil)

// grantIDJAGDetails asks the registered hooks which of the requested entries
// the ID-JAG carries. It returns the kept entries as a JSON array with the
// bytes the client sent, or nil when pending is nil.
//
// The hooks may only keep or drop whole entries (Pending.GrantSubset), as on
// ID-JAG redemption, so the entries AS-2 receives are the ones the client
// asked for: mintIDJAG signs them in encoding/json form, which compacts them
// and escapes <, > and &, and keeps every digit of a number. A type whose hook keeps none of its entries is
// dropped; when no entry is left at all, the request is refused with
// empty_grant, because an ID-JAG without the claim would yield an
// unrestricted token. "[]" is refused the same way. A hook's
// authorizationdetails.Error passes through with its code and reason; any
// other hook error, a *fosite.RFC6749Error included, or a hook that changes or
// adds an entry, is a server_error whose cause is never logged or sent, as on
// the other two hook paths.
//
// The hook Request describes the grant behind the presented refresh token,
// which is opaque; Subject.Expiry is the earliest bound the grant mints
// under. GrantedAudience is the target issuer, the ID-JAG's aud.
// Exchange.RequestedTokenType is the ID-JAG type. For a client with an actor
// profile, the actor token is validated only after the hooks run, so
// Exchange.Actor is empty and ActorTokenPresent is true; for any other client
// Actor is the client ID, or the verified SPIFFE ID for a SPIFFE client, as on
// token exchange for an access token.
//
// decide runs in both handler phases, so the hooks run twice per request;
// they must be decide-only.
func grantIDJAGDetails(
	ctx context.Context, pending *authorizationdetails.Pending, d *idJAGDecision,
	client fosite.Client, grant *storage.ExchangeGrant,
) (json.RawMessage, error) {
	if pending == nil {
		return nil, nil
	}
	exchange := &authorizationdetails.ExchangeContext{
		Actor:              d.mint.RequestingClientID,
		RequestedTokenType: oauthproto.TokenTypeIDJAG,
	}
	if spiffeClient, ok := client.(*registration.AuthenticatedSPIFFEClient); ok {
		exchange.Actor = spiffeClient.SPIFFEID().String()
	}
	if d.actor != nil {
		exchange.Actor = ""
		exchange.ActorTokenPresent = true
	}
	subset, err := pending.GrantSubset(ctx, authorizationdetails.Request{
		ClientID:        d.mint.RequestingClientID,
		GrantedAudience: []string{d.mint.Target.Issuer},
		Subject: authorizationdetails.Subject{
			Issuer:   d.mint.Issuer,
			Subject:  grant.Subject,
			ClientID: grant.ClientID,
			IssuedAt: grant.CreatedAt,
			Expiry:   earliest(d.mint.NotAfter),
		},
		Exchange: exchange,
	})
	if err != nil {
		// Logged with the reasons and fields the other two hook paths use
		// (grantFailure); a failed hook's error text is never logged or sent.
		var typed authorizationdetails.Error
		if errors.As(err, &typed) {
			return nil, refuse(ctx, d.log.With("code", string(typed.Code), "hook_reason", typed.Reason),
				rejectHookRejected, errorsx.WithStack(exactReason(string(typed.Code), typed.Reason)))
		}
		return nil, refuse(ctx, d.log, rejectHookFailed, errorsx.WithStack(fosite.ErrServerError.WithDescription(
			"The authorization_details could not be processed.")))
	}
	if subset.Entries == nil {
		// The dropped types are registered names, never free-form request text.
		return nil, refuse(ctx, d.log.With("types", subset.Dropped), reasonDetailsEmptyGrant,
			idJAGDetailsRefusal(reasonDetailsEmptyGrant))
	}
	return subset.Entries, nil
}

// earliest returns the earliest non-zero time, or the zero time for none.
func earliest(times []time.Time) time.Time {
	var out time.Time
	for _, t := range times {
		if !t.IsZero() && (out.IsZero() || t.Before(out)) {
			out = t
		}
	}
	return out
}

// idJAGDetailsRefusal is invalid_authorization_details with reason as the whole
// error_description.
func idJAGDetailsRefusal(reason string) error {
	return errorsx.WithStack(exactReason(string(authorizationdetails.CodeInvalidAuthorizationDetails), reason))
}
