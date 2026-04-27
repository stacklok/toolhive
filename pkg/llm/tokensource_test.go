// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// minimalConfig returns a Config with the minimum fields for a configured gateway.
func minimalConfig() *Config {
	return &Config{
		GatewayURL: "https://llm.example.com",
		OIDC: OIDCConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "test-client",
		},
	}
}

// ── DeriveSecretKey ───────────────────────────────────────────────────────────

func TestDeriveSecretKey(t *testing.T) {
	t.Parallel()

	key1 := DeriveSecretKey("https://llm.example.com", "https://auth.example.com")
	key2 := DeriveSecretKey("https://llm.example.com", "https://auth.example.com")
	key3 := DeriveSecretKey("https://other.example.com", "https://auth.example.com")

	assert.Equal(t, key1, key2, "same inputs must produce the same key")
	assert.NotEqual(t, key1, key3, "different gateway URLs must produce different keys")
	assert.Contains(t, key1, "LLM_OAUTH_", "key must start with expected prefix")
}

// ── ensureOfflineAccess ───────────────────────────────────────────────────────

func TestEnsureOfflineAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  []string
		expect []string
	}{
		{
			name:   "already present",
			input:  []string{"openid", "offline_access"},
			expect: []string{"openid", "offline_access"},
		},
		{
			name:   "not present",
			input:  []string{"openid"},
			expect: []string{"openid", "offline_access"},
		},
		{
			name:   "empty",
			input:  []string{},
			expect: []string{"offline_access"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ensureOfflineAccess(tc.input)
			assert.Equal(t, tc.expect, got)
		})
	}
}

// ── Token – non-interactive, no cached token → ErrTokenRequired ──────────────

func TestTokenSource_NonInteractive_NoCache_ReturnsErrTokenRequired(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	// Secrets provider returns not-found for any key lookup.
	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), gomock.Any()).
		Return("", errors.New("not found")).
		AnyTimes()

	cfg := minimalConfig()
	ts := NewTokenSource(cfg, mockSecrets, false /* non-interactive */, nil)

	_, err := ts.Token(context.Background())
	require.ErrorIs(t, err, ErrTokenRequired)
}

// ── Token – non-interactive, actionable tier-2 error surfaces as lastErr ─────

// When the secrets backend returns a non-not-found error (e.g. keyring locked),
// Token() must return that specific error rather than the generic ErrTokenRequired
// so the caller can distinguish a backend failure from a missing session.
func TestTokenSource_NonInteractive_BackendError_ReturnsLastErr(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	// "keyring is locked" does not match IsNotFoundError, so it is an actionable
	// error, not a cache miss. tier 1.5 silently ignores it; tier 2 surfaces it.
	backendErr := errors.New("keyring is locked")
	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), gomock.Any()).
		Return("", backendErr).
		AnyTimes()

	ts := NewTokenSource(minimalConfig(), mockSecrets, false /* non-interactive */, nil)
	_, err := ts.Token(context.Background())

	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTokenRequired),
		"backend error must surface as lastErr, not the generic ErrTokenRequired")
	assert.ErrorContains(t, err, "keyring is locked")
}

// ── Token – non-interactive, no secrets provider → error (not ErrTokenRequired) ──

// When secretsProvider is nil, tier 2 returns an actionable error rather than
// the generic ErrTokenRequired so the caller knows why the token could not be
// obtained (no secrets store configured).
func TestTokenSource_NonInteractive_NilSecrets_ReturnsError(t *testing.T) {
	t.Parallel()

	cfg := minimalConfig()
	ts := NewTokenSource(cfg, nil /* no secrets */, false, nil)

	_, err := ts.Token(context.Background())
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrTokenRequired),
		"nil secrets provider should return an actionable error, not the generic ErrTokenRequired")
}

// ── refreshTokenKey – uses CachedRefreshTokenRef when set ────────────────────

func TestTokenSource_RefreshTokenKey_UsesCached(t *testing.T) {
	t.Parallel()

	cfg := minimalConfig()
	cfg.OIDC.CachedRefreshTokenRef = "my-persisted-key"

	ts := NewTokenSource(cfg, nil, false, nil)
	assert.Equal(t, "my-persisted-key", ts.refreshTokenKey())
}

