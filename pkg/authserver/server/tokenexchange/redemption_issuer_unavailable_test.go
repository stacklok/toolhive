// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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
			// A valid empty set. Encoding a zero jose.JSONWebKeySet would send
			// {"keys":null}, which jwx rejects before the post-fetch code runs.
			_, _ = w.Write([]byte(`{"keys":[]}`))
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

// capturingHandler records every slog record it is handed.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (*capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *capturingHandler) WithGroup(string) slog.Handler { return h }

// errorsWithMessage returns the Error-level records whose message is msg.
func (h *capturingHandler) errorsWithMessage(msg string) []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Level == slog.LevelError && r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

// recordAttr returns the value of the named attribute on r.
func recordAttr(r slog.Record, key string) (slog.Value, bool) {
	var found slog.Value
	var ok bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			found, ok = a.Value, true
			return false
		}
		return true
	})
	return found, ok
}

//nolint:paralleltest // swaps slog.SetDefault; racing with any concurrent test that logs via the default logger
func TestIssuerKeysUnavailableLogsOncePerWindow(t *testing.T) {
	tests := []struct {
		name    string
		outage  jwksOutage
		wantMsg string
		// reopenWindow moves the issuer's rate-limit window back so the next
		// failure is logged again.
		reopenWindow func(c *externalIssuerConfig, past time.Time)
	}{
		{
			name:    "fetch failure",
			outage:  jwksDown,
			wantMsg: "issuer signing keys unavailable; assertions from this issuer are refused until the next fetch attempt",
			reopenWindow: func(c *externalIssuerConfig, past time.Time) {
				c.fetchFailedAt = past
			},
		},
		{
			name:    "empty key set after fetch",
			outage:  jwksEmpty,
			wantMsg: "issuer signing keys unusable; assertions from this issuer are refused until its key set changes",
			reopenWindow: func(c *externalIssuerConfig, past time.Time) {
				c.keysUnusableLoggedAt = past
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &capturingHandler{}
			prev := slog.Default()
			prevWriter, prevFlags := log.Writer(), log.Flags()
			slog.SetDefault(slog.New(handler))
			t.Cleanup(func() {
				slog.SetDefault(prev)
				// slog.SetDefault also redirects the log package's output and flags,
				// and restoring the previous slog default does not undo that.
				log.SetOutput(prevWriter)
				log.SetFlags(prevFlags)
			})

			served := newTestJWKS(t)
			validator := newUnavailabilityValidator(t, tt.outage, served)
			validate := func() {
				raw := served.signTokenWithType(t, "", jwtBearerExternalClaims(), nil)
				_, err := validator.ValidateJWTBearerAssertion(context.Background(), raw, []string{testTokenEndpoint})
				require.Error(t, err)
				require.ErrorIs(t, err, errIssuerKeysUnavailable)
			}

			for range 3 {
				validate()
			}
			records := handler.errorsWithMessage(tt.wantMsg)
			require.Len(t, records, 1, "three refusals in one window log one Error line")
			issuer, ok := recordAttr(records[0], "issuer")
			require.True(t, ok, "the line names the issuer")
			assert.Equal(t, testExternalIssuer, issuer.String())

			issuerConfig := validator.issuers[testExternalIssuer]
			issuerConfig.mu.Lock()
			tt.reopenWindow(issuerConfig, time.Now().Add(-jwksFetchFailureBackoff-time.Second))
			issuerConfig.mu.Unlock()

			validate()
			assert.Len(t, handler.errorsWithMessage(tt.wantMsg), 2, "a refusal after the window elapsed logs again")
		})
	}
}
