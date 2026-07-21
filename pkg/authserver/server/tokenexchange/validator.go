// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tokenexchange implements RFC 8693 token exchange for the authorization server.
// It provides validation of subject tokens that were issued by the same authorization
// server, enabling agents to act on behalf of users through delegation.
package tokenexchange

import (
	"context"
	"fmt"
	"slices"
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
	// Scopes is the space-delimited scope string from the "scope" claim.
	// Empty if the subject token carries no scope claim.
	Scopes string
	// MayAct holds the authorized actor from the "may_act" claim (RFC 8693 §4.1).
	// Nil when the subject token does not carry a may_act claim.
	MayAct *MayActClaim
	// Extra contains all non-standard claims not captured by other fields.
	Extra map[string]any
}

// MayActClaim represents the RFC 8693 §4.1 may_act claim from a subject token.
// It identifies the party authorized to act on behalf of the subject.
type MayActClaim struct {
	Sub string `json:"sub"`
}

// SubjectTokenValidator validates subject tokens presented during RFC 8693 token exchange.
// It verifies that the token was issued by this authorization server by checking
// the signature against the server's own JWKS, and validates standard JWT claims.
type SubjectTokenValidator struct {
	publicJWKS       *jose.JSONWebKeySet
	issuer           string
	allowedAudiences []string
}

// NewSubjectTokenValidator creates a new validator for subject tokens.
// The jwks parameter must be non-nil and contain only the authorization server's
// public signing keys (e.g. AuthorizationServerConfig.PublicJWKS) — the validator
// only ever verifies signatures, so it must not be handed private key material.
// The issuer parameter is the expected "iss" claim value. allowedAudiences is the
// set of audiences this server accepts in a subject token's "aud" claim; per the
// same secure default as AuthorizationServerConfig.AllowedAudiences, an empty
// allowedAudiences rejects every subject token rather than skipping the check.
func NewSubjectTokenValidator(
	jwks *jose.JSONWebKeySet, issuer string, allowedAudiences []string,
) (*SubjectTokenValidator, error) {
	if jwks == nil {
		return nil, fmt.Errorf("JWKS must not be nil")
	}
	if issuer == "" {
		return nil, fmt.Errorf("issuer must not be empty")
	}
	return &SubjectTokenValidator{
		publicJWKS:       jwks,
		issuer:           issuer,
		allowedAudiences: allowedAudiences,
	}, nil
}

// Validate parses and verifies a raw JWT subject token.
// It checks the signature against the server's JWKS, validates issuer and audience,
// ensures the token is not expired, and requires a subject claim for delegation.
//
// The subject token's "aud" claim is checked against allowedAudiences, but this
// validator deliberately does not require that the authorization server itself
// (i.e. the token endpoint) be among those audiences. RFC 8693 leaves subject-token
// validation criteria out of scope, and ToolHive's vMCP flow legitimately exchanges
// tokens addressed to a downstream/upstream resource rather than the AS. The residual
// cross-resource risk is mitigated elsewhere: the token is still pinned to the
// server-wide allowedAudiences, and the handler enforces the requested resource
// against the client's registered audiences.
//
// Returns the validated claims on success, or a descriptive error on failure.
func (v *SubjectTokenValidator) Validate(_ context.Context, rawToken string) (*ValidatedClaims, error) {
	parsedToken, err := jwt.ParseSigned(rawToken, allowedSignatureAlgorithms)
	if err != nil {
		return nil, fmt.Errorf("subject token is not a valid JWT: %w", err)
	}

	standardClaims, extraClaims, err := verifySignature(parsedToken, v.publicJWKS)
	if err != nil {
		return nil, err
	}

	// Validate issuer and expiry. Audience is intentionally excluded from
	// jwt.Expected here — go-jose's AnyAudience check is skipped entirely when
	// empty, which is the opposite of this server's secure default (empty
	// AllowedAudiences means no audience is permitted). Audience is checked
	// explicitly below so an empty allowlist fails closed.
	expected := jwt.Expected{
		Issuer: v.issuer,
	}
	if err := standardClaims.ValidateWithLeeway(expected, 0); err != nil {
		return nil, fmt.Errorf("subject token claims validation failed: %w", err)
	}

	if err := validateAudience(standardClaims.Audience, v.allowedAudiences); err != nil {
		return nil, fmt.Errorf("subject token claims validation failed: %w", err)
	}

	// Expiry is required for delegation — a subject token without an expiry
	// cannot be safely bounded by the delegation lifetime.
	if standardClaims.Expiry == nil {
		return nil, fmt.Errorf("subject token is missing required 'exp' claim")
	}

	// Subject is required for delegation — the token must identify the user.
	if standardClaims.Subject == "" {
		return nil, fmt.Errorf("subject token is missing required 'sub' claim")
	}

	// If may_act is present, it must be a well-formed object with a string sub.
	// A present-but-malformed may_act is treated as an invalid token, not
	// an absent one — fail closed to prevent silent downgrade to client_id.
	if rawMayAct, ok := extraClaims["may_act"]; ok && rawMayAct != nil {
		m, ok := rawMayAct.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("subject token has malformed 'may_act' claim: expected a JSON object")
		}
		sub, ok := m["sub"].(string)
		if !ok || sub == "" {
			return nil, fmt.Errorf("subject token has malformed 'may_act' claim: missing or invalid 'sub'")
		}
	}

	return buildValidatedClaims(standardClaims, extraClaims), nil
}