// ── refreshTokenKey – derives key when no cached ref ─────────────────────────

func TestTokenSource_RefreshTokenKey_DerivedWhenEmpty(t *testing.T) {
	t.Parallel()

	cfg := minimalConfig()
	ts := NewTokenSource(cfg, nil, false, nil)

	key := ts.refreshTokenKey()
	expected := DeriveSecretKey(cfg.GatewayURL, cfg.OIDC.Issuer)
	assert.Equal(t, expected, key)
}

// ── preemptiveTokenSource – shifts expiry back by preemptiveRefreshWindow ────

// staticTokenSource is a minimal oauth2.TokenSource for tests.
type staticTokenSource struct{ tok *oauth2.Token }

func (s *staticTokenSource) Token() (*oauth2.Token, error) { return s.tok, nil }

func TestPreemptiveTokenSource_ShiftsExpiry(t *testing.T) {
	t.Parallel()

	realExpiry := time.Now().Add(2 * time.Minute)
	inner := &staticTokenSource{tok: &oauth2.Token{
		AccessToken: "access",
		Expiry:      realExpiry,
	}}

	pts := &preemptiveTokenSource{inner: inner}
	tok, err := pts.Token()
	require.NoError(t, err)

	wantExpiry := realExpiry.Add(-preemptiveRefreshWindow)
	assert.WithinDuration(t, wantExpiry, tok.Expiry, time.Millisecond,
		"expiry must be shifted back by preemptiveRefreshWindow")
}

func TestPreemptiveTokenSource_ZeroExpiry_Unchanged(t *testing.T) {
	t.Parallel()

	inner := &staticTokenSource{tok: &oauth2.Token{
		AccessToken: "access",
		Expiry:      time.Time{}, // zero — no expiry info
	}}

	pts := &preemptiveTokenSource{inner: inner}
	tok, err := pts.Token()
	require.NoError(t, err)
	assert.True(t, tok.Expiry.IsZero(), "zero expiry must be passed through unchanged")
}

func TestPreemptiveTokenSource_PropagatesError(t *testing.T) {
	t.Parallel()

	inner := &errTokenSource{err: errors.New("refresh failed")}

	pts := &preemptiveTokenSource{inner: inner}
	_, err := pts.Token()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh failed")
}

type errTokenSource struct{ err error }

func (e *errTokenSource) Token() (*oauth2.Token, error) { return nil, e.err }

// ── Preemptive refresh: expiry is shifted back by 30 s ───────────────────────

func TestPreemptiveRefreshWindow_Value(t *testing.T) {
	t.Parallel()
	// The window must be exactly 30 s so token helpers and proxy workers
	// consistently treat tokens as expired before the gateway does.
	assert.Equal(t, 30*time.Second, preemptiveRefreshWindow)
}

func TestTokenSource_PreemptiveRefreshWindow_ExpiryShift(t *testing.T) {
	t.Parallel()

	// Verify the expiry shift arithmetic: a token expiring in 2 minutes should
	// have its effective expiry moved back by preemptiveRefreshWindow.
	realExpiry := time.Now().Add(2 * time.Minute)
	shifted := realExpiry.Add(-preemptiveRefreshWindow)

	assert.True(t, shifted.Before(realExpiry),
		"shifted expiry must be earlier than the real expiry")
	assert.InDelta(t,
		preemptiveRefreshWindow.Seconds(),
		realExpiry.Sub(shifted).Seconds(),
		0.001,
		"shift must equal preemptiveRefreshWindow",
	)
}

// ── tryRestoreFromCache – empty secret value treated as missing ───────────────

func TestTokenSource_TryRestoreFromCache_EmptySecret_ReturnsError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	// Provider returns success but empty string — treated as "no token".
	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), gomock.Any()).
		Return("", nil)

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	err := ts.tryRestoreFromCache(context.Background())
	require.Error(t, err, "empty refresh token must be treated as missing")
}

// ── tryRestoreFromCache – backend error is propagated, not swallowed ──────────

func TestTokenSource_TryRestoreFromCache_BackendError_Propagated(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	// Simulate a real backend failure (keyring locked, network error, etc.).
	backendErr := errors.New("keyring is locked")
	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), gomock.Any()).
		Return("", backendErr)

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	err := ts.tryRestoreFromCache(context.Background())

	require.Error(t, err)
	// Must surface the underlying backend error, not hide it as "no cached token".
	assert.Contains(t, err.Error(), "keyring is locked",
		"backend error must be propagated, not swallowed as a cache miss")
}

