// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"fmt"
	"slices"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"
)

// SubjectMode selects how an ID-JAG's subject becomes the issued token's sub.
type SubjectMode string

const (
	// SubjectModeNamespaced issues <iss>#<sub>. The zero value means this.
	SubjectModeNamespaced SubjectMode = "namespaced"
	// SubjectModeResolve maps the subject to a local user.
	SubjectModeResolve SubjectMode = "resolve"
)

// HasResolveIssuer reports whether any trusted issuer has subject_mode resolve.
func HasResolveIssuer(trustedIssuers []TrustedIssuer) bool {
	return slices.ContainsFunc(trustedIssuers, func(ti TrustedIssuer) bool {
		return ti.JWTBearerGrant != nil && ti.JWTBearerGrant.SubjectMode == SubjectModeResolve
	})
}

// refuseSubjectUnknown is the refusal for a valid ID-JAG whose subject is not
// allowed here.
func refuseSubjectUnknown() error {
	return errorsx.WithStack(redemptionRefusal(fosite.ErrAccessDenied.ErrorField, reasonSubjectUnknown, ""))
}

// validateSubjectMode checks the subject_mode and actor_issuers settings of
// ti's JWT-bearer grant policy, which must be non-nil.
func validateSubjectMode(ti TrustedIssuer) error {
	policy := ti.JWTBearerGrant
	switch policy.SubjectMode {
	case "", SubjectModeNamespaced:
		if len(policy.ActorIssuers) > 0 {
			return fmt.Errorf("issuer_url %q: jwt_bearer_grant.actor_issuers requires subject_mode resolve", ti.IssuerURL)
		}
		return nil
	case SubjectModeResolve:
	default:
		return fmt.Errorf("issuer_url %q: jwt_bearer_grant.subject_mode has unsupported value %q",
			ti.IssuerURL, policy.SubjectMode)
	}
	if len(policy.SubjectBindings) > 0 {
		return fmt.Errorf("issuer_url %q: jwt_bearer_grant.subject_bindings must be empty when subject_mode is resolve",
			ti.IssuerURL)
	}
	if slices.Contains(effectiveAssertionTypes(policy), JWTBearerAssertionTypeJWTBearer) {
		return fmt.Errorf("issuer_url %q: jwt_bearer_grant.subject_mode resolve requires accepted_assertion_types [id_jag]",
			ti.IssuerURL)
	}
	return nil
}
