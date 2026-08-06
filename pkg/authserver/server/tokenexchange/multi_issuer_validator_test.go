// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	v, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, issuers)
	require.NoError(t, err)
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
				assert.Empty(t, vc.ExternalIssuer, "self-issued path must never set ExternalIssuer")
			},
		},
		{
			name: "external token accepted",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				assert.Equal(t, testExternalIssuer, vc.ExternalIssuer)
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
			name: "external token leaves AllowedDelegateClients nil when issuer does not configure it",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
				// AllowedDelegateClients intentionally unset: permissive default.
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]interface{}{"azp": "ext-agent"})
			},
			check: func(t *testing.T, vc *ValidatedClaims) {
				t.Helper()
				assert.Nil(t, vc.AllowedDelegateClients)
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
				AllowedDelegateClients: []string{"toolhive-agent-a"},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(),
					map[string]any{"may_act": map[string]any{"sub": "some-toolhive-client"}})
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				ActorClaim:       "appid",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				ActorClaim:       "cid",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				ActorClaim:       "client_id",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				// AllowedActors intentionally empty: may_act is authoritative and
				// skips the allowlist entirely.
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(),
					map[string]any{"may_act": map[string]any{"sub": "some-toolhive-client"}})
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
			name: "external token may_act wins even when azp is not allowlisted",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"someone-else"},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "not-in-the-allowlist",
					"may_act": map[string]any{"sub": "some-toolhive-client"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
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
			// The external issuer's own URL is never a valid may_act.iss
			// value: a regression that compared against issuerConfig.IssuerURL
			// instead of v.selfIssuer would wrongly accept this.
			name: "external token may_act.iss matching the external issuer's own URL rejected",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
			name: "external token missing exp claim",
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          jwksServer.URL + "/jwks",
				AllowedActors:    []string{"ext-agent"},
			}},
			token: func(t *testing.T) string {
				t.Helper()
				return externalJWKS.signToken(t, externalClaims(), map[string]any{
					"azp":     "ext-agent",
					"may_act": map[string]any{"sub": "some-toolhive-client"},
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
		IssuerURL:        trailingSlashIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          jwksServer.URL + "/jwks",
		AllowedActors:    []string{"ext-agent"},
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
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          jwksServer.URL + "/jwks",
		AllowedActors:    []string{"ext-agent"},
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
		name           string
		selfValidator  *SelfIssuedTokenValidator
		selfIssuer     string
		trustedIssuers []TrustedIssuer
		errContains    string
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
				IssuerURL:        "",
				ExpectedAudience: testExternalAudience,
			}},
			errContains: "issuer_url is required",
		},
		{
			name:          "empty ExpectedAudience rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: "",
			}},
			errContains: "expected_audience is required",
		},
		{
			name:          "IssuerURL equal to selfIssuer rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testIssuer,
				ExpectedAudience: testExternalAudience,
			}},
			errContains: "must not equal the authorization server's own issuer",
		},
		{
			name:          "duplicate IssuerURL rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{
				{IssuerURL: testExternalIssuer, ExpectedAudience: testExternalAudience},
				{IssuerURL: testExternalIssuer, ExpectedAudience: testExternalAudience},
			},
			errContains: "configured more than once",
		},
		{
			name:          `ActorClaim "sub" rejected — assignClaim never leaves it in Extra`,
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				ActorClaim:       "sub",
			}},
			errContains: "actor_claim",
		},
		{
			name:          `ActorClaim "scope" rejected — rerouted to a structured field`,
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				ActorClaim:       "scope",
			}},
			errContains: "actor_claim",
		},
		{
			name:          `ActorClaim "may_act" rejected — rerouted to a structured field`,
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				ActorClaim:       "may_act",
			}},
			errContains: "actor_claim",
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
			// AllowPrivateIPs without a hand-configured jwks_url would let
			// OIDC discovery choose the private target the dial is allowed to
			// reach; validateTrustedIssuer must reject it so a caller that
			// skips Config.Validate (factory, tests) cannot bypass the
			// config-time check in pkg/authserver/config.go.
			name:          "AllowPrivateIPs without JWKSURL rejected",
			selfValidator: selfValidator,
			selfIssuer:    testIssuer,
			trustedIssuers: []TrustedIssuer{{
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				AllowPrivateIPs:  true,
			}},
			errContains: "allow_private_ips requires jwks_url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v, err := NewMultiIssuerTokenValidator(tt.selfValidator, tt.selfIssuer, tt.trustedIssuers)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errContains)
			assert.Nil(t, v)
		})
	}
}

