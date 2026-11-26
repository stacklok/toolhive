// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"regexp"
	"slices"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Reasons sent in error_description with invalid_authorization_details when a
// redemption, or a request that mints an ID-JAG, is refused for its
// authorization_details. The log line of a refusal names the same reason.
const (
	reasonDetailsParameterNotAllowed = "parameter_not_allowed"
	reasonDetailsUnknownType         = "unknown_type"
	reasonDetailsMalformedEntry      = "malformed_entry"
	reasonDetailsEmptyGrant          = "empty_grant"
)

// rejectClaimShape is the logged reason of a redemption refused for a claim
// that is not an array of objects, which the client sees as
// invalid_assertion.
const rejectClaimShape = "claim_not_array_of_objects"

// maxLoggedTypes bounds the entry types an unknown_type WARN line names.
const maxLoggedTypes = 8

// loggableType matches an entry type that may be logged as written: a URI or
// a short name. Any other type is logged as invalidTypeLabel, so the line
// never carries free-form claim text.
var loggableType = regexp.MustCompile(`^[A-Za-z0-9._:/#-]{1,128}$`)

const invalidTypeLabel = "<invalid>"

// refuseDetailsParameter refuses a JWT-bearer or ID-JAG redemption that sends
// the authorization_details parameter. Redemption takes the details only from
// the claim the ID-JAG's issuer signed, so the parameter is refused, never
// ignored: a client that sends it would otherwise get a token without the
// restriction it asked for. A single empty value counts as absent (RFC 6749
// section 3.2). A single non-empty value is refused with
// parameter_not_allowed, and any repeat, empty values included, with
// invalid_request, as RFC 6749 section 5.2 answers a repeated parameter.
// logClientID names the client on the refusal's log line, written at the
// level that level returns for the logged reason. level is called only for a
// refusal, so a caller can bound how often the line is at Warn.
func refuseDetailsParameter(
	ctx context.Context, form url.Values, logClientID string, level func(reason string) slog.Level,
) error {
	_, present, err := readDetailsParam(form)
	switch {
	case err != nil:
		// readDetailsParam's only error is errRepeatedDetailsParam.
		slog.Log(ctx, level(rejectRepeated), "authorization_details rejected",
			"client_id", logClientID, "reason", rejectRepeated)
		return errorsx.WithStack(repeatedAuthorizationDetailsError())
	case !present:
		return nil
	}
	slog.Log(ctx, level(reasonDetailsParameterNotAllowed), "authorization_details rejected",
		"client_id", logClientID, "reason", reasonDetailsParameterNotAllowed)
	return detailsRefusal(reasonDetailsParameterNotAllowed)
}

// grantSignedDetails processes the authorization_details claim of a validated
// ID-JAG, as draft section 4.4.1 requires of the redeeming server. It returns
// the granted entries for the token and the response, or nil when the claim is
// absent. It runs before the assertion is consumed, so a refusal leaves the
// ID-JAG unconsumed.
//
// The draft lets this server "modify, filter, or omit" the entries. This
// server only omits whole signed entries: each type's hook keeps or drops its
// entries (Pending.GrantSubset), and the token carries the kept entries with
// the bytes the issuer signed. A type whose hook keeps none of its entries is
// dropped; when no entry is left at all the redemption is refused with
// empty_grant, because a token without the claim would be unrestricted. A
// hook that changes or adds an entry is a hook bug answered with
// server_error. Narrowing inside an entry would be a per-type extension.
//
// A refused ID-JAG is not consumed, so it can be resent until it expires:
// each refusal is logged at Warn the first time per (issuer, reason) and at
// Debug after that.
//
// clientID is the authenticated client. resolvedUser is the local user this
// redemption resolved the subject to, empty for a namespaced redemption.
func (h *IDJAGHandler) grantSignedDetails(
	ctx context.Context, claims *ValidatedClaims, clientID, resource, resolvedUser string,
) (json.RawMessage, error) {
	if !restrictionsOf(claims).AuthorizationDetails {
		return nil, nil
	}
	level := func(reason string) slog.Level { return h.redeemer.firstRefusalLevel(claims.Issuer, reason) }
	signed, ok := signedEntries(claims.AuthorizationDetails)
	if !ok {
		logDetailsRejected(ctx, level, clientID, rejectClaimShape)
		return nil, invalidAssertion(
			"The ID-JAG assertion authorization_details claim must be a non-empty array of objects.")
	}
	// Without a registry no type is known, whatever the entries hold.
	var pending *authorizationdetails.Pending
	err := authorizationdetails.ErrUnknownType
	if h.detailsRegistry != nil {
		pending, err = h.detailsRegistry.Parse(string(claims.AuthorizationDetails), oauthproto.GrantTypeJWTBearer)
	}
	switch {
	case errors.Is(err, authorizationdetails.ErrUnknownType):
		// The issuer and the types tell the operator which registration is
		// missing; the entries themselves are never logged.
		slog.Log(ctx, level(reasonDetailsUnknownType), "authorization_details rejected",
			"client_id", clientID, "reason", reasonDetailsUnknownType,
			"issuer", claims.Issuer, "types", entryTypes(signed))
		return nil, detailsRefusal(reasonDetailsUnknownType)
	case err != nil:
		logDetailsRejected(ctx, level, clientID, reasonDetailsMalformedEntry)
		return nil, detailsRefusal(reasonDetailsMalformedEntry)
	}

	subject := detailsSubject(claims)
	subject.External = true
	subset, err := pending.GrantSubset(ctx, authorizationdetails.Request{
		ClientID:        clientID,
		GrantedAudience: []string{resource},
		Subject:         subject,
		Redemption:      &authorizationdetails.RedemptionContext{ResolvedUser: resolvedUser},
	})
	if err != nil {
		return nil, errorsx.WithStack(grantFailure(ctx, clientID, err, level))
	}
	if subset.Entries == nil {
		// The types are registered names, never free-form claim text.
		slog.Log(ctx, level(reasonDetailsEmptyGrant), "authorization_details rejected",
			"client_id", clientID, "reason", reasonDetailsEmptyGrant, "types", subset.Dropped)
		return nil, detailsRefusal(reasonDetailsEmptyGrant)
	}
	return subset.Entries, nil
}

