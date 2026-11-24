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
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Reasons sent in error_description with invalid_authorization_details when a
// redemption is refused for its authorization_details. The WARN line of a
// refusal logs the same reason.
const (
	reasonDetailsParameterNotAllowed = "parameter_not_allowed"
	reasonDetailsUnknownType         = "unknown_type"
	reasonDetailsMalformedEntry      = "malformed_entry"
	reasonDetailsEmptyGrant          = "empty_grant"
)

// Logged reasons of a redemption refusal that the client sees under another
// code: invalid_assertion for a claim that is not an array of objects, and
// server_error for a hook that granted an entry the ID-JAG did not sign.
const (
	rejectClaimShape     = "claim_not_array_of_objects"
	rejectGrantNotSigned = "grant_not_signed"
)

// maxLoggedTypes bounds the entry types an unknown_type WARN line names.
const maxLoggedTypes = 8

// loggableType matches an entry type that may be logged as written: a URI or
// a short name. Any other type is logged as invalidTypeLabel, so the line
// never carries free-form claim text.
var loggableType = regexp.MustCompile(`^[A-Za-z0-9._:/#-]{1,128}$`)

const invalidTypeLabel = "<invalid>"

var (
	errGrantEmpty     = errors.New("authorization_details hooks granted no entry")
	errGrantNotSigned = errors.New("authorization_details hooks granted an entry the ID-JAG did not sign")
)

// refuseDetailsParameter refuses a JWT-bearer or ID-JAG redemption that sends
// the authorization_details parameter. Redemption takes the details only from
// the claim the ID-JAG's issuer signed, so the parameter is refused, never
// ignored: a client that sends it would otherwise get a token without the
// restriction it asked for. A single empty value counts as absent (RFC 6749
// section 3.2); any repeat, empty values included, is refused. logClientID
// names the client on the refusal's log line, written at the level that level
// returns. level is called only for a refusal, so a caller can bound how often
// the line is at Warn.
func refuseDetailsParameter(ctx context.Context, form url.Values, logClientID string, level func() slog.Level) error {
	if _, present, err := readDetailsParam(form); err == nil && !present {
		return nil
	}
	slog.Log(ctx, level(), "authorization_details rejected",
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
// server only drops whole signed entries: every granted entry must equal a
// signed one as a JSON value, and a hook that changes or adds one is a hook
// bug answered with server_error. Narrowing inside an entry is a per-type
// extension (see "Narrow authorization_details on token exchange and refresh"
// (#6912)).
//
// clientID is the authenticated client. resolvedUser is the local user this
// redemption resolved the subject to, empty for a namespaced redemption.
func (h *IDJAGHandler) grantSignedDetails(
	ctx context.Context, claims *ValidatedClaims, clientID, resource, resolvedUser string,
) (json.RawMessage, error) {
	if !restrictionsOf(claims).AuthorizationDetails {
		return nil, nil
	}
	signed, ok := signedEntries(claims.AuthorizationDetails)
	if !ok {
		warnAuthorizationDetailsRejected(clientID, rejectClaimShape)
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
		slog.Warn("authorization_details rejected", "client_id", clientID, "reason", reasonDetailsUnknownType,
			"issuer", claims.Issuer, "types", entryTypes(signed))
		return nil, detailsRefusal(reasonDetailsUnknownType)
	case err != nil:
		warnAuthorizationDetailsRejected(clientID, reasonDetailsMalformedEntry)
		return nil, detailsRefusal(reasonDetailsMalformedEntry)
	}

	subject := detailsSubject(claims, resolvedUser)
	subject.External = true
	granted, err := pending.Grant(ctx, authorizationdetails.Request{
		ClientID:        clientID,
		GrantedAudience: []string{resource},
		Subject:         subject,
	})
	if errors.Is(err, authorizationdetails.ErrNoEntries) {
		// A hook dropped every entry of its type. A redemption is never
		// treated as unrestricted, so that is a refusal, not a hook bug.
		warnAuthorizationDetailsRejected(clientID, reasonDetailsEmptyGrant)
		return nil, detailsRefusal(reasonDetailsEmptyGrant)
	}
	if err != nil {
		return nil, errorsx.WithStack(authorizationDetailsGrantError(clientID, err))
	}
	switch err := checkGrantedSigned(signed, granted); {
	case errors.Is(err, errGrantEmpty):
		warnAuthorizationDetailsRejected(clientID, reasonDetailsEmptyGrant)
		return nil, detailsRefusal(reasonDetailsEmptyGrant)
	case err != nil:
		warnAuthorizationDetailsRejected(clientID, rejectGrantNotSigned)
		return nil, errorsx.WithStack(
			fosite.ErrServerError.WithDescription("The authorization_details could not be processed."))
	}
	return granted, nil
}

// detailsRefusal is invalid_authorization_details with reason as the whole
// error_description. A signed claim whose content is refused gets this code
// too, as the call-binding issue (#7061) specifies, although draft section
// 4.4.3 would answer an ID-JAG that fails a validation rule with
// invalid_grant: the client can act on the RFC 9396 reason.
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

// checkGrantedSigned requires every granted entry to equal a distinct signed
// entry as a JSON value. Numbers are compared as written, so 1 and 1.0
// differ. It returns errGrantEmpty when nothing was granted and
// errGrantNotSigned when an entry was changed, added or repeated. Each entry
// is reduced once to canonical bytes and matched through a count, so the cost
// is linear in the number of entries.
func checkGrantedSigned(signed []json.RawMessage, granted json.RawMessage) error {
	var grantedEntries []json.RawMessage
	if err := json.Unmarshal(granted, &grantedEntries); err != nil {
		return errGrantNotSigned
	}
	if len(grantedEntries) == 0 {
		return errGrantEmpty
	}
	available := make(map[string]int, len(signed))
	for _, entry := range signed {
		key, err := canonicalJSON(entry)
		if err != nil {
			return errGrantNotSigned
		}
		available[key]++
	}
	for _, entry := range grantedEntries {
		key, err := canonicalJSON(entry)
		if err != nil || available[key] == 0 {
			return errGrantNotSigned
		}
		available[key]--
	}
	return nil
}

// canonicalJSON re-encodes a JSON value with object members sorted and
// numbers kept as written, so two values are equal exactly when their
// canonical forms are.
func canonicalJSON(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	return string(canonical), err
}

// setRedemptionDetails copies the granted entries into the access token's
// authorization_details claim. In memory the claim is a json.RawMessage, so
// the token carries every digit; a session read back from storage holds it
// as decoded JSON ([]any), and a reader of a stored session must accept both.
func setRedemptionDetails(issued *session.Session, granted json.RawMessage) {
	if granted != nil {
		issued.JWTClaims.Extra[authorizationDetailsParam] = granted
	}
}

// echoRedemptionDetails returns the entries the issued token carries in the
// token response's authorization_details member, which the draft requires
// whether or not the redemption asked.
func echoRedemptionDetails(requester fosite.AccessRequester, responder fosite.AccessResponder) {
	issued, ok := requester.GetSession().(*session.Session)
	if !ok || issued.JWTClaims == nil {
		return
	}
	if granted, present := issued.JWTClaims.Extra[authorizationDetailsParam]; present {
		responder.SetExtra(authorizationDetailsParam, granted)
	}
}