func TestNewMultiIssuerTokenValidator_EmptyAllowedActorsAccepted(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	// Empty AllowedActors must be accepted by the constructor: a may_act-only
	// issuer (no allowlisted actors at all) is a legitimate configuration.
	v, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, []TrustedIssuer{{
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
	}})
	require.NoError(t, err)
	assert.NotNil(t, v)
}

func TestNewMultiIssuerTokenValidator_ClonesAllowedActors(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)

	allowedActors := []string{"ext-agent"}
	trustedIssuers := []TrustedIssuer{{
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          jwksServer.URL + "/jwks",
		AllowedActors:    allowedActors,
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
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          jwksServer.URL + "/jwks",
		AllowedActors:    []string{"ext-agent"},
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
// directly via the externalIssuerConfig seam (the same pattern
// TestMultiIssuerTokenValidator_RegisteredButUnreadyRetainsPolicyClaim uses),
// bypassing constructor validation so an empty JWKSURL can be tested at all.
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
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL},
					httpClient:    srv.Client(),
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
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL + "/"},
					httpClient:    client,
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
					TrustedIssuer: TrustedIssuer{IssuerURL: "http://example.com/\x00"},
					httpClient:    &http.Client{},
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
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL},
					httpClient:    srv.Client(),
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
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL},
					httpClient:    srv.Client(),
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
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL},
					httpClient:    srv.Client(),
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
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL},
					httpClient:    srv.Client(),
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
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL},
					httpClient:    srv.Client(),
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
					TrustedIssuer: TrustedIssuer{IssuerURL: srv.URL},
					httpClient:    srv.Client(),
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
		IssuerURL:         server.URL,
		ExpectedAudience:  testExternalAudience,
		InsecureAllowHTTP: true,
		AllowPrivateIPs:   false,
	}}
	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)
	validator, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, trustedIssuers)
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
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          jwksServer.URL + "/jwks",
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

// TestValidateJWKSURL exercises ValidateJWKSURL directly: this is the SSRF
// guard applied in ensureRegistered to every JWKS URL for a given issuer, whether
// hand-configured on TrustedIssuer or resolved via discovery, and shared
// verbatim with pkg/authserver/config.go's config-time check
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

			err := ValidateJWKSURL(tt.url, tt.insecureAllowHTTP, false)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestMultiIssuerTokenValidator_FetchJWKS exercises ensureRegistered's and
