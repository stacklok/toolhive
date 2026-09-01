// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/jwks"
	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	testExternalIssuer   = "https://keycloak.example.com/realms/test"
	testExternalAudience = "toolhive-authserver"
)

// startJWKSServer creates a test HTTP server that serves a JWKS endpoint.
// The returned server must be closed by the caller.
func startJWKSServer(t *testing.T, tj *testJWKS) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		// Serve only the public keys.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tj.publicJWKS())
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// writeTLSCABundle writes srv's self-signed certificate as a PEM CA bundle.
func writeTLSCABundle(t *testing.T, srv *httptest.Server) string {
	t.Helper()

	caPath := filepath.Join(t.TempDir(), "ca.crt")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(caPath, caPEM, 0600))
	return caPath
}

// startTLSJWKSServer creates a TLS test server that serves a JWKS endpoint and
// returns the path to a CA bundle that trusts its certificate.
func startTLSJWKSServer(t *testing.T, tj *testJWKS) (*httptest.Server, string) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tj.publicJWKS())
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv, writeTLSCABundle(t, srv)
}

// newMultiValidator creates a MultiIssuerTokenValidator configured for testing.
// The external JWKS URL is pre-resolved (no discovery needed) unless jwksURL is empty.
func newMultiValidator(
	t *testing.T,
	selfJWKS *testJWKS,
	trustedIssuers []TrustedIssuer,
) *MultiIssuerTokenValidator {
	t.Helper()

	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	// Copy rather than mutate the caller's slice: give every test issuer
	// permissive flags so its httptest discovery/JWKS server, which runs on
	// loopback over plain HTTP, is reachable.
	issuers := make([]TrustedIssuer, len(trustedIssuers))
	for i, ti := range trustedIssuers {
		ti.InsecureAllowHTTP = true
		ti.AllowPrivateIPs = true
		issuers[i] = ti
	}

	v, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, issuers, nil)
	require.NoError(t, err)
	return v
}

// discoveryFetcher builds a jwks.Fetcher around the given client, for tests
// exercising discoverJWKSURL directly: discovery reads the fetcher's client
// (issuerConfig.jwks.HTTPClient()), so there is no separate per-issuer client
// field to set anymore.
func discoveryFetcher(t *testing.T, client *http.Client) *jwks.Fetcher {
	t.Helper()

	f, err := jwks.NewFetcher(context.Background(), jwks.WithHTTPClient(client))
	require.NoError(t, err)
	return f
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

func TestMultiIssuerTokenValidator_ExternalIssuerCABundle(t *testing.T) {
	t.Parallel()

	t.Run("trusts private TLS JWKS with configured bundle", func(t *testing.T) {
		t.Parallel()

		selfJWKS := newTestJWKS(t)
		externalJWKS := newTestJWKS(t)
		srv, caPath := startTLSJWKSServer(t, externalJWKS)
		validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
			IssuerURL:              testExternalIssuer,
			ExpectedAudience:       testExternalAudience,
			JWKSURL:                srv.URL + "/jwks",
			CAFilePath:             caPath,
			AllowedActors:          []string{"ext-agent"},
			AllowedDelegateClients: []string{anyDelegateClient},
		}})

		result, err := validator.Validate(context.Background(), externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": "ext-agent"}))
		require.NoError(t, err)
		assert.Equal(t, "ext-user-456", result.Subject)
	})

	t.Run("rejects private TLS JWKS without configured bundle", func(t *testing.T) {
		t.Parallel()

		selfJWKS := newTestJWKS(t)
		externalJWKS := newTestJWKS(t)
		srv, _ := startTLSJWKSServer(t, externalJWKS)
		validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
			IssuerURL:              testExternalIssuer,
			ExpectedAudience:       testExternalAudience,
			JWKSURL:                srv.URL + "/jwks",
			AllowedActors:          []string{"ext-agent"},
			AllowedDelegateClients: []string{anyDelegateClient},
		}})

		result, err := validator.Validate(context.Background(), externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": "ext-agent"}))
		require.Error(t, err)
		assert.Nil(t, result)
		// jwx's httprc layer does not propagate the x509 cause: the registration
		// fails as "resource registered but not ready" once the fetch times out.
		// Assert on the stage that failed instead, which together with the
		// success case above pins the bundle as the load-bearing difference.
		assert.Contains(t, err.Error(), "failed to fetch JWKS")
		// This subtest waits out httpTimeout (10s) because the fetch context is
		// detached from the caller's, so a shorter context here cannot bound it.
	})

	t.Run("unreadable bundle fails construction", func(t *testing.T) {
		t.Parallel()

		selfJWKS := newTestJWKS(t)
		selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
		require.NoError(t, err)

		_, err = NewMultiIssuerTokenValidator(selfValidator, testIssuer, []TrustedIssuer{{
			IssuerURL:              testExternalIssuer,
			ExpectedAudience:       testExternalAudience,
			JWKSURL:                "https://issuer.example.com/jwks",
			CAFilePath:             filepath.Join(t.TempDir(), "missing-ca.crt"),
			AllowedDelegateClients: []string{anyDelegateClient},
		}}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), testExternalIssuer)
		assert.Contains(t, err.Error(), "failed to read CA certificate bundle")
	})
}

func TestMultiIssuerTokenValidator_DiscoverJWKSURLWithCABundle(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	srv := httptest.NewUnstartedServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   srv.URL,
			"jwks_uri": srv.URL + "/jwks",
		})
	})
	srv.StartTLS()
	t.Cleanup(srv.Close)

	issuerConfig, err := newExternalIssuerConfig(TrustedIssuer{
		IssuerURL:              srv.URL,
		CAFilePath:             writeTLSCABundle(t, srv),
		AllowPrivateIPs:        true,
		AllowedDelegateClients: []string{anyDelegateClient},
	})
	require.NoError(t, err)

	jwksURL, err := (&MultiIssuerTokenValidator{}).discoverJWKSURL(context.Background(), issuerConfig)
	require.NoError(t, err)
	assert.Equal(t, srv.URL+"/jwks", jwksURL)
}

func TestMultiIssuerTokenValidator_PrivateTLSKeyRotationRefreshesImmediately(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	keyV1 := newECDSAJWK(t, "v1")
	keyV2 := newECDSAJWK(t, "v2")

	var mu sync.Mutex
	currentKey := keyV1
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		key := currentKey
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(publicJWKSOf(key))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                srv.URL + "/jwks",
		CAFilePath:             writeTLSCABundle(t, srv),
		AllowedDelegateClients: []string{"some-toolhive-client"},
		AllowMayAct:            true,
	}})

	_, err := validator.Validate(context.Background(), signExternalToken(t, keyV1, externalClaims()))
	require.NoError(t, err)

	mu.Lock()
	currentKey = keyV2
	mu.Unlock()

	claims := externalClaims()
	claims.ID = "jti-post-private-tls-rotation"
	result, err := validator.Validate(context.Background(), signExternalToken(t, keyV2, claims))
	require.NoError(t, err)
	assert.Equal(t, "ext-user-456", result.Subject)
}

// TestCompileActorMatcher_AcceptsAnyWellTypedExpression confirms
// compileActorMatcher does not check an expression's output type: any
// expression that parses and type-checks against the "claims" variable
// compiles successfully, even one that can never return bool. The only
// place a non-bool result is caught is at evaluation, via EvaluateBool's
// runtime type assertion — see
// TestCompileActorMatcher_NonBoolResultDeniesAtEvaluation.
func TestCompileActorMatcher_AcceptsAnyWellTypedExpression(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{`"engineering"`, "1", "true", "claims.foo"} {
		t.Run(expression, func(t *testing.T) {
			t.Parallel()

			expr, err := compileActorMatcher(expression)
			require.NoError(t, err)
			assert.NotNil(t, expr)
		})
	}
}

// TestCompileActorMatcher_NonBoolResultDeniesAtEvaluation confirms a
// syntactically- and type-valid ActorMatcher that can never return bool
// (here, "claims.foo" where foo is a string claim) is accepted at compile
// time — see TestCompileActorMatcher_AcceptsAnyWellTypedExpression — but
// every evaluation against it fails, via EvaluateBool's runtime type
// assertion. A regression that treated this evaluation error as "no
// match" rather than surfacing it would be indistinguishable from this test
// passing, which is why it asserts on the error directly rather than only
// on the boolean result.
func TestCompileActorMatcher_NonBoolResultDeniesAtEvaluation(t *testing.T) {
	t.Parallel()

	expr, err := compileActorMatcher("claims.foo")
	require.NoError(t, err)

	matched, err := expr.EvaluateBool(map[string]any{"claims": map[string]any{"foo": "a-string-value"}})
	require.Error(t, err)
	assert.False(t, matched)
}

