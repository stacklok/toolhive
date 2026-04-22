// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

// signToken creates a signed JWT with the given claims using the test JWKS key.
func (tj *testJWKS) signToken(t *testing.T, claims jwt.Claims, extraClaims map[string]interface{}) string {
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
func validExtraClaims() map[string]interface{} {
	return map[string]interface{}{
		"name":      "Test User",
		"email":     "test@example.com",
		"client_id": "mcp-client-1",
		"tsid":      "session-xyz",
	}
}

func TestSubjectTokenValidator_NewValidation(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)

	t.Run("nil JWKS returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewSubjectTokenValidator(nil, testIssuer)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JWKS must not be nil")
	})

	t.Run("empty issuer returns error", func(t *testing.T) {
		t.Parallel()
		_, err := NewSubjectTokenValidator(tj.jwks, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "issuer must not be empty")
	})

	t.Run("valid params succeed", func(t *testing.T) {
		t.Parallel()
		v, err := NewSubjectTokenValidator(tj.jwks, testIssuer)
		require.NoError(t, err)
		assert.NotNil(t, v)
	})
}

func TestSubjectTokenValidator_Validate(t *testing.T) {
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
				assert.Equal(t, "mcp-client-1", vc.ClientID)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			validator, err := NewSubjectTokenValidator(tj.jwks, testIssuer)
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
