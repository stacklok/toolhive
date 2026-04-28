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

	// Subject is the canonical user identifier carried into session storage
	// and issued JWTs. Source by provider type:
	//   - OIDC: "sub" from the validated ID token.
	//   - OAuth2 with userInfo: "sub" (or field-mapped) from userinfo.
	//   - OAuth2 without userInfo: synthesized "tk-…" value derived from the
	//     access token (Synthetic=true; see synthesizeSubjectFromAccessToken).
	//
	// Stability: stable across refresh-token rotation that preserves the
	// access token; in synthesis mode it rotates per fresh authorization
	// code flow, so callers must treat synthesized Subjects as ephemeral
	// session keys, not stable per-user identifiers.
	Subject string

	// Name is the user's display name from the upstream IDP (optional).
	Name string

	// Email is the user's email address from the upstream IDP (optional).
	Email string

	// Synthetic is true when Subject was generated locally because the
	// upstream has no userinfo or ID-token-derived identity. Synthetic
	// subjects rotate per re-auth; callers MUST NOT persist them as stable
	// per-user keys (doing so grows `users` without bound). Use the
	// synthesized Subject as an ephemeral session key only.
	Synthetic bool
}

// ErrIdentityResolutionFailed indicates identity could not be determined.
var ErrIdentityResolutionFailed = errors.New("failed to resolve user identity")
