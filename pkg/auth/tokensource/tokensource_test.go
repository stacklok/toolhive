// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokensource_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/auth/oauth"
	"github.com/stacklok/toolhive/pkg/auth/tokensource"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// ── helpers ───────────────────────────────────────────────────────────────────

const (
	testGatewayURL   = "https://llm.example.com"
	testIssuer       = "https://auth.example.com"
	testClientID     = "test-client"
	testKeyPrefix    = "TEST_OAUTH_"
	testRefreshToken = "stored-rt"

	// Markers planted in the fake token endpoint's error response. Everything a
	// token endpoint sends back is untrusted, so tests assert none of it reaches
	// a rendered message.
	testResponseBodySecret = "rt-should-not-leak"
	testErrorDescription   = "the refresh token is invalid or has expired"
)

var errTestFallback = errors.New("test: authentication required")

func minimalOpts(sp *secretsmocks.MockProvider) tokensource.Options {
	return tokensource.Options{
		OIDC: tokensource.OIDCParams{
			Issuer:   testIssuer,
			ClientID: testClientID,
		},
		SecretsProvider: sp,
		Interactive:     false,
		KeyProvider:     func() string { return tokensource.DeriveSecretKey(testKeyPrefix, testGatewayURL, testIssuer) },
		FallbackErr:     errTestFallback,
	}
}

// ── DeriveSecretKey ───────────────────────────────────────────────────────────

func TestDeriveSecretKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		prefix      string
		resourceURL string
		issuer      string
	}{
		{"deterministic", "P_", testGatewayURL, testIssuer},
		{"different prefix", "Q_", testGatewayURL, testIssuer},
		{"different url", "P_", "https://other.example.com", testIssuer},
	}

	key00 := tokensource.DeriveSecretKey("P_", testGatewayURL, testIssuer)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tokensource.DeriveSecretKey(tc.prefix, tc.resourceURL, tc.issuer)
			assert.True(t, strings.HasPrefix(got, tc.prefix), "key must start with prefix")
			if tc.prefix == "P_" && tc.resourceURL == testGatewayURL && tc.issuer == testIssuer {
				assert.Equal(t, key00, got, "same inputs must produce same key")
			} else {
				assert.NotEqual(t, key00, got, "different inputs must produce different keys")
			}
		})
	}
}

func TestDeriveSecretKey_NullByteIsolatesSegments(t *testing.T) {
	t.Parallel()
	// "ab"+"c" and "a"+"bc" must not collide.
	k1 := tokensource.DeriveSecretKey("P_", "ab", "c")
	k2 := tokensource.DeriveSecretKey("P_", "a", "bc")
	assert.NotEqual(t, k1, k2, "null byte separator must prevent segment conflation")
}

// ── EnsureOfflineAccess ───────────────────────────────────────────────────────

func TestEnsureOfflineAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  []string
		expect []string
	}{
		{"already present", []string{"openid", "offline_access"}, []string{"openid", "offline_access"}},
		{"not present", []string{"openid"}, []string{"openid", "offline_access"}},
		{"empty", []string{}, []string{"offline_access"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tokensource.EnsureOfflineAccess(tc.input)
			assert.Equal(t, tc.expect, got)
		})
	}
}

// ── non-interactive / no cache ────────────────────────────────────────────────

func TestToken_NonInteractive_NoCache_ReturnsFallbackErr(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	mock.EXPECT().GetSecret(gomock.Any(), gomock.Any()).
		Return("", errors.New("not found")).AnyTimes()

	ts := tokensource.New(minimalOpts(mock))
	_, err := ts.Token(context.Background())
	require.ErrorIs(t, err, errTestFallback)
}

func TestToken_NonInteractive_BackendError_SurfacesLastErr(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	backendErr := errors.New("keyring is locked")
	mock.EXPECT().GetSecret(gomock.Any(), gomock.Any()).
		Return("", backendErr).AnyTimes()

	ts := tokensource.New(minimalOpts(mock))
	_, err := ts.Token(context.Background())
	require.Error(t, err)
	assert.False(t, errors.Is(err, errTestFallback),
		"backend error must surface, not the generic fallback")
	assert.ErrorContains(t, err, "keyring is locked")
}

