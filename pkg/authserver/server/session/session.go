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

// ClientIDClaimKey is the JWT claim key for the OAuth client ID.
// This identifies which client was issued the token.
const ClientIDClaimKey = "client_id"

// Session extends fosite's JWT session with an IDP session reference.
// This allows the authorization server to link issued tokens to
// upstream IDP tokens stored separately.
//
// Most methods are provided by the embedded *oauth2.JWTSession. This type
// only adds IDP session tracking and overrides Clone() to include the
// UpstreamSessionID field.
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
}

// New creates a new Session with the given subject, IDP session ID, and client ID.
//
// Parameters:
//   - subject: The OAuth subject (user identifier). May be empty for placeholder sessions.
//   - idpSessionID: Links to upstream IDP tokens in storage. If provided, it will be
//     included in the JWT claims as "tsid" to allow proxy middleware to look up
//     upstream IDP tokens.
//   - clientID: The OAuth client ID. When provided, it will be included in the JWT
//     claims as "client_id" for binding verification per RFC 9068.
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
func New(subject, idpSessionID, clientID string) *Session {
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
// This overrides the embedded JWTSession.Clone() to also copy UpstreamSessionID.
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
