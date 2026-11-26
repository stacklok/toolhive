// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
)

var threeUpstreamChain = []string{"provider-1", "provider-2", "provider-3"}

// threeLegSetup builds a handler over provider-1..3 with NonFatalSecondaryLegs
// set to nonFatal, and returns the mocks keyed by upstream name.
func threeLegSetup(t *testing.T, nonFatal bool) (*Handler, *testStorageState, map[string]*mockIDPProvider) {
	t.Helper()
	provider, oauth2Config, stor, storState := baseTestSetup(t)
	oauth2Config.NonFatalSecondaryLegs = nonFatal

	mocks := make(map[string]*mockIDPProvider, len(threeUpstreamChain))
	upstreams := make([]NamedUpstream, 0, len(threeUpstreamChain))
	for _, name := range threeUpstreamChain {
		m := &mockIDPProvider{
			providerType:     upstream.ProviderTypeOAuth2,
			authorizationURL: "https://" + name + ".example.com/authorize",
			exchangeResult: &upstream.Identity{
				Tokens: &upstream.Tokens{
					AccessToken: name + "-access-token",
					ExpiresAt:   time.Now().Add(time.Hour),
				},
				Subject: "subject-" + name,
				Name:    "Leg User",
				Email:   "leg@example.com",
			},
		}
		mocks[name] = m
		upstreams = append(upstreams, NamedUpstream{Name: name, Provider: m})
	}
	handler, err := NewHandler(provider, oauth2Config, stor, upstreams)
	require.NoError(t, err)
	return handler, storState, mocks
}

// firstLegPending is the inbound pending for provider-1, as /oauth/authorize
// would store it.
func firstLegPending(sessionID string) *storage.PendingAuthorization {
	return &storage.PendingAuthorization{
		ClientID:             testAuthClientID,
		RedirectURI:          testAuthRedirectURI,
		State:                "client-state",
		PKCEChallenge:        "client-challenge",
		PKCEMethod:           "S256",
		Scopes:               []string{"openid"},
		UpstreamPKCEVerifier: "first-leg-verifier-1234567890123456789012",
		UpstreamNonce:        "first-leg-nonce",
		UpstreamProviderName: "provider-1",
		SessionID:            sessionID,
		CreatedAt:            time.Now(),
	}
}

// laterLegPending is a pending for a non-first leg, as continueChainOrComplete
// would store it after the first leg resolved "resolved-user".
func laterLegPending(sessionID, provider string, skipped ...string) *storage.PendingAuthorization {
	p := firstLegPending(sessionID)
	p.UpstreamProviderName = provider
	p.ChainUpstreams = threeUpstreamChain
	p.SkippedUpstreams = skipped
	p.ResolvedUserID = "resolved-user"
	p.ResolvedUserName = "Leg User"
	p.ResolvedUserEmail = "leg@example.com"
	return p
}

// seedToken stores a live token for provider in sessionID, owned by "resolved-user".
func seedToken(storState *testStorageState, sessionID, provider string) {
	storState.upstreamTokens[sessionID+":"+provider] = &storage.UpstreamTokens{
		ProviderID:  provider,
		AccessToken: provider + "-seeded",
		ExpiresAt:   time.Now().Add(time.Hour),
		ClientID:    testAuthClientID,
		UserID:      "resolved-user",
	}
}

// pendingFor returns the stored pending authorization for provider, and its state.
func pendingFor(t *testing.T, storState *testStorageState, provider string) (string, *storage.PendingAuthorization) {
	t.Helper()
	for state, p := range storState.pendingAuths {
		if p.UpstreamProviderName == provider {
			return state, p
		}
	}
	require.Failf(t, "pending not found", "no pending for %s", provider)
	return "", nil
}

// requireCodeIssued asserts the response is the client redirect carrying an
// authorization code and no error.
func requireCodeIssued(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	location := rec.Header().Get("Location")
	assert.True(t, strings.HasPrefix(location, testAuthRedirectURI), "redirect goes to the client: %s", location)
	loc, err := url.Parse(location)
	require.NoError(t, err)
	assert.NotEmpty(t, loc.Query().Get("code"), "client must receive an authorization code")
	assert.Empty(t, loc.Query().Get("error"))
}

// requireClientError asserts the response is the client redirect carrying errCode.
func requireClientError(t *testing.T, rec *httptest.ResponseRecorder, errCode string) {
	t.Helper()
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, errCode, loc.Query().Get("error"))
	assert.Empty(t, loc.Query().Get("code"))
}

