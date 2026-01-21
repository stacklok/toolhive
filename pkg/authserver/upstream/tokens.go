// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
