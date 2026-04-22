// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tokenexchange implements RFC 8693 token exchange for the authorization server.
// It provides validation of subject tokens that were issued by the same authorization
// server, enabling agents to act on behalf of users through delegation.
package tokenexchange

import (
	"context"
	"fmt"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// allowedSignatureAlgorithms lists the signing algorithms accepted during JWT verification.
// This restricts which algorithms the validator will accept, preventing algorithm
// confusion attacks where an attacker might try to use a weaker algorithm.
var allowedSignatureAlgorithms = []jose.SignatureAlgorithm{
	jose.ES256,
	jose.ES384,
	jose.RS256,
	jose.RS384,
	jose.RS512,
	jose.EdDSA,
}

// ValidatedClaims holds the verified claims extracted from a subject token.
// All fields are populated from a successfully validated JWT that was issued
// by this authorization server.
type ValidatedClaims struct {
	// Subject is the user identity from the "sub" claim (required for delegation).
	Subject string
	// Issuer is the token issuer from the "iss" claim.
	Issuer string
	// Audience is the list of intended recipients from the "aud" claim.
	Audience []string
	// Expiry is the token expiration time from the "exp" claim.
	Expiry time.Time
	// IssuedAt is the token issuance time from the "iat" claim.
	IssuedAt time.Time
	// JWTID is the unique token identifier from the "jti" claim.
	JWTID string
	// Name is the user's display name from the custom "name" claim.
	Name string
	// Email is the user's email address from the custom "email" claim.
	Email string
	// ClientID is the OAuth client ID from the custom "client_id" claim.
	ClientID string
	// Extra contains all non-standard claims not captured by other fields.
	Extra map[string]interface{}
}

// SubjectTokenValidator validates subject tokens presented during RFC 8693 token exchange.
// It verifies that the token was issued by this authorization server by checking
// the signature against the server's own JWKS, and validates standard JWT claims.
type SubjectTokenValidator struct {
	jwks   *jose.JSONWebKeySet
	issuer string
}

// NewSubjectTokenValidator creates a new validator for subject tokens.
// The jwks parameter must be non-nil and contain the authorization server's
// signing keys (private keys are accepted; only the public portion is used
// for verification). The issuer parameter is the expected "iss" claim value
// and is also used as the expected audience, since tokens issued by this
// server are intended for this server during token exchange.
func NewSubjectTokenValidator(jwks *jose.JSONWebKeySet, issuer string) (*SubjectTokenValidator, error) {
	if jwks == nil {
		return nil, fmt.Errorf("JWKS must not be nil")
	}
	if issuer == "" {
		return nil, fmt.Errorf("issuer must not be empty")
	}
	return &SubjectTokenValidator{
		jwks:   jwks,
		issuer: issuer,
	}, nil
}

// Validate parses and verifies a raw JWT subject token.
// It checks the signature against the server's JWKS, validates issuer and audience,
// ensures the token is not expired, and requires a subject claim for delegation.
// Returns the validated claims on success, or a descriptive error on failure.
func (v *SubjectTokenValidator) Validate(_ context.Context, rawToken string) (*ValidatedClaims, error) {
	parsedToken, err := jwt.ParseSigned(rawToken, allowedSignatureAlgorithms)
	if err != nil {
		return nil, fmt.Errorf("subject token is not a valid JWT: %w", err)
	}

	// Extract public keys from the JWKS for signature verification.
	publicJWKS := v.publicKeys()

	// Try each key in the JWKS until one verifies the signature.
	var standardClaims jwt.Claims
	var extraClaims map[string]interface{}

	if err := verifySignature(parsedToken, publicJWKS, &standardClaims, &extraClaims); err != nil {
		return nil, err
	}

	// Validate standard claims: issuer, audience, and expiry.
	expected := jwt.Expected{
		Issuer:      v.issuer,
		AnyAudience: jwt.Audience{v.issuer},
	}
	if err := standardClaims.ValidateWithLeeway(expected, 0); err != nil {
		return nil, fmt.Errorf("subject token claims validation failed: %w", err)
	}

	// Subject is required for delegation — the token must identify the user.
	if standardClaims.Subject == "" {
		return nil, fmt.Errorf("subject token is missing required 'sub' claim")
	}

	return buildValidatedClaims(standardClaims, extraClaims), nil
}

// verifySignature attempts to verify the JWT signature using the provided public keys.
// It tries each key in the JWKS and returns on the first successful verification.
// On failure, the last verification error is wrapped for debugging.
func verifySignature(
	token *jwt.JSONWebToken,
	publicJWKS *jose.JSONWebKeySet,
	standardClaims *jwt.Claims,
	extraClaims *map[string]interface{},
) error {
	var lastErr error
	for _, key := range publicJWKS.Keys {
		*extraClaims = make(map[string]interface{})
		if err := token.Claims(key, standardClaims, extraClaims); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	if lastErr != nil {
		return fmt.Errorf("subject token signature verification failed: %w", lastErr)
	}
	return fmt.Errorf("subject token signature verification failed: no keys in JWKS")
}

// publicKeys extracts the public key portion of each key in the JWKS.
func (v *SubjectTokenValidator) publicKeys() *jose.JSONWebKeySet {
	result := &jose.JSONWebKeySet{
		Keys: make([]jose.JSONWebKey, 0, len(v.jwks.Keys)),
	}
	for _, key := range v.jwks.Keys {
		result.Keys = append(result.Keys, key.Public())
	}
	return result
}

// buildValidatedClaims constructs a ValidatedClaims from standard and extra claims.
func buildValidatedClaims(
	standard jwt.Claims,
	extra map[string]interface{},
) *ValidatedClaims {
	vc := &ValidatedClaims{
		Subject:  standard.Subject,
		Issuer:   standard.Issuer,
		Audience: []string(standard.Audience),
		JWTID:    standard.ID,
		Extra:    make(map[string]interface{}),
	}

	if standard.Expiry != nil {
		vc.Expiry = standard.Expiry.Time()
	}
	if standard.IssuedAt != nil {
		vc.IssuedAt = standard.IssuedAt.Time()
	}

	// Extract well-known custom claims and collect the rest into Extra.
	// Standard JWT registered claims are filtered out since they are already
	// captured in the structured fields above.
	for k, val := range extra {
		switch k {
		case "name":
			if s, ok := val.(string); ok {
				vc.Name = s
			}
		case "email":
			if s, ok := val.(string); ok {
				vc.Email = s
			}
		case "client_id":
			if s, ok := val.(string); ok {
				vc.ClientID = s
			}
		case "sub", "iss", "aud", "exp", "iat", "nbf", "jti":
			// Skip registered JWT claims — already in structured fields.
		default:
			vc.Extra[k] = val
		}
	}

	return vc
}
