// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Internal tests for the preemptive refresh chain. These live in the same
// package so they can access unexported types and constants.
package tokensource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// fakeSecretsProvider is a minimal secrets.Provider for internal tests.
type fakeSecretsProvider struct{ val string }

func (f *fakeSecretsProvider) GetSecret(_ context.Context, _ string) (string, error) {
	if f.val == "" {
		return "", fmt.Errorf("not found")
	}
	return f.val, nil
}
func (*fakeSecretsProvider) SetSecret(_ context.Context, _, _ string) error { return nil }
func (*fakeSecretsProvider) DeleteSecret(_ context.Context, _ string) error { return nil }
func (*fakeSecretsProvider) ListSecrets(_ context.Context) ([]secrets.SecretDescription, error) {
	return nil, nil
}
func (*fakeSecretsProvider) DeleteSecrets(_ context.Context, _ []string) error { return nil }
func (*fakeSecretsProvider) Cleanup() error                                    { return nil }
func (*fakeSecretsProvider) Capabilities() secrets.ProviderCapabilities {
	return secrets.ProviderCapabilities{}
}

// countingTokenSource counts Token() invocations and delegates to tokenFn.
// Not safe for concurrent use — caller guarantees serialisation.
type countingTokenSource struct {
	calls   int
	tokenFn func(call int) *oauth2.Token
}

func (c *countingTokenSource) Token() (*oauth2.Token, error) {
	c.calls++
	return c.tokenFn(c.calls), nil
}

type errTokenSource struct{ err error }

func (e *errTokenSource) Token() (*oauth2.Token, error) { return nil, e.err }

// ── preemptiveTokenSource ─────────────────────────────────────────────────────

func TestPreemptiveTokenSource_ShiftsExpiry(t *testing.T) {
	t.Parallel()

	realExpiry := time.Now().Add(2 * time.Minute)
	inner := &staticTokenSource{tok: &oauth2.Token{AccessToken: "access", Expiry: realExpiry}}

	pts := &preemptiveTokenSource{inner: inner}
	tok, err := pts.Token()
	require.NoError(t, err)

	wantExpiry := realExpiry.Add(-preemptiveRefreshWindow)
	assert.WithinDuration(t, wantExpiry, tok.Expiry, time.Millisecond)
}

func TestPreemptiveTokenSource_ZeroExpiry_Unchanged(t *testing.T) {
	t.Parallel()

	inner := &staticTokenSource{tok: &oauth2.Token{AccessToken: "access", Expiry: time.Time{}}}
	pts := &preemptiveTokenSource{inner: inner}
	tok, err := pts.Token()
	require.NoError(t, err)
	assert.True(t, tok.Expiry.IsZero())
}

func TestPreemptiveTokenSource_PropagatesError(t *testing.T) {
	t.Parallel()

	pts := &preemptiveTokenSource{inner: &errTokenSource{err: fmt.Errorf("refresh failed")}}
	_, err := pts.Token()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refresh failed")
}

func TestPreemptiveRefreshWindow_Is30s(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 30*time.Second, preemptiveRefreshWindow,
		"preemptiveRefreshWindow must be 30 s — token helpers and proxy workers depend on this value")
}

// ── withPreemptiveRefresh ─────────────────────────────────────────────────────

// TestWithPreemptiveRefresh_ExactlyOneRefreshPerWindow is a regression test for
// the composition bug where an inner ReuseTokenSource inside the preemptive
// chain would return the same stale cached token on every call inside the
// preemptive window, causing the outer ReuseTokenSource to thrash indefinitely.
//
// Correct behaviour: the first call inside the preemptive window triggers exactly
// one inner Token() call returning a fresh long-lived token. The outer
// ReuseTokenSource then serves all subsequent calls from cache — zero further inner calls.
func TestWithPreemptiveRefresh_ExactlyOneRefreshPerWindow(t *testing.T) {
	t.Parallel()

	fake := &countingTokenSource{
		tokenFn: func(call int) *oauth2.Token {
			if call == 1 {
				return &oauth2.Token{
					AccessToken: "token-short",
					Expiry:      time.Now().Add(preemptiveRefreshWindow / 2),
				}
			}
			return &oauth2.Token{AccessToken: "token-fresh", Expiry: time.Now().Add(2 * time.Minute)}
		},
	}

	src := withPreemptiveRefresh(fake)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "token-short", tok.AccessToken)
	assert.Equal(t, 1, fake.calls)

	const iterations = 10
	for i := range iterations {
		tok, err = src.Token()
		require.NoError(t, err)
		assert.Equal(t, "token-fresh", tok.AccessToken, "iteration %d", i)
	}
	assert.Equal(t, 2, fake.calls,
		"inner source must be called exactly twice: once for the initial short token, "+
			"once for the preemptive refresh")
}

