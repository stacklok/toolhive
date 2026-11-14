// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestMultiIssuerTokenValidator_ValidateJWTBearerAssertion(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: time.Hour.String(),
			SubjectBindings: []JWTBearerSubjectBinding{{
				Subject:          "ext-user-456",
				AllowedResources: []string{"https://mcp.example.com"},
			}},
		},
	}})

	tests := []struct {
		name    string
		claims  func() jwt.Claims
		signer  *testJWKS
		wantErr string
	}{
		{
			name:   "valid assertion does not require delegation consent",
			claims: func() jwt.Claims { return jwtBearerExternalClaims() },
			signer: externalJWKS,
		},
		{
			name: "audience is accepted as long as it intersects the accepted set, even if multi-valued",
			claims: func() jwt.Claims {
				claims := jwtBearerExternalClaims()
				claims.Audience = jwt.Audience{testTokenEndpoint, "https://other.example.com"}
				return claims
			},
			signer: externalJWKS,
		},
		{
			name: "audience is rejected when it matches none of the accepted authorization server identities",
			claims: func() jwt.Claims {
				claims := jwtBearerExternalClaims()
				claims.Audience = jwt.Audience{"https://other.example.com"}
				return claims
			},
			signer:  externalJWKS,
			wantErr: "assertion audience must include one of the accepted authorization server identities",
		},
		{
			name: "subject is required",
			claims: func() jwt.Claims {
				claims := jwtBearerExternalClaims()
				claims.Subject = ""
				return claims
			},
			signer:  externalJWKS,
			wantErr: "missing required 'sub'",
		},
		{
			name: "expiry is required",
			claims: func() jwt.Claims {
				claims := jwtBearerExternalClaims()
				claims.Expiry = nil
				return claims
			},
			signer:  externalJWKS,
			wantErr: "missing required 'exp'",
		},
		{
			name: "expired assertion is rejected",
			claims: func() jwt.Claims {
				claims := jwtBearerExternalClaims()
				claims.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Hour))
				return claims
			},
			signer:  externalJWKS,
			wantErr: "claims validation failed",
		},
		{
			name: "issuer must be trusted",
			claims: func() jwt.Claims {
				claims := jwtBearerExternalClaims()
				claims.Issuer = "https://untrusted.example.com"
				return claims
			},
			signer:  externalJWKS,
			wantErr: "untrusted assertion issuer",
		},
		{
			name: "issued at is required",
			claims: func() jwt.Claims {
				claims := jwtBearerExternalClaims()
				claims.IssuedAt = nil
				return claims
			},
			signer:  externalJWKS,
			wantErr: "missing required 'iat'",
		},
		{
			name: "missing JWT ID is accepted",
			claims: func() jwt.Claims {
				claims := jwtBearerExternalClaims()
				claims.ID = ""
				return claims
			},
			signer: externalJWKS,
		},
		{
			name:    "invalid signature is rejected",
			claims:  func() jwt.Claims { return jwtBearerExternalClaims() },
			signer:  newTestJWKS(t),
			wantErr: "signature verification failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := tt.signer.signToken(t, tt.claims(), nil)
			claims, err := validator.ValidateJWTBearerAssertion(context.Background(), raw, []string{testTokenEndpoint})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, claims)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "ext-user-456", claims.Subject)
			assert.Equal(t, testExternalIssuer, claims.Issuer)
		})
	}
}

// TestMultiIssuerTokenValidator_ValidateJWTBearerAssertionAcceptsIDJAGType pins
// that the subject-token typ deny does not reach redemption: an ID-JAG from a
// trusted issuer still verifies as a JWT-bearer assertion.
func TestMultiIssuerTokenValidator_ValidateJWTBearerAssertionAcceptsIDJAGType(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: time.Hour.String(),
			SubjectBindings: []JWTBearerSubjectBinding{{
				Subject:          "ext-user-456",
				AllowedResources: []string{"https://mcp.example.com"},
			}},
		},
	}})

	raw := externalJWKS.signTokenWithType(t, oauthproto.JWTTypeIDJAG, jwtBearerExternalClaims(), nil)
	claims, err := validator.ValidateJWTBearerAssertion(context.Background(), raw, []string{testTokenEndpoint})
	require.NoError(t, err)
	assert.Equal(t, "ext-user-456", claims.Subject)
}

func TestMultiIssuerTokenValidator_ValidateJWTBearerAssertionRejectsIssuerWithoutGrant(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedDelegateClients: []string{anyDelegateClient},
	}})

	raw := externalJWKS.signToken(t, externalClaims(), nil)
	claims, err := validator.ValidateJWTBearerAssertion(context.Background(), raw, []string{testTokenEndpoint})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not enabled for the JWT-bearer grant")
	assert.Nil(t, claims)

}

// TestMultiIssuerTokenValidator_ValidateJWTBearerAssertion_AcceptedAudiences
// covers JWTBearerGrant.AcceptedAudiences: it widens the accepted "aud" set
// beyond the literal token endpoint, e.g. so an issuer's assertion can carry
// an alternate identity for this AS (a migrated issuer URL) instead of the
// endpoint the caller happens to be calling.
func TestMultiIssuerTokenValidator_ValidateJWTBearerAssertion_AcceptedAudiences(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	const alternateASIdentity = "https://auth.example.com/legacy-token-endpoint"
	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge:   time.Hour.String(),
			AcceptedAudiences: []string{alternateASIdentity},
			SubjectBindings: []JWTBearerSubjectBinding{
				{Subject: "ext-user-456", AllowedResources: []string{"https://mcp.example.com"}},
			},
		},
	}})

	tests := []struct {
		name    string
		aud     jwt.Audience
		wantErr string
	}{
		{
			name:    "the configured alternate identity is accepted",
			aud:     jwt.Audience{alternateASIdentity},
			wantErr: "",
		},
		{
			name:    "the literal token endpoint is no longer accepted once AcceptedAudiences is configured",
			aud:     jwt.Audience{testTokenEndpoint},
			wantErr: "assertion audience must include one of the accepted authorization server identities",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := jwtBearerExternalClaims()
			claims.Audience = tt.aud
			raw := externalJWKS.signToken(t, claims, nil)

			validated, err := validator.ValidateJWTBearerAssertion(context.Background(), raw, []string{testTokenEndpoint})
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "ext-user-456", validated.Subject)
		})
	}
}