// logDetailsRejected logs a redemption's authorization_details refusal at the
// level that level returns for reason.
func logDetailsRejected(ctx context.Context, level func(reason string) slog.Level, clientID, reason string) {
	slog.Log(ctx, level(reason), "authorization_details rejected", "client_id", clientID, "reason", reason)
}

// detailsRefusal is invalid_authorization_details with reason as the whole
// error_description. A signed claim whose content is refused gets this code
// too, although draft section 4.4.3 would answer an ID-JAG that fails a
// validation rule with invalid_grant: the client can act on the RFC 9396
// reason.
func detailsRefusal(reason string) error {
	return errorsx.WithStack(
		redemptionRefusal(string(authorizationdetails.CodeInvalidAuthorizationDetails), reason, ""))
}

// signedEntries splits the raw claim into its entries. It reports false unless
// the claim is a JSON array holding at least one entry and only objects, so
// null, [], a string, an object and an array with a non-object are refused.
// Deeper checks are Registry.Parse's.
func signedEntries(raw json.RawMessage) ([]json.RawMessage, bool) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || len(entries) == 0 {
		return nil, false
	}
	for _, entry := range entries {
		if trimmed := bytes.TrimSpace(entry); len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, false
		}
	}
	return entries, true
}

// entryTypes lists the distinct string "type" members of entries, in order,
// for a log line: at most maxLoggedTypes of them, each one that loggableType
// does not match replaced by invalidTypeLabel. The objects are decoded into
// maps, so a member in another letter case is not read as "type".
func entryTypes(entries []json.RawMessage) []string {
	var types []string
	for _, entry := range entries {
		if len(types) == maxLoggedTypes {
			break
		}
		var object map[string]json.RawMessage
		var name string
		if json.Unmarshal(entry, &object) != nil || json.Unmarshal(object["type"], &name) != nil {
			continue
		}
		if !loggableType.MatchString(name) {
			name = invalidTypeLabel
		}
		if !slices.Contains(types, name) {
			types = append(types, name)
		}
	}
	return types
}

// setRedemptionDetails copies the granted entries into the access token's
// authorization_details claim. The claim is a json.RawMessage, in memory and
// in a session read back from storage (see session.Session.UnmarshalJSON), so
// the token and a stored session keep every digit of a number.
func setRedemptionDetails(issued *session.Session, granted json.RawMessage) {
	if len(granted) > 0 {
		issued.JWTClaims.Extra[claimnames.AuthorizationDetailsKey] = granted
	}
}

// echoRedemptionDetails returns the entries the issued token carries in the
// token response's authorization_details member, which the draft requires
// whether or not the redemption asked.
func echoRedemptionDetails(requester fosite.AccessRequester, responder fosite.AccessResponder) {
	issued, ok := requester.GetSession().(*session.Session)
	if !ok || issued == nil || issued.JWTSession == nil || issued.JWTClaims == nil {
		return
	}
	if granted, present := issued.JWTClaims.Extra[claimnames.AuthorizationDetailsKey]; present {
		responder.SetExtra(authorizationDetailsParam, granted)
	}
}