// lookupJWKS's error paths through the full Validate path, bypassing OIDC
// discovery via a preconfigured JWKSURL. Registration and the JWKS's own
// zero-keys/too-many-keys checks now go through the shared jwk.Cache and
// lookupJWKS respectively rather than a private HTTP fetch.
//
// For "non-200 response" and "malformed JSON body", verified empirically:
// httprc.Resource's `ready` channel only ever closes on a *successful*
// fetch, so Register's WithWaitReady(true) wait (ensureRegistered's
// first-ever-registration path) can't distinguish "still fetching" from
// "fetch failed" — it just blocks until fetchCtx's own deadline and returns
// a generic timeout, discarding the real HTTP/parse error, which is why
// these two cases assert on "context deadline exceeded" rather than on
// jwx's own status-code or parse-error text (that text only ever surfaces
// on a *later* validation of the same never-yet-succeeded issuer, via
// ensureRegistered's Refresh-based retry branch — not exercised by a single
// Validate call here). "zero keys" and "too many keys" are unaffected: a
// JWKS that parses but fails this file's own key-count checks still
// registers successfully, so lookupJWKS's checks run immediately.
func TestMultiIssuerTokenValidator_FetchJWKS(t *testing.T) {
	t.Parallel()

	// Built once: maxJWKSKeys+1 distinct public keys for the "too many keys" case.
	tooManyKeys := make([]jose.JSONWebKey, maxJWKSKeys+1)
	for i := range tooManyKeys {
		key := newECDSAJWK(t, fmt.Sprintf("k%d", i))
		tooManyKeys[i] = key.Public()
	}

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
				IssuerURL:        testExternalIssuer,
				ExpectedAudience: testExternalAudience,
				JWKSURL:          srv.URL + "/jwks",
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
		Claims(map[string]any{"may_act": map[string]any{"sub": "some-toolhive-client"}}).
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
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          srv.URL + "/jwks",
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
// refreshOnUnknownKid's minKidRefreshInterval gate: repeated subject tokens
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
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          srv.URL + "/jwks",
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
	// gate must hold for the remaining four within minKidRefreshInterval.
	assert.Equal(t, primedFetches+1, fetchCount.Load(),
		"repeated unknown-kid tokens within the window must not each force a fetch")
}

// TestMultiIssuerTokenValidator_NeverFetchedRetryIsRateLimited proves
// ensureRegistered's jwksFetchFailureBackoff gate: an issuer whose endpoint
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
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          srv.URL + "/jwks",
	}}
	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	rawToken := signExternalToken(t, newECDSAJWK(t, "any-kid"), externalClaims())

	// First attempt: no cached value yet, so this one genuinely fetches (and
	// blocks on Register's own wait — see ensureRegistered's doc comment).
	_, err := validator.Validate(context.Background(), rawToken)
	require.Error(t, err)
	require.Equal(t, int32(1), fetchCount.Load())

	// Further attempts within jwksFetchFailureBackoff must replay the stored
	// error instead of fetching again.
	for range 3 {
		_, err := validator.Validate(context.Background(), rawToken)
		require.Error(t, err)
	}
	assert.Equal(t, int32(1), fetchCount.Load(),
		"repeated requests against a never-successfully-fetched issuer must not each force a fetch")
}

