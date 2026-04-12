// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testExternalIssuer   = "https://keycloak.example.com/realms/test"
	testExternalAudience = "toolhive-authserver"
)

// newExternalTestJWKS creates a separate JWKS for simulating an external issuer.
// It reuses newTestJWKS but conceptually represents a different signing authority.
func newExternalTestJWKS(t *testing.T) *testJWKS {
	t.Helper()
	return newTestJWKS(t)
}

// startJWKSServer creates a test HTTP server that serves a JWKS endpoint.
// The returned server must be closed by the caller.
func startJWKSServer(t *testing.T, tj *testJWKS) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		// Serve only the public keys.
		publicJWKS := publicKeysFrom(t, tj)
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(publicJWKS)
		require.NoError(t, err)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// publicKeysFrom extracts the public portion of the test JWKS keys.
func publicKeysFrom(t *testing.T, tj *testJWKS) map[string]interface{} {
	t.Helper()
	keys := make([]map[string]interface{}, 0, len(tj.jwks.Keys))
	for _, key := range tj.jwks.Keys {
		pub := key.Public()
		raw, err := pub.MarshalJSON()
		require.NoError(t, err, "failed to marshal public key")
		var m map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &m), "failed to unmarshal public key")
		keys = append(keys, m)
	}
	return map[string]interface{}{"keys": keys}
}

// startDiscoveryServer creates a test HTTP server that serves both OIDC discovery
// and JWKS endpoints, simulating an external OIDC provider.
func startDiscoveryServer(t *testing.T, tj *testJWKS) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		// The jwks_uri must use the test server's own base URL, which we
		// don't know until the server starts. We use the Host header to
		// construct it.
		scheme := "http"
		jwksURI := scheme + "://" + r.Host + "/jwks"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   testExternalIssuer,
			"jwks_uri": jwksURI,
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		publicJWKS := publicKeysFrom(t, tj)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(publicJWKS)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newMultiValidator creates a MultiIssuerTokenValidator configured for testing.
// The external JWKS URL is pre-resolved (no discovery needed) unless jwksURL is empty.
func newMultiValidator(
	t *testing.T,
	selfJWKS *testJWKS,
	trustedIssuers []TrustedIssuer,
) *MultiIssuerTokenValidator {
	t.Helper()

	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.jwks, testIssuer)
	require.NoError(t, err)

	v := NewMultiIssuerTokenValidator(selfValidator, testIssuer, trustedIssuers)
	v.insecureSkipJWKSURLValidation = true // Allow HTTP test servers
	return v
}