func TestToken_NonInteractive_NilSecrets_ReturnsActionableError(t *testing.T) {
	t.Parallel()

	opts := minimalOpts(nil)
	opts.SecretsProvider = nil
	ts := tokensource.New(opts)
	_, err := ts.Token(context.Background())
	require.Error(t, err)
	assert.False(t, errors.Is(err, errTestFallback),
		"nil secrets provider must return an actionable error, not the generic fallback")
}

// ── in-memory tier (tier 1) ───────────────────────────────────────────────────

// TestToken_InMemoryCache_ServesWithoutSecretsLookup verifies that after a token
// is obtained via the refresh-token cache, subsequent calls are served from the
// in-memory cache without hitting the token endpoint again.
func TestToken_InMemoryCache_ServesWithoutSecretsLookup(t *testing.T) {
	t.Parallel()

	var tokenEndpointCalls atomic.Int32
	srv := fakeOIDCServer(t, &tokenEndpointCalls, "cached-access-token",
		time.Now().Add(10*time.Minute), "rt-rotated")

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	// AT cache miss; refresh token present → tier 2 path.
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "", errors.New("not found")
			}
			return testRefreshToken, nil
		}).AnyTimes()
	mock.EXPECT().SetSecret(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	ts := tokensource.New(optsWithFakeOIDC(srv, mock))

	// First call restores from refresh token cache → hits token endpoint.
	tok1, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cached-access-token", tok1)
	calls1 := tokenEndpointCalls.Load()

	// Second call must be served from in-memory cache — no new token-endpoint calls.
	tok2, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, tok1, tok2)
	assert.Equal(t, calls1, tokenEndpointCalls.Load(),
		"second call must not hit token endpoint again")
}

// ── access-token cache (tier 1.5) ────────────────────────────────────────────

func TestToken_AccessTokenCache_ValidToken_Served(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)

	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	cachedAT := "cached-at|" + expiry

	// First GetSecret for the AT cache key → cached token.
	// Key ending in _AT is the access-token cache.
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return cachedAT, nil
			}
			return "", errors.New("not found")
		}).AnyTimes()

	ts := tokensource.New(minimalOpts(mock))
	tok, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "cached-at", tok)
}

func TestToken_AccessTokenCache_Expired_FallsThrough(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)

	expiry := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339) // expired
	cachedAT := "stale-at|" + expiry

	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return cachedAT, nil
			}
			return "", errors.New("not found")
		}).AnyTimes()

	ts := tokensource.New(minimalOpts(mock))
	_, err := ts.Token(context.Background())
	// Falls through to FallbackErr because expired AT and no refresh token.
	require.ErrorIs(t, err, errTestFallback)
}

// When the cached AT entry has no "|" separator (malformed), tryAccessTokenCache
// must treat it as a miss and fall through rather than panicking or returning
// a partial value.
func TestToken_AccessTokenCache_MalformedEntry_FallsThrough(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)

	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "no-pipe-separator", nil // malformed: no "|"
			}
			return "", errors.New("not found")
		}).AnyTimes()

	ts := tokensource.New(minimalOpts(mock))
	_, err := ts.Token(context.Background())
	require.ErrorIs(t, err, errTestFallback, "malformed AT cache must be treated as a miss")
}

// ── refresh-token cache (tier 2/3) ───────────────────────────────────────────

func TestToken_RefreshTokenCache_UsesPersistedToken(t *testing.T) {
	t.Parallel()

	srv := fakeOIDCServerSimple(t, "fresh-access-token", time.Now().Add(time.Hour), "rt-rotated")

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)

	// AT cache miss; refresh token present for the base key.
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "", errors.New("not found")
			}
			return "stored-refresh-token", nil
		}).AnyTimes()
	mock.EXPECT().SetSecret(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	opts := optsWithFakeOIDC(srv, mock)
	ts := tokensource.New(opts)

	tok, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "fresh-access-token", tok)
}

