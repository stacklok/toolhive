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

package upstream

import (
	"errors"
)

// ProviderType identifies the type of upstream Identity Provider.
type ProviderType string

// Identity holds the identity resolved from an upstream IDP after
// exchanging an authorization code. It combines the tokens (for storage and
// refresh) with the subject identifier (for internal user resolution).
type Identity struct {
	// Tokens contains the tokens obtained from the upstream IDP.
	Tokens *Tokens

	// Subject is the canonical user identifier used in the embedded auth
	// server's session storage and in the JWTs it issues. Its source depends
	// on the provider:
	//
	//   - For OIDC providers, Subject is the "sub" claim from the validated
	//     ID token.
	//   - For OAuth2 providers with a userinfo endpoint configured, Subject
	//     is the "sub" (or field-mapped equivalent) returned by that
	//     endpoint.
	//   - For OAuth2 providers with no userinfo endpoint configured, Subject
	//     is a synthesized opaque value with the "tk-" prefix derived from a
	//     SHA-256 prefix of the access token. In this branch Synthetic is
	//     true; see synthesizeSubjectFromAccessToken.
	//
	// Stability contract: across a refresh-token rotation that returns the
	// same access token (or while the original access token is still in
	// flight), Subject is stable. On a fresh authorization code flow that
	// issues a new access token, Subject for the synthesized branch rotates
	// — callers must treat such Subjects as ephemeral session keys, not
	// stable per-user identifiers.
	Subject string

	// Name is the user's display name from the upstream IDP (optional).
	Name string

	// Email is the user's email address from the upstream IDP (optional).
	Email string

	// Synthetic is true when Subject was synthesized by the upstream provider
	// (rather than resolved from a userinfo endpoint or ID token) because the
	// upstream exposes no real user-identity surface. Synthetic subjects rotate
	// on every re-authentication, so callers MUST NOT persist them as a stable
	// per-user key — doing so creates an unbounded `users` table over time.
	// Use the synthesized Subject as an ephemeral session key only and bypass
	// the user-resolution layer that would otherwise create a new internal user
	// record on each re-auth.
	Synthetic bool
}

// ErrIdentityResolutionFailed indicates identity could not be determined.
var ErrIdentityResolutionFailed = errors.New("failed to resolve user identity")