// externalClaims returns standard JWT claims for a token issued by the external issuer.
func externalClaims() jwt.Claims {
	now := time.Now()
	return jwt.Claims{
		Subject:   "ext-user-456",
		Issuer:    testExternalIssuer,
		Audience:  jwt.Audience{testExternalAudience},
		Expiry:    jwt.NewNumericDate(now.Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		ID:        "jti-ext-789",
	}
}

func TestMultiIssuerTokenValidator_Validate(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newExternalTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)

	tests := []struct {
		name           string
		trustedIssuers []TrustedIssuer
		token          func(t *testing.T) string
		wantErr        bool
		errContains    string
		check          func(t *testing.T, vc *ValidatedClaims)
	}{
		{
			name: "self-issued token routes to self validator",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return selfJWKS.signToken(t, validClaims(), validExtraClaims())
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.Equal(t, "user-123", vc.Subject)
				assert.Equal(t, testIssuer, vc.Issuer)
				assert.Equal(t, []string{testIssuer}, vc.Audience)
				assert.Equal(t, "Test User", vc.Name)
				assert.Equal(t, "test@example.com", vc.Email)
			},
		},
		{
			name: "external token accepted",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]interface{}{
					"name":  "External User",
					"email": "ext@keycloak.example.com",
				})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.Equal(t, "ext-user-456", vc.Subject)
				assert.Equal(t, testExternalIssuer, vc.Issuer)
				assert.Equal(t, []string{testExternalAudience}, vc.Audience)
				assert.Equal(t, "jti-ext-789", vc.JWTID)
				assert.Equal(t, "External User", vc.Name)
				assert.Equal(t, "ext@keycloak.example.com", vc.Email)
				assert.False(t, vc.Expiry.IsZero())
				assert.False(t, vc.IssuedAt.IsZero())
			},
		},
		{
			name: "external token wrong audience",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
			}},
			token: func(t *testing.T) string {
				t.Helper()
				claims := externalClaims()
				claims.Audience = jwt.Audience{"wrong-audience"}
				return externalJWKS.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "claims validation failed",
		},
		{
			name: "unknown issuer rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
			}},
			token: func(t *testing.T) string {
				t.Helper()
				claims := externalClaims()
				claims.Issuer = "https://evil.example.com"
				return externalJWKS.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "untrusted issuer",
		},
		{
			name: "external token bad signature",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
			}},
			token: func(t *testing.T) string {
				t.Helper()
				// Sign with a different key than the one the JWKS server serves.
				wrongJWKS := newTestJWKS(t)
				return wrongJWKS.signToken(t, externalClaims(), nil)
			},
			wantErr:     true,
			errContains: "signature verification failed",
		},
		{
			name: "self-issued token signed by external key fails",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
			}},
			token: func(t *testing.T) string {
				t.Helper()
				// Token claims say iss=self, but signed by the external key.
				// Routes to self validator, which rejects the signature.
				return externalJWKS.signToken(t, validClaims(), nil)
			},
			wantErr:     true,
			errContains: "signature verification failed",
		},
		{
			name: "external token missing subject",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
			}},
			token: func(t *testing.T) string {
				t.Helper()
				claims := externalClaims()
				claims.Subject = ""
				return externalJWKS.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "missing required 'sub' claim",
		},
		{
			name: "external token expired",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
			}},
			token: func(t *testing.T) string {
				t.Helper()
				claims := externalClaims()
				claims.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Hour))
				claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
				return externalJWKS.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "claims validation failed",
		},
		{
			name:           "malformed token",
			trustedIssuers: nil,
			token: func(_ *testing.T) string {
				return "not-a-jwt"
			},
			wantErr:     true,
			errContains: "failed to determine token issuer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			validator := newMultiValidator(t, selfJWKS, tt.trustedIssuers)
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

func TestMultiIssuerTokenValidator_OIDCDiscovery(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newExternalTestJWKS(t)
	discoveryServer := startDiscoveryServer(t, externalJWKS)

	// Configure the trusted issuer WITHOUT a JWKS URL, forcing OIDC discovery.
	// The discovery server's URL is used as the issuer URL so that the
	// /.well-known/openid-configuration endpoint is reachable.
	trustedIssuers := []TrustedIssuer{{
		IssuerURL:        discoveryServer.URL,
		ExpectedAudience: testExternalAudience,
		// JWKSURL intentionally left empty to trigger discovery.
	}}

	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	// Sign a token with the external key, using the discovery server's URL as issuer.
	claims := jwt.Claims{
		Subject:   "discovered-user",
		Issuer:    discoveryServer.URL,
		Audience:  jwt.Audience{testExternalAudience},
		Expiry:    jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		ID:        "jti-disc-001",
	}
	rawToken := externalJWKS.signToken(t, claims, nil)

	result, err := validator.Validate(context.Background(), rawToken)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "discovered-user", result.Subject)
	assert.Equal(t, discoveryServer.URL, result.Issuer)
}

func TestMultiIssuerTokenValidator_JWKSCaching(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newExternalTestJWKS(t)

	// Track how many times the JWKS endpoint is hit.
	var fetchCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		fetchCount.Add(1)
		publicJWKS := publicKeysFrom(t, externalJWKS)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(publicJWKS)
	})
	jwksServer := httptest.NewServer(mux)
	t.Cleanup(jwksServer.Close)

	trustedIssuers := []TrustedIssuer{{
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          jwksServer.URL + "/jwks",
	}}

	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	// Validate two tokens — the JWKS should be fetched only once (cached).
	for i := range 2 {
		claims := externalClaims()
		claims.ID = fmt.Sprintf("jti-cache-%d", i)
		rawToken := externalJWKS.signToken(t, claims, nil)

		result, err := validator.Validate(context.Background(), rawToken)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "ext-user-456", result.Subject)
	}

	assert.Equal(t, int32(1), fetchCount.Load(), "JWKS should be fetched only once due to caching")
}