// captureWarnings routes the default slog logger to a JSON buffer for the rest
// of the test and returns a function that parses the records logged so far.
func captureWarnings(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []map[string]any {
		var records []map[string]any
		dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
		for dec.More() {
			var rec map[string]any
			require.NoError(t, dec.Decode(&rec))
			records = append(records, rec)
		}
		return records
	}
}

// skipRecords returns the "skipping failed upstream chain leg" records, with the
// timestamp removed so they compare as plain field maps.
func skipRecords(records []map[string]any) []map[string]any {
	var skips []map[string]any
	for _, r := range records {
		if r["msg"] == "skipping failed upstream chain leg" {
			delete(r, "time")
			skips = append(skips, r)
		}
	}
	return skips
}

// skipRecord is the expected WARN record for one skipped leg.
func skipRecord(provider, failureClass string) map[string]any {
	return map[string]any{
		"level":         "WARN",
		"msg":           "skipping failed upstream chain leg",
		"provider":      provider,
		"failure_class": failureClass,
	}
}

// TestCallback_NonFatalSecondaryLeg_IdPErrorSkipsTheLeg walks a three-upstream
// chain in which the user cancels provider-2's consent screen. It is not
// parallel because it swaps the process-wide slog default to assert the log line.
//
//nolint:paralleltest // mutates the process-global slog default
func TestCallback_NonFatalSecondaryLeg_IdPErrorSkipsTheLeg(t *testing.T) {
	records := captureWarnings(t)
	handler, storState, mocks := threeLegSetup(t, true)
	const sessionID = "idp-error-session"

	// Leg 1 completes and redirects to provider-2.
	cookie := bindPending(t, storState, "leg1", firstLegPending(sessionID))
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, newCallbackRequest("code=leg1-code&state=leg1", cookie))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	require.Contains(t, rec.Header().Get("Location"), "https://provider-2.example.com/authorize")
	leg2State, leg2 := pendingFor(t, storState, "provider-2")
	readsBeforeSkip := len(storState.getAllUpstreamCtx)

	// The IdP answers provider-2's leg with access_denied: the browser goes to provider-3.
	rec2 := httptest.NewRecorder()
	handler.CallbackHandler(rec2, newCallbackRequest(
		"error=access_denied&error_description=user+cancelled&state="+leg2State,
		bindingCookieFrom(t, rec, leg2State)))
	require.Equal(t, http.StatusFound, rec2.Code, rec2.Body.String())
	require.Contains(t, rec2.Header().Get("Location"), "https://provider-3.example.com/authorize")
	leg3State, leg3 := pendingFor(t, storState, "provider-3")
	assert.Equal(t, []string{"provider-2"}, leg3.SkippedUpstreams)
	assertCallbackContextUsers(t, storState.getAllUpstreamCtx[readsBeforeSkip:], leg2.ResolvedUserID)

	// Leg 3 completes: the client receives an authorization code.
	rec3 := httptest.NewRecorder()
	handler.CallbackHandler(rec3, newCallbackRequest("code=leg3-code&state="+leg3State, bindingCookieFrom(t, rec2, leg3State)))
	requireCodeIssued(t, rec3)
	assert.Equal(t, "leg3-code", mocks["provider-3"].capturedCode)
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-1")
	assert.NotContains(t, storState.upstreamTokens, sessionID+":provider-2")
	assert.Contains(t, storState.upstreamTokens, sessionID+":provider-3")

	// provider-2's failure logs two WARN lines: the existing upstream-error line,
	// and one skip line naming the provider and failure class and carrying no
	// token, code or user value. (The walk also logs an unrelated last-used
	// timestamp WARN against the test storage, left out here.)
	all := records()
	var msgs []any
	for _, r := range all {
		if r["msg"] != "failed to update identity last used timestamp" {
			msgs = append(msgs, r["msg"])
		}
	}
	assert.Equal(t, []any{"upstream IDP returned error", "skipping failed upstream chain leg"}, msgs)
	assert.Equal(t, []map[string]any{skipRecord("provider-2", "idp_error")}, skipRecords(all))
}