func TestMultiIssuerTokenValidator_Validate(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
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
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{anyDelegateClient},
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
				assert.Empty(t, vc.ExternalIssuer, "self-issued path must never set ExternalIssuer")
			},
		},
		{
			name: "external token allowed actor bypasses a non-matching actor matcher",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				ActorMatcher:           `false`,
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]interface{}{
					"name":  "External User",
					"email": "ext@keycloak.example.com",
					"azp":   "ext-agent",
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
				assert.Equal(t, "ext-agent", vc.ExternalActor, "actor claim matched via default azp")
				assert.True(t, vc.ExternalActorAuthorized)
				assert.Equal(t, testExternalIssuer, vc.ExternalIssuer)
			},
		},
		{
			// Neither of the other two ActorMatcher: "false" cases in this
			// table actually exercises rejection: the one above has a
			// matching AllowedActors entry, and the may_act one below has
			// may_act. A regression that treated an ActorMatcher evaluation
			// of false as authorized (instead of returning a matcher-specific
			// authorization error when no allowlist exists) would slip past both.
			// This case has no AllowedActors entry and no may_act claim, so a
			// false matcher result is the only signal in play and must reject.
			name: "external token actor matcher false with no other consent signal rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				ActorMatcher:           `false`,
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": "ext-agent"})
			},
			wantErr:     true,
			errContains: "was not authorized by actor matcher",
		},
		{
			name: "external token actor matcher receives complete standard and nested custom claims",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"different-external-actor"},
				ActorMatcher: `claims.iss == "https://keycloak.example.com/realms/test" && claims.sub == "ext-user-456" && ` +
					`claims.azp == "ext-agent" && claims.client_metadata.trust_tier == "high"`,
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":             "ext-agent",
					"client_metadata": map[string]any{"trust_tier": "high"},
				})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.True(t, vc.ExternalActorAuthorized)
				assert.Empty(t, vc.ExternalActor, "a matcher need not resolve an external actor claim")
			},
		},
		{
			name: "external token surfaces configured AllowedDelegateClients",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{"toolhive-agent-a", "toolhive-agent-b"},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]interface{}{"azp": "ext-agent"})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.Equal(t, "ext-agent", vc.ExternalActor)
				assert.Equal(t, []string{"toolhive-agent-a", "toolhive-agent-b"}, vc.AllowedDelegateClients,
					"the validator surfaces the issuer's configured allowlist for the handler to enforce")
			},
		},
		{
			// #5989 hardening: AllowedDelegateClients is required (validateTrustedIssuer
			// rejects an empty one), so the permissive case is now the wildcard,
			// declared explicitly, rather than an absent field.
			name: "external token surfaces the wildcard AllowedDelegateClients when the issuer opts into it",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]interface{}{"azp": "ext-agent"})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.Equal(t, []string{anyDelegateClient}, vc.AllowedDelegateClients)
			},
		},
		{
			// #5989 fix: an issuer emitting may_act with no way to populate
			// AllowedActors (e.g. Entra, Okta) previously had no per-client
			// containment at all. AllowedDelegateClients must now surface on
			// the may_act path too, for checkDelegationConsent to enforce.
			name: "external token with may_act still surfaces configured AllowedDelegateClients",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				ActorMatcher:           `false`,
				AllowedDelegateClients: []string{"toolhive-agent-a"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(),
					map[string]any{"may_act": map[string]any{"sub": "some-toolhive-client", "iss": testIssuer}})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				require.NotNil(t, vc.MayAct)
				assert.Equal(t, []string{"toolhive-agent-a"}, vc.AllowedDelegateClients,
					"may_act bypasses the AllowedActors allowlist, but not per-client containment")
			},
		},
		{
			name: "external token custom actor claim appid accepted",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				ActorClaim:             "appid",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"appid": "ext-agent"})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.Equal(t, "ext-agent", vc.ExternalActor)
			},
		},
		{
			name: "external token custom actor claim cid accepted",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				ActorClaim:             "cid",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"cid": "ext-agent"})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.Equal(t, "ext-agent", vc.ExternalActor)
			},
		},
		{
			name: "external token actor claim client_id resolves from ClientID field",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				ActorClaim:             "client_id",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				// "client_id" is routed to ValidatedClaims.ClientID by assignClaim, so
				// it never lands in Extra — resolveAllowedActor must fall back to
				// reading the structured field instead.
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"client_id": "ext-agent"})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.Equal(t, "ext-agent", vc.ClientID)
				assert.Equal(t, "ext-agent", vc.ExternalActor)
			},
		},
		{
			name: "external token actor not in allowed actors rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": "some-other-client"})
			},
			wantErr:     true,
			errContains: "not in the allowed actors list",
		},
		{
			name: "external token missing actor claim rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), nil)
			},
			wantErr:     true,
			errContains: "required for delegation consent",
		},
		{
			name: "external token actor claim is a number rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": 123})
			},
			wantErr:     true,
			errContains: "required for delegation consent",
		},
		{
			name: "external token actor claim is a bool rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": true})
			},
			wantErr:     true,
			errContains: "required for delegation consent",
		},
		{
			name: "external token actor claim is an array rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": []string{"ext-agent"}})
			},
			wantErr:     true,
			errContains: "required for delegation consent",
		},
		{
			name: "external token actor claim is a nested object rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(),
					map[string]any{"azp": map[string]any{"id": "ext-agent"}})
			},
			wantErr:     true,
			errContains: "required for delegation consent",
		},
		{
			name: "external token actor claim empty string rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": ""})
			},
			wantErr:     true,
			errContains: "required for delegation consent",
		},
		{
			name: "external token no may_act and empty allowed actors rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				// AllowedActors intentionally empty.
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": "ext-agent"})
			},
			wantErr:     true,
			errContains: "not in the allowed actors list",
		},
		{
			name: "external token with may_act and empty allowed actors accepted",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				// AllowedActors intentionally empty: permitted may_act is authoritative
				// and skips the allowlist entirely.
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(),
					map[string]any{"may_act": map[string]any{"sub": "some-toolhive-client", "iss": testIssuer}})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				require.NotNil(t, vc.MayAct)
				assert.Equal(t, "some-toolhive-client", vc.MayAct.Sub)
				assert.Empty(t, vc.ExternalActor, "allowlist is skipped whenever may_act is present")
				assert.Equal(t, testExternalIssuer, vc.ExternalIssuer,
					"provenance must still be recorded on the may_act path, which bypasses the allowlist")
			},
		},
		{
			// #5989 fix: the external path cannot leave may_act.iss optional
			// the way the self-issued path does (see
			// TestSelfIssuedTokenValidator_Validate's "token with may_act
			// claim extracts MayAct" for the self-issued equivalent, which
			// omits iss and is accepted). Without this, an external issuer
			// could authorize ANY ToolHive client via a bare may_act.sub,
			// bypassing AllowedActors/AllowedDelegateClients entirely.
			name: "external token may_act missing iss rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(),
					map[string]any{"may_act": map[string]any{"sub": "some-toolhive-client"}})
			},
			wantErr:     true,
			errContains: "missing required 'iss'",
		},
		{
			name: "external token may_act wins even when azp is not allowlisted",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"someone-else"},
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "not-in-the-allowlist",
					"may_act": map[string]any{"sub": "some-toolhive-client", "iss": testIssuer},
				})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				require.NotNil(t, vc.MayAct)
				assert.Empty(t, vc.ExternalActor)
				assert.Equal(t, testExternalIssuer, vc.ExternalIssuer)
			},
		},
		{
			name: "external token malformed may_act — JSON string, not object — rejected outright",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				// azp is allowlisted, so a regression that fell through to the
				// allowlist instead of rejecting would wrongly accept this.
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "ext-agent",
					"may_act": "agent-a",
				})
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "external token malformed may_act — non-string sub — rejected outright",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "ext-agent",
					"may_act": map[string]any{"sub": 123},
				})
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "external token malformed may_act — empty sub — rejected outright",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "ext-agent",
					"may_act": map[string]any{"sub": ""},
				})
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "external token malformed may_act — array sub — rejected outright",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "ext-agent",
					"may_act": map[string]any{"sub": []string{"agent-a"}},
				})
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "external token malformed may_act — object with no sub key — rejected outright",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "ext-agent",
					"may_act": map[string]any{"subject": "agent-a"},
				})
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			// Proves validateMayActShape is passed v.selfIssuer, not
			// issuerConfig.IssuerURL: may_act.sub is always compared against
			// a ToolHive client ID by checkDelegationConsent, regardless of
			// which external issuer validated the surrounding token, so its
			// optional iss must be checked against THIS server's own
			// issuer.
			name: "external token may_act.iss matching this server's own issuer accepted",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(),
					map[string]any{"may_act": map[string]any{"sub": "some-toolhive-client", "iss": testIssuer}})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				require.NotNil(t, vc.MayAct)
				assert.Equal(t, testIssuer, vc.MayAct.Iss)
			},
		},
		{
			name: "external token may_act without issuer opt-in rejected before allowlist fallback",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{"some-toolhive-client"},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "ext-agent",
					"may_act": map[string]any{"sub": "some-toolhive-client", "iss": testIssuer},
				})
			},
			wantErr:     true,
			errContains: "allow_may_act is disabled",
		},
		{
			// The external issuer's own URL is never a valid may_act.iss
			// value: a regression that compared against issuerConfig.IssuerURL
			// instead of v.selfIssuer would wrongly accept this.
			name: "external token may_act.iss matching the external issuer's own URL rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(),
					map[string]any{"may_act": map[string]any{"sub": "some-toolhive-client", "iss": testExternalIssuer}})
			},
			wantErr:     true,
			errContains: "malformed 'may_act' claim",
		},
		{
			name: "external token carrying c_hash (an ID token) rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":    "ext-agent",
					"c_hash": "some-hash-value",
				})
			},
			wantErr:     true,
			errContains: "'c_hash'",
		},
		{
			name: "external token wrong audience",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{anyDelegateClient},
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
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{anyDelegateClient},
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
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{anyDelegateClient},
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
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{anyDelegateClient},
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
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{anyDelegateClient},
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
			name: "external token missing exp claim",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				claims := externalClaims()
				claims.Expiry = nil
				return externalJWKS.signToken(t, claims, map[string]any{"azp": "ext-agent"})
			},
			wantErr:     true,
			errContains: "missing required 'exp' claim",
		},
		{
			// Distinct from "malformed token" below: that hits the JWT parse
			// failure inside peekIssuer, this hits its separate empty-issuer
			// check on an otherwise well-formed token. Both wrap to the same
			// outer "failed to determine token issuer" message, so assert on
			// the distinct inner text instead.
			name: "token missing iss claim",
			token: func(t *testing.T) string {
				t.Helper()
				claims := externalClaims()
				claims.Issuer = ""
				return externalJWKS.signToken(t, claims, nil)
			},
			wantErr:     true,
			errContains: "missing 'iss' claim",
		},
		{
			// Proves the allowlist is skipped (not just "would have failed
			// anyway") whenever may_act is present: both azp and
			// AllowedActors would satisfy resolveAllowedActor if it ran, so
			// a regression that calls it unconditionally and only gates the
			// error on MayAct==nil would still populate ExternalActor here.
			name: "external token may_act present alongside an allowlisted azp still yields empty ExternalActor",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{"some-toolhive-client"},
				AllowMayAct:            true,
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "ext-agent",
					"may_act": map[string]any{"sub": "some-toolhive-client", "iss": testIssuer},
				})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				require.NotNil(t, vc.MayAct)
				assert.Empty(t, vc.ExternalActor, "allowlist must be skipped entirely when may_act is present")
			},
		},
		{
			name: "external token expired",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				AllowedDelegateClients: []string{anyDelegateClient},
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

// TestMultiIssuerTokenValidator_TrailingSlashIssuerExactMatch covers a
// trailing-slash issuer_url (e.g. Microsoft Entra ID v1's
// "https://sts.windows.net/{tenant}/") being accepted as an exact "iss"
// match. JWKSURL is set explicitly rather than left for discovery to resolve:
// validateTrustedIssuer rejects {AllowPrivateIPs: true, JWKSURL: ""} (the
// combination newMultiValidator forces on every test issuer to reach its
// loopback server), so an empty JWKSURL would fail construction here.
// discoverJWKSURL's own trailing-slash trim is covered directly by
// TestMultiIssuerTokenValidator_DiscoverJWKSURL instead; this test is only
// about the trailing-slash IssuerURL matching the token's "iss" claim exactly
// (per OIDC Core §3.1.3.3), which happens regardless of how JWKSURL was
// resolved.
func TestMultiIssuerTokenValidator_TrailingSlashIssuerExactMatch(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)

	trailingSlashIssuer := "https://tenant.example.com/realm/"
	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              trailingSlashIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedActors:          []string{"ext-agent"},
		AllowedDelegateClients: []string{anyDelegateClient},
	}}

	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	claims := jwt.Claims{
		Subject:   "trailing-slash-user",
		Issuer:    trailingSlashIssuer,
		Audience:  jwt.Audience{testExternalAudience},
		Expiry:    jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		NotBefore: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
		ID:        "jti-trailing-slash-001",
	}
	rawToken := externalJWKS.signToken(t, claims, map[string]any{"azp": "ext-agent"})

	result, err := validator.Validate(context.Background(), rawToken)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "trailing-slash-user", result.Subject)
	assert.Equal(t, trailingSlashIssuer, result.Issuer)
}