// ── tryRestoreFromCache – uses CachedRefreshTokenRef key for lookup ──────────

func TestTokenSource_TryRestoreFromCache_UsesPersistedKey(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	cfg := minimalConfig()
	cfg.OIDC.CachedRefreshTokenRef = "persisted-ref-key"

	// Must look up the persisted key, not a derived one.
	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), "persisted-ref-key").
		Return("", errors.New("not found"))

	ts := NewTokenSource(cfg, mockSecrets, false, nil)
	_ = ts.tryRestoreFromCache(context.Background())
}

// ── tryRestoreFromCache – GetSecret is called with the derived key ────────────

// TestTokenSource_TryRestoreFromCache_CallsGetSecretWithDerivedKey verifies that
// tier 2 looks up the refresh token using the derived key (no cached ref set).
// A short context deadline makes the subsequent OIDC discovery call fail
// immediately so the test never makes a real network connection.
func TestTokenSource_TryRestoreFromCache_CallsGetSecretWithDerivedKey(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	cfg := minimalConfig()
	expectedKey := DeriveSecretKey(cfg.GatewayURL, cfg.OIDC.Issuer)

	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), expectedKey).
		Return("stored-refresh-token", nil)

	// Cancel immediately after GetSecret so OIDC discovery aborts without
	// making a real network call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ts := NewTokenSource(cfg, mockSecrets, false, nil)
	err := ts.tryRestoreFromCache(ctx)

	// Error must come from the cancelled context, not from a missing token.
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "no cached refresh token",
		"error must be from OIDC discovery, not from missing token")
}

// ── makeTokenPersister – stores refresh token, calls updater, invalidates AT ──

func TestTokenSource_MakeTokenPersister_StoresTokenAndCallsUpdater(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	cfg := minimalConfig()
	const secretKey = "LLM_OAUTH_test"
	// Pin CachedRefreshTokenRef so refreshTokenKey() == secretKey.
	// accessTokenCacheKey() is then secretKey+"_AT", which must be the key
	// the persister invalidates — same root as the refresh token it just wrote.
	cfg.OIDC.CachedRefreshTokenRef = secretKey
	wantToken := "new-refresh-token"
	wantExpiry := time.Now().Add(time.Hour)

	// The persister must write the refresh token then invalidate the AT cache.
	atKey := secretKey + "_AT"
	gomock.InOrder(
		mockSecrets.EXPECT().
			SetSecret(gomock.Any(), secretKey, wantToken).
			Return(nil),
		mockSecrets.EXPECT().
			SetSecret(gomock.Any(), atKey, "").
			Return(nil),
	)

	var updaterKey string
	var updaterExpiry time.Time
	updater := func(key string, expiry time.Time) {
		updaterKey = key
		updaterExpiry = expiry
	}

	ts := NewTokenSource(cfg, mockSecrets, false, updater)
	persister := ts.makeTokenPersister(secretKey)

	require.NoError(t, persister(wantToken, wantExpiry))
	assert.Equal(t, secretKey, updaterKey, "updater must receive the secret key")
	assert.Equal(t, wantExpiry, updaterExpiry, "updater must receive the token expiry")
}

// ── makeTokenPersister – SetSecret failure is returned as error ───────────────