// TestCallback_NonFatalSecondaryLeg_ExchangeFailureCompletesTheChain is not
// parallel because it swaps the process-wide slog default to assert the log line.
//
//nolint:paralleltest // mutates the process-global slog default
func TestCallback_NonFatalSecondaryLeg_ExchangeFailureCompletesTheChain(t *testing.T) {
	tests := []struct {
		name        string
		exchangeErr error
		wantFailure string
	}{
		{name: "token request fails", exchangeErr: errors.New("token endpoint returned 401"), wantFailure: "code_exchange"},
		{
			name:        "identity validation fails",
			exchangeErr: fmt.Errorf("%w: nonce mismatch", upstream.ErrIdentityResolutionFailed),
			wantFailure: "identity_resolution",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := captureWarnings(t)
			handler, storState, mocks := threeLegSetup(t, true)
			const sessionID = "exchange-failure-session"
			seedToken(storState, sessionID, "provider-1")
			seedToken(storState, sessionID, "provider-2")
			mocks["provider-3"].exchangeErr = tt.exchangeErr

			cookie := bindPending(t, storState, "leg3", laterLegPending(sessionID, "provider-3"))
			rec := httptest.NewRecorder()
			handler.CallbackHandler(rec, newCallbackRequest("code=leg3-code&state=leg3", cookie))

			requireCodeIssued(t, rec)
			assert.NotContains(t, storState.upstreamTokens, sessionID+":provider-3", "the skipped leg has no stored token")
			assert.Equal(t, "provider-1-seeded", storState.upstreamTokens[sessionID+":provider-1"].AccessToken)
			assert.Equal(t, "provider-2-seeded", storState.upstreamTokens[sessionID+":provider-2"].AccessToken)
			assert.Empty(t, storState.pendingAuths)
			assertCallbackContextUsers(t, storState.getAllUpstreamCtx, "resolved-user")
			assert.Equal(t, []map[string]any{skipRecord("provider-3", tt.wantFailure)}, skipRecords(records()))
		})
	}
}

// TestCallback_NonFatalSecondaryLeg_SkippedLegKeepsItsStoredToken covers a
// re-connect: provider-2 holds an expired credential from an earlier
// authorization, is prompted, and the user cancels. The skip must leave that
// credential in place rather than delete it.
func TestCallback_NonFatalSecondaryLeg_SkippedLegKeepsItsStoredToken(t *testing.T) {
	t.Parallel()
	handler, storState, _ := threeLegSetup(t, true)
	const sessionID = "reconnect-session"
	seedToken(storState, sessionID, "provider-1")
	seedToken(storState, sessionID, "provider-2")
	seedToken(storState, sessionID, "provider-3")
	storState.upstreamTokens[sessionID+":provider-2"].ExpiresAt = time.Now().Add(-time.Hour)

	cookie := bindPending(t, storState, "leg2", laterLegPending(sessionID, "provider-2"))
	rec := httptest.NewRecorder()
	handler.CallbackHandler(rec, newCallbackRequest("error=access_denied&state=leg2", cookie))

	requireCodeIssued(t, rec)
	require.Contains(t, storState.upstreamTokens, sessionID+":provider-2", "the skipped leg's earlier credential is kept")
	assert.Equal(t, "provider-2-seeded", storState.upstreamTokens[sessionID+":provider-2"].AccessToken)
}

// TestCallback_NonFatalSecondaryLeg_AuthorizeURLFailureSkips is not parallel
// because it swaps the process-wide slog default to assert the log lines.
//
//nolint:paralleltest // mutates the process-global slog default
func TestCallback_NonFatalSecondaryLeg_AuthorizeURLFailureSkips(t *testing.T) {
	tests := []struct {
		name        string
		failURLFor  []string
		wantNextLeg string // empty: the chain completes
	}{
		{name: "skips to the leg after it", failURLFor: []string{"provider-2"}, wantNextLeg: "provider-3"},
		{name: "completes the chain when it was the last", failURLFor: []string{"provider-3"}},
		{name: "skips every failing leg in turn", failURLFor: []string{"provider-2", "provider-3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := captureWarnings(t)
			handler, storState, mocks := threeLegSetup(t, true)
			for _, name := range tt.failURLFor {
				mocks[name].authURLErr = errors.New("bad authorization endpoint")
			}
			const sessionID = "authurl-session"
			// provider-2 is satisfied unless its URL is the one failing, so the walk
			// reaches the failing leg straight from the first leg.
			if tt.failURLFor[0] == "provider-3" {
				seedToken(storState, sessionID, "provider-2")
			}

			cookie := bindPending(t, storState, "leg1", firstLegPending(sessionID))
			rec := httptest.NewRecorder()
			handler.CallbackHandler(rec, newCallbackRequest("code=leg1-code&state=leg1", cookie))

			// One WARN per skipped leg, in walk order.
			wantSkips := make([]map[string]any, 0, len(tt.failURLFor))
			for _, name := range tt.failURLFor {
				wantSkips = append(wantSkips, skipRecord(name, "authorization_url"))
			}
			assert.Equal(t, wantSkips, skipRecords(records()))

			if tt.wantNextLeg == "" {
				requireCodeIssued(t, rec)
				assert.Empty(t, storState.pendingAuths, "no pending is left behind for a skipped leg")
				return
			}
			require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Header().Get("Location"), "https://"+tt.wantNextLeg+".example.com/authorize")
			require.Len(t, storState.pendingAuths, 1, "no pending is left behind for a skipped leg")
			_, next := pendingFor(t, storState, tt.wantNextLeg)
			assert.Equal(t, tt.failURLFor, next.SkippedUpstreams)
		})
	}
}

