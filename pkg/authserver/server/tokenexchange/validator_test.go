// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testIssuer = "https://auth.example.com"

// testJWKS holds a signing key and JWKS for test token generation.
type testJWKS struct {
	privateKey *ecdsa.PrivateKey
	jwk        jose.JSONWebKey
	jwks       *jose.JSONWebKeySet
}

// newTestJWKS generates a fresh ECDSA P-256 key pair and wraps it in a JWKS.
func newTestJWKS(t *testing.T) *testJWKS {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	jwk := jose.JSONWebKey{
		Key:       privateKey,
		KeyID:     "test-key-1",
		Algorithm: string(jose.ES256),
		Use:       "sig",
	}

	return &testJWKS{
		privateKey: privateKey,
		jwk:        jwk,
		jwks:       &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}},
	}
}

// publicJWKS returns the public-only JWKS, mirroring how Factory constructs
// the validator from AuthorizationServerConfig.PublicJWKS().
func (tj *testJWKS) publicJWKS() *jose.JSONWebKeySet {
	return &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{tj.jwk.Public()}}
}

// signToken creates a signed JWT with the given claims using the test JWKS key.
func (tj *testJWKS) signToken(t *testing.T, claims jwt.Claims, extraClaims map[string]any) string {
	t.Helper()

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: tj.jwk},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	require.NoError(t, err)

	builder := jwt.Signed(signer).Claims(claims)
	if extraClaims != nil {
		builder = builder.Claims(extraClaims)
	}

	raw, err := builder.Serialize()
	require.NoError(t, err)

	return raw
}

// validClaims returns a set of standard claims that pass all validation checks.
func validClaims() jwt.Claims {
	now := time.Now()
	return jwt.Claims{
		Subject:   "user-123",
		Issuer:    testIssuer,
		Audience:  jwt.Audience{testIssuer},
		Expiry:    jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		ID:        "jti-abc-123",
	}
}

// validExtraClaims returns extra claims that match the session package claim keys.
func validExtraClaims() map[string]any {
	return map[string]any{
		"name":      "Test User",
		"email":     "test@example.com",
		"client_id": testAgentClientID,
		"tsid":      "session-xyz",
	}
}

func TestSelfIssuedTokenValidator_NewValidation(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)

	t.Run("nil JWKS returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewSelfIssuedTokenValidator(nil, testIssuer, []string{testIssuer})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JWKS must not be nil")
	})

	t.Run("empty issuer returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewSelfIssuedTokenValidator(tj.publicJWKS(), "", []string{testIssuer})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "issuer must not be empty")
	})

	t.Run("valid params succeed", func(t *testing.T) {
		t.Parallel()
		v, err := NewSelfIssuedTokenValidator(tj.publicJWKS(), testIssuer, []string{testIssuer})
		require.NoError(t, err)
		assert.NotNil(t, v)
	})
}