func TestToken_RefreshTokenCache_EmptyValue_TreatedAsMiss(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	// Provider returns success but empty string — treated as "no token".
	mock.EXPECT().GetSecret(gomock.Any(), gomock.Any()).Return("", nil).AnyTimes()

	ts := tokensource.New(minimalOpts(mock))
	_, err := ts.Token(context.Background())
	require.ErrorIs(t, err, errTestFallback)
}

// ── KeyProvider is honoured ───────────────────────────────────────────────────

func TestToken_KeyProvider_CachedRefUsed(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)

	const cachedKey = "my-persisted-key"
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.Eq(cachedKey+"_AT")).
		Return("", errors.New("not found")).AnyTimes()
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.Eq(cachedKey)).
		Return("", errors.New("not found")).AnyTimes()

	opts := minimalOpts(mock)
	opts.KeyProvider = func() string { return cachedKey }

	ts := tokensource.New(opts)
	_, _ = ts.Token(context.Background())
	// The mock expectations verify that cachedKey was used — no assertion needed here.
}

// ── ConfigPersister is called on refresh-token rotation ──────────────────────

// TestToken_ConfigPersister_CalledOnRotation verifies that when the OIDC provider
// rotates the refresh token during a Token() call, the ConfigPersister callback
// is invoked. This exercises the PersistingTokenSource → makeTokenPersister →
// persistConfig chain.
func TestToken_ConfigPersister_CalledOnRotation(t *testing.T) {
	t.Parallel()

	// Token server returns a "rotated" refresh token on each call.
	srv := fakeOIDCServerSimple(t, "at", time.Now().Add(time.Hour), "rotated-rt")

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	// AT cache miss; refresh token present.
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "", errors.New("not found")
			}
			return "initial-rt", nil
		}).AnyTimes()
	mock.EXPECT().SetSecret(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	var persisted bool
	opts := optsWithFakeOIDC(srv, mock)
	opts.ConfigPersister = func(_ string, _ time.Time) { persisted = true }

	ts := tokensource.New(opts)
	_, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.True(t, persisted, "ConfigPersister must be called when the refresh token is rotated")
}

// ── fakeTokenServer helpers ───────────────────────────────────────────────────

// fakeOIDCServer creates a minimal OIDC discovery + token server. It counts
// token-endpoint calls via callCount and returns at/rt on each call.
func fakeOIDCServer(t *testing.T, callCount *atomic.Int32, at string, expiry time.Time, rt string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()

	oidcHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"response_types_supported":["code"]}`,
			srv.URL, srv.URL+"/auth", srv.URL+"/token", srv.URL+"/jwks")
	}
	mux.HandleFunc("/.well-known/openid-configuration", oidcHandler)
	mux.HandleFunc("/.well-known/oauth-authorization-server", oidcHandler)
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		callCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":%d,"token_type":"Bearer"}`,
			at, rt, int(time.Until(expiry).Seconds()))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeOIDCServerSimple is like fakeOIDCServer but without call counting.
func fakeOIDCServerSimple(t *testing.T, at string, expiry time.Time, rt string) *httptest.Server {
	t.Helper()
	var n atomic.Int32
	return fakeOIDCServer(t, &n, at, expiry, rt)
}

// optsWithFakeOIDC builds Options pointing at the given fake OIDC server.
func optsWithFakeOIDC(srv *httptest.Server, sp *secretsmocks.MockProvider) tokensource.Options {
	return tokensource.Options{
		OIDC: tokensource.OIDCParams{
			Issuer:   srv.URL,
			ClientID: testClientID,
		},
		SecretsProvider: sp,
		Interactive:     false,
		KeyProvider:     func() string { return "test-key" },
		FallbackErr:     errTestFallback,
	}
}

// ── oauth2ConfigFrom sanity ───────────────────────────────────────────────────