func TestNextMissingUpstream_IgnoresSkippedLegs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		skipped []string
		want    string
	}{
		{name: "no skipped legs prompts the first missing", skipped: nil, want: "provider-2"},
		{name: "a skipped leg is passed over", skipped: []string{"provider-2"}, want: "provider-3"},
		{name: "all missing legs skipped completes", skipped: []string{"provider-2", "provider-3"}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, storState, _ := threeLegSetup(t, true)
			seedToken(storState, "test-session", "provider-1")

			got, err := handler.nextMissingUpstream(context.Background(), "test-session", threeUpstreamChain, tt.skipped)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCallback_NonFatalSecondaryLeg_UnskippableFailuresStayFatal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// pending is seeded under state "leg" and bound unless unbound is set.
		pending     func() *storage.PendingAuthorization
		exchangeErr bool
		query       string
		unbound     bool
		check       func(t *testing.T, rec *httptest.ResponseRecorder, storState *testStorageState)
	}{
		{
			name:    "first-leg IdP error",
			pending: func() *storage.PendingAuthorization { return firstLegPending("s") },
			query:   "error=access_denied&state=leg",
			check: func(t *testing.T, rec *httptest.ResponseRecorder, _ *testStorageState) {
				t.Helper()
				requireClientError(t, rec, "access_denied")
			},
		},
		{
			name:        "first-leg exchange failure",
			pending:     func() *storage.PendingAuthorization { return firstLegPending("s") },
			exchangeErr: true,
			query:       "code=c&state=leg",
			check: func(t *testing.T, rec *httptest.ResponseRecorder, _ *testStorageState) {
				t.Helper()
				requireClientError(t, rec, "server_error")
			},
		},
		{
			name:        "state mismatch on a later leg",
			pending:     func() *storage.PendingAuthorization { return laterLegPending("s", "provider-2") },
			exchangeErr: true,
			query:       "code=c&state=unknown-state",
			check: func(t *testing.T, rec *httptest.ResponseRecorder, storState *testStorageState) {
				t.Helper()
				assert.Equal(t, http.StatusBadRequest, rec.Code)
				assert.Contains(t, storState.pendingAuths, "leg")
			},
		},
		{
			name:    "browser-binding mismatch on a later leg's IdP error",
			pending: func() *storage.PendingAuthorization { return laterLegPending("s", "provider-2") },
			query:   "error=access_denied&state=leg",
			unbound: true,
			check: func(t *testing.T, rec *httptest.ResponseRecorder, storState *testStorageState) {
				t.Helper()
				assert.Equal(t, http.StatusBadRequest, rec.Code)
				assert.Contains(t, storState.pendingAuths, "leg", "the owner's pending survives")
			},
		},
		{
			name:        "browser-binding mismatch on a later leg's exchange failure",
			pending:     func() *storage.PendingAuthorization { return laterLegPending("s", "provider-2") },
			exchangeErr: true,
			query:       "code=c&state=leg",
			unbound:     true,
			check: func(t *testing.T, rec *httptest.ResponseRecorder, storState *testStorageState) {
				t.Helper()
				assert.Equal(t, http.StatusBadRequest, rec.Code)
				assert.Contains(t, storState.pendingAuths, "leg", "the owner's pending survives")
			},
		},
		{
			name: "SingleLeg IdP error on a later upstream",
			pending: func() *storage.PendingAuthorization {
				p := laterLegPending("s", "provider-2")
				p.SingleLeg, p.ChainUpstreams = true, nil
				return p
			},
			query: "error=access_denied&state=leg",
			check: func(t *testing.T, rec *httptest.ResponseRecorder, _ *testStorageState) {
				t.Helper()
				requireClientError(t, rec, "access_denied")
			},
		},
		{
			name: "SingleLeg exchange failure on a later upstream",
			pending: func() *storage.PendingAuthorization {
				p := laterLegPending("s", "provider-2")
				p.SingleLeg, p.ChainUpstreams = true, nil
				return p
			},
			exchangeErr: true,
			query:       "code=c&state=leg",
			check: func(t *testing.T, rec *httptest.ResponseRecorder, _ *testStorageState) {
				t.Helper()
				requireClientError(t, rec, "server_error")
			},
		},
		{
			// Not first by name, but the pending never resolved a user, so it
			// would establish one: only canSkipCallbackLeg's ResolvedUserID check
			// keeps it fatal.
			name: "later upstream whose pending has no resolved user",
			pending: func() *storage.PendingAuthorization {
				p := laterLegPending("s", "provider-2")
				p.ResolvedUserID, p.ChainUpstreams = "", nil
				return p
			},
			query: "error=access_denied&state=leg",
			check: func(t *testing.T, rec *httptest.ResponseRecorder, _ *testStorageState) {
				t.Helper()
				requireClientError(t, rec, "access_denied")
			},
		},
		{
			name: "IdP error with a corrupt stored redirect URI",
			pending: func() *storage.PendingAuthorization {
				p := laterLegPending("s", "provider-2")
				p.RedirectURI = "http://%zz"
				return p
			},
			query: "error=access_denied&state=leg",
			check: func(t *testing.T, rec *httptest.ResponseRecorder, _ *testStorageState) {
				t.Helper()
				assert.Equal(t, http.StatusBadGateway, rec.Code, "no client to redirect to: the generic error page")
			},
		},
		{
			name: "pending skipping the first upstream",
			pending: func() *storage.PendingAuthorization {
				return laterLegPending("s", "provider-2", "provider-1")
			},
			query: "error=access_denied&state=leg",
			check: func(t *testing.T, rec *httptest.ResponseRecorder, _ *testStorageState) {
				t.Helper()
				requireClientError(t, rec, "server_error")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, storState, mocks := threeLegSetup(t, true)
			seedToken(storState, "s", "provider-1")
			if tt.exchangeErr {
				for _, m := range mocks {
					m.exchangeErr = errors.New("exchange failed")
				}
			}
			cookie := bindPending(t, storState, "leg", tt.pending())
			if tt.unbound {
				cookie.Value = "not-the-binding"
			}
			rec := httptest.NewRecorder()
			handler.CallbackHandler(rec, newCallbackRequest(tt.query, cookie))
			tt.check(t, rec, storState)
			assert.NotContains(t, rec.Header().Get("Location"), "code=")
		})
	}
}