func TestMultiIssuerTokenValidator_JWKSCaching(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)

	// Track how many times the JWKS endpoint is hit.
	var fetchCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(externalJWKS.publicJWKS())
	})
	jwksServer := httptest.NewServer(mux)
	t.Cleanup(jwksServer.Close)

	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedActors:          []string{"ext-agent"},
		AllowedDelegateClients: []string{anyDelegateClient},
	}}

	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	// Validate two tokens — the JWKS should be fetched only once (cached).
	for i := range 2 {
		claims := externalClaims()
		claims.ID = fmt.Sprintf("jti-cache-%d", i)
		rawToken := externalJWKS.signToken(t, claims, map[string]any{"azp": "ext-agent"})

		result, err := validator.Validate(context.Background(), rawToken)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "ext-user-456", result.Subject)
	}

	assert.Equal(t, int32(1), fetchCount.Load(), "JWKS should be fetched only once due to caching")
}

func TestNewMultiIssuerTokenValidator_Validation(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	tests := []struct {
		name             string
		selfValidator    *SelfIssuedTokenValidator
		selfIssuer       string
		trustedIssuers   []TrustedIssuer
		allowedAudiences []string
		errContains      string
	}{
		{
			name:        "nil selfValidator rejected",
			selfIssuer:  testIssuer,
			errContains: "selfValidator must not be nil",
		},
		{
			name:          "empty selfIssuer rejected",
			selfValidator: selfValidator,
			selfIssuer:    "",
			errContains:   "selfIssuer must not be empty",
		},
		{
			name:          "empty IssuerURL rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              "",
				ExpectedAudience:       testExternalAudience,
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			errContains: "issuer_url is required",
		},
		{
			name:          "empty ExpectedAudience rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       "",
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			errContains: "expected_audience is required",
		},
		{
			name:          "IssuerURL equal to selfIssuer rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testIssuer,
				ExpectedAudience:       testExternalAudience,
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			errContains: "must not equal the authorization server's own issuer",
		},
		{
			name:          "duplicate IssuerURL rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{
				{IssuerURL: testExternalIssuer, ExpectedAudience: testExternalAudience, AllowedDelegateClients: []string{anyDelegateClient}},
				{IssuerURL: testExternalIssuer, ExpectedAudience: testExternalAudience, AllowedDelegateClients: []string{anyDelegateClient}},
			},
			errContains: "configured more than once",
		},
		{
			name:          `ActorClaim "sub" rejected — assignClaim never leaves it in Extra`,
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				ActorClaim:             "sub",
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			errContains: "actor_claim",
		},
		{
			name:          `ActorClaim "scope" rejected — rerouted to a structured field`,
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				ActorClaim:             "scope",
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			errContains: "actor_claim",
		},
		{
			name:          `ActorClaim "may_act" rejected — rerouted to a structured field`,
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				ActorClaim:             "may_act",
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			errContains: "actor_claim",
		},
		{
			name:          "malformed ActorMatcher rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				ActorMatcher:           "claims.",
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			errContains: "actor_matcher",
		},
		{
			name:          "AllowedDelegateClients with an empty entry rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				AllowedDelegateClients: []string{"toolhive-agent-a", ""},
			}},
			errContains: "allowed_delegate_clients must not contain an empty client ID",
		},
		{
			// #5989 hardening: permissiveness must be declared with the
			// wildcard, not obtained by omission — pins the fail-closed
			// default this PR introduces.
			name:          "AllowedDelegateClients absent rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				// AllowedDelegateClients intentionally unset.
			}},
			errContains: "allowed_delegate_clients is required",
		},
		{
			name:          "AllowedDelegateClients empty (non-nil) slice rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				AllowedDelegateClients: []string{},
			}},
			errContains: "allowed_delegate_clients is required",
		},
		{
			// The wildcard authorizes every ToolHive client explicitly; mixing
			// it with specific IDs would either be redundant or, worse, give
			// the false impression that only the listed IDs are bound.
			// Rejecting outright avoids silently ignoring the specific IDs.
			name:          "AllowedDelegateClients wildcard mixed with a specific client ID rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				AllowedDelegateClients: []string{anyDelegateClient, "toolhive-agent-a"},
			}},
			errContains: "must not combine the wildcard",
		},
		{
			name:          "AllowMayAct with wildcard AllowedDelegateClients rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				AllowedDelegateClients: []string{anyDelegateClient},
				AllowMayAct:            true,
			}},
			errContains: "allow_may_act",
		},
		{
			// AllowPrivateIPs without a hand-configured jwks_url would let
			// OIDC discovery choose the private target the dial is allowed to
			// reach; validateTrustedIssuer must reject it so a caller that
			// skips Config.Validate (factory, tests) cannot bypass the
			// config-time check in pkg/authserver/config.go.
			name:          "AllowPrivateIPs without JWKSURL rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				AllowPrivateIPs:        true,
				AllowedDelegateClients: []string{anyDelegateClient},
			}},
			errContains: "allow_private_ips requires jwks_url",
		},
		{
			// A jwtBearerGrant issuer that also sets an RFC 8693-delegation-only
			// field (actorClaim here) must still supply expected_audience: the
			// delegation logic validates the subject token's audience against
			// it, and an empty ExpectedAudience can never match (#6391 review).
			name:          "jwtBearerGrant with ActorClaim but no ExpectedAudience rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ActorClaim:             "client_id",
				AllowedDelegateClients: []string{anyDelegateClient},
				JWTBearerGrant: &JWTBearerGrantPolicy{
					MaxAssertionAge: "1h",
					SubjectBindings: []JWTBearerSubjectBinding{
						{Subject: "agent", AllowedResources: []string{"https://api.example.com/resource"}},
					},
				},
			}},
			errContains: "expected_audience is required",
		},
		{
			// AllowedActors is also an RFC 8693-delegation-only signal, and must
			// be caught by the same check even though ActorClaim defaults to
			// "azp" and is never itself set here (#6391 review follow-up).
			name:          "jwtBearerGrant with AllowedActors but no ExpectedAudience rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				AllowedActors:          []string{"ext-agent"},
				AllowedDelegateClients: []string{anyDelegateClient},
				JWTBearerGrant: &JWTBearerGrantPolicy{
					MaxAssertionAge: "1h",
					SubjectBindings: []JWTBearerSubjectBinding{
						{Subject: "agent", AllowedResources: []string{"https://api.example.com/resource"}},
					},
				},
			}},
			errContains: "expected_audience is required",
		},
		{
			// AcceptedAudiences identifies this authorization server, not a
			// resource; an entry shared with a resource audience would let
			// an upstream resource-scoped access token be replayed as a
			// JWT-bearer assertion (see validateJWTBearerAcceptedAudiences).
			name:          "accepted_audiences overlapping a resource audience rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL: testExternalIssuer,
				JWTBearerGrant: &JWTBearerGrantPolicy{
					MaxAssertionAge: "1h",
					SubjectBindings: []JWTBearerSubjectBinding{
						{Subject: "agent", AllowedResources: []string{"https://api.example.com/resource"}},
					},
					AcceptedAudiences: []string{"https://api.example.com/resource"},
				},
			}},
			allowedAudiences: []string{"https://api.example.com/resource"},
			errContains:      "must not also be a configured resource audience",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v, err := NewMultiIssuerTokenValidator(tt.selfValidator, tt.selfIssuer, tt.trustedIssuers, tt.allowedAudiences)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
			assert.Nil(t, v)
		})
	}
}

