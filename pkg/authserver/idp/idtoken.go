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

package idp

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

	// RawClaims contains all claims from the ID Token payload as jwt.MapClaims.
	RawClaims jwt.MapClaims
}

// IDTokenValidatorConfig configures the ID Token validator.
type IDTokenValidatorConfig struct {
	// ExpectedIssuer is the expected value for the iss claim.
	ExpectedIssuer string

	// ExpectedAudience is the expected value for the aud claim (typically the client_id).
	ExpectedAudience string

	// ClockSkew is the allowed clock skew for time-based validations.
	// Default is 0 (no skew allowed).
	ClockSkew time.Duration

	// SkipExpiryValidation skips the exp claim validation.
	// Should only be used for testing.
	SkipExpiryValidation bool
}

// ID Token validation errors.
var (
	ErrIDTokenRequired       = errors.New("id token is required")
	ErrIDTokenMissingIssuer  = errors.New("id token missing iss claim")
	ErrIDTokenIssuerMismatch = errors.New("id token issuer mismatch")
	ErrIDTokenMissingAud     = errors.New("id token missing aud claim")
	ErrIDTokenAudMismatch    = errors.New("id token audience mismatch")
	ErrIDTokenMissingExp     = errors.New("id token missing exp claim")
	ErrIDTokenExpired        = errors.New("id token has expired")
	ErrIDTokenMissingNonce   = errors.New("id token missing nonce claim")
	ErrIDTokenNonceMismatch  = errors.New("id token nonce mismatch")
)

// IDTokenValidator validates OIDC ID Tokens.
// This is a minimal implementation that parses and validates basic claims
// without signature verification.
//
// TODO: Fetch JWKS from jwks_uri endpoint
// TODO: Verify JWT signature against JWKS
// TODO: Validate iat claim is within acceptable range
// TODO: Validate nonce if present (requires state tracking)
// TODO: Support at_hash validation (requires access token)
type IDTokenValidator struct {
	config IDTokenValidatorConfig
}

// NewIDTokenValidator creates a new ID Token validator.
func NewIDTokenValidator(config IDTokenValidatorConfig) (*IDTokenValidator, error) {
	if config.ExpectedIssuer == "" {
		return nil, errors.New("expected issuer is required")
	}
	if config.ExpectedAudience == "" {
		return nil, errors.New("expected audience is required")
	}
	return &IDTokenValidator{config: config}, nil
}

// ValidateIDToken validates an ID Token and returns the parsed claims.
// This performs the following validations per OIDC Core Section 3.1.3.7:
//   - Parses the JWT payload (without signature verification)
//   - Validates the iss claim matches the expected issuer
//   - Validates the aud claim contains the expected audience
//   - Validates the exp claim is not expired (unless skipped)
//
// Note: This is a minimal implementation. Signature verification should be
// added before using in production.
func (v *IDTokenValidator) ValidateIDToken(idToken string) (*IDTokenClaims, error) {
	if idToken == "" {
		return nil, ErrIDTokenRequired
	}

	// Parse the JWT payload without signature verification
	// This uses the same pattern as pkg/auth/oauth/flow.go::extractJWTClaims
	claims, err := v.parseIDToken(idToken)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ID token: %w", err)
	}

	// Validate issuer (REQUIRED per OIDC Core 3.1.3.7 step 1)
	if err := v.validateIssuer(claims); err != nil {
		return nil, err
	}

	// Validate audience (REQUIRED per OIDC Core 3.1.3.7 step 2)
	if err := v.validateAudience(claims); err != nil {
		return nil, err
	}

	// Validate expiration (REQUIRED per OIDC Core 3.1.3.7 step 9)
	if !v.config.SkipExpiryValidation {
		if err := v.validateExpiration(claims); err != nil {
			return nil, err
		}
	}

	return claims, nil
}

// ValidateIDTokenWithNonce validates an ID Token with nonce verification.
// This performs all validations from ValidateIDToken plus:
//   - Validates the nonce claim matches the expected nonce (OIDC Core Section 3.1.3.7 step 11)
//
// The expectedNonce should match the nonce that was sent in the authorization request.
// Per OIDC Core Section 3.1.2.1, when a nonce is sent in the authorization request,
// the ID Token MUST contain a nonce claim with the exact same value.
func (v *IDTokenValidator) ValidateIDTokenWithNonce(idToken, expectedNonce string) (*IDTokenClaims, error) {
	// First perform standard validation
	claims, err := v.ValidateIDToken(idToken)
	if err != nil {
		return nil, err
	}

	// Then validate nonce if expected
	if expectedNonce != "" {
		if err := validateNonce(claims, expectedNonce); err != nil {
			return nil, err
		}
	}

	return claims, nil
}

