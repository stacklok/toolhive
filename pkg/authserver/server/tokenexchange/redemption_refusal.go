// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"
)

// Reason codes a client can read from the start of a redemption refusal's
// error_description.
const (
	// reasonInvalidAssertion marks a failed validation rule: a re-issued
	// assertion may succeed. It is only ever used with invalid_grant.
	reasonInvalidAssertion = "invalid_assertion"
	// reasonSubjectUnknown marks a valid ID-JAG whose subject is not allowed
	// here.
	reasonSubjectUnknown = "subject_unknown"
	// reasonResourceDenied marks a valid ID-JAG whose subject may not obtain a
	// token for the requested resource.
	reasonResourceDenied = "resource_denied"
	// reasonActorDenied marks a valid ID-JAG whose actor is not allowed here.
	reasonActorDenied = "actor_denied"
	// reasonIssuerUnavailable marks an assertion whose issuer's signing keys
	// could not be obtained. The refusal is temporarily_unavailable (HTTP 503)
	// and the same request may succeed once the issuer is reachable.
	reasonIssuerUnavailable = "issuer_unavailable"
	// reasonClientAuthRequired marks an ID-JAG redemption by a client that is
	// public or sent no client credentials. It is only ever used with
	// invalid_client and says nothing about the assertion, which was not validated or consumed.
	reasonClientAuthRequired = "client_authentication_required"
)

// reasonAuthorizationDetailsAssertion is the logged reason where an assertion
// that carries authorization_details is refused at redemption. It is not sent
// to the client, which sees invalid_assertion.
const reasonAuthorizationDetailsAssertion = "authorization_details_assertion"

const hintAssertionValidationFailed = "The assertion could not be validated."

// redemptionRefusal builds a JWT-bearer or ID-JAG redemption refusal whose
// error_description is reason, followed by ": " and hint when hint is not
// empty. It is a literal with no HintField on purpose: fosite's WithHint puts
// its default text first, so the reason would not lead.
//
// The description must match ^[a-z_]+(: .*)?$ and hint must be printable ASCII
// without '"' or '\', and must never contain a claim value.
func redemptionRefusal(code, reason, hint string) *fosite.RFC6749Error {
	description := reason
	if hint != "" {
		description += ": " + hint
	}
	return exactReason(code, description)
}

// invalidAssertion is the refusal for an assertion that failed a validation
// rule: invalid_grant with the invalid_assertion reason.
func invalidAssertion(hint string) error {
	return errorsx.WithStack(redemptionRefusal(fosite.ErrInvalidGrant.ErrorField, reasonInvalidAssertion, hint))
}

// refuseSignedDetails refuses a plain RFC 7523 assertion that carries an
// authorization_details claim. RFC 7523 defines no such claim, so the plain
// grant cannot know what it restricts and refuses it instead of issuing a
// token without it. An ID-JAG's claim is processed instead (grantSignedDetails).
// Only the details claim is read: redemption does not refuse the
// delegated-access marker, so an assertion that carries both is refused for
// its details.
//
// JWTBearerHandler calls it after the issuer's policy lookup and before the
// assertion is consumed, so it runs only for issuers enabled for the grant.
// The refusal can newly break such an issuer's redemptions, so the operator
// gets a Warn line naming the issuer. The plain grant needs no client
// authentication and a refused assertion is not consumed, so one assertion can
// be resent until it expires: only the first refusal per issuer is logged at
// Warn and later ones at Debug.
func (r *redeemer) refuseSignedDetails(ctx context.Context, claims *ValidatedClaims) error {
	if !restrictionsOf(claims).AuthorizationDetails {
		return nil
	}
	level := r.firstRefusalLevel(claims.Issuer, reasonAuthorizationDetailsAssertion)
	slog.Log(ctx, level, "Assertion refused", "reason", reasonAuthorizationDetailsAssertion, "issuer", claims.Issuer)
	return invalidAssertion("The assertion must not carry an 'authorization_details' claim.")
}

// clientAuthRequired is the refusal for an ID-JAG redemption by a client that
// did not authenticate: invalid_client with the client_authentication_required
// reason and HTTP 401, the status fosite uses for its own invalid_client. RFC
// 6749 section 5.2 allows 401 for invalid_client in every case and requires it
// when the client used the Authorization header, so one status fits all. The
// hint says nothing about the assertion.
func clientAuthRequired() error {
	refusal := redemptionRefusal(fosite.ErrInvalidClient.ErrorField, reasonClientAuthRequired,
		"The ID-JAG grant requires client authentication.")
	refusal.CodeField = http.StatusUnauthorized
	return errorsx.WithStack(refusal)
}

// issuerUnavailable is the refusal for an assertion whose issuer's signing
// keys could not be obtained: temporarily_unavailable with HTTP 503 and the
// bare issuer_unavailable description, which never names the issuer. The cause
// is wrapped for errors.Is but not copied into the debug field: the validator
// logs it once per backoff window, and a per-request copy would repeat it on
// every refused request.
func issuerUnavailable(cause error) error {
	refusal := redemptionRefusal(fosite.ErrTemporarilyUnavailable.ErrorField, reasonIssuerUnavailable, "")
	refusal.CodeField = fosite.ErrTemporarilyUnavailable.CodeField
	return errorsx.WithStack(refusal.WithWrap(cause))
}

// assertionValidationRefusal maps a failure of assertion validation to its
// refusal: issuer_unavailable when the issuer's keys could not be obtained, a
// server error when the failure is not the assertion's (a misconfigured
// validator, or a request that ended mid-validation), invalid_assertion with
// hint otherwise.
func assertionValidationRefusal(err error, hint string) error {
	switch {
	case errors.Is(err, errIssuerKeysUnavailable):
		return issuerUnavailable(err)
	case errors.Is(err, errDefaultAudiencesEmpty),
		errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return serverFailure(hintAssertionValidationFailed, err)
	}
	return invalidAssertion(hint)
}