// TestCallback_NonFatalSecondaryLeg_RejectedPendingLogsNoSkip is not parallel
// because it swaps the process-wide slog default to assert the log lines.
//
//nolint:paralleltest // mutates the process-global slog default
func TestCallback_NonFatalSecondaryLeg_RejectedPendingLogsNoSkip(t *testing.T) {
	tests := []struct {
		name    string
		pending *storage.PendingAuthorization
	}{
		{name: "skips the first upstream", pending: laterLegPending("s", "provider-2", "provider-1")},
		{
			name: "stale later leg with no chain",
			pending: func() *storage.PendingAuthorization {
				p := laterLegPending("s", "provider-2")
				p.ChainUpstreams = nil
				return p
			}(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records := captureWarnings(t)
			handler, storState, _ := threeLegSetup(t, true)
			seedToken(storState, "s", "provider-1")
			cookie := bindPending(t, storState, "leg", tt.pending)

			rec := httptest.NewRecorder()
			handler.CallbackHandler(rec, newCallbackRequest("error=access_denied&state=leg", cookie))

			requireClientError(t, rec, "server_error")
			assert.Empty(t, skipRecords(records()), "a rejected pending is not logged as a skipped leg")
		})
	}
}

// A handler with no upstreams has no secondary legs: an IdP error for a bound
// later-leg pending is an ordinary failure, not a skip.
func TestCallback_NonFatalSecondaryLeg_NoUpstreamsIsNotSkippable(t *testing.T) {
	t.Parallel()

	provider, oauth2Config, stor, storState := baseTestSetup(t)
	oauth2Config.NonFatalSecondaryLegs = true
	handler, err := NewHandler(provider, oauth2Config, stor, nil)
	require.NoError(t, err)
	cookie := bindPending(t, storState, "leg", laterLegPending("s", "provider-2"))

	var rec *httptest.ResponseRecorder
	require.NotPanics(t, func() {
		rec = httptest.NewRecorder()
		handler.CallbackHandler(rec, newCallbackRequest("error=access_denied&state=leg", cookie))
	})
	requireClientError(t, rec, "access_denied")
}

