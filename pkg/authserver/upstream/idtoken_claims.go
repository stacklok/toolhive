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
	"time"

	"github.com/go-jose/go-jose/v3"
)

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

// ID Token validation errors.
var (
	ErrIDTokenRequired          = errors.New("id token is required")
	ErrIDTokenMissingIssuer     = errors.New("id token missing iss claim")
	ErrIDTokenIssuerMismatch    = errors.New("id token issuer mismatch")
	ErrIDTokenMissingAud        = errors.New("id token missing aud claim")
	ErrIDTokenAudMismatch       = errors.New("id token audience mismatch")
	ErrIDTokenMissingExp        = errors.New("id token missing exp claim")
	ErrIDTokenExpired           = errors.New("id token has expired")
	ErrIDTokenMissingNonce      = errors.New("id token missing nonce claim")
	ErrIDTokenNonceMismatch     = errors.New("id token nonce mismatch")
	ErrIDTokenSignatureInvalid  = errors.New("id token signature verification failed")
	ErrIDTokenKeyNotFound       = errors.New("id token signing key not found in JWKS")
	ErrIDTokenJWKSFetchFailed   = errors.New("failed to fetch JWKS")
	ErrIDTokenMissingAlgorithm  = errors.New("id token missing algorithm in header")
	ErrIDTokenUnsupportedAlg    = errors.New("id token uses unsupported algorithm")
	ErrIDTokenMissingSigningKey = errors.New("jwks_uri is required for signature verification")
)

// supportedSignatureAlgorithms defines the asymmetric signature algorithms supported
// for validating ID tokens received from external upstream Identity Providers.
// This follows OIDC Core Section 3.1.3.7 validation requirements.
//
// Symmetric algorithms (HS256, etc.) are intentionally excluded because:
// - They require sharing the client secret for verification
// - They are inappropriate when validating tokens from external IDPs
// - RFC 8725 (JWT BCP) recommends asymmetric algorithms for this use case
var supportedSignatureAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512, // RSA PKCS#1 v1.5
	jose.ES256, jose.ES384, jose.ES512, // ECDSA
	jose.PS256, jose.PS384, jose.PS512, // RSA-PSS
	jose.EdDSA, // Edwards curve
}
