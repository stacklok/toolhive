// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// exchangeGrantType is the authorization_details type that asks the server to
// issue a refresh token through token exchange. An entry looks like
//
//	{"type": "https://toolhive.dev/exchange_grant",
//	 "issuable_token_types": ["<one token-type URN>"],
//	 "expires_at": 1893456000}
//
// where expires_at is optional and holds integer seconds since the epoch. The
// entry carries no target: whoever later consumes the grant limits audience and
// resource by client policy.
const exchangeGrantType = "https://toolhive.dev/exchange_grant"

// Entry members. Any other member, including "locations", is refused.
const (
	exchangeGrantMemberType      = "type"
	exchangeGrantMemberTypes     = "issuable_token_types"
	exchangeGrantMemberExpiresAt = "expires_at"
)

// Reasons returned to the client with invalid_authorization_details. They are
// fixed strings and never carry request data.
const (
	reasonSingleEntry           = "single_entry_required"
	reasonMalformedEntry        = "malformed_entry"
	reasonIssuableTypes         = "invalid_issuable_token_types"
	reasonTokenTypeNotPermitted = "token_type_not_permitted"
	reasonExpiresAt             = "invalid_expires_at"
	reasonParentExpired         = "parent_expired"
)

// exchangeGrantEntry is the decoded form of an exchange-grant entry.
type exchangeGrantEntry struct {
	Type               string   `json:"type"`
	IssuableTokenTypes []string `json:"issuable_token_types"`
	ExpiresAt          *int64   `json:"expires_at,omitempty"`
}

// newExchangeGrantRegistry returns a registry holding only the exchange-grant
// type. It is used only by the refresh-token handler, so the embedder's
// registry is never consulted and this type is never accepted on the
// access-token path. It is immutable and safe for concurrent use.
func newExchangeGrantRegistry() (*authorizationdetails.Registry, error) {
	return authorizationdetails.New(map[string]authorizationdetails.Type{
		exchangeGrantType: {
			GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
			Grant:      grantExchangeGrantShape,
		},
	})
}

// grantExchangeGrantShape is the Grant hook. It is decide-only and holds no
// state: it checks that exactly one entry was sent and that it has the right
// shape, and returns the entry unchanged. Its output is the request, NOT the
// echo: the handler must echo the entry returned by validateTopLevelEntry or
// validateChildEntry, never Pending.Grant's output. Policy, caps and the echo
// are applied by those functions, which know the client.
//
// The hook is never called when the parameter is absent or "[]", so the
// handler must go through parseExchangeGrantDetails and singleGrantedEntry.
func grantExchangeGrantShape(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
	if len(req.Entries) != 1 {
		return authorizationdetails.Result{}, invalidExchangeGrant(reasonSingleEntry)
	}
	if _, err := decodeExchangeGrantEntry(req.Entries[0]); err != nil {
		return authorizationdetails.Result{}, err
	}
	return authorizationdetails.Result{Entries: req.Entries}, nil
}

// parseExchangeGrantDetails parses the authorization_details form value with
// the exchange-grant registry. Unlike Handler.parseAuthorizationDetails, an
// absent or empty value is never ignored: the refresh-token handler requires
// the parameter, so it is refused with invalid_request (RFC 6749 section 5.2).
// A literal "[]" is present but empty and is refused later by
// singleGrantedEntry with invalid_authorization_details.
func parseExchangeGrantDetails(
	registry *authorizationdetails.Registry, form url.Values,
) (*authorizationdetails.Pending, error) {
	values := form[authorizationDetailsParam]
	if len(values) == 0 || (len(values) == 1 && values[0] == "") {
		return nil, missingExchangeGrantDetailsError()
	}
	if len(values) > 1 {
		return nil, repeatedAuthorizationDetailsError()
	}
	pending, err := registry.Parse(values[0], oauthproto.GrantTypeTokenExchange)
	if err != nil {
		return nil, invalidAuthorizationDetailsError()
	}
	return pending, nil
}