// TestEnsureOfflineAccess_DoesNotMutateInput ensures the input slice is not
// modified when offline_access is appended.
func TestEnsureOfflineAccess_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	input := []string{"openid"}
	got := tokensource.EnsureOfflineAccess(input)
	assert.Len(t, input, 1, "input slice must not be mutated")
	assert.Len(t, got, 2)
}

// TestNew_DefaultFallbackErr verifies the default error message when no
// FallbackErr is set.
func TestNew_DefaultFallbackErr(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	mock.EXPECT().GetSecret(gomock.Any(), gomock.Any()).Return("", errors.New("not found")).AnyTimes()

	opts := minimalOpts(mock)
	opts.FallbackErr = nil // let New() set the default
	ts := tokensource.New(opts)
	_, err := ts.Token(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication required")
}

// TestNew_NilKeyProvider_Panics verifies that New panics early when
// KeyProvider is nil rather than producing a hard-to-diagnose nil-deref inside
// Token().
func TestNew_NilKeyProvider_Panics(t *testing.T) {
	t.Parallel()

	opts := minimalOpts(nil)
	opts.KeyProvider = nil
	assert.Panics(t, func() { tokensource.New(opts) })
}

// ── cacheAccessToken: skips when expiry is zero ──────────────────────────────

// When the token endpoint omits expires_in (zero Expiry), cacheAccessToken must
// not write to the AT cache — there is no expiry to store.
func TestToken_CacheAccessToken_ZeroExpiry_Skipped(t *testing.T) {
	t.Parallel()

	var atCacheWrites int
	var srv *httptest.Server
	mux := http.NewServeMux()
	oidcHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q}`,
			srv.URL, srv.URL+"/auth", srv.URL+"/token", srv.URL+"/jwks")
	}
	mux.HandleFunc("/.well-known/openid-configuration", oidcHandler)
	mux.HandleFunc("/.well-known/oauth-authorization-server", oidcHandler)
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// expires_in omitted → zero Expiry → cacheAccessToken must skip.
		_, _ = fmt.Fprintf(w, `{"access_token":"tok","refresh_token":"rt","token_type":"Bearer"}`)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "", errors.New("not found")
			}
			return testRefreshToken, nil
		}).AnyTimes()
	mock.EXPECT().
		SetSecret(gomock.Any(), gomock.AssignableToTypeOf(""), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key, val string) error {
			// Count only non-empty AT writes — empty writes are invalidations from
			// makeTokenPersister, not cacheAccessToken writes.
			if strings.HasSuffix(key, "_AT") && val != "" {
				atCacheWrites++
			}
			return nil
		}).AnyTimes()

	opts := optsWithFakeOIDC(srv, mock)
	ts := tokensource.New(opts)
	tok, err := ts.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok", tok)
	assert.Zero(t, atCacheWrites, "cacheAccessToken must not write to AT cache when expiry is zero")
}

// ── cacheAccessToken: SetSecret failure degrades silently ────────────────────

// When the secrets provider fails to write the AT cache, cacheAccessToken must
// suppress the error (logged at debug level) and return the token normally.
func TestToken_CacheAccessToken_SetSecretFails_DegradesSilently(t *testing.T) {
	t.Parallel()

	srv := fakeOIDCServerSimple(t, "access-tok", time.Now().Add(time.Hour), "rt-new")

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)

	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "", errors.New("not found")
			}
			return testRefreshToken, nil
		}).AnyTimes()
	// SetSecret fails for the _AT key — cacheAccessToken must not propagate the error.
	mock.EXPECT().
		SetSecret(gomock.Any(), gomock.AssignableToTypeOf(""), gomock.AssignableToTypeOf("")).
		Return(errors.New("keyring write failed")).AnyTimes()

	opts := optsWithFakeOIDC(srv, mock)
	ts := tokensource.New(opts)
	tok, err := ts.Token(context.Background())
	// Token is still returned despite the write failure — graceful degradation.
	require.NoError(t, err)
	assert.Equal(t, "access-tok", tok)
}

// ── buildOAuth2Config: OIDC discovery failure propagates ─────────────────────

// When OIDC discovery fails (bad issuer), tryRestoreFromCache must surface the
// error rather than returning a generic cache-miss or fallback error.
func TestToken_OIDCDiscoveryFails_PropagatesError(t *testing.T) {
	t.Parallel()

	// Server that always returns 500 — OIDC well-known endpoints will fail.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "", errors.New("not found")
			}
			return testRefreshToken, nil
		}).AnyTimes()

	opts := minimalOpts(mock)
	opts.OIDC.Issuer = srv.URL
	ts := tokensource.New(opts)

	_, err := ts.Token(context.Background())
	require.Error(t, err)
	// OIDC failure must propagate as lastErr, not the generic FallbackErr.
	assert.False(t, errors.Is(err, errTestFallback), "OIDC discovery failure must not collapse to FallbackErr")
}

// ── performBrowserFlow: OIDC discovery failure in interactive mode ────────────

// When interactive mode is enabled but OIDC discovery fails, Token() must
// return the discovery error wrapped as "OIDC browser flow failed", not the
// generic FallbackErr.
func TestToken_Interactive_OIDCDiscoveryFails_ReturnsError(t *testing.T) {
	t.Parallel()

	// Server that always returns 500 — OIDC well-known endpoints will fail.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	opts := tokensource.Options{
		OIDC:        tokensource.OIDCParams{Issuer: srv.URL, ClientID: testClientID},
		Interactive: true,
		KeyProvider: func() string { return "test-key" },
		FallbackErr: errTestFallback,
	}
	ts := tokensource.New(opts)

	_, err := ts.Token(context.Background())
	require.Error(t, err)
	assert.False(t, errors.Is(err, errTestFallback),
		"browser flow OIDC failure must not collapse to FallbackErr")
	assert.Contains(t, err.Error(), "OIDC browser flow failed")
}

// ── SkipBrowser is forwarded to the OAuth flow ────────────────────────────────

// TestToken_Interactive_SkipBrowser_ForwardedToFlow verifies that the SkipBrowser
// option reaches flow.Start unchanged. The OAuth flow itself is stubbed via the
// startFlow seam so the assertion holds without opening a browser or binding a
// callback listener; the rest of performBrowserFlow runs against the fake OIDC
// token endpoint.
//
//nolint:paralleltest // overrides the package-global startFlow seam; must not race other tests
func TestToken_Interactive_SkipBrowser_ForwardedToFlow(t *testing.T) {
	cases := []struct {
		name        string
		skipBrowser bool
	}{
		{name: "skip browser", skipBrowser: true},
		{name: "open browser", skipBrowser: false},
	}
	for _, tc := range cases {
		//nolint:paralleltest // shares the package-global startFlow seam with the parent
		t.Run(tc.name, func(t *testing.T) {
			expiry := time.Now().Add(time.Hour)
			srv := fakeOIDCServerSimple(t, "at-skip", expiry, "rt-skip")

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)
			mock := secretsmocks.NewMockProvider(ctrl)
			mock.EXPECT().GetSecret(gomock.Any(), gomock.Any()).Return("", errors.New("not found")).AnyTimes()
			mock.EXPECT().SetSecret(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

			var (
				called  bool
				gotSkip bool
			)
			restore := tokensource.SetStartFlowForTest(
				func(_ context.Context, _ *oauth.Flow, skipBrowser bool) (*oauth.TokenResult, error) {
					called = true
					gotSkip = skipBrowser
					return &oauth.TokenResult{
						AccessToken:  "at-skip",
						RefreshToken: "rt-skip",
						Expiry:       expiry,
						TokenType:    "Bearer",
					}, nil
				},
			)
			t.Cleanup(restore)

			opts := optsWithFakeOIDC(srv, mock)
			opts.Interactive = true
			opts.SkipBrowser = tc.skipBrowser
			ts := tokensource.New(opts)

			tok, err := ts.Token(context.Background())
			require.NoError(t, err)
			assert.True(t, called, "interactive flow must invoke the OAuth flow starter")
			assert.Equal(t, "at-skip", tok, "the access token from the flow must be returned")
			assert.Equal(t, tc.skipBrowser, gotSkip, "SkipBrowser option must be forwarded to flow.Start verbatim")
		})
	}
}

// ── A rejected stored credential reports as FallbackErr ──────────────────────

// fakeOIDCServerRejecting serves discovery normally but answers every token
// exchange with an RFC 6749 error response, the shape an IdP returns once a
// refresh token has expired, been revoked, or been rotated out from under the
// caller. status and errorCode control the response.
func fakeOIDCServerRejecting(t *testing.T, status int, errorCode string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()

	oidcHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"response_types_supported":["code"]}`,
			srv.URL, srv.URL+"/auth", srv.URL+"/token", srv.URL+"/jwks")
	}
	mux.HandleFunc("/.well-known/openid-configuration", oidcHandler)
	mux.HandleFunc("/.well-known/oauth-authorization-server", oidcHandler)
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if errorCode == "" {
			// A non-spec-compliant body, as a WAF or reverse proxy would return.
			_, _ = fmt.Fprint(w, `<html>blocked</html>`)
			return
		}
		_, _ = fmt.Fprintf(w,
			`{"error":%q,"error_description":%q,"secret":%q}`,
			errorCode, testErrorDescription, testResponseBodySecret)
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// storedRefreshTokenOnly wires a secrets mock that misses the access-token cache
// and returns a stored refresh token for the base key.
func storedRefreshTokenOnly(t *testing.T) *secretsmocks.MockProvider {
	t.Helper()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)
	mock := secretsmocks.NewMockProvider(ctrl)
	mock.EXPECT().
		GetSecret(gomock.Any(), gomock.AssignableToTypeOf("")).
		DoAndReturn(func(_ context.Context, key string) (string, error) {
			if strings.HasSuffix(key, "_AT") {
				return "", errors.New("not found")
			}
			return testRefreshToken, nil
		}).AnyTimes()
	mock.EXPECT().SetSecret(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	return mock
}