func TestTokenSource_MakeTokenPersister_SetSecretFailure(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	mockSecrets.EXPECT().
		SetSecret(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("keyring locked"))

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	persister := ts.makeTokenPersister("some-key")

	err := persister("token", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keyring locked")
}

// ── updateConfigTokenRef – calls the injected updater ────────────────────────

func TestTokenSource_UpdateConfigTokenRef_CallsUpdater(t *testing.T) {
	t.Parallel()

	var gotKey string
	var gotExpiry time.Time

	updater := func(key string, expiry time.Time) {
		gotKey = key
		gotExpiry = expiry
	}

	ts := NewTokenSource(minimalConfig(), nil, false, updater)
	wantExpiry := time.Now().Add(time.Hour)
	ts.updateConfigTokenRef("test-key", wantExpiry)

	assert.Equal(t, "test-key", gotKey)
	assert.Equal(t, wantExpiry, gotExpiry)
}

// ── updateConfigTokenRef – nil updater is a no-op ────────────────────────────

func TestTokenSource_UpdateConfigTokenRef_NilUpdater_NoOp(t *testing.T) {
	t.Parallel()

	ts := NewTokenSource(minimalConfig(), nil, false, nil)
	// Must not panic.
	assert.NotPanics(t, func() {
		ts.updateConfigTokenRef("key", time.Now())
	})
}

// ── tryAccessTokenCache ───────────────────────────────────────────────────────

func TestTokenSource_TryAccessTokenCache_NilProvider_ReturnsFalse(t *testing.T) {
	t.Parallel()

	ts := NewTokenSource(minimalConfig(), nil, false, nil)
	tok, found := ts.tryAccessTokenCache(context.Background())
	assert.False(t, found)
	assert.Empty(t, tok)
}

func TestTokenSource_TryAccessTokenCache_NotFound_ReturnsFalse(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), gomock.Any()).
		Return("", errors.New("not found"))

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	tok, found := ts.tryAccessTokenCache(context.Background())
	assert.False(t, found)
	assert.Empty(t, tok)
}

func TestTokenSource_TryAccessTokenCache_ValidToken_ReturnsToken(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	expiry := time.Now().Add(5 * time.Minute).UTC()
	cached := "access-token-value|" + expiry.Format(time.RFC3339)

	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), gomock.Any()).
		Return(cached, nil)

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	tok, found := ts.tryAccessTokenCache(context.Background())
	assert.True(t, found)
	assert.Equal(t, "access-token-value", tok)
}

func TestTokenSource_TryAccessTokenCache_ExpiredToken_ReturnsFalse(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	expiry := time.Now().Add(-1 * time.Minute).UTC()
	cached := "access-token-value|" + expiry.Format(time.RFC3339)

	mockSecrets.EXPECT().
		GetSecret(gomock.Any(), gomock.Any()).
		Return(cached, nil)

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	tok, found := ts.tryAccessTokenCache(context.Background())
	assert.False(t, found)
	assert.Empty(t, tok)
}

func TestTokenSource_TryAccessTokenCache_MalformedEntry_ReturnsFalse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{"no pipe", "just-a-token"},
		{"bad expiry", "token|not-a-date"},
		{"empty value", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)
			mockSecrets := secretsmocks.NewMockProvider(ctrl)

			mockSecrets.EXPECT().
				GetSecret(gomock.Any(), gomock.Any()).
				Return(tc.raw, nil).
				AnyTimes()

			ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
			tok, found := ts.tryAccessTokenCache(context.Background())
			assert.False(t, found)
			assert.Empty(t, tok)
		})
	}
}

// ── cacheAccessToken ──────────────────────────────────────────────────────────

func TestTokenSource_CacheAccessToken_StoresToken(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	expiry := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	expectedVal := "my-access-token|" + expiry.Format(time.RFC3339)

	mockSecrets.EXPECT().
		SetSecret(gomock.Any(), gomock.Any(), expectedVal).
		Return(nil)

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	ts.cacheAccessToken(context.Background(), "my-access-token", expiry)
}

func TestTokenSource_CacheAccessToken_NilProvider_NoOp(t *testing.T) {
	t.Parallel()

	ts := NewTokenSource(minimalConfig(), nil, false, nil)
	// Must not panic.
	assert.NotPanics(t, func() {
		ts.cacheAccessToken(context.Background(), "token", time.Now().Add(time.Hour))
	})
}

func TestTokenSource_CacheAccessToken_ZeroExpiry_NoOp(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)
	// SetSecret must NOT be called.

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	ts.cacheAccessToken(context.Background(), "token", time.Time{})
}

func TestTokenSource_CacheAccessToken_WriteFailure_DoesNotPanic(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mockSecrets := secretsmocks.NewMockProvider(ctrl)

	mockSecrets.EXPECT().
		SetSecret(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("keyring locked"))

	ts := NewTokenSource(minimalConfig(), mockSecrets, false, nil)
	assert.NotPanics(t, func() {
		ts.cacheAccessToken(context.Background(), "token", time.Now().Add(time.Hour))
	})
}