func jwtBearerExternalClaims() jwt.Claims {
	now := time.Now()
	return jwt.Claims{
		Subject:  "ext-user-456",
		Issuer:   testExternalIssuer,
		Audience: jwt.Audience{testTokenEndpoint},
		Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt: jwt.NewNumericDate(now),
		ID:       "jwt-bearer-jti",
	}
}

func TestMultiIssuerTokenValidator_ValidateJWTBearerAssertionRejectsIDTokenClaims(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL: testExternalIssuer, ExpectedAudience: testExternalAudience, JWKSURL: jwksServer.URL + "/jwks",
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: time.Hour.String(),
			SubjectBindings: []JWTBearerSubjectBinding{{
				Subject:          "ext-user-456",
				AllowedResources: []string{"https://mcp.example.com"},
			}},
		},
	}})

	raw := externalJWKS.signToken(t, jwtBearerExternalClaims(), map[string]any{"at_hash": "id-token-marker"})
	_, err := validator.ValidateJWTBearerAssertion(context.Background(), raw, []string{testTokenEndpoint})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at_hash")
}

func TestValidateJWTBearerAssertionClaims_ClockSkewAndExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*jwt.Claims)
		wantErr string
	}{
		{name: "not before within leeway", mutate: func(c *jwt.Claims) { c.NotBefore = jwt.NewNumericDate(time.Now().Add(30 * time.Second)) }},
		{name: "issued at within leeway", mutate: func(c *jwt.Claims) { c.IssuedAt = jwt.NewNumericDate(time.Now().Add(30 * time.Second)) }},
		{name: "not before beyond leeway", mutate: func(c *jwt.Claims) { c.NotBefore = jwt.NewNumericDate(time.Now().Add(2 * time.Minute)) }, wantErr: "claims validation failed"},
		{name: "expired within leeway remains rejected", mutate: func(c *jwt.Claims) { c.Expiry = jwt.NewNumericDate(time.Now().Add(-30 * time.Second)) }, wantErr: "assertion has expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := jwtBearerExternalClaims()
			tt.mutate(&claims)
			err := validateJWTBearerAssertionClaims(claims, testExternalIssuer, []string{testTokenEndpoint})
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestMultiIssuerTokenValidator_ValidateJWTBearerAssertion_DefaultAudiences(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	newValidator := func(accepted []string) *MultiIssuerTokenValidator {
		return newMultiValidator(t, selfJWKS, []TrustedIssuer{{
			IssuerURL:              testExternalIssuer,
			ExpectedAudience:       testExternalAudience,
			JWKSURL:                jwksServer.URL + "/jwks",
			AllowedDelegateClients: []string{anyDelegateClient},
			JWTBearerGrant: &JWTBearerGrantPolicy{
				MaxAssertionAge:   time.Hour.String(),
				AcceptedAudiences: accepted,
				SubjectBindings: []JWTBearerSubjectBinding{
					{Subject: "ext-user-456", AllowedResources: []string{"https://mcp.example.com"}},
				},
			},
		}})
	}

	const (
		secondDefault = "https://auth.example.com"
		configured    = "https://configured.example.com"
	)
	const wantAudErr = "assertion audience must include one of the accepted authorization server identities"
	tests := []struct {
		name     string
		defaults []string
		accepted []string
		aud      jwt.Audience
		wantErr  string
	}{
		{name: "nil defaults are refused", defaults: nil, aud: jwt.Audience{testTokenEndpoint}, wantErr: "default audiences must not be empty"},
		{name: "empty defaults are refused", defaults: []string{}, aud: jwt.Audience{testTokenEndpoint}, wantErr: "default audiences must not be empty"},
		{name: "an empty element is refused", defaults: []string{testTokenEndpoint, ""}, aud: jwt.Audience{testTokenEndpoint}, wantErr: "default audiences must not be empty"},
		{name: "the second default is accepted", defaults: []string{testTokenEndpoint, secondDefault}, aud: jwt.Audience{secondDefault}},
		{
			name:     "an audience outside the defaults is rejected",
			defaults: []string{testTokenEndpoint, secondDefault},
			aud:      jwt.Audience{"https://other.example.com"},
			wantErr:  wantAudErr,
		},
		{
			name:     "a configured list replaces the second default",
			defaults: []string{testTokenEndpoint, secondDefault}, accepted: []string{configured},
			aud: jwt.Audience{secondDefault}, wantErr: wantAudErr,
		},
		{
			name:     "a configured list replaces the token endpoint default",
			defaults: []string{testTokenEndpoint, secondDefault}, accepted: []string{configured},
			aud: jwt.Audience{testTokenEndpoint}, wantErr: wantAudErr,
		},
		{
			name:     "a configured list accepts its own entry",
			defaults: []string{testTokenEndpoint, secondDefault}, accepted: []string{configured},
			aud: jwt.Audience{configured},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := jwtBearerExternalClaims()
			claims.Audience = tt.aud
			raw := externalJWKS.signToken(t, claims, nil)

			validated, err := newValidator(tt.accepted).ValidateJWTBearerAssertion(context.Background(), raw, tt.defaults)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "ext-user-456", validated.Subject)
		})
	}
}