func TestValidateJWTBearerAcceptedAudiences_RejectsResourceAudienceOverlap(t *testing.T) {
	t.Parallel()

	err := validateJWTBearerAcceptedAudiences(
		TrustedIssuer{IssuerURL: testExternalIssuer},
		[]string{"https://api.example.com/resource"},
		[]string{"https://api.example.com/resource"},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not also be a configured resource audience")
}

func TestNewMultiIssuerTokenValidator_EmptyAllowedActorsAccepted(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	// Empty AllowedActors must be accepted by the constructor: a may_act-only
	// issuer (no allowlisted actors at all) is a legitimate configuration.
	v, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		AllowedDelegateClients: []string{anyDelegateClient},
	}}, nil)
	require.NoError(t, err)
	assert.NotNil(t, v)
}

// TestNewMultiIssuerTokenValidator_GrantOnlyIssuerAccepted pins the case this
// PR exists for: a jwtBearerGrant-only issuer (none of ExpectedAudience,
// ActorClaim, ActorMatcher, AllowMayAct, or AllowedActors set) needs neither
// ExpectedAudience nor AllowedDelegateClients, since RFC 8693 delegation is
// not in play.
func TestNewMultiIssuerTokenValidator_GrantOnlyIssuerAccepted(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	v, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, []TrustedIssuer{{
		IssuerURL: testExternalIssuer,
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: "1h",
			SubjectBindings: []JWTBearerSubjectBinding{
				{Subject: "agent", AllowedResources: []string{"https://api.example.com/resource"}},
			},
		},
	}}, nil)
	require.NoError(t, err)
	assert.NotNil(t, v)
}

// syncBuffer is a concurrency-safe io.Writer over a bytes.Buffer, used by
// TestNewMultiIssuerTokenValidator_AudienceShapeWarning to capture
// slog.Default() output. slog.SetDefault is process-global, so a plain
// bytes.Buffer would race against any other goroutine that logs while a
// capturing handler is installed. Mirrors the identically-named helper in
// pkg/authserver/runner/embeddedauthserver_test.go; kept as a separate copy
// since it is an unexported test type not worth exporting across packages.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestNewMultiIssuerTokenValidator_AudienceShapeWarning pins
// looksLikeResourceIdentifier's startup warning: a TrustedIssuer whose
// ExpectedAudience has no URI scheme (e.g. a bare client-ID-shaped value)
// logs a slog.Warn naming the issuer, since that audience shape cannot
// distinguish an ID token from an access token for that issuer (the token
// itself may carry neither "at_hash" nor "c_hash", which OIDC permits — see
// rejectIDTokenClaims's doc comment). A URI-shaped audience (has a "://"
// scheme) must NOT trigger the warning.
//
// NOT t.Parallel(): swaps the package-global slog.Default() and restores it
// via t.Cleanup; see syncBuffer's doc comment for why a concurrent writer
// alone would be safe but an overlapping swap/restore pair would not be.
//
//nolint:paralleltest // mutates the package-global slog.Default()
func TestNewMultiIssuerTokenValidator_AudienceShapeWarning(t *testing.T) {
	tests := []struct {
		name             string
		expectedAudience string
		wantWarning      bool
	}{
		{
			name:             "bare client-ID-shaped audience warns",
			expectedAudience: "toolhive-authserver",
			wantWarning:      true,
		},
		{
			name:             "bare GUID audience warns",
			expectedAudience: "3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			wantWarning:      true,
		},
		{
			name:             "https URI audience does not warn",
			expectedAudience: "https://api.example.com",
			wantWarning:      false,
		},
		{
			name:             "api URI audience does not warn",
			expectedAudience: "api://3f2504e0-4f89-11d3-9a0c-0305e82c3301",
			wantWarning:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selfJWKS := newTestJWKS(t)
			selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
			require.NoError(t, err)

			var buf syncBuffer
			prev := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(prev) })

			v, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       tt.expectedAudience,
				AllowedActors:          []string{"ext-agent"}, // avoid the unrelated AllowedActors warning
				AllowedDelegateClients: []string{anyDelegateClient},
			}}, nil)
			require.NoError(t, err)
			require.NotNil(t, v)

			logged := buf.String()
			const wantSubstring = "does not look like a resource identifier"
			if tt.wantWarning {
				assert.Contains(t, logged, wantSubstring)
				assert.Contains(t, logged, testExternalIssuer)
			} else {
				assert.NotContains(t, logged, wantSubstring)
			}
		})
	}
}

