// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"
)

// SubjectMode selects how an ID-JAG's subject becomes the issued token's sub.
type SubjectMode string

const (
	// SubjectModeNamespaced issues <iss>#<sub>. The zero value means this.
	SubjectModeNamespaced SubjectMode = "namespaced"
	// SubjectModeResolve maps the subject to a local user. Closed by
	// delegatedAccessEnabled.
	SubjectModeResolve SubjectMode = "resolve"
)

// delegatedAccessEnabled is the gate on redemptions from resolve issuers. While
// it is false, every such redemption is refused as an unknown subject before
// anything is consumed, so a client sees what it sees for an unmapped user. It
// is not a setting, an acknowledgement or a build tag.
const delegatedAccessEnabled = false

// refuseSubjectUnknown is the refusal for a valid ID-JAG whose subject is not
// allowed here, and for a redemption that reaches the closed gate.
func refuseSubjectUnknown() error {
	return errorsx.WithStack(redemptionRefusal(fosite.ErrAccessDenied.ErrorField, reasonSubjectUnknown, ""))
}

// checkSubjectModeGate refuses a redemption from a resolve issuer while
// delegatedAccessEnabled is false.
func checkSubjectModeGate(policy *JWTBearerGrantPolicy) error {
	if policy.SubjectMode == SubjectModeResolve && !delegatedAccessEnabled {
		return refuseSubjectUnknown()
	}
	return nil
}

// warnSubjectResolutionDisabled logs once, at construction, that redemptions
// from the resolve issuers in policies are refused. A nil logger means
// slog.Default().
func warnSubjectResolutionDisabled(logger *slog.Logger, policies map[string]*JWTBearerGrantPolicy) {
	if delegatedAccessEnabled {
		return
	}
	var issuers []string
	for _, issuerURL := range slices.Sorted(maps.Keys(policies)) {
		if policies[issuerURL].SubjectMode == SubjectModeResolve {
			issuers = append(issuers, issuerURL)
		}
	}
	if len(issuers) == 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("Resolution of the subject at ID-JAG redemption is not enabled in this build; "+
		"redemptions from resolve issuers are refused", "issuers", issuers)
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