// validateNonce validates the nonce claim matches the expected value.
// Per OIDC Core Section 3.1.3.7 step 11, when a nonce was sent in the
// authorization request, the ID Token MUST contain a matching nonce claim.
func validateNonce(claims *IDTokenClaims, expectedNonce string) error {
	if claims.Nonce == "" {
		return ErrIDTokenMissingNonce
	}
	if claims.Nonce != expectedNonce {
		return fmt.Errorf("%w: expected %q, got %q",
			ErrIDTokenNonceMismatch, expectedNonce, claims.Nonce)
	}
	return nil
}

// parseIDToken parses a JWT ID Token without signature verification.
// Uses jwt.ParseUnverified which is the standard Go pattern for extracting
// claims when signature verification is not needed or will be done separately.
func (*IDTokenValidator) parseIDToken(idToken string) (*IDTokenClaims, error) {
	// Parse without verification to extract claims
	// This is the same pattern used in pkg/auth/oauth/flow.go
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	token, _, err := parser.ParseUnverified(idToken, jwt.MapClaims{})
	if err != nil {
		return nil, err
	}

	rawClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("failed to extract claims")
	}

	return extractIDTokenClaims(rawClaims), nil
}

// extractIDTokenClaims builds an IDTokenClaims struct from raw JWT claims.
func extractIDTokenClaims(rawClaims jwt.MapClaims) *IDTokenClaims {
	claims := &IDTokenClaims{RawClaims: rawClaims}

	// Extract standard claims using jwt library methods
	claims.Issuer, _ = rawClaims.GetIssuer()
	claims.Subject, _ = rawClaims.GetSubject()
	claims.Audience, _ = rawClaims.GetAudience()
	if exp, err := rawClaims.GetExpirationTime(); err == nil && exp != nil {
		claims.ExpiresAt = exp.Time
	}
	if iat, err := rawClaims.GetIssuedAt(); err == nil && iat != nil {
		claims.IssuedAt = iat.Time
	}

	// Extract optional string claims
	claims.Nonce, _ = rawClaims["nonce"].(string)
	claims.AuthorizedParty, _ = rawClaims["azp"].(string)
	claims.Email, _ = rawClaims["email"].(string)
	claims.Name, _ = rawClaims["name"].(string)
	claims.EmailVerified, _ = rawClaims["email_verified"].(bool)

	// Extract auth_time (Unix timestamp)
	claims.AuthTime = extractUnixTime(rawClaims, "auth_time")

	return claims
}

// extractUnixTime extracts a Unix timestamp from raw claims.
func extractUnixTime(claims jwt.MapClaims, key string) time.Time {
	val, ok := claims[key]
	if !ok {
		return time.Time{}
	}
	switch v := val.(type) {
	case float64:
		return time.Unix(int64(v), 0)
	case int64:
		return time.Unix(v, 0)
	default:
		return time.Time{}
	}
}

// validateIssuer validates the iss claim matches the expected issuer.
func (v *IDTokenValidator) validateIssuer(claims *IDTokenClaims) error {
	if claims.Issuer == "" {
		return ErrIDTokenMissingIssuer
	}
	if claims.Issuer != v.config.ExpectedIssuer {
		return fmt.Errorf("%w: expected %q, got %q",
			ErrIDTokenIssuerMismatch, v.config.ExpectedIssuer, claims.Issuer)
	}
	return nil
}

// validateAudience validates the aud claim contains the expected audience.
func (v *IDTokenValidator) validateAudience(claims *IDTokenClaims) error {
	if len(claims.Audience) == 0 {
		return ErrIDTokenMissingAud
	}

	for _, aud := range claims.Audience {
		if aud == v.config.ExpectedAudience {
			return nil
		}
	}

	return fmt.Errorf("%w: expected %q in audience",
		ErrIDTokenAudMismatch, v.config.ExpectedAudience)
}

// validateExpiration validates the exp claim is not expired.
func (v *IDTokenValidator) validateExpiration(claims *IDTokenClaims) error {
	if claims.ExpiresAt.IsZero() {
		return ErrIDTokenMissingExp
	}

	now := time.Now()
	expiryWithSkew := claims.ExpiresAt.Add(v.config.ClockSkew)

	if now.After(expiryWithSkew) {
		return fmt.Errorf("%w: expired at %s", ErrIDTokenExpired, claims.ExpiresAt.Format(time.RFC3339))
	}

	return nil
}