//nolint:paralleltest // mutates the package-global slog.Default()
func TestMultiIssuerTokenValidator_ActorMatcherEvaluationFailure(t *testing.T) {
	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)

	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          jwksServer.URL + "/jwks",
		// timestamp() on a malformed value is CEL's own example of an
		// evaluation error that quotes the offending value verbatim —
		// this exercises that the claim value never reaches the error
		// or the log, not just an attribute-missing error that never
		// carried a value to begin with.
		ActorMatcher:           `timestamp(claims["sensitive"]) > timestamp("2000-01-01T00:00:00Z")`,
		AllowedDelegateClients: []string{anyDelegateClient},
	}})

	rawToken := externalJWKS.signToken(t, externalClaims(), map[string]any{"sensitive": "sensitive-claim-value"})
	_, err := validator.Validate(context.Background(), rawToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "actor matcher evaluation failed")
	assert.Contains(t, buf.String(), "level=DEBUG")
	assert.NotContains(t, err.Error(), "sensitive-claim-value", "returned error must not carry claim values")
	assert.NotContains(t, buf.String(), "sensitive-claim-value", "diagnostics must not log claims")
	assert.NotContains(t, buf.String(), rawToken, "diagnostics must not log tokens")
}

func TestNewMultiIssuerTokenValidator_ClonesAllowedActors(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)

	allowedActors := []string{"ext-agent"}
	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedActors:          allowedActors,
		AllowedDelegateClients: []string{anyDelegateClient},
	}}
	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	// Mutate the caller's slice after construction. If the constructor didn't
	// clone it, this would change what the validator accepts.
	allowedActors[0] = "someone-else"

	rawToken := externalJWKS.signToken(t, externalClaims(), map[string]any{"azp": "ext-agent"})
	result, err := validator.Validate(context.Background(), rawToken)
	require.NoError(t, err, "post-construction mutation of the caller's slice must not affect validation")
	require.NotNil(t, result)
	assert.Equal(t, "ext-agent", result.ExternalActor)
}

// TestMultiIssuerTokenValidator_ClockSkewLeeway exercises externalClockSkewLeeway
// (60s): nbf/iat tolerate it, but exp is enforced strictly by an independent
// check in validateExternalToken, since go-jose's leeway would otherwise widen
// exp too and let a genuinely expired subject token through.
func TestMultiIssuerTokenValidator_ClockSkewLeeway(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)

	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedActors:          []string{"ext-agent"},
		AllowedDelegateClients: []string{anyDelegateClient},
	}}
	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	tests := []struct {
		name        string
		claims      func() jwt.Claims
		wantErr     bool
		errContains string
	}{
		{
			name: "nbf ~30s in the future is within leeway",
			claims: func() jwt.Claims {
				c := externalClaims()
				c.NotBefore = jwt.NewNumericDate(time.Now().Add(30 * time.Second))
				return c
			},
		},
		{
			name: "nbf ~5m in the future exceeds leeway",
			claims: func() jwt.Claims {
				c := externalClaims()
				c.NotBefore = jwt.NewNumericDate(time.Now().Add(5 * time.Minute))
				return c
			},
			wantErr:     true,
			errContains: "claims validation failed",
		},
		{
			// go-jose's leeway also widens exp, so a naive implementation would
			// accept this. validateExternalToken's independent strict check
			// must reject it instead.
			name: "expired ~30s ago is rejected by the strict expiry check, not the leeway",
			claims: func() jwt.Claims {
				c := externalClaims()
				c.Expiry = jwt.NewNumericDate(time.Now().Add(-30 * time.Second))
				return c
			},
			wantErr:     true,
			errContains: "subject token has expired",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rawToken := externalJWKS.signToken(t, tt.claims(), map[string]any{"azp": "ext-agent"})
			result, err := validator.Validate(context.Background(), rawToken)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
		})
	}
}

// TestMultiIssuerTokenValidator_DiscoverJWKSURL exercises discoverJWKSURL
// directly via the externalIssuerConfig seam, bypassing constructor
// validation so an empty JWKSURL can be tested at all.
// NewMultiIssuerTokenValidator can no longer exercise OIDC discovery through
// its own constructor: validateTrustedIssuer rejects {AllowPrivateIPs: true,
// JWKSURL: ""}, and every test issuer built through it runs on a loopback
// httptest server, which requires AllowPrivateIPs: true to be reachable at
// all. discoverJWKSURL itself only reads issuerConfig.IssuerURL and
// .httpClient, so it needs neither a real validator nor a registered issuer.
func TestMultiIssuerTokenValidator_DiscoverJWKSURL(t *testing.T) {
	t.Parallel()

	newDiscoveryServer := func(t *testing.T, handler http.HandlerFunc) *httptest.Server {
		t.Helper()
		mux := http.NewServeMux()
		mux.HandleFunc("/.well-known/openid-configuration", handler)
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return srv
	}

	tests := []struct {
		name string
		// build returns the issuerConfig to call discoverJWKSURL with, and
		// (only for the no-error cases) the jwks_uri it must return.
		build       func(t *testing.T) (issuerConfig *externalIssuerConfig, wantJWKSURI string)
		errContains string
	}{
		{
			name: "happy path returns the advertised jwks_uri",
			build: func(t *testing.T) (*externalIssuerConfig, string) {
				t.Helper()
				srv := newDiscoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
					base := "http://" + r.Host
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"issuer":   base,
						"jwks_uri": base + "/jwks",
					})
				})
				return &externalIssuerConfig{
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL, AllowedDelegateClients: []string{anyDelegateClient}},
					jwks:          discoveryFetcher(t, srv.Client()),
				}, srv.URL + "/jwks"
			},
		},
		{
			// Proves two things at once: the well-known path is built by
			// trimming the trailing slash (a client that refuses to follow
			// redirects would see a 301 from http.ServeMux's path-cleaning if
			// the trim were dropped, since the request path would then
			// contain "//"), and the issuer-match comparison below that use
			// the *untrimmed* IssuerURL (the discovery document's own
			// "issuer" here still carries the trailing slash).
			name: "trailing-slash issuer_url is trimmed before the well-known path is appended",
			build: func(t *testing.T) (*externalIssuerConfig, string) {
				t.Helper()
				srv := newDiscoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
					base := "http://" + r.Host + "/"
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"issuer":   base,
						"jwks_uri": base + "jwks",
					})
				})
				client := srv.Client()
				client.CheckRedirect = func(*http.Request, []*http.Request) error {
					return fmt.Errorf("unexpected redirect: issuer_url trailing slash was not trimmed")
				}
				return &externalIssuerConfig{
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL + "/", AllowedDelegateClients: []string{anyDelegateClient}},
					jwks:          discoveryFetcher(t, client),
				}, srv.URL + "/jwks"
			},
		},
		{
			name: "invalid issuer_url fails to build the discovery request",
			build: func(t *testing.T) (*externalIssuerConfig, string) {
				t.Helper()
				return &externalIssuerConfig{
					// A raw control character is rejected by url.Parse inside
					// http.NewRequestWithContext, before any network I/O.
					TrustedIssuer: TrustedIssuer{IssuerURL: "http://example.com/\x00", AllowedDelegateClients: []string{anyDelegateClient}},
					jwks:          discoveryFetcher(t, &http.Client{}),
				}, ""
			},
			errContains: "failed to create discovery request",
		},
		{
			name: "unreachable issuer fails the discovery request",
			build: func(t *testing.T) (*externalIssuerConfig, string) {
				t.Helper()
				srv := httptest.NewServer(http.NotFoundHandler())
				srv.Close() // closed before use: the port is now guaranteed unreachable.
				return &externalIssuerConfig{
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL, AllowedDelegateClients: []string{anyDelegateClient}},
					jwks:          discoveryFetcher(t, srv.Client()),
				}, ""
			},
			errContains: "discovery request failed",
		},
		{
			name: "non-200 response",
			build: func(t *testing.T) (*externalIssuerConfig, string) {
				t.Helper()
				srv := newDiscoveryServer(t, func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
				})
				return &externalIssuerConfig{
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL, AllowedDelegateClients: []string{anyDelegateClient}},
					jwks:          discoveryFetcher(t, srv.Client()),
				}, ""
			},
			errContains: "discovery endpoint returned status 500",
		},
		{
			name: "malformed JSON body fails to parse",
			build: func(t *testing.T) (*externalIssuerConfig, string) {
				t.Helper()
				srv := newDiscoveryServer(t, func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte("{not-json"))
				})
				return &externalIssuerConfig{
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL, AllowedDelegateClients: []string{anyDelegateClient}},
					jwks:          discoveryFetcher(t, srv.Client()),
				}, ""
			},
			// The distinguishing substring from json.Unmarshal itself, not
			// just the wrapping "failed to parse discovery document": the
			// oversized-body case below wraps the same outer message but
			// fails for a different underlying reason.
			errContains: "invalid character",
		},
		{
			// A body over maxResponseBodySize (1 MiB) is not its own error
			// branch: io.LimitReader silently truncates it, and the
			// truncated bytes then fail to parse as JSON — so this exercises
			// the same "failed to parse discovery document" wrapper as the
			// malformed-JSON case above, but for a distinct underlying
			// reason (an unterminated value, not a syntax error), which is
			// what's asserted on below.
			name: "oversized discovery document is truncated and fails JSON parsing",
			build: func(t *testing.T) (*externalIssuerConfig, string) {
				t.Helper()
				srv := newDiscoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
					issuer := "http://" + r.Host
					w.Header().Set("Content-Type", "application/json")
					// Deliberately WELL-FORMED and complete: issuer matches and
					// jwks_uri is present, so if the whole body were read this
					// document would parse and discovery would succeed. Only the
					// maxResponseBodySize cap cutting inside the padding makes it
					// fail. That is what pins the cap — an unterminated body
					// would fail the same way with the cap deleted.
					_, _ = w.Write([]byte(`{"issuer":"` + issuer + `","jwks_uri":"` + issuer + `/jwks","padding":"`))
					_, _ = w.Write([]byte(strings.Repeat("a", 2*maxResponseBodySize)))
					_, _ = w.Write([]byte(`"}`))
				})
				return &externalIssuerConfig{
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL, AllowedDelegateClients: []string{anyDelegateClient}},
					jwks:          discoveryFetcher(t, srv.Client()),
				}, ""
			},
			errContains: "unexpected end of JSON input",
		},
		{
			name: "issuer mismatch is rejected",
			build: func(t *testing.T) (*externalIssuerConfig, string) {
				t.Helper()
				srv := newDiscoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"issuer":   "https://different-issuer.example.com",
						"jwks_uri": "http://" + r.Host + "/jwks",
					})
				})
				return &externalIssuerConfig{
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL, AllowedDelegateClients: []string{anyDelegateClient}},
					jwks:          discoveryFetcher(t, srv.Client()),
				}, ""
			},
			errContains: "does not match expected issuer",
		},
		{
			name: "missing jwks_uri is rejected",
			build: func(t *testing.T) (*externalIssuerConfig, string) {
				t.Helper()
				srv := newDiscoveryServer(t, func(w http.ResponseWriter, r *http.Request) {
					// Issuer must match so discovery reaches the jwks_uri check.
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]string{
						"issuer": "http://" + r.Host,
					})
				})
				return &externalIssuerConfig{
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL, AllowedDelegateClients: []string{anyDelegateClient}},
					jwks:          discoveryFetcher(t, srv.Client()),
				}, ""
			},
			errContains: "missing 'jwks_uri'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			issuerConfig, wantJWKSURI := tt.build(t)
			gotJWKSURI, err := (&MultiIssuerTokenValidator{}).discoverJWKSURL(context.Background(), issuerConfig)

			if tt.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Empty(t, gotJWKSURI)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, wantJWKSURI, gotJWKSURI)
		})
	}
}