// TestMultiIssuerTokenValidator_SharedJWKSURL_SamePolicy proves that two
// issuers resolving to the identical jwksURL under the identical HTTP
// transport policy both validate successfully, sharing the one underlying
// jwk.Cache entry. This is the common real-world case claimJWKSURLPolicy must
// not regress: Microsoft Entra v1 tenants share one tenant-independent JWKS
// endpoint, so two Entra tenants configured as separate trusted issuers
// collide on the same jwks_url by construction.
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
			IssuerURL:        issuerAURL,
			ExpectedAudience: audienceA,
			JWKSURL:          jwksServer.URL + "/jwks",
			AllowedActors:    []string{"agent-a"},
		},
		{
			IssuerURL:        issuerBURL,
			ExpectedAudience: audienceB,
			JWKSURL:          jwksServer.URL + "/jwks",
			AllowedActors:    []string{"agent-b"},
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
// fail-closed half of claimJWKSURLPolicy: two issuers resolving to the same
// jwksURL but configuring different insecure_allow_http/allow_private_ips
// settings must not have the second one silently inherit the first one's
// *http.Client. The shared jwks_url here is a syntactically valid HTTPS host
// that intentionally never resolves — ValidateJWKSURL accepts it regardless
// of either issuer's own flags (both being moot for an https, non-IP-literal
// host), so the conflict is detected before any network attempt for the
// second issuer, and independently of whether the first issuer's own fetch
// ever succeeds.
func TestMultiIssuerTokenValidator_SharedJWKSURL_DifferingPolicy(t *testing.T) {
	t.Parallel()

	const (
		sharedJWKSURL = "https://shared-jwks.invalid/jwks"
		issuerAURL    = "https://issuer-a.example.com"
		issuerBURL    = "https://issuer-b.example.com"
	)

	selfJWKS := newTestJWKS(t)
	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	validator, err := NewMultiIssuerTokenValidator(selfValidator, testIssuer, []TrustedIssuer{
		{
			IssuerURL:        issuerAURL,
			ExpectedAudience: "aud-a",
			JWKSURL:          sharedJWKSURL,
			AllowedActors:    []string{"agent-a"},
			// InsecureAllowHTTP/AllowPrivateIPs both left at their strict
			// (false) default.
		},
		{
			IssuerURL:         issuerBURL,
			ExpectedAudience:  "aud-b",
			JWKSURL:           sharedJWKSURL,
			AllowedActors:     []string{"agent-b"},
			InsecureAllowHTTP: true,
			AllowPrivateIPs:   true,
		},
	})
	require.NoError(t, err)

	tokenFor := func(issuer, audience, actor string) string {
		now := time.Now()
		claims := jwt.Claims{
			Subject:   "shared-user",
			Issuer:    issuer,
			Audience:  jwt.Audience{audience},
			Expiry:    jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		}
		return selfJWKS.signToken(t, claims, map[string]any{"azp": actor})
	}

	// Issuer A claims the shared jwks_url first. Its own fetch will fail
	// (the host never resolves), but that's irrelevant here: the claim is
	// recorded before the network attempt, which is the property under test.
	_, err = validator.Validate(context.Background(), tokenFor(issuerAURL, "aud-a", "agent-a"))
	require.Error(t, err, "the unresolvable shared host must fail issuer A's own fetch")

	// Issuer B shares the same URL under a different policy: it must fail
	// closed, naming both issuers and the shared URL, rather than silently
	// reusing issuer A's *http.Client.
	_, err = validator.Validate(context.Background(), tokenFor(issuerBURL, "aud-b", "agent-b"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), issuerAURL)
	assert.Contains(t, err.Error(), issuerBURL)
	assert.Contains(t, err.Error(), sharedJWKSURL)
	assert.Contains(t, err.Error(), "insecure_allow_http")
}

// TestMultiIssuerTokenValidator_RetryAfterFetchFailureRefreshes proves the
// regression-safety half of the ensureRegistered rewrite (removing
// externalIssuerConfig.added in favor of asking v.jwksCache.IsRegistered
// directly): once a JWKS fetch has failed but the resource was genuinely
// registered with the shared cache, a later retry — once
// jwksFetchFailureBackoff has elapsed — must refresh the existing
// registration rather than attempt to register it again, which would fail
// with "already registered".
func TestMultiIssuerTokenValidator_RetryAfterFetchFailureRefreshes(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)

	var succeed atomic.Bool
	var fetchCount atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		fetchCount.Add(1)
		if !succeed.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(externalJWKS.publicJWKS())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	trustedIssuers := []TrustedIssuer{{
		IssuerURL:        testExternalIssuer,
		ExpectedAudience: testExternalAudience,
		JWKSURL:          srv.URL + "/jwks",
		AllowedActors:    []string{"ext-agent"},
	}}
	validator := newMultiValidator(t, selfJWKS, trustedIssuers)

	tokenWithID := func(jti string) string {
		claims := externalClaims()
		claims.ID = jti
		return externalJWKS.signToken(t, claims, map[string]any{"azp": "ext-agent"})
	}

	// First attempt: the endpoint is broken. This genuinely registers the
	// resource with the shared cache (per httprc.Controller.Add's own
	// ordering), but the fetch itself fails.
	_, err := validator.Validate(context.Background(), tokenWithID("jti-1"))
	require.Error(t, err)
	require.Equal(t, int32(1), fetchCount.Load())

	// Force the backoff gate open, as if jwksFetchFailureBackoff had elapsed,
	// without waiting the real 30s out.
	issuerConfig := validator.issuers[testExternalIssuer]
	issuerConfig.mu.Lock()
	issuerConfig.fetchFailedAt = time.Now().Add(-jwksFetchFailureBackoff - time.Second)
	issuerConfig.mu.Unlock()

	// The endpoint now recovers. The retry must go through Refresh — a
	// second Register call against the same URL would fail with "already
	// registered".
	succeed.Store(true)
	result, err := validator.Validate(context.Background(), tokenWithID("jti-2"))
	require.NoError(t, err, "retry after a registered-but-failed fetch must refresh, not re-register")
	require.NotNil(t, result)
}