// ── withPreemptiveRefresh – composition regression ───────────────────────────

// countingTokenSource is a test helper that counts Token() invocations and
// delegates to a caller-supplied function so each call can return a distinct
// token. It is not safe for concurrent use; all calls come through the
// ReuseTokenSource mutex so no additional locking is needed.
type countingTokenSource struct {
	calls   int
	tokenFn func(call int) *oauth2.Token
}

func (c *countingTokenSource) Token() (*oauth2.Token, error) {
	c.calls++
	return c.tokenFn(c.calls), nil
}

// TestWithPreemptiveRefresh_ExactlyOneRefreshPerWindow is a regression test for
// the composition bug where an inner ReuseTokenSource inside the preemptive
// chain would return the same stale cached token on every call inside the
// preemptive window, causing the outer ReuseTokenSource to see an expired token
// on every successive call and re-enter the inner chain indefinitely.
//
// Correct behaviour: the first call inside the preemptive window triggers
// exactly one inner Token() call that returns a fresh long-lived token. The
// outer ReuseTokenSource caches the shifted expiry (which is now in the future)
// and serves all subsequent calls from cache — no further inner calls.
func TestWithPreemptiveRefresh_ExactlyOneRefreshPerWindow(t *testing.T) {
	t.Parallel()

	// Call 1 returns a short-lived token whose shifted expiry is already in the
	// past, so the outer ReuseTokenSource immediately re-enters the inner chain
	// on the very next call.
	// Call 2+ returns a long-lived token; after shifting its expiry is well in
	// the future, so the outer ReuseTokenSource can cache it and serve all
	// subsequent Token() calls without touching the inner source again.
	fake := &countingTokenSource{
		tokenFn: func(call int) *oauth2.Token {
			if call == 1 {
				return &oauth2.Token{
					AccessToken: "token-short",
					Expiry:      time.Now().Add(preemptiveRefreshWindow / 2),
				}
			}
			return &oauth2.Token{
				AccessToken: "token-fresh",
				Expiry:      time.Now().Add(2 * time.Minute),
			}
		},
	}

	src := withPreemptiveRefresh(fake)

	// First call: outer has no token → calls inner (fake call 1) → short token.
	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "token-short", tok.AccessToken)
	assert.Equal(t, 1, fake.calls)

	// Subsequent calls: the short token's shifted expiry is already past, so the
	// outer calls the inner once more (fake call 2), receives a long-lived token,
	// and caches its shifted expiry which is well in the future. All remaining
	// calls must be served from the outer cache — inner must not be called again.
	const iterations = 10
	for i := 0; i < iterations; i++ {
		tok, err = src.Token()
		require.NoError(t, err)
		assert.Equal(t, "token-fresh", tok.AccessToken,
			"all calls after the preemptive refresh must return the cached fresh token")
	}

	assert.Equal(t, 2, fake.calls,
		"inner source must be called exactly twice: once for the initial short-lived "+
			"token and once for the preemptive refresh; all remaining calls must be "+
			"served from the outer ReuseTokenSource cache")
}

// ── withPreemptiveRefreshFrom – pre-seeds outer cache with shifted initial token ──

// TestWithPreemptiveRefreshFrom_PreSeededToken verifies that withPreemptiveRefreshFrom
// serves the pre-seeded initial token without calling the inner source, and only
// calls the inner source after the shifted expiry passes.
func TestWithPreemptiveRefreshFrom_PreSeededToken(t *testing.T) {
	t.Parallel()

	fake := &countingTokenSource{
		tokenFn: func(_ int) *oauth2.Token {
			return &oauth2.Token{
				AccessToken: "refreshed-token",
				Expiry:      time.Now().Add(2 * time.Minute),
			}
		},
	}

	initial := &oauth2.Token{
		AccessToken: "initial-token",
		Expiry:      time.Now().Add(2 * time.Minute), // long-lived — shifted expiry is in the future
	}

	src := withPreemptiveRefreshFrom(initial, fake)

	// All calls within the shifted window must return the pre-seeded token
	// without touching the inner source.
	const iterations = 5
	for i := 0; i < iterations; i++ {
		tok, err := src.Token()
		require.NoError(t, err)
		assert.Equal(t, "initial-token", tok.AccessToken,
			"call %d: must return pre-seeded token without hitting inner source", i+1)
	}
	assert.Equal(t, 0, fake.calls, "inner source must not be called while initial token is valid")
}