// TestMultiIssuerTokenValidator_DiscoveryRefusesPrivateAddress proves the
// dial-time private-IP guard on the strict (AllowPrivateIPs: false) path: an
// issuer with an empty JWKSURL forces OIDC discovery, and every test server
// in this file runs on loopback — so with AllowPrivateIPs left false,
// discovery must be refused before any response is even read, regardless of
// what the discovery endpoint would have returned. The content-level failure
// modes of discoverJWKSURL itself (non-200, malformed doc, issuer mismatch,
// missing jwks_uri) are exercised directly by
// TestMultiIssuerTokenValidator_DiscoverJWKSURL instead, since none of them
// can be reached through this constructor path anymore.
func TestMultiIssuerTokenValidator_DiscoveryRefusesPrivateAddress(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)

	// The handler would, if reached, serve a valid discovery document — this
	// confirms the failure happens at the dial, not because the endpoint
	// itself is broken.
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":   base,
			"jwks_uri": base + "/jwks",
		})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// JWKSURL is left empty to force discovery. AllowPrivateIPs is
	// deliberately false: newMultiValidator would force it true to reach the
	// loopback server, defeating the point of this test, so the constructor
	// is called directly here instead.
	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              server.URL,
		ExpectedAudience:       testExternalAudience,
		InsecureAllowHTTP:      true,
		AllowPrivateIPs:        false,
		AllowedDelegateClients: []string{anyDelegateClient},
	}}
	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)
	validator, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, trustedIssuers, nil)
	require.NoError(t, err)

	claims := externalClaims()
	claims.Issuer = server.URL
	rawToken := externalJWKS.signToken(t, claims, nil)

	result, err := validator.Validate(context.Background(), rawToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OIDC discovery failed")
	assert.Contains(t, err.Error(), networking.ErrPrivateIpAddress,
		"the failure must be the dial-time private-IP guard specifically, not just any discovery error")
	assert.Nil(t, result)
}

func TestMultiIssuerTokenValidator_KidMismatch(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)

	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		AllowedDelegateClients: []string{anyDelegateClient},
	}}
	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	// Sign with a key whose public half is NOT in the served JWKS and whose
	// kid does not match any served key. The kid lookup misses, so the
	// validator falls back to trying every served key and fails verification.
	unknownKey := newECDSAJWK(t, "unknown-kid")
	claims := externalClaims()
	rawToken := signWithJWK(t, unknownKey, jose.ES256, claims)

	result, err := validator.Validate(context.Background(), rawToken)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "signature verification failed")
	assert.Nil(t, result)
}