// TestWithPreemptiveRefresh_NonCachingRefresher_NoResource verifies the fix
// using a real NonCachingRefresher to avoid an internal-caching thrash.
func TestWithPreemptiveRefresh_NonCachingRefresher_NoResource(t *testing.T) {
	t.Parallel()

	var serverCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := int(serverCalls.Add(1))
		expiresIn := 120
		if call == 1 {
			expiresIn = 15 // inside the preemptive window
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","token_type":"Bearer","expires_in":%d}`,
			call, expiresIn)
	}))
	t.Cleanup(srv.Close)

	cfg := &oauth2.Config{
		ClientID: "test-client",
		Endpoint: oauth2.Endpoint{TokenURL: srv.URL, AuthStyle: oauth2.AuthStyleInParams},
	}
	ncr := oauth.NewNonCachingRefresher(cfg, "refresh-token", "")
	src := withPreemptiveRefresh(ncr)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "token-1", tok.AccessToken)
	assert.Equal(t, int32(1), serverCalls.Load())

	const iterations = 10
	for i := range iterations {
		tok, err = src.Token()
		require.NoError(t, err)
		assert.Equal(t, "token-2", tok.AccessToken, "iteration %d", i)
	}
	assert.Equal(t, int32(2), serverCalls.Load())
}

// TestWithPreemptiveRefresh_CachingInnerSource_Thrashes documents the failure
// mode when a caching inner source is used — it thrashes on every outer call.
func TestWithPreemptiveRefresh_CachingInnerSource_Thrashes(t *testing.T) {
	t.Parallel()

	staleExpiry := time.Now().Add(preemptiveRefreshWindow / 2)
	cachingInner := &countingTokenSource{
		tokenFn: func(_ int) *oauth2.Token {
			return &oauth2.Token{AccessToken: "stale", Expiry: staleExpiry}
		},
	}

	src := withPreemptiveRefresh(cachingInner)

	const iterations = 10
	for range iterations {
		_, err := src.Token()
		require.NoError(t, err)
	}
	assert.Equal(t, iterations, cachingInner.calls,
		"caching inner source causes thrashing — one inner call per outer Token() call")
}

// ── withPreemptiveRefreshFrom ─────────────────────────────────────────────────

func TestWithPreemptiveRefreshFrom_PreSeededToken(t *testing.T) {
	t.Parallel()

	fake := &countingTokenSource{
		tokenFn: func(_ int) *oauth2.Token {
			return &oauth2.Token{AccessToken: "refreshed", Expiry: time.Now().Add(2 * time.Minute)}
		},
	}
	initial := &oauth2.Token{AccessToken: "initial", Expiry: time.Now().Add(2 * time.Minute)}

	src := withPreemptiveRefreshFrom(initial, fake)

	for i := range 5 {
		tok, err := src.Token()
		require.NoError(t, err)
		assert.Equal(t, "initial", tok.AccessToken, "iteration %d", i)
	}
	assert.Equal(t, 0, fake.calls, "inner source must not be called while initial token is valid")
}

func TestWithPreemptiveRefreshFrom_ShortLivedInitial_NoSeed(t *testing.T) {
	t.Parallel()

	fake := &countingTokenSource{
		tokenFn: func(_ int) *oauth2.Token {
			return &oauth2.Token{AccessToken: "inner-token", Expiry: time.Now().Add(2 * time.Minute)}
		},
	}
	initial := &oauth2.Token{
		AccessToken: "short-lived",
		Expiry:      time.Now().Add(preemptiveRefreshWindow / 2),
	}

	src := withPreemptiveRefreshFrom(initial, fake)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "inner-token", tok.AccessToken)
	assert.Equal(t, 1, fake.calls)
}

func TestWithPreemptiveRefreshFrom_NilInitial(t *testing.T) {
	t.Parallel()

	fake := &countingTokenSource{
		tokenFn: func(_ int) *oauth2.Token {
			return &oauth2.Token{AccessToken: "inner-token", Expiry: time.Now().Add(2 * time.Minute)}
		},
	}

	src := withPreemptiveRefreshFrom(nil, fake)

	tok, err := src.Token()
	require.NoError(t, err)
	assert.Equal(t, "inner-token", tok.AccessToken)
	assert.Equal(t, 1, fake.calls)
}

// staticTokenSource is a minimal oauth2.TokenSource used in internal tests.
type staticTokenSource struct{ tok *oauth2.Token }

func (s *staticTokenSource) Token() (*oauth2.Token, error) { return s.tok, nil }

// nonCachingRefresher sanity check for an absent refresh token.
func TestNonCachingRefresher_EmptyRefreshToken_ReturnsError(t *testing.T) {
	t.Parallel()

	ncr := oauth.NewNonCachingRefresher(&oauth2.Config{ClientID: "test"}, "", "")
	_, err := ncr.Token()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no refresh token available")
}

