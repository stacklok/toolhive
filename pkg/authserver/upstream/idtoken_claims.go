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

import "time"

// IDTokenClaims contains the standard OIDC ID Token claims.
// See OIDC Core Section 2 for claim definitions.
type IDTokenClaims struct {
	// Issuer is the issuer identifier (iss claim).
	Issuer string

	// Subject is the subject identifier (sub claim).
	Subject string

	// Audience contains the audience(s) this ID Token is intended for (aud claim).
	Audience []string

	// ExpiresAt is the expiration time (exp claim).
	ExpiresAt time.Time

	// IssuedAt is the time at which the ID Token was issued (iat claim).
	IssuedAt time.Time

	// Nonce is the value used to associate a client session with an ID Token (nonce claim).
	Nonce string

	// AuthTime is the time when the end-user authentication occurred (auth_time claim).
	AuthTime time.Time

	// AuthorizedParty is the party to which the ID Token was issued (azp claim).
	AuthorizedParty string

	// Email is the user's email address.
	Email string

	// EmailVerified indicates whether the user's email has been verified.
	EmailVerified bool

	// Name is the user's full name.
	Name string

	// RawClaims contains all claims from the ID Token payload.
	RawClaims map[string]any
}
