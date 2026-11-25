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

// refuseDetailsRepeated is the logged reason of an ID-JAG request whose
// authorization_details parameter is repeated. The client gets invalid_request
// with the shared "must not be repeated" description, as on token exchange.
const refuseDetailsRepeated = "authorization_details_repeated"

// parseIDJAGDetails reads the authorization_details parameter of a request for
// an ID-JAG and runs the registry's shape checks. It returns a nil Pending when
// the parameter is absent or is one empty value. It reads nothing from the
// store, so decideIDJAG calls it before the grant is read.
//
// A value that is not a JSON array of objects gets malformed_entry, whether or
// not a registry is set; "[]" is left to grantIDJAGDetails, which refuses it
// as empty_grant. An ID-JAG carries the entries it is minted with,
// so a type this server has no hook for is refused, never ignored: with no
// registry, or with a type not registered for token exchange, the request
// gets unknown_type. A value that breaks the registry's generic rules gets
// malformed_entry. Both are
// invalid_authorization_details with the reason as the whole description, the
// codes ID-JAG redemption uses for the same causes. A repeated parameter,
// empty values included, is invalid_request.
func (h *RefreshTokenHandler) parseIDJAGDetails(
	ctx context.Context, form url.Values, log *slog.Logger,
) (*authorizationdetails.Pending, error) {
	raw, present, err := readDetailsParam(form)
	switch {
	case err != nil:
		// readDetailsParam's only error is errRepeatedDetailsParam.
		return nil, refuse(ctx, log, refuseDetailsRepeated, errorsx.WithStack(repeatedAuthorizationDetailsError()))
	case !present:
		return nil, nil
	}
	entries, ok := arrayOfObjects(raw)
	if !ok {
		return nil, refuse(ctx, log, reasonDetailsMalformedEntry, idJAGDetailsRefusal(reasonDetailsMalformedEntry))
	}
	err = authorizationdetails.ErrUnknownType
	var pending *authorizationdetails.Pending
	switch {
	case h.detailsRegistry != nil:
		pending, err = h.detailsRegistry.Parse(raw, oauthproto.GrantTypeTokenExchange)
	case len(entries) == 0:
		// "[]" names no type, so it goes on to be refused as empty_grant
		// whether or not a registry is set.
		pending, err = emptyDetails()
	}
	switch {
	case errors.Is(err, authorizationdetails.ErrUnknownType):
		// The types tell the operator which registration is missing; the
		// entries themselves are never logged.
		return nil, refuse(ctx, log.With("types", entryTypes(entries)), reasonDetailsUnknownType,
			idJAGDetailsRefusal(reasonDetailsUnknownType))
	case err != nil:
		return nil, refuse(ctx, log, reasonDetailsMalformedEntry, idJAGDetailsRefusal(reasonDetailsMalformedEntry))
	}
	return pending, nil
}

// grantIDJAGDetails asks the registered hooks which of the requested entries
// the ID-JAG carries. It returns the kept entries as a JSON array with the
// bytes the client sent, or nil when pending is nil.
//
// The hooks may only keep or drop whole entries (Pending.GrantSubset), as on
// ID-JAG redemption, so the entries AS-2 receives are the ones the client
// asked for, byte for byte. A type whose hook keeps none of its entries is
// dropped; when no entry is left at all, the request is refused with
// empty_grant, because an ID-JAG without the claim would yield an
// unrestricted token. "[]" is refused the same way. A hook's
// authorizationdetails.Error passes through with its code and reason; any
// other hook failure, or a hook that changes or adds an entry, is a
// server_error whose cause is never logged or sent.
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
		reason, clientErr := exchangeGrantDetailsError(err)
		return nil, refuse(ctx, d.log, reason, clientErr)
	}
	if subset.Entries == nil {
		// The dropped types are registered names, never free-form request text.
		return nil, refuse(ctx, d.log.With("types", subset.Dropped), reasonDetailsEmptyGrant,
			idJAGDetailsRefusal(reasonDetailsEmptyGrant))
	}
	return subset.Entries, nil
}

// arrayOfObjects splits raw into its entries. It reports false unless raw is
// one JSON array holding only objects; the empty array is accepted.
func arrayOfObjects(raw string) ([]json.RawMessage, bool) {
	var entries []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &entries); err != nil || entries == nil {
		return nil, false
	}
	for _, entry := range entries {
		if entry[0] != '{' {
			return nil, false
		}
	}
	return entries, true
}

// emptyDetails is the parsed form of "[]", which needs no registered type.
func emptyDetails() (*authorizationdetails.Pending, error) {
	registry, err := authorizationdetails.New(nil)
	if err != nil {
		return nil, err
	}
	return registry.Parse("[]", oauthproto.GrantTypeTokenExchange)
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
