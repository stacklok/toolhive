// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"context"

	"github.com/stacklok/toolhive/pkg/auth"
)

// SubjectEnricher returns extra audit subjects for a request.
//
// An embedder sets it from Go (see WithSubjectEnricher); it is never read from
// the serialized Config. It lets an embedder record caller attributes only it
// knows how to derive, such as the OAuth client id (whose claim differs by
// IdP) or whether the caller is a workload identity.
//
// identity is the authenticated caller, or nil when the request carries no
// identity. Returned keys are added to the event's subjects; empty values are
// dropped, and the keys the auditor itself writes (user, user_id, client_name,
// client_version) are reserved and never taken from the enricher. A nil or
// empty map adds nothing.
//
// The enricher runs once per audited event, on the request path, so it must be
// fast, must not block, and must be safe for concurrent use.
type SubjectEnricher func(ctx context.Context, identity *auth.Identity) map[string]string

// Option configures an Auditor or a WorkflowAuditor.
type Option func(*options)

// options holds the settings both auditor constructors accept.
type options struct {
	subjectEnricher SubjectEnricher
}

// WithSubjectEnricher sets the enricher whose subjects are added to every
// audit event. A nil enricher is ignored.
func WithSubjectEnricher(enricher SubjectEnricher) Option {
	return func(o *options) {
		o.subjectEnricher = enricher
	}
}

// buildOptions applies opts to a zero options value.
func buildOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// reservedSubjectKeys are the subject keys the auditor itself writes. An
// enricher can neither overwrite nor supply them.
var reservedSubjectKeys = map[string]struct{}{
	SubjectKeyUser:          {},
	SubjectKeyUserID:        {},
	SubjectKeyClientName:    {},
	SubjectKeyClientVersion: {},
}

// applySubjectEnricher adds the enricher's subjects to subjects, skipping
// reserved keys and empty values. It is a no-op when enricher is nil.
func applySubjectEnricher(
	ctx context.Context,
	enricher SubjectEnricher,
	identity *auth.Identity,
	subjects map[string]string,
) {
	if enricher == nil {
		return
	}
	for key, value := range enricher(ctx, identity) {
		if key == "" || value == "" {
			continue
		}
		if _, reserved := reservedSubjectKeys[key]; reserved {
			continue
		}
		subjects[key] = value
	}
}
