// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package session provides OAuth session management for the authorization server.
// Sessions link issued access tokens to upstream identity provider tokens,
// enabling token exchange and refresh operations.
package session

import (
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/token/jwt"
)

// Factory is a function that creates a new session given subject, IDP session ID, and client ID.
// This allows storage implementations to create sessions without importing this package directly.
//
// The factory is primarily used during deserialization where the clientID may be empty
// because the client_id claim is preserved in the JWT claims Extra map from the original
// serialized session data.
type Factory func(subject, idpSessionID, clientID string) fosite.Session

// UserClaims holds optional user profile claims from the upstream IDP.
// Using a struct avoids parameter-ordering mistakes in function signatures
// that accept multiple string parameters.
type UserClaims struct {
	// Name is the user's display name (OIDC "name" claim).
	Name string
	// Email is the user's email address (OIDC "email" claim).
	Email string
}

// UpstreamSession is an interface for sessions that support IDP linking and JWT claims.
// It embeds oauth2.JWTSessionContainer (which includes fosite.Session, GetJWTClaims,
// and GetJWTHeader) and adds IDP session tracking.
// This interface is used by the storage layer for serialization/deserialization.
type UpstreamSession interface {
	oauth2.JWTSessionContainer
	GetIDPSessionID() string
}

// TokenSessionIDClaimKey is the JWT claim key for the token session ID.
// This links JWT access tokens to stored upstream IDP tokens.
// We use "tsid" instead of "sid" to avoid confusion with OIDC session management
// which defines "sid" for different purposes (RFC 7519, OIDC Session Management).
const TokenSessionIDClaimKey = "tsid"

// NoUpstreamSessionClaimKey marks a token this authorization server minted
// without any link to an upstream IdP login, so no upstream credential exists
// for it. Today only the RFC 7523 JWT-bearer grant sets it: its subject is a
// synthetic machine identity (issuer#subject) asserted by a separate trust
// root, with no end user and no upstream session.
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
// access token.
const NoUpstreamSessionClaimKey = "https://toolhive.dev/no_upstream_session"

// ClientIDClaimKey is the JWT claim key for the OAuth client ID.
// This identifies which client was issued the token.
const ClientIDClaimKey = "client_id"

// NameClaimKey is the JWT claim key for the user's display name.
// Per OIDC Core Section 5.1.
const NameClaimKey = "name"

// EmailClaimKey is the JWT claim key for the user's email address.
// Per OIDC Core Section 5.1.
const EmailClaimKey = "email"

const subjectIdentifierClaimKey = "sub_id"

// UpstreamSubjectClaimKey is the collision-resistant claim name (RFC 7519 §4.2) of
// the claim that carries the upstream subject a consumer can key on across upstream
// client registrations. It is emitted together with the RFC 9493 sub_id claim and
// under the same disclosure policy.
const UpstreamSubjectClaimKey = "https://toolhive.dev/upstream_subject"

// UpstreamSubject is the verified identity of the upstream OIDC login that
// established the local user. Issuer and Subject are the exact iss and sub of the
// validated upstream ID token. StableClaim names the claim the upstream is
// configured to key users on (its subject_claim, or "sub"), and StableValue is that
// claim's value from the same ID token. All four are set together or not at all.
//
// upstream.Identity carries the same four values as its Verified* fields, because
// the upstream package cannot import this one. verifiedUpstreamIdentity in
// handlers/callback.go converts between them; a member added here must be added
// there and to upstream.Identity too, or IsComplete never holds and the claims
// are silently dropped.
type UpstreamSubject struct {
	Issuer      string `json:"iss,omitempty"`
	Subject     string `json:"sub,omitempty"`
	StableClaim string `json:"stable_claim,omitempty"`
	StableValue string `json:"stable_value,omitempty"`
}

// IsComplete reports whether every member is set. Only a complete value is
// stored or disclosed.
func (u UpstreamSubject) IsComplete() bool {
	return u.Issuer != "" && u.Subject != "" && u.StableClaim != "" && u.StableValue != ""
}

// Session extends fosite's JWT session with an IDP session reference.
// This allows the authorization server to link issued tokens to
// upstream IDP tokens stored separately.
//
// Most methods are provided by the embedded *oauth2.JWTSession. This type
// adds IDP session tracking and verified upstream identity provenance; Clone()
// preserves those fields along with the embedded JWT session.
//
// Concurrency: Sessions are designed to be request-scoped and are not
// safe for concurrent access from multiple goroutines. This follows
// Fosite's design pattern where sessions are created per-request,
// populated by handlers, and then persisted to storage. The storage
// layer is responsible for thread-safe access to stored sessions.
type Session struct {
	*oauth2.JWTSession

	// UpstreamSessionID links this session to stored upstream IDP tokens.
	// This ID is used to retrieve the IDP tokens from storage.
	UpstreamSessionID string

	// VerifiedUpstream is preserved server-side through code/refresh storage. It is
	// not recorded at new logins when SubjectIdentifierAudiences is empty, and
	// becomes the sub_id and upstream subject claims only for listed audiences.
	VerifiedUpstream UpstreamSubject `json:"verified_upstream,omitzero"`
}

