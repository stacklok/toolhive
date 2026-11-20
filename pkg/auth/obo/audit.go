// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import (
	"errors"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
)

// AuditEventType is the AuditEvent.Type for an OBO exchange decision. The proxy
// middleware and the vMCP strategy both emit it.
const AuditEventType = "obo_token_exchange"

// NewInteractionRequiredAuditEvent builds the audit event for an OBO exchange
// that Entra refused with a conditional-access or claims challenge. It records
// the denied subject (an identifier, not a credential) and only the
// non-sensitive fields the entra error carries: OAuthError, Description,
// HTTPStatus, and whether a claims challenge was present. It never records the
// user token, the client secret, an issued token, or the raw claims challenge.
//
// Shared by the proxy middleware and the vMCP strategy so both surfaces emit
// the same record shape. identity may be nil.
func NewInteractionRequiredAuditEvent(
	source audit.EventSource, component string, identity *auth.Identity, target string, err error,
) *audit.AuditEvent {
	var subjects map[string]string
	if identity != nil && identity.Subject != "" {
		subjects = map[string]string{"sub": identity.Subject}
	}

	event := audit.NewAuditEvent(AuditEventType, source, audit.OutcomeDenied, subjects, component).
		WithTarget(map[string]string{"audience": target})

	extra := map[string]any{"reason": "interaction_required"}
	var exErr *entra.ExchangeError
	if errors.As(err, &exErr) {
		extra["oauth_error"] = exErr.OAuthError
		extra["description"] = exErr.Description
		extra["http_status"] = exErr.HTTPStatus
		extra["claims_challenge_present"] = exErr.Claims != ""
	}
	event.Metadata.Extra = extra
	return event
}