// TestValidateJWKSURL exercises jwks.ValidateJWKSURL directly: this is the SSRF
// guard applied by jwks.Fetcher.EnsureRegistered to every JWKS URL for a given
// issuer, whether hand-configured on TrustedIssuer or resolved via discovery,
// and shared verbatim with pkg/authserver/config.go's config-time check
// (validateJWKSEndpointURL) so the two can't drift out of sync. The
// equivalent check on redirect hops (networking.SameHostRedirectPolicy) and
// the dial-time IP guard (networking.NewHostScopedClientBuilder) are
// exercised via the networking package's own tests, not here. These cases
// all pass insecureAllowHTTP=false, allowPrivateIPs=false — the strict
// defaults — since every other test in this file goes through
// newMultiValidator, which sets both permissive flags on its test issuers to
// reach their httptest servers over plain HTTP on loopback; the
// insecureAllowHTTP=true cases below are the exception, covering the laxity
// that must not extend beyond "http" to every other non-https scheme.
func TestValidateJWKSURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		url               string
		insecureAllowHTTP bool
		wantErr           string
	}{
		{name: "https accepted", url: "https://issuer.example.com/jwks"},
		{name: "http rejected", url: "http://issuer.example.com/jwks", wantErr: "must use HTTPS"},
		{
			name:    "userinfo with password rejected",
			url:     "https://user:hunter2@issuer.example.com/jwks",
			wantErr: "must not contain userinfo",
		},
		{
			// url.Parse sets User for a bare username too, and net/http would
			// still send it as a Basic auth header.
			name:    "userinfo without password rejected",
			url:     "https://user@issuer.example.com/jwks",
			wantErr: "must not contain userinfo",
		},
		{
			name:              "userinfo rejected even with insecureAllowHTTP",
			url:               "http://user:hunter2@issuer.example.com/jwks",
			insecureAllowHTTP: true,
			wantErr:           "must not contain userinfo",
		},
		{
			name:              "http accepted with insecureAllowHTTP",
			url:               "http://issuer.example.com/jwks",
			insecureAllowHTTP: true,
		},
		{
			name:              "ftp rejected even with insecureAllowHTTP",
			url:               "ftp://issuer.example.com/jwks",
			insecureAllowHTTP: true,
			wantErr:           "must use HTTPS",
		},
		{
			name:              "no scheme rejected even with insecureAllowHTTP",
			url:               "//issuer.example.com/jwks",
			insecureAllowHTTP: true,
			wantErr:           "must use HTTPS",
		},
		{name: "loopback IP literal rejected", url: "https://127.0.0.1/jwks", wantErr: "private or loopback"},
		{name: "private IP literal rejected", url: "https://10.1.2.3/jwks", wantErr: "private or loopback"},
		{name: "malformed URL rejected", url: "://not-a-url", wantErr: "invalid URL"},
		{name: "missing host rejected", url: "https:///jwks", wantErr: "host is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := jwks.ValidateJWKSURL(tt.url, tt.insecureAllowHTTP, false)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestMultiIssuerTokenValidator_FetchJWKS exercises the fetcher's and
// lookupJWKS's error paths through the full Validate path, bypassing OIDC
// discovery via a preconfigured JWKSURL. Registration and the JWKS's
// too-many-keys check now go through the issuer's own jwks.Fetcher (whose
// registration, body cap, and key-count internals are exercised directly by
// pkg/auth/jwks' own tests), while the zero-keys check stays in lookupJWKS.
//
// For "non-200 response" and "malformed JSON body", verified empirically:
// httprc.Resource's `ready` channel only ever closes on a *successful*
// fetch, so Register's WithWaitReady(true) wait (the fetcher's
// first-ever-registration path) can't distinguish "still fetching" from
// "fetch failed" — it just blocks until fetchCtx's own deadline and returns
// a generic timeout, discarding the real HTTP/parse error, which is why
// these two cases assert on "context deadline exceeded" rather than on
// jwx's own status-code or parse-error text (that text only ever surfaces
// on a *later* validation of the same never-yet-succeeded issuer, via
// the fetcher's Refresh-based retry branch — not exercised by a single
// Validate call here). "zero keys" and "too many keys" are unaffected: a
// JWKS that parses but fails the key-count checks still
// registers successfully, so those checks run immediately.
func TestMultiIssuerTokenValidator_FetchJWKS(t *testing.T) {
	t.Parallel()

	// Built once: jwks.DefaultMaxKeys+1 distinct public keys for the "too many keys" case.
	tooManyKeys := make([]jose.JSONWebKey, jwks.DefaultMaxKeys+1)
	for i := range tooManyKeys {
		key := newECDSAJWK(t, fmt.Sprintf("k%d", i))
		tooManyKeys[i] = key.Public()
	}

	// A complete, otherwise-valid JWKS document padded past
	// maxResponseBodySize with an unrelated field — see its use below for why
	// that matters.
	oversizedJWKSDoc := func() []byte {
		paddingKey := newECDSAJWK(t, "padding-kid")
		raw, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{paddingKey.Public()}})
		require.NoError(t, err)
		var doc map[string]any
		require.NoError(t, json.Unmarshal(raw, &doc))
		doc["padding"] = strings.Repeat("a", 4*1024*1024)
		padded, err := json.Marshal(doc)
		require.NoError(t, err)
		return padded
	}()

	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantErr string
	}{
		{
			name: "non-200 response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr: "context deadline exceeded",
		},
		{
			name: "malformed JSON body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{not-json"))
			},
			wantErr: "context deadline exceeded",
		},
		{
			name: "zero keys",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{}})
			},
			wantErr: "contains no keys",
		},
		{
			name: "too many keys",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: tooManyKeys})
			},
			wantErr: "too many keys",
		},
		{
			// Pins limitedBodyTransport's cap on the JWKS fetch path (jwx's
			// own httprc.MaxBufferSize ceiling is ~1000 MiB, far too high to
			// bound anything here on its own). Deliberately WELL-FORMED and
			// complete, unlike the other failure cases above: the padding
			// field is oversized but the document would parse successfully
			// if read in full, so only limitedBodyTransport cutting the read
			// short makes this fail — the same technique
			// TestMultiIssuerTokenValidator_DiscoverJWKSURL's oversized-body
			// case uses for the discovery path. The 4 MiB padding size is a
			// fixed literal independent of maxResponseBodySize (1 MiB): sizing
			// it as a multiple of that constant would make a broken cap and a
			// shrunken constant fail identically, hiding a regression in the
			// cap itself.
			name: "oversized JWKS response is rejected rather than parsed",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(oversizedJWKSDoc)
			},
			wantErr: "context deadline exceeded",
		},
	}

	selfJWKS := newTestJWKS(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("/jwks", tt.handler)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			trustedIssuers := []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                srv.URL + "/jwks",
				AllowedDelegateClients: []string{anyDelegateClient},
			}}
			validator := newMultiValidator(t, selfJWKS, trustedIssuers)

			rawToken := signExternalToken(t, newECDSAJWK(t, "any-kid"), externalClaims())
			_, err := validator.Validate(context.Background(), rawToken)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// signExternalToken signs claims with the given key for the external-issuer
// path, adding a may_act claim so the token satisfies delegation consent
// without needing an AllowedActors allowlist match — signWithJWK doesn't
// support extra claims like azp.
func signExternalToken(t *testing.T, key jose.JSONWebKey, claims jwt.Claims) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	require.NoError(t, err)
	raw, err := jwt.Signed(signer).
		Claims(claims).
		Claims(map[string]any{"may_act": map[string]any{"sub": "some-toolhive-client", "iss": testIssuer}}).
		Serialize()
	require.NoError(t, err)
	return raw
}

// TestMultiIssuerTokenValidator_KeyRotationRefreshesImmediately is the test
// that demonstrates the point of the cache rewrite: a subject token signed
// with a newly rotated key, carrying a kid the validator has never seen, must
// validate successfully on the very first attempt — not after jwx's own
// 15-minute-minimum background refresh, and not only on a second, separate
// request.
func TestMultiIssuerTokenValidator_KeyRotationRefreshesImmediately(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	keyV1 := newECDSAJWK(t, "v1")
	keyV2 := newECDSAJWK(t, "v2")

	var mu sync.Mutex
	currentKey := keyV1
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		key := currentKey
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(publicJWKSOf(key))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                srv.URL + "/jwks",
		AllowedDelegateClients: []string{"some-toolhive-client"},
		AllowMayAct:            true,
	}}
	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	// Prime the cache: validate successfully against v1.
	_, err := validator.Validate(context.Background(), signExternalToken(t, keyV1, externalClaims()))
	require.NoError(t, err)

	// Rotate: the server now serves only v2's public key, under a new kid.
	mu.Lock()
	currentKey = keyV2
	mu.Unlock()

	claims := externalClaims()
	claims.ID = "jti-post-rotation"
	tokenV2 := signExternalToken(t, keyV2, claims)

	done := make(chan struct {
		result *ValidatedClaims
		err    error
	}, 1)
	go func() {
		result, err := validator.Validate(context.Background(), tokenV2)
		done <- struct {
			result *ValidatedClaims
			err    error
		}{result, err}
	}()

	select {
	case out := <-done:
		require.NoError(t, out.err)
		require.NotNil(t, out.result)
		assert.Equal(t, "ext-user-456", out.result.Subject)
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for validation after key rotation")
	}
}

// TestMultiIssuerTokenValidator_UnknownKidRefreshIsRateLimited proves
// jwks.Fetcher.RefreshOnUnknownKid's minKidRefreshInterval gate: repeated
// subject tokens
// naming a kid absent from the cached JWKS must not force a fetch per
// request — only the first ever unknown-kid attempt (within the interval)
// may do so.
func TestMultiIssuerTokenValidator_UnknownKidRefreshIsRateLimited(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	keyV1 := newECDSAJWK(t, "v1")

	var fetchCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		fetchCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(publicJWKSOf(keyV1))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                srv.URL + "/jwks",
		AllowedDelegateClients: []string{"some-toolhive-client"},
		AllowMayAct:            true,
	}}
	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	// Prime the cache with a first, successful validation.
	_, err := validator.Validate(context.Background(), signExternalToken(t, keyV1, externalClaims()))
	require.NoError(t, err)
	primedFetches := fetchCount.Load()

	// Repeatedly present a token naming a kid absent from the served JWKS.
	unknownKey := newECDSAJWK(t, "unknown-kid")
	for i := range 5 {
		claims := externalClaims()
		claims.ID = fmt.Sprintf("jti-unknown-%d", i)
		rawToken := signExternalToken(t, unknownKey, claims)
		_, err := validator.Validate(context.Background(), rawToken)
		require.Error(t, err)
	}

	// Only the first unknown-kid attempt should have forced a refresh; the
	// gate must hold for the remaining four within the fetcher's
	// minKidRefreshInterval window.
	assert.Equal(t, primedFetches+1, fetchCount.Load(),
		"repeated unknown-kid tokens within the window must not each force a fetch")
}