// verifySignature attempts to verify the JWT signature using the provided public keys.
// If the JWT header names a key ID (kid) that matches a key in the JWKS, only that
// key is tried: a token naming a kid but signed by a different key is a spoofed-kid
// attempt and must fail, not fall back to verifying against the rest of the keyset.
// When there is no kid, or the kid doesn't match any key in the JWKS, every key is
// tried in turn. Returns on the first successful verification. On failure, the last
// verification error is wrapped.
func verifySignature(
	token *jwt.JSONWebToken,
	publicJWKS *jose.JSONWebKeySet,
) (jwt.Claims, map[string]any, error) {
	candidates := publicJWKS.Keys
	if len(token.Headers) > 0 && token.Headers[0].KeyID != "" {
		if matched := publicJWKS.Key(token.Headers[0].KeyID); len(matched) > 0 {
			candidates = matched
		}
	}

	var lastErr error
	for _, key := range candidates {
		var claims jwt.Claims
		extra := map[string]any{}
		err := token.Claims(key, &claims, &extra)
		if err == nil {
			return claims, extra, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return jwt.Claims{}, nil, fmt.Errorf("subject token signature verification failed: %w", lastErr)
	}
	return jwt.Claims{}, nil, fmt.Errorf("subject token signature verification failed: no keys in JWKS")
}

// validateAudience checks that tokenAudience intersects allowedAudiences.
// Per this server's secure default (see AuthorizationServerConfig.AllowedAudiences),
// an empty allowedAudiences means no audience is permitted, so the check fails
// closed rather than skipping validation.
func validateAudience(tokenAudience jwt.Audience, allowedAudiences []string) error {
	if len(allowedAudiences) == 0 {
		return fmt.Errorf("no audiences are configured on this server")
	}
	for _, aud := range tokenAudience {
		if slices.Contains(allowedAudiences, aud) {
			return nil
		}
	}
	return fmt.Errorf("token audience %v does not match any allowed audience", []string(tokenAudience))
}

// buildValidatedClaims constructs a ValidatedClaims from standard and extra claims.
func buildValidatedClaims(
	standard jwt.Claims,
	extra map[string]any,
) *ValidatedClaims {
	vc := &ValidatedClaims{
		Subject:  standard.Subject,
		Issuer:   standard.Issuer,
		Audience: []string(standard.Audience),
		JWTID:    standard.ID,
		Extra:    make(map[string]any),
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
		assignClaim(vc, k, val)
	}

	return vc
}

// assignClaim routes one non-standard JWT claim onto its structured
// ValidatedClaims field, or into Extra if it isn't a well-known claim.
// Registered JWT claims (sub, iss, aud, exp, iat, nbf, jti) are dropped —
// they're already captured in buildValidatedClaims's structured fields.
func assignClaim(vc *ValidatedClaims, key string, val any) {
	switch key {
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
	case "scope":
		if s, ok := val.(string); ok {
			vc.Scopes = s
		}
	case "may_act":
		if m, ok := val.(map[string]any); ok {
			if s, ok := m["sub"].(string); ok {
				vc.MayAct = &MayActClaim{Sub: s}
			}
		}
	case "sub", "iss", "aud", "exp", "iat", "nbf", "jti":
		// Skip registered JWT claims — already in structured fields.
	default:
		vc.Extra[key] = val
	}
}