func TestResolveChain_RejectsInvalidSkippedUpstreams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resolved bool // false: a first-leg pending, whose chain is computed now
		chain    []string
		skipped  []string
		wantErr  string
	}{
		{name: "valid skipped leg", resolved: true, skipped: []string{"provider-2"}},
		{name: "no skipped legs", resolved: true},
		{
			name: "names the first upstream", resolved: true, skipped: []string{"provider-1"},
			wantErr: "stored skipped upstreams are invalid",
		},
		{
			name: "names an upstream outside the chain", resolved: true, skipped: []string{"provider-9"},
			wantErr: "stored skipped upstreams are invalid",
		},
		{
			name: "duplicates a skipped leg", resolved: true, skipped: []string{"provider-2", "provider-2"},
			wantErr: "stored skipped upstreams are invalid",
		},
		{
			name:  "first-leg pending with a made-up chain and skipped leg",
			chain: threeUpstreamChain, skipped: []string{"provider-2"},
			wantErr: "first chain leg carries a chain or skipped upstreams",
		},
		{
			name:    "first-leg pending with only a chain",
			chain:   threeUpstreamChain,
			wantErr: "first chain leg carries a chain or skipped upstreams",
		},
		{
			name: "first-leg pending with only a skipped leg", skipped: []string{"provider-2"},
			wantErr: "first chain leg carries a chain or skipped upstreams",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The filter keeps every upstream; it records calls so a rejected
			// first-leg pending can be shown never to reach it.
			filter := &stubUpstreamFilter{keep: threeUpstreamChain[1:]}
			handler := newChainTestHandler(t, threeUpstreamChain, WithUpstreamFilter(filter))
			pending := firstLegPending("s")
			pending.ChainUpstreams = tt.chain
			pending.SkippedUpstreams = tt.skipped
			if tt.resolved {
				pending = laterLegPending("s", "provider-3", tt.skipped...)
			}

			chain, err := handler.resolveChain(context.Background(), pending, filterPrincipal{})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Zero(t, filter.calls, "an invalid pending is rejected before the filter runs")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, threeUpstreamChain, chain)
		})
	}
}

func TestCallback_SecondaryLegFailureIsFatalByDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(mocks map[string]*mockIDPProvider, storState *testStorageState) (*storage.PendingAuthorization, string)
		wantErr string
	}{
		{
			name: "IdP error on the second leg",
			setup: func(_ map[string]*mockIDPProvider, _ *testStorageState) (*storage.PendingAuthorization, string) {
				return laterLegPending("s", "provider-2"), "error=access_denied&state=leg"
			},
			wantErr: "access_denied",
		},
		{
			name: "exchange failure on the last leg",
			setup: func(mocks map[string]*mockIDPProvider, storState *testStorageState) (*storage.PendingAuthorization, string) {
				seedToken(storState, "s", "provider-2")
				mocks["provider-3"].exchangeErr = errors.New("exchange failed")
				return laterLegPending("s", "provider-3"), "code=c&state=leg"
			},
			wantErr: "server_error",
		},
		{
			name: "authorization URL failure on the next leg",
			setup: func(mocks map[string]*mockIDPProvider, _ *testStorageState) (*storage.PendingAuthorization, string) {
				mocks["provider-2"].authURLErr = errors.New("bad authorization endpoint")
				return firstLegPending("s"), "code=c&state=leg"
			},
			wantErr: "server_error",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			handler, storState, mocks := threeLegSetup(t, false)
			seedToken(storState, "s", "provider-1")
			pending, query := tt.setup(mocks, storState)
			cookie := bindPending(t, storState, "leg", pending)

			rec := httptest.NewRecorder()
			handler.CallbackHandler(rec, newCallbackRequest(query, cookie))
			requireClientError(t, rec, tt.wantErr)
		})
	}
}