// New creates a new Session with the given subject, IDP session ID, client ID, and
// optional user profile claims.
//
// Parameters:
//   - subject: The OAuth subject (user identifier). May be empty for placeholder sessions.
//   - idpSessionID: Links to upstream IDP tokens in storage. If provided, it will be
//     included in the JWT claims as "tsid" to allow proxy middleware to look up
//     upstream IDP tokens.
//   - clientID: The OAuth client ID. When provided, it will be included in the JWT
//     claims as "client_id" for binding verification per RFC 9068.
//   - claims: Optional user profile claims (name, email) from the upstream IDP.
//     Included in the JWT per OIDC Core Section 5.1.
//
// ClientID handling:
//   - For token issuance (authorize handler): Pass the client ID to ensure RFC 9068
//     compliance. The client_id claim will be embedded in the JWT.
//   - For token exchange (token handler): May be empty - Fosite copies claims from
//     the stored authorize session, preserving the original client_id.
//   - For deserialization: May be empty - the client_id claim is preserved in the
//     JWT claims Extra map from the serialized session data.
//
// Note: The remaining RFC 9068 required claims (iss, aud, exp, iat, jti) are
// populated by Fosite during token generation. This session only initializes
// custom claims that are not part of Fosite's standard JWT handling.
func New(subject, idpSessionID, clientID string, claims UserClaims) *Session {
	// Initialize the Extra map for JWT claims
	claimsExtra := make(map[string]any)

	// Add tsid claim if idpSessionID is provided
	if idpSessionID != "" {
		claimsExtra[TokenSessionIDClaimKey] = idpSessionID
	}

	// Add client_id claim for binding verification per RFC 9068
	// This may be empty for placeholder sessions or deserialization;
	// in those cases the claim is preserved from the original session.
	if clientID != "" {
		claimsExtra[ClientIDClaimKey] = clientID
	}

	// Add user profile claims per OIDC Core Section 5.1
	if claims.Name != "" {
		claimsExtra[NameClaimKey] = claims.Name
	}
	if claims.Email != "" {
		claimsExtra[EmailClaimKey] = claims.Email
	}

	return &Session{
		JWTSession: &oauth2.JWTSession{
			JWTClaims: &jwt.JWTClaims{
				Subject: subject,
				Extra:   claimsExtra,
			},
			JWTHeader: &jwt.Headers{
				Extra: make(map[string]any),
			},
			Subject: subject, // Also set on JWTSession for fosite compatibility
		},
		UpstreamSessionID: idpSessionID,
	}
}

// Clone creates a deep copy of the session.
// This overrides the embedded JWTSession.Clone() to also copy UpstreamSessionID
// and the verified upstream identity.
func (s *Session) Clone() fosite.Session {
	if s == nil {
		return nil
	}

	var jwtSession *oauth2.JWTSession
	if s.JWTSession != nil {
		jwtSession = s.JWTSession.Clone().(*oauth2.JWTSession)
	}

	return &Session{
		JWTSession:        jwtSession,
		UpstreamSessionID: s.UpstreamSessionID,
		VerifiedUpstream:  s.VerifiedUpstream,
	}
}

// ClearSubjectIdentifier removes any persisted or source-token sub_id and
// upstream subject claim before the current grant decides whether it is
// eligible to issue them. It mutates the receiver's claims in place; a caller
// holding a session loaded from storage must Clone it first.
func (s *Session) ClearSubjectIdentifier() {
	if s == nil || s.JWTSession == nil || s.JWTClaims == nil {
		return
	}
	delete(s.JWTClaims.Extra, subjectIdentifierClaimKey)
	delete(s.JWTClaims.Extra, UpstreamSubjectClaimKey)
}

// SetSubjectIdentifier sets the RFC 9493 sub_id claim and the upstream subject
// claim from the verified upstream identity. It does nothing unless the identity
// is complete. It mutates the receiver's claims in place; a caller holding a
// session loaded from storage must Clone it first.
func (s *Session) SetSubjectIdentifier(upstream UpstreamSubject) {
	if s == nil || s.JWTSession == nil || s.JWTClaims == nil || !upstream.IsComplete() {
		return
	}
	if s.JWTClaims.Extra == nil {
		s.JWTClaims.Extra = make(map[string]any)
	}
	s.JWTClaims.Extra[subjectIdentifierClaimKey] = map[string]string{
		"format": "iss_sub",
		"iss":    upstream.Issuer,
		"sub":    upstream.Subject,
	}
	s.JWTClaims.Extra[UpstreamSubjectClaimKey] = map[string]string{
		"iss":   upstream.Issuer,
		"claim": upstream.StableClaim,
		"value": upstream.StableValue,
	}
}

// GetIDPSessionID returns the IDP session ID.
func (s *Session) GetIDPSessionID() string {
	if s == nil {
		return ""
	}
	return s.UpstreamSessionID
}

// SetIDPSessionID sets the IDP session ID.
func (s *Session) SetIDPSessionID(id string) {
	if s == nil {
		return
	}
	s.UpstreamSessionID = id
}

// Compile-time interface compliance check.
var _ UpstreamSession = (*Session)(nil)