// TestNonCachingRefresher_StandardPath_NoResourceParam verifies the standard OAuth 2.0
// refresh path: no "resource" parameter, returned access token is used, and the
// previous refresh token is preserved when the IdP does not rotate one.
func TestNonCachingRefresher_StandardPath_NoResourceParam(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		assert.NotContains(t, string(raw), "resource=",
			"standard path must not include a resource parameter")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(w, `{"access_token":"new-at","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)

	cfg := &oauth2.Config{
		ClientID: "test-client",
		Endpoint: oauth2.Endpoint{TokenURL: srv.URL, AuthStyle: oauth2.AuthStyleInParams},
		Scopes:   []string{"openid"},
	}
	ncr := oauth.NewNonCachingRefresher(cfg, "my-refresh-token", "")

	tok, err := ncr.Token()
	require.NoError(t, err)
	assert.Equal(t, "new-at", tok.AccessToken)
	assert.Equal(t, "my-refresh-token", tok.RefreshToken,
		"refresh token must be preserved when IdP does not rotate it")
}

// ── tryInMemoryToken: expired in-memory token (internal path) ────────────────

// When the in-memory source returns a token whose Valid() is false but no error
// (e.g. already-expired), tryInMemoryToken must clear the source and return
// errCacheMiss so the next tier is tried.
func TestTryInMemoryToken_ExpiredToken_ReturnsCacheMiss(t *testing.T) {
	t.Parallel()

	expiredToken := &oauth2.Token{
		AccessToken: "stale",
		Expiry:      time.Now().Add(-time.Minute),
	}
	ts := &OAuthTokenSource{
		opts:        Options{KeyProvider: func() string { return "k" }, FallbackErr: errors.New("fallback")},
		tokenSource: &staticTokenSource{tok: expiredToken},
	}

	tok, err := ts.tryInMemoryToken()
	assert.Empty(t, tok)
	assert.ErrorIs(t, err, errCacheMiss)
	assert.Nil(t, ts.tokenSource, "expired in-memory source must be cleared")
}

// ── tryCachedToken: token endpoint error propagates (internal path) ──────────

// When tryRestoreFromCache succeeds (refresh token found) but the token endpoint
// returns an error during the exchange, tryCachedToken propagates that error.
func TestTryCachedToken_TokenEndpointError_PropagatesError(t *testing.T) {
	t.Parallel()

	// Serve OIDC discovery so buildOAuth2Config succeeds, but fail the token exchange.
	var srv *httptest.Server
	mux := http.NewServeMux()
	oidcDoc := func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}
	mux.HandleFunc("/.well-known/openid-configuration", oidcDoc)
	mux.HandleFunc("/.well-known/oauth-authorization-server", oidcDoc)
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ts := &OAuthTokenSource{
		opts: Options{
			OIDC:            OIDCParams{Issuer: srv.URL, ClientID: "c"},
			KeyProvider:     func() string { return "k" },
			FallbackErr:     errors.New("fallback"),
			SecretsProvider: &fakeSecretsProvider{val: "stored-refresh-token"},
		},
	}

	tok, err := ts.tryCachedToken(context.Background())
	assert.Empty(t, tok)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errCacheMiss)
	assert.Nil(t, ts.tokenSource, "tokenSource must be cleared on error")
}

// ── tryInMemoryToken: inner source returns a real error ───────────────────────

// When t.tokenSource.Token() itself returns an error (rather than just an
// expired/invalid token), tryInMemoryToken must clear the source and propagate
// the error so the caller knows a real problem occurred.
func TestTryInMemoryToken_InnerError_PropagatesError(t *testing.T) {
	t.Parallel()

	innerErr := fmt.Errorf("token endpoint unavailable")
	ts := &OAuthTokenSource{
		opts:        Options{KeyProvider: func() string { return "k" }, FallbackErr: errors.New("fallback")},
		tokenSource: &errTokenSource{err: innerErr},
	}

	tok, err := ts.tryInMemoryToken()
	assert.Empty(t, tok)
	require.ErrorIs(t, err, innerErr)
	assert.Nil(t, ts.tokenSource, "erroring in-memory source must be cleared")
}

// TestToken_InMemoryError_SetsLastErr verifies that a real error from the
// in-memory tier (tier 1) is surfaced as lastErr in non-interactive mode
// rather than being silently replaced by FallbackErr.
//
// Set-up: tokenSource is pre-seeded with a source that always errors. The
// secrets provider returns "not found" for all keys, so tier 1.5 and tier 2
// both miss cleanly — lastErr from tier 1 is the only non-cacheMiss error.
func TestToken_InMemoryError_SetsLastErr(t *testing.T) {
	t.Parallel()

	innerErr := fmt.Errorf("token endpoint unavailable")
	ts := &OAuthTokenSource{
		opts: Options{
			KeyProvider:     func() string { return "k" },
			FallbackErr:     errors.New("fallback"),
			SecretsProvider: &fakeSecretsProvider{val: ""}, // "not found" for all keys
		},
		tokenSource: &errTokenSource{err: innerErr},
	}

	_, err := ts.Token(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, innerErr, "in-memory tier error must be surfaced as lastErr, not the fallback")
}