// singleGrantedEntry returns the one entry of the array Pending.Grant produced.
// Grant returns "[]" for a request without entries, since the hook never runs
// for it, so the count is checked here as well.
func singleGrantedEntry(granted json.RawMessage) (json.RawMessage, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(granted, &entries); err != nil || len(entries) != 1 {
		return nil, invalidExchangeGrant(reasonSingleEntry)
	}
	return entries[0], nil
}

// exchangeGrantExpiresIn is the expires_in of the response, in whole seconds,
// measured from the same now that validated the entry.
func exchangeGrantExpiresIn(expiresAt, now time.Time) time.Duration {
	return time.Duration(expiresAt.Unix()-now.Unix()) * time.Second
}

// decodeExchangeGrantEntry decodes an entry strictly: unknown members are
// refused, issuable_token_types must hold exactly one known token-type URN,
// and expires_at, when present, must be an integer literal. Member names match
// exactly, not case-insensitively. The input must have passed Registry.Parse or
// be hook output, because map decoding keeps the last of any duplicate member.
func decodeExchangeGrantEntry(raw json.RawMessage) (exchangeGrantEntry, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return exchangeGrantEntry{}, invalidExchangeGrant(reasonMalformedEntry)
	}
	for name := range members {
		switch name {
		case exchangeGrantMemberType, exchangeGrantMemberTypes, exchangeGrantMemberExpiresAt:
		default:
			return exchangeGrantEntry{}, invalidExchangeGrant(reasonMalformedEntry)
		}
	}

	var entry exchangeGrantEntry
	if err := json.Unmarshal(members[exchangeGrantMemberType], &entry.Type); err != nil || entry.Type != exchangeGrantType {
		return exchangeGrantEntry{}, invalidExchangeGrant(reasonMalformedEntry)
	}

	typesRaw, ok := members[exchangeGrantMemberTypes]
	if !ok {
		return exchangeGrantEntry{}, invalidExchangeGrant(reasonIssuableTypes)
	}
	if err := json.Unmarshal(typesRaw, &entry.IssuableTokenTypes); err != nil || len(entry.IssuableTokenTypes) != 1 ||
		!slices.Contains([]string{oauthproto.TokenTypeIDJAG, oauthproto.TokenTypeRefreshToken}, entry.IssuableTokenTypes[0]) {
		return exchangeGrantEntry{}, invalidExchangeGrant(reasonIssuableTypes)
	}

	if expiresRaw, present := members[exchangeGrantMemberExpiresAt]; present {
		// ParseInt refuses fractions and exponents such as 1.0 and 1e3,
		// and null, strings and other JSON types.
		seconds, err := strconv.ParseInt(string(expiresRaw), 10, 64)
		if err != nil {
			return exchangeGrantEntry{}, invalidExchangeGrant(reasonExpiresAt)
		}
		entry.ExpiresAt = &seconds
	}
	return entry, nil
}

// validateTopLevelEntry applies the client's policy to an entry sent with an
// access-token subject. It returns the granted entry, with expires_at filled in
// or capped (RFC 9396 section 7.1), and the grant's ExpiresAt. The input must
// have passed Registry.Parse or be hook output.
//
// The response's expires_in is exchangeGrantExpiresIn(expiresAt, now) with the
// same now. Errors are plain authorizationdetails.Error values that
// authorizationDetailsGrantError maps to invalid_authorization_details.
//
// The grant's lifetime is now plus the client's cap for the entry's token type.
// It is deliberately not bounded by the subject token's exp: the grant exists
// for use after that token and the login have ended (RFC 8693 section 2.2.1).
// The stored SourceExpiresAt only sets how long the subject's replay marker is
// kept. Logout and upstream revocation do not end the grant; only revocation
// or expiry does.
func validateTopLevelEntry(
	raw json.RawMessage, policy ConfiguredClientPolicy, now time.Time,
) (json.RawMessage, time.Time, error) {
	entry, err := decodeExchangeGrantEntry(raw)
	if err != nil {
		return nil, time.Time{}, err
	}
	tokenType := entry.IssuableTokenTypes[0]
	capSeconds, err := exchangeGrantCapSeconds(policy, tokenType)
	if err != nil {
		return nil, time.Time{}, err
	}
	nowSec := now.Unix()
	return grantEntry(entry, tokenType, nowSec, nowSec+capSeconds)
}

