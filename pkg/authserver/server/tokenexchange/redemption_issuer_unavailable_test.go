// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jwksOutage names how the issuer's JWKS endpoint misbehaves in a test.
type jwksOutage int

const (
	jwksServing jwksOutage = iota
	jwksDown
	jwksEmpty
)

// newUnavailabilityValidator builds a validator for testExternalIssuer whose
// JWKS endpoint behaves as outage says. When it serves, it serves served's keys.
func newUnavailabilityValidator(t *testing.T, outage jwksOutage, served *testJWKS) *MultiIssuerTokenValidator {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if outage == jwksEmpty {
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{})
			return
		}
		_ = json.NewEncoder(w).Encode(served.publicJWKS())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if outage == jwksDown {
		srv.Close()
	}
	return newMultiValidator(t, newTestJWKS(t), []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                srv.URL + "/jwks",
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: time.Hour.String(),
			SubjectBindings: []JWTBearerSubjectBinding{{Subject: "ext-user-456", AllowedResources: []string{"https://mcp.example.com"}}},
		},
	}})
}

func TestMultiIssuerTokenValidator_IssuerKeysUnavailableSentinel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		outage       jwksOutage
		signerKID    string // empty: sign with the served key
		wantSentinel bool
	}{
		{name: "fetch failure", outage: jwksDown, wantSentinel: true},
		{name: "empty key set", outage: jwksEmpty, wantSentinel: true},
		{name: "unknown kid with working keys", outage: jwksServing, signerKID: "unknown-kid", wantSentinel: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			served := newTestJWKS(t)
			validator := newUnavailabilityValidator(t, tt.outage, served)
			signer := served
			if tt.signerKID != "" {
				signer = newTestJWKSWithKID(t, tt.signerKID)
			}
			raw := signer.signTokenWithType(t, "", jwtBearerExternalClaims(), nil)

			_, err := validator.ValidateJWTBearerAssertion(context.Background(), raw, []string{testTokenEndpoint})

			require.Error(t, err)
			assert.Equal(t, tt.wantSentinel, errors.Is(err, errIssuerKeysUnavailable), "error: %v", err)
		})
	}
}

func TestMultiIssuerTokenValidator_EndedRequestIsNotIssuerUnavailable(t *testing.T) {
	t.Parallel()

	served := newTestJWKS(t)
	validator := newUnavailabilityValidator(t, jwksServing, served)
	raw := served.signTokenWithType(t, "", jwtBearerExternalClaims(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := validator.ValidateJWTBearerAssertion(ctx, raw, []string{testTokenEndpoint})

	require.Error(t, err)
	assert.False(t, errors.Is(err, errIssuerKeysUnavailable), "error: %v", err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMultiIssuerTokenValidator_EmptyDefaultAudiencesSentinel(t *testing.T) {
	t.Parallel()

	validator := newUnavailabilityValidator(t, jwksServing, newTestJWKS(t))

	_, err := validator.ValidateJWTBearerAssertion(context.Background(), "x", nil)

	assert.ErrorIs(t, err, errDefaultAudiencesEmpty)
}
