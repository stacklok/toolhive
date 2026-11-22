// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
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
	// reasonIssuerUnavailable marks an assertion whose issuer's signing keys
	// could not be obtained. The refusal is temporarily_unavailable (HTTP 503)
	// and the same request may succeed once the issuer is reachable.
	reasonIssuerUnavailable = "issuer_unavailable"
	// reasonClientAuthRequired marks an ID-JAG redemption by a client that is
	// public or sent no client credentials. It is only ever used with
	// invalid_client and says nothing about the assertion, which was not validated or consumed.
	reasonClientAuthRequired = "client_authentication_required"
)

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