// A permanent token-endpoint verdict means the stored credential is dead and
// only an interactive login can replace it. Callers keyed on FallbackErr to
// render their re-login remediation must therefore see FallbackErr, not the raw
// OAuth error, which reads like a transient provider fault.
func TestToken_NonInteractive_RejectedGrant_ReportsFallbackErr(t *testing.T) {
	t.Parallel()

	srv := fakeOIDCServerRejecting(t, http.StatusBadRequest, "invalid_grant")
	opts := optsWithFakeOIDC(srv, storedRefreshTokenOnly(t))

	_, err := tokensource.New(opts).Token(context.Background())

	require.Error(t, err)
	require.ErrorIs(t, err, errTestFallback,
		"a rejected stored credential must report as FallbackErr so callers render the re-login remediation")
}

// The cause stays reachable underneath the sentinel so diagnostics can still
// identify which verdict the IdP rendered, even though the rendered message
// deliberately says nothing about it.
func TestToken_NonInteractive_RejectedGrant_KeepsCauseReachable(t *testing.T) {
	t.Parallel()

	srv := fakeOIDCServerRejecting(t, http.StatusBadRequest, "invalid_grant")
	opts := optsWithFakeOIDC(srv, storedRefreshTokenOnly(t))

	_, err := tokensource.New(opts).Token(context.Background())

	require.Error(t, err)
	retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err)
	require.True(t, ok, "the underlying *oauth2.RetrieveError must stay reachable via errors.As")
	assert.Equal(t, "invalid_grant", retrieveErr.ErrorCode)
}

