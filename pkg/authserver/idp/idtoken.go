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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v3"
	"github.com/go-jose/go-jose/v3/jwt"
	"github.com/ory/fosite"
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

// idTokenValidatorConfig configures the ID Token validator.
type idTokenValidatorConfig struct {
	// expectedIssuer is the expected value for the iss claim.
	expectedIssuer string

	// expectedAudience is the expected value for the aud claim (typically the client_id).
	expectedAudience string

	// jwksURI is the URL of the JWKS endpoint for signature verification.
	// If empty, signature verification is skipped.
	jwksURI string

	// clockSkew is the allowed clock skew for time-based validations.
	// Default is 0 (no skew allowed).
	clockSkew time.Duration

	// skipExpiryValidation skips the exp claim validation.
	// Should only be used for testing.
	skipExpiryValidation bool

	// skipSignatureVerification skips JWT signature verification.
	// Should only be used for testing.
	skipSignatureVerification bool
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
// for ID token verification per OIDC Core Section 3.1.3.7.
// Symmetric algorithms (HS256, etc.) are excluded as they require the client secret
// and are less secure for ID token validation from external IDPs.
var supportedSignatureAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512, // RSA PKCS#1 v1.5
	jose.ES256, jose.ES384, jose.ES512, // ECDSA
	jose.PS256, jose.PS384, jose.PS512, // RSA-PSS
	jose.EdDSA, // Edwards curve
}

// idTokenValidator validates OIDC ID Tokens.
// This implementation verifies JWT signatures using the JWKS from the upstream IDP
// and validates standard OIDC claims per OIDC Core Section 3.1.3.7.
//
// TODO: Validate iat claim is within acceptable range
// TODO: Support at_hash validation (requires access token)
type idTokenValidator struct {
	config      idTokenValidatorConfig
	jwksFetcher fosite.JWKSFetcherStrategy
}

// newIDTokenValidator creates a new ID Token validator.
// If jwksURI is provided, signature verification will be enabled.
// If jwksURI is empty and skipSignatureVerification is false, validation will fail
// when validateIDToken is called with a signed token.
func newIDTokenValidator(config idTokenValidatorConfig) (*idTokenValidator, error) {
	if config.expectedIssuer == "" {
		return nil, errors.New("expected issuer is required")
	}
	if config.expectedAudience == "" {
		return nil, errors.New("expected audience is required")
	}

	v := &idTokenValidator{config: config}

	// Initialize JWKS fetcher if JWKS URI is provided
	if config.jwksURI != "" {
		v.jwksFetcher = fosite.NewDefaultJWKSFetcherStrategy()
	}

	return v, nil
}

// validateIDToken validates an ID Token and returns the parsed claims.
// This performs the following validations per OIDC Core Section 3.1.3.7:
//   - Verifies the JWT signature against the IDP's JWKS (if jwksURI configured)
//   - Validates the iss claim matches the expected issuer
//   - Validates the aud claim contains the expected audience
//   - Validates the exp claim is not expired (unless skipped)
func (v *idTokenValidator) validateIDToken(idToken string) (*IDTokenClaims, error) {
	return v.validateIDTokenWithContext(context.Background(), idToken)
}

// validateIDTokenWithContext validates an ID Token with a context for JWKS fetching.
func (v *idTokenValidator) validateIDTokenWithContext(ctx context.Context, idToken string) (*IDTokenClaims, error) {
	if idToken == "" {
		return nil, ErrIDTokenRequired
	}

	// Verify signature and extract claims
	claims, err := v.verifyAndParseIDToken(ctx, idToken)
	if err != nil {
		return nil, err
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
	if !v.config.skipExpiryValidation {
		if err := v.validateExpiration(claims); err != nil {
			return nil, err
		}
	}

	return claims, nil
}

// validateIDTokenWithNonce validates an ID Token with nonce verification.
// This performs all validations from validateIDToken plus:
//   - Validates the nonce claim matches the expected nonce (OIDC Core Section 3.1.3.7 step 11)
//
// The expectedNonce should match the nonce that was sent in the authorization request.
// Per OIDC Core Section 3.1.2.1, when a nonce is sent in the authorization request,
// the ID Token MUST contain a nonce claim with the exact same value.
func (v *idTokenValidator) validateIDTokenWithNonce(idToken, expectedNonce string) (*IDTokenClaims, error) {
	return v.validateIDTokenWithNonceAndContext(context.Background(), idToken, expectedNonce)
}

// validateIDTokenWithNonceAndContext validates an ID Token with nonce verification and context.
func (v *idTokenValidator) validateIDTokenWithNonceAndContext(
	ctx context.Context,
	idToken, expectedNonce string,
) (*IDTokenClaims, error) {
	// First perform standard validation
	claims, err := v.validateIDTokenWithContext(ctx, idToken)
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

// verifyAndParseIDToken verifies the JWT signature and extracts claims.
// If signature verification is enabled (jwksURI is set), it fetches the JWKS,
// finds the appropriate key, and verifies the signature.
// If signature verification is skipped (for testing), it parses without verification.
func (v *idTokenValidator) verifyAndParseIDToken(ctx context.Context, idToken string) (*IDTokenClaims, error) {
	// Parse the JWT to inspect headers and get claims
	parsedJWT, err := jwt.ParseSigned(idToken)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ID token: %w", err)
	}

	// Get raw claims for extraction (without verification first)
	var rawClaims map[string]any
	if err := parsedJWT.UnsafeClaimsWithoutVerification(&rawClaims); err != nil {
		return nil, fmt.Errorf("failed to extract claims: %w", err)
	}

	// Skip signature verification if configured (for testing only)
	if v.config.skipSignatureVerification {
		return extractIDTokenClaims(rawClaims), nil
	}

	// Verify signature is required
	if v.jwksFetcher == nil {
		if v.config.jwksURI == "" {
			return nil, ErrIDTokenMissingSigningKey
		}
		return nil, fmt.Errorf("JWKS fetcher not initialized")
	}

	// Get the key to use for verification
	key, err := v.getVerificationKey(ctx, parsedJWT)
	if err != nil {
		return nil, err
	}

	// Verify signature and extract claims
	var verifiedClaims map[string]any
	if err := parsedJWT.Claims(key, &verifiedClaims); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIDTokenSignatureInvalid, err)
	}

	return extractIDTokenClaims(verifiedClaims), nil
}

