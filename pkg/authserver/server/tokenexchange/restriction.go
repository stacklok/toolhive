// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import "github.com/stacklok/toolhive/pkg/authserver/claimnames"

// claimConfirmation is the RFC 7800 confirmation claim.
const claimConfirmation = "cnf"

// restrictions lists the claims a validated token carries that limit how it
// may be used. Each field is a separate fact, so a token can carry any
// combination, and each call site decides which facts it refuses and in which
// order. Only presence counts; no value is ever read.
type restrictions struct {
	// DelegatedAccess is the delegated-access marker. A marked token is
	// terminal: no exchange may mint another token from it.
	DelegatedAccess bool
	// AuthorizationDetails is an RFC 9396 authorization_details claim. Such a
	// token is terminal too: a token minted from it would not carry the claim,
	// so the exchange would drop the restriction.
	AuthorizationDetails bool
	// Confirmation is an RFC 7800 cnf claim. Redemption refuses it until proof
	// of possession can be verified.
	Confirmation bool
}

// restrictionsOf reports which restricting claims a validated token carries.
// It is safe on nil claims and on nil Claims or Extra maps. The check does not
// depend on the issuer: which issuers reach it is the call site's decision.
func restrictionsOf(claims *ValidatedClaims) restrictions {
	if claims == nil {
		return restrictions{}
	}
	return restrictions{
		DelegatedAccess: hasClaim(claims, claimnames.DelegatedAccessKey),
		// The raw member is what grantSignedDetails reads, so it counts on its
		// own; a member present as null is the bytes "null" and counts too.
		AuthorizationDetails: hasClaim(claims, authorizationDetailsParam) || claims.AuthorizationDetails != nil,
		Confirmation:         hasClaim(claims, claimConfirmation),
	}
}

// hasClaim reports whether name is present in the validated token. Claims is
// the full decoded set and Extra a subset of it. Both are checked so
// hand-built ValidatedClaims and a later change to assignClaim stay safe.
func hasClaim(claims *ValidatedClaims, name string) bool {
	_, inClaims := claims.Claims[name]
	_, inExtra := claims.Extra[name]
	return inClaims || inExtra
}