// Everything the token endpoint sends back is untrusted: an
// *oauth2.RetrieveError's own Error() embeds the raw body, which can echo back
// bearer material, and the parsed 'error' and 'error_description' fields are
// arbitrary server-chosen strings. The rendered message must therefore
// interpolate nothing but the caller's own sentinel.
func TestToken_NonInteractive_RejectedGrant_MessageCarriesNoServerText(t *testing.T) {
	t.Parallel()

	srv := fakeOIDCServerRejecting(t, http.StatusBadRequest, "invalid_grant")
	opts := optsWithFakeOIDC(srv, storedRefreshTokenOnly(t))

	_, err := tokensource.New(opts).Token(context.Background())

	require.Error(t, err)
	assert.Equal(t,
		errTestFallback.Error()+" (the identity provider rejected the stored credential)",
		err.Error(),
		"the message must be the sentinel plus fixed text, with nothing from the token endpoint")
	assert.NotContains(t, err.Error(), testResponseBodySecret,
		"the raw token-endpoint response body must never reach the rendered message")
	assert.NotContains(t, err.Error(), testErrorDescription,
		"the server-supplied error_description must never reach the rendered message")
}

// invalid_client, unauthorized_client, and invalid_scope are permanent, but they
// indict the client registration or the requested scopes rather than the stored
// refresh token. A fresh login reproduces them unchanged, so reporting them as
// the re-login sentinel would send the user in a circle and hide the code that
// names the real problem.
func TestToken_NonInteractive_ClientVerdict_SurfacesVerbatim(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"invalid_client", "unauthorized_client", "invalid_scope"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()

			srv := fakeOIDCServerRejecting(t, http.StatusUnauthorized, code)
			opts := optsWithFakeOIDC(srv, storedRefreshTokenOnly(t))

			_, err := tokensource.New(opts).Token(context.Background())

			require.Error(t, err)
			assert.False(t, errors.Is(err, errTestFallback),
				"a verdict on the client, not the credential, must not report as re-login required")
			retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err)
			require.True(t, ok, "the OAuth error must still surface so the operator sees the code")
			assert.Equal(t, code, retrieveErr.ErrorCode)
		})
	}
}

