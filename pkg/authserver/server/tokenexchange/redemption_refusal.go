// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
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
)

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
