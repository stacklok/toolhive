// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamfilter

import "context"

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

// UpstreamFilter narrows the authorization chain to a subset of the configured
// upstreams, keyed on the identity established by the first upstream. It is
// consulted exactly once per authorization, in the callback handler, after the
// first upstream (upstreams[0]) resolves. The first upstream is always required:
// it is never passed to the filter and cannot be removed by it.
//
// FilterUpstreams receives, in order:
//   - ctx: the request context of the first leg's callback.
//   - principal: the first upstream's credential-free identity and claims, used for
//     authorization decisions. See FilterPrincipal for where each value comes from.
//     The filter MUST treat principal (including its claims map) as read-only.
//   - configured: the names of the non-first configured upstreams, in configured
//     order.
//
// Claims contract: values are untyped and come straight from the upstream IdP, so
// implementations MUST assert types defensively (e.g. comma-ok on map/slice
// access) — a malformed IdP response must not be able to panic the filter. GetClaims()
// is nil not only for OAuth2 identityFromToken / synthetic upstreams (which resolve
// no structured claim set) but also on a transient OIDC claims-extraction failure,
// so a security-relevant filter MUST treat nil/absent claims as fail-closed. Note
// that the `aud` claim, when present, is the upstream IdP's client_id — not this
// authorization server.
//
// It returns the subset to keep. The handler preserves configured order and
// ignores any returned name that is not one of the non-first configured upstreams,
// so the filter cannot reorder the chain or introduce unknown providers. A
// returned error fails the authorization with a server error — the handler never
// falls back to walking every upstream on error.
//
// The "first-leg identity" guarantee holds even across a rolling upgrade of a
// persistent backend: a subsequent-leg pending that lacks a computed chain (e.g.
// one written before this feature existed) is rejected and forces a fresh
// authorization, rather than re-running the filter against a later leg.
type UpstreamFilter interface {
	FilterUpstreams(
		ctx context.Context,
		principal FilterPrincipal,
		configured []string,
	) ([]string, error)
}