// getVerificationKey fetches the JWKS and finds the appropriate key for verification.
// It handles key rotation by matching the kid (key ID) from the JWT header.
func (v *idTokenValidator) getVerificationKey(ctx context.Context, parsedJWT *jwt.JSONWebToken) (any, error) {
	// Fetch JWKS from the upstream IDP
	jwks, err := v.jwksFetcher.Resolve(ctx, v.config.jwksURI, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrIDTokenJWKSFetchFailed, err)
	}

	// Get the key ID from JWT header
	if len(parsedJWT.Headers) == 0 {
		return nil, fmt.Errorf("ID token has no headers")
	}

	header := parsedJWT.Headers[0]

	// Validate the algorithm is supported
	if header.Algorithm == "" {
		return nil, ErrIDTokenMissingAlgorithm
	}
	if !isAlgorithmSupported(jose.SignatureAlgorithm(header.Algorithm)) {
		return nil, fmt.Errorf("%w: %s", ErrIDTokenUnsupportedAlg, header.Algorithm)
	}

	// Find the key by kid
	kid := header.KeyID
	if kid == "" {
		// If no kid, and JWKS has exactly one key, use it
		if len(jwks.Keys) == 1 {
			return jwks.Keys[0].Key, nil
		}
		return nil, fmt.Errorf("%w: no kid in token header and JWKS has %d keys", ErrIDTokenKeyNotFound, len(jwks.Keys))
	}

	// Look up the key by kid
	keys := jwks.Key(kid)
	if len(keys) == 0 {
		// Key not found - try refreshing the JWKS in case of key rotation
		jwks, err = v.jwksFetcher.Resolve(ctx, v.config.jwksURI, true) // Force refresh
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrIDTokenJWKSFetchFailed, err)
		}
		keys = jwks.Key(kid)
		if len(keys) == 0 {
			return nil, fmt.Errorf("%w: kid=%s", ErrIDTokenKeyNotFound, kid)
		}
	}

	// Use the first matching key
	return keys[0].Key, nil
}

// isAlgorithmSupported checks if the algorithm is in our supported list.
func isAlgorithmSupported(alg jose.SignatureAlgorithm) bool {
	for _, supported := range supportedSignatureAlgorithms {
		if supported == alg {
			return true
		}
	}
	return false
}

// extractIDTokenClaims builds an IDTokenClaims struct from raw JWT claims.
func extractIDTokenClaims(rawClaims map[string]any) *IDTokenClaims {
	claims := &IDTokenClaims{RawClaims: rawClaims}

	// Extract standard claims
	claims.Issuer, _ = rawClaims["iss"].(string)
	claims.Subject, _ = rawClaims["sub"].(string)
	claims.Audience = extractAudience(rawClaims)
	claims.ExpiresAt = extractUnixTime(rawClaims, "exp")
	claims.IssuedAt = extractUnixTime(rawClaims, "iat")

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

// extractAudience extracts the audience claim which can be a string or array of strings.
func extractAudience(claims map[string]any) []string {
	audVal, ok := claims["aud"]
	if !ok {
		return nil
	}

	// Single string audience
	if aud, ok := audVal.(string); ok {
		return []string{aud}
	}

	// Array of strings
	if audArray, ok := audVal.([]any); ok {
		result := make([]string, 0, len(audArray))
		for _, v := range audArray {
			if s, ok := v.(string); ok {
				result = append(result, s)
			}
		}
		return result
	}

	return nil
}

// extractUnixTime extracts a Unix timestamp from raw claims.
func extractUnixTime(claims map[string]any, key string) time.Time {
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
func (v *idTokenValidator) validateIssuer(claims *IDTokenClaims) error {
	if claims.Issuer == "" {
		return ErrIDTokenMissingIssuer
	}
	if claims.Issuer != v.config.expectedIssuer {
		return fmt.Errorf("%w: expected %q, got %q",
			ErrIDTokenIssuerMismatch, v.config.expectedIssuer, claims.Issuer)
	}
	return nil
}

// validateAudience validates the aud claim contains the expected audience.
func (v *idTokenValidator) validateAudience(claims *IDTokenClaims) error {
	if len(claims.Audience) == 0 {
		return ErrIDTokenMissingAud
	}

	for _, aud := range claims.Audience {
		if aud == v.config.expectedAudience {
			return nil
		}
	}

	return fmt.Errorf("%w: expected %q in audience",
		ErrIDTokenAudMismatch, v.config.expectedAudience)
}

// validateExpiration validates the exp claim is not expired.
func (v *idTokenValidator) validateExpiration(claims *IDTokenClaims) error {
	if claims.ExpiresAt.IsZero() {
		return ErrIDTokenMissingExp
	}

	now := time.Now()
	expiryWithSkew := claims.ExpiresAt.Add(v.config.clockSkew)

	if now.After(expiryWithSkew) {
		return fmt.Errorf("%w: expired at %s", ErrIDTokenExpired, claims.ExpiresAt.Format(time.RFC3339))
	}

	return nil
}