// TestMultiIssuerTokenValidator_RegisteredButUnreadyRetainsPolicyClaim forces
// httprc.Controller.Add's mode-b failure — the resource IS tracked by the
// cache (registration itself succeeded) but its first fetch fails, so
// Register returns an error wrapping httprc.ErrNotReady — and proves the
// issuer's jwksURL policy claim is KEPT, not rolled back. Releasing it here
// would let a second issuer, sharing the same jwks_url under a different
// transport policy, claim the URL and then Refresh through the FIRST
// issuer's *http.Client on the retry path — reopening the
// silent-policy-inheritance hole claimJWKSURLPolicy exists to close.
func TestMultiIssuerTokenValidator_RegisteredButUnreadyRetainsPolicyClaim(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)

	// Always fails: httprc.Controller.Add inserts the resource into the
	// backend's map before waiting on this first fetch, so the failure here
	// happens strictly after the resource is already tracked.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	cache, err := jwk.NewCache(context.Background(), httprc.NewClient())
	require.NoError(t, err)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cache.Shutdown(shutdownCtx)
	})

	issuerConfig := &externalIssuerConfig{
		TrustedIssuer: TrustedIssuer{
			IssuerURL:         testExternalIssuer,
			ExpectedAudience:  testExternalAudience,
			JWKSURL:           srv.URL,
			AllowedActors:     []string{"ext-agent"},
			InsecureAllowHTTP: true,
			AllowPrivateIPs:   true,
		},
		jwksURL:    srv.URL,
		httpClient: srv.Client(),
	}
	validator := &MultiIssuerTokenValidator{
		selfIssuer:      testIssuer,
		selfValidator:   selfValidator,
		issuers:         map[string]*externalIssuerConfig{testExternalIssuer: issuerConfig},
		jwksCache:       cache,
		jwksURLPolicies: make(map[string]jwksURLPolicy),
	}

	err = validator.registerOrRefresh(context.Background(), issuerConfig)
	require.Error(t, err, "the first fetch must fail (server always returns 500)")
	require.ErrorIs(t, err, httprc.ErrNotReady(),
		"the failure must be the registered-but-not-ready case this test targets")

	_, claimed := validator.jwksURLPolicies[srv.URL]
	assert.True(t, claimed, "the policy claim must be retained: the resource is tracked by the cache")

	registeredCtx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	assert.True(t, cache.IsRegistered(registeredCtx, srv.URL),
		"sanity check: the cache must still be tracking the URL despite the fetch failure")
}

