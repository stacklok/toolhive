// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import "github.com/stacklok/toolhive/pkg/authserver/claimnames"

// restriction names the claim that makes a validated token terminal: a token
// no exchange may turn into one without that claim.
type restriction int

const (
	restrictionNone restriction = iota
	restrictionDelegatedAccess
	restrictionAuthorizationDetails
)

// terminalRestriction reports which terminal claim a validated token carries.
// The delegated-access marker wins when both are present. Only presence counts
// and the value is never read. It is safe on nil claims.
//
// Refusal is a fail-closed rule for a server that knows no authorization_details
// types: a token without the claim is unrestricted, so no exchange may drop it.
//
// Which tokens reach this check depends on the call site. Only the RFC 8693
// subject_token accepts external issuers (h.validator). Actor tokens
// (h.selfValidator) and exchange-grant opening only ever see self-issued
// tokens. At ID-JAG issuance only a token signed by this server is refused: an
// external actor token is the actor validator's decision, because its details
// would describe the agent's own limits, not the subject's grant.
func terminalRestriction(claims *ValidatedClaims) restriction {
	if claims == nil {
		return restrictionNone
	}
	// Claims is the full decoded set and Extra a subset of it. Both are checked
	// so hand-built ValidatedClaims and a later change to assignClaim stay safe.
	if hasClaim(claims, claimnames.DelegatedAccessKey) {
		return restrictionDelegatedAccess
	}
	if hasClaim(claims, authorizationDetailsParam) {
		return restrictionAuthorizationDetails
	}
	return restrictionNone
}

func hasClaim(claims *ValidatedClaims, name string) bool {
	_, inClaims := claims.Claims[name]
	_, inExtra := claims.Extra[name]
	return inClaims || inExtra
}