// TestMultiIssuerTokenValidator_NeverFetchedRetryIsRateLimited proves
// jwks.WithFetchFailureBackoff's fetch-failure backoff gate: an issuer whose
// endpoint
// has never once succeeded must not be re-fetched on every request — key
// resolution runs before signature verification, so without this gate any
// client holding a subject token naming this issuer could drive one real
// outbound fetch per request.
func TestMultiIssuerTokenValidator_NeverFetchedRetryIsRateLimited(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)

	var fetchCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		fetchCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	trustedIssuers := []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                srv.URL + "/jwks",
		AllowedDelegateClients: []string{anyDelegateClient},
	}}
	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	rawToken := signExternalToken(t, newECDSAJWK(t, "any-kid"), externalClaims())

	// First attempt: no cached value yet, so this one genuinely fetches (and
	// blocks on Register's own wait — see jwks.Fetcher.EnsureRegistered's
	// doc comment).
	_, err := validator.Validate(context.Background(), rawToken)
	require.Error(t, err)
	require.Equal(t, int32(1), fetchCount.Load())

	// Further attempts within the fetch-failure backoff window must replay the
	// stored error instead of fetching again.
	for range 3 {
		_, err := validator.Validate(context.Background(), rawToken)
		require.Error(t, err)
	}
	assert.Equal(t, int32(1), fetchCount.Load(),
		"repeated requests against a never-successfully-fetched issuer must not each force a fetch")
}

// TestMultiIssuerTokenValidator_SharedJWKSURL_SamePolicy proves that two
// issuers resolving to the identical jwksURL under the identical HTTP
// transport policy both validate successfully — each through its own
// jwks.Fetcher (see externalIssuerConfig.jwks), which owns its own jwk.Cache
// and *http.Client. This is
// the common real-world case: Microsoft Entra v1 tenants share one
// tenant-independent JWKS endpoint, so two Entra tenants configured as
// separate trusted issuers collide on the same jwks_url by construction.
func TestMultiIssuerTokenValidator_SharedJWKSURL_SamePolicy(t *testing.T) {
	t.Parallel()

	const (
		issuerAURL = "https://issuer-a.example.com"
		issuerBURL = "https://issuer-b.example.com"
		audienceA  = "aud-a"
		audienceB  = "aud-b"
	)

	selfJWKS := newTestJWKS(t)
	sharedJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, sharedJWKS)

	trustedIssuers := []TrustedIssuer{
		{
			IssuerURL:              issuerAURL,
			ExpectedAudience:       audienceA,
			JWKSURL:                jwksServer.URL + "/jwks",
			AllowedActors:          []string{"agent-a"},
			AllowedDelegateClients: []string{anyDelegateClient},
		},
		{
			IssuerURL:              issuerBURL,
			ExpectedAudience:       audienceB,
			JWKSURL:                jwksServer.URL + "/jwks",
			AllowedActors:          []string{"agent-b"},
			AllowedDelegateClients: []string{anyDelegateClient},
		},
	}
	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	tokenFor := func(issuer, audience, actor, jti string) string {
		now := time.Now()
		claims := jwt.Claims{
			Subject:   "shared-user",
			Issuer:    issuer,
			Audience:  jwt.Audience{audience},
			Expiry:    jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ID:        jti,
		}
		return sharedJWKS.signToken(t, claims, map[string]any{"azp": actor})
	}

	resultA, err := validator.Validate(context.Background(), tokenFor(issuerAURL, audienceA, "agent-a", "jti-a"))
	require.NoError(t, err, "first issuer to register the shared jwks_url must validate")
	assert.Equal(t, issuerAURL, resultA.ExternalIssuer)

	resultB, err := validator.Validate(context.Background(), tokenFor(issuerBURL, audienceB, "agent-b", "jti-b"))
	require.NoError(t, err, "second issuer sharing the same jwks_url under the same policy must also validate")
	assert.Equal(t, issuerBURL, resultB.ExternalIssuer)
}

// TestMultiIssuerTokenValidator_SharedJWKSURL_DifferingPolicy proves the
// property a per-issuer jwk.Cache adds over a shared one: two issuers
// resolving to the same jwks_url but configuring DIFFERENT
// insecure_allow_http/allow_private_ips settings both validate
// independently, each fetching through its own dedicated *http.Client. A
// shared cache could not do this — httprc keys a cached resource by URL
// alone and only honors jwk.WithHTTPClient on a URL's first Register call,
// so the second issuer would have silently inherited the first one's client
// and transport policy. Splitting the cache per issuer removes that
// collision instead of merely guarding against it.
func TestMultiIssuerTokenValidator_SharedJWKSURL_DifferingPolicy(t *testing.T) {
	t.Parallel()

	const (
		issuerAURL = "https://issuer-a.example.com"
		issuerBURL = "https://issuer-b.example.com"
		audienceA  = "aud-a"
		audienceB  = "aud-b"
	)

	selfJWKS := newTestJWKS(t)
	sharedJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, sharedJWKS)
	sharedJWKSURL := jwksServer.URL + "/jwks"

	// Deliberately NOT newMultiValidator: that helper forces
	// InsecureAllowHTTP and AllowPrivateIPs to true on every issuer so its
	// loopback httptest servers are reachable, which would erase the very
	// difference this test exists to exercise. Both issuers share one
	// plain-HTTP loopback jwks_url and allow private IPs, and differ ONLY in
	// InsecureAllowHTTP — so each is judged against its own transport policy:
	// A is refused for its own reason (no HTTP permitted), B succeeds.
	//
	// Under a shared cache B could not succeed here: the policy-claim guard
	// rejected any second issuer whose policy differed from the URL's first
	// claimant, and without that guard B would have silently inherited A's
	// client. Per-issuer caches make both outcomes independent.
	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)
	validator, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, []TrustedIssuer{
		{
			IssuerURL:              issuerAURL,
			ExpectedAudience:       audienceA,
			JWKSURL:                sharedJWKSURL,
			AllowedActors:          []string{"agent-a"},
			AllowPrivateIPs:        true,
			InsecureAllowHTTP:      false,
			AllowedDelegateClients: []string{anyDelegateClient},
		},
		{
			IssuerURL:              issuerBURL,
			ExpectedAudience:       audienceB,
			JWKSURL:                sharedJWKSURL,
			AllowedActors:          []string{"agent-b"},
			AllowPrivateIPs:        true,
			InsecureAllowHTTP:      true,
			AllowedDelegateClients: []string{anyDelegateClient},
		},
	}, nil)
	require.NoError(t, err)

	tokenFor := func(issuer, audience, actor, jti string) string {
		now := time.Now()
		claims := jwt.Claims{
			Subject:   "shared-user",
			Issuer:    issuer,
			Audience:  jwt.Audience{audience},
			Expiry:    jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ID:        jti,
		}
		return sharedJWKS.signToken(t, claims, map[string]any{"azp": actor})
	}

	// A is judged against its OWN policy: it forbids plain HTTP, so its fetch
	// of the shared http:// jwks_url is refused. Not a policy-conflict error —
	// A is simply misconfigured for this URL.
	_, err = validator.Validate(context.Background(), tokenFor(issuerAURL, audienceA, "agent-a", "jti-a"))
	require.Error(t, err, "the issuer forbidding plain HTTP must be refused for its own jwks_url")
	assert.Contains(t, err.Error(), "must use HTTPS",
		"the refusal must come from issuer A's own transport policy")

	// B shares that exact URL but permits HTTP, and succeeds — the outcome a
	// shared cache could not produce, since A reached the URL first.
	resultB, err := validator.Validate(context.Background(), tokenFor(issuerBURL, audienceB, "agent-b", "jti-b"))
	require.NoError(t, err, "the issuer permitting HTTP must validate independently, "+
		"neither blocked by nor inheriting issuer A's stricter policy")
	assert.Equal(t, issuerBURL, resultB.ExternalIssuer)
}