// TestMultiIssuerTokenValidator_FailedRegisterReleasesPolicyClaim forces
// httprc.Controller.Add's mode-a failure — Register returns an error AND the
// resource was never tracked by the cache (IsRegistered false) — and proves
// the failed issuer's jwksURL policy claim is rolled back, so a later issuer
// resolving to the same jwks_url under a DIFFERENT transport policy is not
// permanently blocked for the life of the process.
//
// The shared jwks_url is a syntactically valid HTTPS host that never resolves
// (the same trick TestMultiIssuerTokenValidator_SharedJWKSURL_DifferingPolicy
// uses): ValidateJWKSURL accepts it under either issuer's flags, so both
// issuers reach Register. The cache's httprc whitelist rejects the FIRST
// IsAllowed call — issuer A's Register, which then fails before sendBackend,
// so the cache never tracks the URL (mode-a) — and allows every later one.
// Issuer B's Register therefore gets past the whitelist, tracks the URL, and
// fails only on the unresolvable fetch: a mode-b failure whose error names
// the JWKS fetch, NOT the policy-conflict error claimJWKSURLPolicy would
// return if A's orphaned claim were left in place.
func TestMultiIssuerTokenValidator_FailedRegisterReleasesPolicyClaim(t *testing.T) {
	t.Parallel()

	const (
		sharedJWKSURL = "https://shared-jwks.invalid/jwks"
		issuerAURL    = "https://issuer-a.example.com"
		issuerBURL    = "https://issuer-b.example.com"
	)

	selfJWKS := newTestJWKS(t)

	// Reject exactly one IsAllowed call: the whitelist is consulted only in
	// httprc.Controller.Add (registration), never in Lookup/Refresh, so the
	// first rejection is issuer A's Register below.
	var rejected atomic.Bool
	cache, err := jwk.NewCache(context.Background(), httprc.NewClient(httprc.WithWhitelist(
		httprc.WhitelistFunc(func(string) bool {
			return rejected.Swap(true)
		}),
	)))
	require.NoError(t, err)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cache.Shutdown(shutdownCtx)
	})

	selfValidator, err := NewSelfIssuedTokenValidator(selfJWKS.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)

	// Issuer A configures the strict default policy; issuer B relaxes both
	// gates. The two share one jwks_url, so B's claimJWKSURLPolicy is the
	// call under test.
	validator := &MultiIssuerTokenValidator{
		selfIssuer:    testIssuer,
		selfValidator: selfValidator,
		issuers: map[string]*externalIssuerConfig{
			issuerAURL: {
				TrustedIssuer: TrustedIssuer{
					IssuerURL:        issuerAURL,
					ExpectedAudience: "aud-a",
					AllowedActors:    []string{"agent-a"},
					// InsecureAllowHTTP/AllowPrivateIPs left at their strict
					// (false) defaults.
				},
				jwksURL:    sharedJWKSURL,
				httpClient: &http.Client{},
			},
			issuerBURL: {
				TrustedIssuer: TrustedIssuer{
					IssuerURL:         issuerBURL,
					ExpectedAudience:  "aud-b",
					AllowedActors:     []string{"agent-b"},
					InsecureAllowHTTP: true,
					AllowPrivateIPs:   true,
				},
				jwksURL:    sharedJWKSURL,
				httpClient: &http.Client{},
			},
		},
		jwksCache:       cache,
		jwksURLPolicies: make(map[string]jwksURLPolicy),
	}

	// Issuer A claims the shared jwks_url, then its Register fails in mode-a:
	// the whitelist rejection means the cache never tracked the URL. The
	// policy claim must be rolled back, not left to block issuer B forever.
	err = validator.registerOrRefresh(context.Background(), validator.issuers[issuerAURL])
	require.Error(t, err, "issuer A's Register must fail (whitelist blocks the shared jwks_url)")
	require.Contains(t, err.Error(), "failed to register JWKS for issuer")

	// Issuer B resolves to the same jwks_url under a different policy. With
	// A's orphaned claim released, B claims the URL afresh and reaches its own
	// Register — which fails only because the shared host never resolves, NOT
	// because claimJWKSURLPolicy failed closed on A's lingering claim. The
	// distinguishing assertion is the absence of the policy-conflict error.
	err = validator.registerOrRefresh(context.Background(), validator.issuers[issuerBURL])
	require.Error(t, err, "issuer B's Register must fail (the shared jwks_url never resolves)")
	assert.NotContains(t, err.Error(), "insecure_allow_http",
		"issuer B must not be permanently blocked by issuer A's failed registration")
	assert.NotContains(t, err.Error(), issuerAURL,
		"the error must be issuer B's own fetch failure, not the cross-issuer policy conflict")
	assert.Contains(t, err.Error(), "failed to register JWKS for issuer")
}
