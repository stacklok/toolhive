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
	"time"
)

// tokenExpirationBuffer is the time buffer before actual expiration to consider a token expired.
// This accounts for clock skew and network latency.
const tokenExpirationBuffer = 30 * time.Second

// Tokens represents the tokens obtained from an upstream Identity Provider.
// This type is used for token exchange with the IDP, but stored separately
// (see storage.IDPTokens for the storage representation).
type Tokens struct {
	// AccessToken is the access token from the upstream IDP.
	AccessToken string

	// RefreshToken is the refresh token from the upstream IDP (if provided).
	RefreshToken string

	// IDToken is the ID token from the upstream IDP (for OIDC).
	IDToken string

	// ExpiresAt is when the access token expires.
	ExpiresAt time.Time
}

// IsExpired returns true if the access token has expired or will expire within the buffer period.
// Returns true for nil receivers (treating nil tokens as expired).
func (t *Tokens) IsExpired() bool {
	if t == nil {
		return true
	}
	return time.Now().Add(tokenExpirationBuffer).After(t.ExpiresAt)
}