// The RFC 6749 'error' field is attacker-influenceable text from whatever
// answered the token endpoint. Only the literal invalid_grant builds the
// sentinel error, so a hostile code cannot reach that rendered message at all —
// there is no interpolation path for it to travel down.
func TestToken_NonInteractive_HostileErrorCode_NeverReachesSentinel(t *testing.T) {
	t.Parallel()

	hostile := "invalid_grant\r\nAuthorization: Bearer " + testResponseBodySecret

	srv := fakeOIDCServerRejecting(t, http.StatusBadRequest, hostile)
	opts := optsWithFakeOIDC(srv, storedRefreshTokenOnly(t))

	_, err := tokensource.New(opts).Token(context.Background())

	require.Error(t, err)
	assert.False(t, errors.Is(err, errTestFallback),
		"only the literal invalid_grant may build the sentinel error")
	assert.NotContains(t, err.Error(), "the identity provider rejected the stored credential",
		"untrusted text must never be interpolated into the sentinel message")
}

// A 5xx is the IdP having a bad day, not a verdict on the credential. It must
// surface verbatim so the caller can retry rather than telling the user to log
// in again.
func TestToken_NonInteractive_TransientVerdict_SurfacesVerbatim(t *testing.T) {
	t.Parallel()

	srv := fakeOIDCServerRejecting(t, http.StatusServiceUnavailable, "temporarily_unavailable")
	opts := optsWithFakeOIDC(srv, storedRefreshTokenOnly(t))

	_, err := tokensource.New(opts).Token(context.Background())

	require.Error(t, err)
	assert.False(t, errors.Is(err, errTestFallback),
		"a 5xx from the token endpoint is transient and must not be reported as a dead credential")
}

// A 4xx with no parseable OAuth error code is infrastructure (a WAF or CDN
// page), not an OAuth verdict, so it must not be reported as a dead credential
// either.
func TestToken_NonInteractive_UnparseableRejection_SurfacesVerbatim(t *testing.T) {
	t.Parallel()

	srv := fakeOIDCServerRejecting(t, http.StatusForbidden, "")
	opts := optsWithFakeOIDC(srv, storedRefreshTokenOnly(t))

	_, err := tokensource.New(opts).Token(context.Background())

	require.Error(t, err)
	assert.False(t, errors.Is(err, errTestFallback),
		"a 4xx without an OAuth error code is upstream infrastructure, not a credential verdict")
}