func TestSelfIssuedTokenValidator_Validate(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)

	tests := []struct {
		name        string
		token       func(t *testing.T) string
		wantErr     bool
		errContains string
		check       func(t *testing.T, vc *ValidatedClaims)
	}{
		{
			name: "valid token with all claims",
			token: func(t *testing.T) string {
				t.Helper()
				return tj.signToken(t, validClaims(), validExtraClaims())
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.Equal(t, "user-123", vc.Subject)
				assert.Equal(t, testIssuer, vc.Issuer)
				assert.Equal(t, []string{testIssuer}, vc.Audience)
				assert.Equal(t, "jti-abc-123", vc.JWTID)
				assert.Equal(t, "Test User", vc.Name)
				assert.Equal(t, "test@example.com", vc.Email)
				assert.Equal(t, testAgentClientID, vc.ClientID)
				assert.False(t, vc.Expiry.IsZero())
				assert.False(t, vc.IssuedAt.IsZero())
				// tsid should be in Extra since it's not a well-known claim field
				assert.Equal(t, "session-xyz", vc.Extra["tsid"])
				// Standard JWT claims should NOT be in Extra
				_, hasSub := vc.Extra["sub"]
				assert.False(t, hasSub, "standard claim 'sub' should not be in Extra")
				_, hasIss := vc.Extra["iss"]
				assert.False(t, hasIss, "standard claim 'iss' should not be in Extra")
			},
		},
		{
			name: "expired token",
			token: func(t *testing.T) string {
				t.Helper()
				claims := validClaims()
				claims.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Hour))
				claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
				return tj.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "claims validation failed",
		},
		{
			name: "wrong issuer",
			token: func(t *testing.T) string {
				t.Helper()
				claims := validClaims()
				claims.Issuer = "https://evil.example.com"
				return tj.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "claims validation failed",
		},
		{
			name: "wrong audience",
			token: func(t *testing.T) string {
				t.Helper()
				claims := validClaims()
				claims.Audience = jwt.Audience{"https://other.example.com"}
				return tj.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "claims validation failed",
		},
		{
			name: "not yet valid — nbf in the future",
			token: func(t *testing.T) string {
				t.Helper()
				claims := validClaims()
				claims.NotBefore = jwt.NewNumericDate(time.Now().Add(time.Hour))
				return tj.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "claims validation failed",
		},
		{
			name: "bad signature — signed with different key",
			token: func(t *testing.T) string {
				t.Helper()
				otherJWKS := newTestJWKS(t)
				return otherJWKS.signToken(t, validClaims(), nil)
			},
			wantErr:     true,
			errContains: "signature verification failed",
		},
		{
			name: "malformed token",
			token: func(_ *testing.T) string {
				return "not-a-jwt"
			},
			wantErr:     true,
			errContains: "not a valid JWT",
		},
		{
			name: "missing subject claim",
			token: func(t *testing.T) string {
				t.Helper()
				claims := validClaims()
				claims.Subject = ""
				return tj.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "missing required 'sub' claim",
		},
		{
			name: "missing exp claim",
			token: func(t *testing.T) string {
				t.Helper()
				claims := validClaims()
				claims.Expiry = nil
				return tj.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "missing required 'exp' claim",
		},
		{
			name: "token with may_act claim extracts MayAct",
			token: func(t *testing.T) string {
				t.Helper()
				extra := validExtraClaims()
				extra["may_act"] = map[string]any{"sub": "authorized-agent"}
				return tj.signToken(t, validClaims(), extra)
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				require.NotNil(t, vc.MayAct)
				assert.Equal(t, "authorized-agent", vc.MayAct.Sub)
			},
		},
		{
			name: "malformed may_act — not an object",
			token: func(t *testing.T) string {
				t.Helper()
				extra := validExtraClaims()
				extra["may_act"] = "not-an-object"
				return tj.signToken(t, validClaims(), extra)
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "malformed may_act — missing sub",
			token: func(t *testing.T) string {
				t.Helper()
				extra := validExtraClaims()
				extra["may_act"] = map[string]any{"foo": "bar"}
				return tj.signToken(t, validClaims(), extra)
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "malformed may_act — non-string sub",
			token: func(t *testing.T) string {
				t.Helper()
				extra := validExtraClaims()
				extra["may_act"] = map[string]any{"sub": 123}
				return tj.signToken(t, validClaims(), extra)
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "may_act with iss matching this server's own issuer accepted",
			token: func(t *testing.T) string {
				t.Helper()
				extra := validExtraClaims()
				extra["may_act"] = map[string]any{"sub": "authorized-agent", "iss": testIssuer}
				return tj.signToken(t, validClaims(), extra)
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				require.NotNil(t, vc.MayAct)
				assert.Equal(t, "authorized-agent", vc.MayAct.Sub)
				assert.Equal(t, testIssuer, vc.MayAct.Iss)
			},
		},
		{
			// RFC 8693 §4.4: sub is only meaningful together with iss. This
			// server only ever grants delegation to its own clients, so an
			// iss naming a different issuer is namespace confusion, not a
			// legitimate qualifier — reject rather than silently ignore it.
			name: "malformed may_act — iss not matching this server's own issuer",
			token: func(t *testing.T) string {
				t.Helper()
				extra := validExtraClaims()
				extra["may_act"] = map[string]any{"sub": "authorized-agent", "iss": "https://evil.example.com"}
				return tj.signToken(t, validClaims(), extra)
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "malformed may_act — empty iss",
			token: func(t *testing.T) string {
				t.Helper()
				extra := validExtraClaims()
				extra["may_act"] = map[string]any{"sub": "authorized-agent", "iss": ""}
				return tj.signToken(t, validClaims(), extra)
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "ID token masquerading as subject token rejected — at_hash present",
			token: func(t *testing.T) string {
				t.Helper()
				extra := validExtraClaims()
				extra["at_hash"] = "some-hash-value"
				return tj.signToken(t, validClaims(), extra)
			},
			wantErr:     true,
			errContains: "'at_hash'",
		},
		{
			name: "ID token masquerading as subject token rejected — c_hash present",
			token: func(t *testing.T) string {
				t.Helper()
				extra := validExtraClaims()
				extra["c_hash"] = "some-hash-value"
				return tj.signToken(t, validClaims(), extra)
			},
			wantErr:     true,
			errContains: "'c_hash'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			validator, err := NewSelfIssuedTokenValidator(tj.publicJWKS(), testIssuer, []string{testIssuer})
			require.NoError(t, err)
			rawToken := tt.token(t)

			result, err := validator.Validate(context.Background(), rawToken)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			if tt.check != nil {
				tt.check(t, result)
			}
		})
	}
}

func TestSelfIssuedTokenValidator_AudienceValidation(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)

	tests := []struct {
		name             string
		allowedAudiences []string
		tokenAudience    jwt.Audience
		wantErr          bool
	}{
		{
			name:             "empty allowedAudiences rejects even a matching-looking token",
			allowedAudiences: nil,
			tokenAudience:    jwt.Audience{testIssuer},
			wantErr:          true,
		},
		{
			name:             "token audience intersects allowedAudiences",
			allowedAudiences: []string{"https://other.example.com", testIssuer},
			tokenAudience:    jwt.Audience{testIssuer},
			wantErr:          false,
		},
		{
			name:             "token audience does not intersect allowedAudiences",
			allowedAudiences: []string{"https://other.example.com"},
			tokenAudience:    jwt.Audience{testIssuer},
			wantErr:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			validator, err := NewSelfIssuedTokenValidator(tj.publicJWKS(), testIssuer, tt.allowedAudiences)
			require.NoError(t, err)

			claims := validClaims()
			claims.Audience = tt.tokenAudience
			rawToken := tj.signToken(t, claims, nil)

			result, err := validator.Validate(context.Background(), rawToken)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "claims validation failed")
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
		})
	}
}

// newECDSAJWK generates a fresh ECDSA P-256 key with the given key ID. The
// returned JWK carries the private key (in its Key field), so it can be used
// directly for both signing (signWithJWK) and JWKS construction.
func newECDSAJWK(t *testing.T, kid string) jose.JSONWebKey {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return jose.JSONWebKey{
		Key:       privateKey,
		KeyID:     kid,
		Algorithm: string(jose.ES256),
		Use:       "sig",
	}
}

// publicJWKSOf builds a JWKS containing only the public portion of each key,
// mirroring how Factory constructs the validator from PublicJWKS().
func publicJWKSOf(keys ...jose.JSONWebKey) *jose.JSONWebKeySet {
	public := make([]jose.JSONWebKey, len(keys))
	for i, k := range keys {
		public[i] = k.Public()
	}
	return &jose.JSONWebKeySet{Keys: public}
}

// signWithJWK signs claims with the given signing key (no extra claims).
func signWithJWK(t *testing.T, signingKey jose.JSONWebKey, alg jose.SignatureAlgorithm, claims jwt.Claims) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: alg, Key: signingKey},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	require.NoError(t, err)
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

func TestSelfIssuedTokenValidator_MultiKeyJWKS(t *testing.T) {
	t.Parallel()

	t.Run("token verified with kid-matched key", func(t *testing.T) {
		t.Parallel()

		jwk1 := newECDSAJWK(t, "test-key-1")
		jwk2 := newECDSAJWK(t, "test-key-2")
		jwks := publicJWKSOf(jwk1, jwk2)

		validator, err := NewSelfIssuedTokenValidator(jwks, testIssuer, []string{testIssuer})
		require.NoError(t, err)

		rawToken := signWithJWK(t, jwk2, jose.ES256, validClaims())

		result, err := validator.Validate(context.Background(), rawToken)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "user-123", result.Subject)
	})

	t.Run("token verified by full iteration when kid absent", func(t *testing.T) {
		t.Parallel()

		jwk1 := newECDSAJWK(t, "test-key-1")
		jwk2 := newECDSAJWK(t, "test-key-2")
		jwks := publicJWKSOf(jwk1, jwk2)

		validator, err := NewSelfIssuedTokenValidator(jwks, testIssuer, []string{testIssuer})
		require.NoError(t, err)

		// Sign with jwk1 (whose public half is in the JWKS) but omit the kid
		// from the signer so the token header carries no kid — the validator
		// must then fall back to iterating all keys rather than kid-matching.
		signingKeyNoKID := jwk1
		signingKeyNoKID.KeyID = ""
		rawToken := signWithJWK(t, signingKeyNoKID, jose.ES256, validClaims())

		result, err := validator.Validate(context.Background(), rawToken)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "user-123", result.Subject)
	})

	t.Run("token signed with different key in JWKS fails", func(t *testing.T) {
		t.Parallel()

		jwk1 := newECDSAJWK(t, "test-key-1")
		jwks := publicJWKSOf(jwk1)

		validator, err := NewSelfIssuedTokenValidator(jwks, testIssuer, []string{testIssuer})
		require.NoError(t, err)

		// Key B is not in the JWKS.
		otherJWK := newECDSAJWK(t, "other-key")
		rawToken := signWithJWK(t, otherJWK, jose.ES256, validClaims())

		result, err := validator.Validate(context.Background(), rawToken)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "signature verification failed")
		assert.Nil(t, result)
	})

	t.Run("token claiming a kid it is not signed with fails", func(t *testing.T) {
		t.Parallel()

		jwk1 := newECDSAJWK(t, "test-key-1")
		jwk2 := newECDSAJWK(t, "test-key-2")
		jwks := publicJWKSOf(jwk1, jwk2)

		validator, err := NewSelfIssuedTokenValidator(jwks, testIssuer, []string{testIssuer})
		require.NoError(t, err)

		// Sign with jwk2's key material but claim jwk1's kid in the header.
		// A validator that fell back to the full keyset after a kid match
		// would wrongly accept this via jwk2; verifySignature must try only
		// the matched key (jwk1) and fail.
		spoofedKey := jwk2
		spoofedKey.KeyID = jwk1.KeyID
		rawToken := signWithJWK(t, spoofedKey, jose.ES256, validClaims())

		result, err := validator.Validate(context.Background(), rawToken)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "signature verification failed")
		assert.Nil(t, result)
	})

	t.Run("RSA-signed token accepted", func(t *testing.T) {
		t.Parallel()

		rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		rsaJWK := jose.JSONWebKey{
			Key:       rsaKey,
			KeyID:     "rsa-key-1",
			Algorithm: string(jose.RS256),
			Use:       "sig",
		}
		jwks := publicJWKSOf(rsaJWK)

		validator, err := NewSelfIssuedTokenValidator(jwks, testIssuer, []string{testIssuer})
		require.NoError(t, err)

		rawToken := signWithJWK(t, rsaJWK, jose.RS256, validClaims())

		result, err := validator.Validate(context.Background(), rawToken)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "user-123", result.Subject)
	})

	t.Run("key marked for a non-signing use is never tried", func(t *testing.T) {
		t.Parallel()

		// Same key material a signature could actually verify against, but
		// declared "enc" in the JWKS — RFC 7517 §4.2 reserves it for a
		// different purpose. Without the use filter, this key would still
		// verify the signature below and wrongly accept the token.
		jwk := newECDSAJWK(t, "enc-key")
		jwk.Use = "enc"
		jwks := publicJWKSOf(jwk)

		validator, err := NewSelfIssuedTokenValidator(jwks, testIssuer, []string{testIssuer})
		require.NoError(t, err)

		// No kid on the token, so verifySignature must fall back to trying
		// every key in the JWKS — and skip this one.
		signingKeyNoKID := jwk
		signingKeyNoKID.KeyID = ""
		rawToken := signWithJWK(t, signingKeyNoKID, jose.ES256, validClaims())

		result, err := validator.Validate(context.Background(), rawToken)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "signature verification failed")
		assert.Nil(t, result)
	})

	t.Run("key with a mismatched declared algorithm is never tried", func(t *testing.T) {
		t.Parallel()

		// The JWKS entry claims RS256, but the token header (and the actual
		// signing) uses ES256 — a misconfigured or spoofed JWKS entry.
		// Without the alg filter, go-jose would still attempt (and, for an
		// EC key, fail) this candidate; the point here is that it must be
		// skipped rather than tried at all.
		jwk := newECDSAJWK(t, "mismatched-alg-key")
		jwk.Algorithm = string(jose.RS256)
		jwks := publicJWKSOf(jwk)

		validator, err := NewSelfIssuedTokenValidator(jwks, testIssuer, []string{testIssuer})
		require.NoError(t, err)

		signingKeyNoKID := jwk
		signingKeyNoKID.KeyID = ""
		rawToken := signWithJWK(t, signingKeyNoKID, jose.ES256, validClaims())

		result, err := validator.Validate(context.Background(), rawToken)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "signature verification failed")
		assert.Nil(t, result)
	})
}
