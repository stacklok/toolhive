// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package claimnames

import "strings"

// TokenSessionIDKey is the JWT claim key for the token session ID.
// This links JWT access tokens to stored upstream IDP tokens.
// We use "tsid" instead of "sid" to avoid confusion with OIDC session management
// which defines "sid" for different purposes (RFC 7519, OIDC Session Management).
// The token-issuing and token-consuming sides must agree on this spelling.
const TokenSessionIDKey = "tsid"

// NoUpstreamSessionKey marks a token this authorization server minted
// without any link to an upstream IdP login, so no upstream credential exists
// for it. Today only the RFC 7523 JWT-bearer grant and the ID-JAG grant set
// it: the subject is a synthetic machine identity (issuer#subject) asserted by
// a separate trust root, with no end user and no upstream session.
//
// It exists so a resource server can distinguish "this token was deliberately
// minted without an upstream session" from "this identity was never enriched
// with upstream credentials" — states that are otherwise indistinguishable, as
// both leave Identity.UpstreamTokens nil. pkg/authz/authorizers/cedar reads it
// to decide whether request-token claims may drive policy. The RFC 8693
// delegation grant needs no marker: its "act" claim already says so.
//
// The name is collision-resistant per RFC 7519 Section 4.3, so a JWT from
// another issuer the deployment trusts cannot assert it by accident. It is a
// private claim; RFC 9068 Section 2.2.3 permits additional claims on an
// access token. The issuing and consuming sides must agree on this spelling.
const NoUpstreamSessionKey = "https://toolhive.dev/no_upstream_session"

// ExternalIssuerKey is the JWT claim key recording the external issuer of an
// externally-asserted subject token, so the fact that an external issuer was
// involved survives re-exchanges of the issued token.
const ExternalIssuerKey = "external_issuer"

// SubjectIdentifierKey is the JWT claim key of the RFC 9493 subject identifier
// claim, which carries the verified upstream identity as an {iss, sub} pair.
// The issuing and consuming sides must agree on this spelling.
const SubjectIdentifierKey = "sub_id"

// UpstreamSubjectKey is the collision-resistant claim name (RFC 7519 Section
// 4.2) of the claim that carries the upstream subject a consumer can key on
// across upstream client registrations. It is emitted together with the RFC 9493
// sub_id claim (SubjectIdentifierKey) and under the same disclosure policy.
const UpstreamSubjectKey = "https://toolhive.dev/upstream_subject"

// QualifySubject returns the "sub" value for a subject asserted by an external
// issuer: "<issuer>#<subject>".
//
// Self-issued tokens keep their native subject, which is a UUID from
// UserResolver.ResolveUser's (providerID, providerSubject) mapping
// (pkg/authserver/server/handlers/user.go), ToolHive's own native subject
// namespace (or a synthesized "tk-" plus hex value).
//
// A subject from a trusted external issuer is qualified instead, because the
// issued token's "sub" is the only signal Cedar's extractClientIDFromClaims
// (pkg/authz/authorizers/cedar/core.go) keys on — it reads "sub" alone, not
// "iss" — and the external issuer chooses that value, not ToolHive. Without
// qualification, an external issuer could pick a "sub" equal to a native
// user's UUID and mint a token indistinguishable from that user's, entirely
// without malicious intent on either issuer's part: two trusted issuers merely
// choosing overlapping subject values collide with no attacker involved.
//
// "#" is an unambiguous separator: an issuer URL cannot itself contain a
// fragment (the OIDC/OAuth "iss" value is a URL without a fragment component,
// and issuer comparison is an exact string match against that value), so
// cutting a qualified subject at its first "#" always recovers (issuer,
// subject) uniquely. A native subject (a UUID, or a "tk-" plus hex value) can
// never collide with a qualified value, since neither contains a "#".
//
// Re-exchanging a previously-issued token does not double-qualify: such a
// token's "iss" is always this server's own issuer (fosite stamps "iss" from
// the server's config at issuance, see session.New), so validation routes it
// to the self-issued path, which never qualifies.
func QualifySubject(issuer, subject string) string {
	return issuer + "#" + subject
}

// IsQualifiedSubject reports whether sub has the qualified form, i.e. contains "#".
// Native subjects never contain "#".
func IsQualifiedSubject(sub string) bool {
	return strings.Contains(sub, "#")
}