func TestWithPreemptiveRefreshFrom_NilInitial_BehavesLikeWithPreemptiveRefresh(t *testing.T) {
	t.Parallel()

	fake := &countingTokenSource{
		tokenFn: func(_ int) *oauth2.Token {
			return &oauth2.Token{
				AccessToken: "inner-token",
				Expiry:      time.Now().Add(2 * time.Minute),
			}
		},
	}

	src := withPreemptiveRefreshFrom(nil, fake)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "inner-token", tok.AccessToken)
	assert.Equal(t, 1, fake.calls, "nil initial must trigger an inner call on first Token()")
}

// ── nonCachingRefresher – no-refresh-token guard ─────────────────────────────

func TestNonCachingRefresher_EmptyRefreshToken_ReturnsError(t *testing.T) {
	t.Parallel()

	cfg := &oauth2.Config{ClientID: "test"}
	ncr := newNonCachingRefresher(cfg, "" /* no refresh token */, "")

	_, err := ncr.Token()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no refresh token available")
}

// TestWithPreemptiveRefresh_CachingInnerSource_LoopRegression is a regression
// test for the bug where a caching inner source (one that returns the same valid
// token every call) caused the outer ReuseTokenSource to loop indefinitely inside
// the preemptive window.
//
// A caching source always returns the same token regardless of how many times
// Token() is called. When preemptiveTokenSource shifts its expiry back by 30 s,
// the outer ReuseTokenSource sees an already-expired token on the very next call
// and re-enters the chain — forever, with no actual IdP refresh.
//
// A non-caching inner source (nonCachingRefresher) fixes this: the first call
// inside the window returns a fresh token with a real-future expiry, which the
// outer cache can hold until the next window.
func TestWithPreemptiveRefresh_CachingInnerSource_LoopRegression(t *testing.T) {
	t.Parallel()

	// cachingSource simulates the old resourceTokenSource / oauth2.ReuseTokenSource
	// behaviour: always returns the same token with a fixed near-future expiry
	// (within the preemptive window), so preemptiveTokenSource always shifts it
	// to already-expired.
	staleExpiry := time.Now().Add(preemptiveRefreshWindow / 2) // inside the window
	cachingInner := &countingTokenSource{
		tokenFn: func(_ int) *oauth2.Token {
			return &oauth2.Token{
				AccessToken: "stale-token",
				Expiry:      staleExpiry, // fixed — never advances
			}
		},
	}

	src := withPreemptiveRefresh(cachingInner)

	// Drive the source several times. With the old caching inner source this
	// would loop forever (calls >> iterations). With the fix (non-caching inner
	// source that returns a fresh token each call), the outer cache eventually
	// settles once the inner returns a token whose shifted expiry is in the future.
	//
	// Here we simply verify that the loop terminates and the number of inner calls
	// is bounded — it should equal the number of Token() calls until the inner
	// source returns a long-lived token. Since our fake always returns the same
	// stale expiry, we expect exactly one call per outer Token() call (the outer
	// never caches) but NOT an infinite loop within a single Token() call.
	const iterations = 5
	for i := 0; i < iterations; i++ {
		tok, err := src.Token()
		require.NoError(t, err)
		assert.Equal(t, "stale-token", tok.AccessToken)
	}
	// Each outer Token() call results in exactly one inner call — never multiple
	// calls per single outer invocation (which would indicate the inner loop bug).
	assert.Equal(t, iterations, cachingInner.calls,
		"each outer Token() call must result in exactly one inner call; "+
			"more calls per invocation would indicate the infinite-loop regression")
}

// ── accessTokenCacheKey ───────────────────────────────────────────────────────

func TestTokenSource_AccessTokenCacheKey_HasATSuffix(t *testing.T) {
	t.Parallel()

	ts := NewTokenSource(minimalConfig(), nil, false, nil)
	key := ts.accessTokenCacheKey()
	assert.True(t, strings.HasSuffix(key, "_AT"),
		"access token cache key must end with _AT, got %q", key)
	assert.Contains(t, key, ts.refreshTokenKey(),
		"access token cache key must be derived from the refresh token key")
}