// validateChildEntry applies the client's policy to an entry sent with a
// refresh-token subject. A child never lists refresh_token, since derivation
// is one level deep, and never outlives its parent. The input must have passed
// Registry.Parse or be hook output; errors and expires_in are as for
// validateTopLevelEntry.
//
// A child with no lifetime left is refused even though storage Live treats
// now == parent.ExpiresAt as live: a zero-lifetime child is useless.
func validateChildEntry(
	raw json.RawMessage, parent *storage.ExchangeGrant, policy ConfiguredClientPolicy, now time.Time,
) (json.RawMessage, time.Time, error) {
	entry, err := decodeExchangeGrantEntry(raw)
	if err != nil {
		return nil, time.Time{}, err
	}
	tokenType := entry.IssuableTokenTypes[0]
	if tokenType == oauthproto.TokenTypeRefreshToken {
		return nil, time.Time{}, invalidExchangeGrant(reasonTokenTypeNotPermitted)
	}
	capSeconds, err := exchangeGrantCapSeconds(policy, tokenType)
	if err != nil {
		return nil, time.Time{}, err
	}
	nowSec := now.Unix()
	ceiling := nowSec + capSeconds
	parentExpires := parent.ExpiresAt.Unix()
	parentBound := parentExpires < ceiling
	if parentBound {
		ceiling = parentExpires
	}
	if ceiling <= nowSec && parentBound {
		return nil, time.Time{}, invalidExchangeGrant(reasonParentExpired)
	}
	return grantEntry(entry, tokenType, nowSec, ceiling)
}

// exchangeGrantCapSeconds returns the lifetime cap the policy sets for a token
// type, in whole seconds. A type with no key is not configured for the client
// and is refused. A cap under one second leaves no lifetime, so it is refused
// too; config validation should prevent it, and this is the backstop.
func exchangeGrantCapSeconds(policy ConfiguredClientPolicy, tokenType string) (int64, error) {
	limit, ok := policy.ExchangeRefreshTokens[tokenType]
	if !ok {
		return 0, invalidExchangeGrant(reasonTokenTypeNotPermitted)
	}
	seconds := int64(limit / time.Second)
	if seconds < 1 {
		return 0, invalidExchangeGrant(reasonExpiresAt)
	}
	return seconds, nil
}

// grantEntry resolves the grant's expiry in whole seconds. The result is the
// requested expires_at when it lies after nowSec and before ceiling, else
// ceiling. A requested value at or before nowSec is refused. Comparing integers
// avoids time.Unix, which cannot represent every int64, so a huge request is
// capped rather than wrapped.
func grantEntry(
	entry exchangeGrantEntry, tokenType string, nowSec, ceiling int64,
) (json.RawMessage, time.Time, error) {
	if ceiling <= nowSec {
		return nil, time.Time{}, invalidExchangeGrant(reasonExpiresAt)
	}
	expires := ceiling
	if requested := entry.ExpiresAt; requested != nil {
		if *requested <= nowSec {
			return nil, time.Time{}, invalidExchangeGrant(reasonExpiresAt)
		}
		if *requested < ceiling {
			expires = *requested
		}
	}
	echo, err := json.Marshal(exchangeGrantEntry{
		Type:               exchangeGrantType,
		IssuableTokenTypes: []string{tokenType},
		ExpiresAt:          &expires,
	})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("encode exchange grant echo: %w", err)
	}
	return echo, time.Unix(expires, 0), nil
}

func invalidExchangeGrant(reason string) error {
	return authorizationdetails.Error{
		Code:   authorizationdetails.CodeInvalidAuthorizationDetails,
		Reason: reason,
	}
}

// missingExchangeGrantDetailsError refuses an absent or empty parameter.
func missingExchangeGrantDetailsError() *fosite.RFC6749Error {
	return &fosite.RFC6749Error{
		ErrorField:       string(authorizationdetails.CodeInvalidRequest),
		DescriptionField: "The authorization_details parameter is required.",
		CodeField:        http.StatusBadRequest,
	}
}
