// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

// FilterPrincipal is a getter contract for identity and claims used in upstream
// authorization decisions; it does not constrain an implementation to contain only
// these values. The authserver callback supplies a private projection containing
// credential-free values: Subject, CanonicalUserID, Name, Email, and Claims. Subject
// is the first upstream's provider-specific mapped subject: OIDC uses the
// configured SubjectClaim (or "sub" by default), OAuth2 may extract it from configured
// userinfo fields or the token response, and a synthetic fallback may apply. It is
// distinct from CanonicalUserID, the canonical storage-user ID (normally the resolved
// internal user ID, but a synthetic identity may use the mapped subject itself, which
// need not be stable). Name and Email come from the first upstream's resolved
// identity or profile and may originate from OIDC claims, configured OAuth2 userinfo
// fields, or token-response extraction. Claims contains the upstream-provided
// ID-token/userinfo claims, or is nil when unavailable; the authorization server
// does not manufacture claims. Callers must treat the returned claims map as
// read-only.
type FilterPrincipal interface {
	GetSubject() string
	GetCanonicalUserID() string
	GetName() string
	GetEmail() string
	GetClaims() map[string]any
}

// filterPrincipal contains only the credential-free values exposed to an
// upstream filter.
type filterPrincipal struct {
	subject         string
	canonicalUserID string
	name            string
	email           string
	claims          map[string]any
}

func (p filterPrincipal) GetSubject() string         { return p.subject }
func (p filterPrincipal) GetCanonicalUserID() string { return p.canonicalUserID }
func (p filterPrincipal) GetName() string            { return p.name }
func (p filterPrincipal) GetEmail() string           { return p.email }
func (p filterPrincipal) GetClaims() map[string]any  { return p.claims }

var _ FilterPrincipal = filterPrincipal{}
