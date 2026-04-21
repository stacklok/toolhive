// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/registry/api"
	"github.com/stacklok/toolhive/pkg/registry/auth"
)

// TestCachedProvider_AuthErrorNotMaskedByStaleCache reproduces a bug in
// refreshCache() at provider_cached.go:116.
//
// Scenario: a user has an existing cache populated from a previous successful
// fetch. Later, their registry returns 401 (token revoked, credentials
// rejected server-side). The CLI should surface the auth error, because the
// user's authorization state may have changed since the cache was populated.
//
// Current behavior: refreshCache() treats ANY error from the upstream fetch
// as a signal to fall back to stale cached data, including authentication
// errors. The CLI silently prints stale registry contents with no error.
//
// This test covers the 401/403 branch (server-side rejection).
func TestCachedProvider_AuthErrorNotMaskedByStaleCache(t *testing.T) {
	t.Parallel()

	var returnUnauthorized atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if returnUnauthorized.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{"next_cursor":""}}`))
	}))
	t.Cleanup(srv.Close)

	// allowPrivateIp=true because httptest binds to 127.0.0.1
	// usePersistent=false exercises only the in-memory cache path
	// tokenSource=nil so the constructor's validation probe runs against the healthy server
	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err, "constructor should succeed while server is healthy")

	// Populate the in-memory cache with a successful fetch.
	_, err = provider.ListServers()
	require.NoError(t, err, "first fetch should succeed and populate cache")

	// Flip the server to 401 — equivalent to the registry now rejecting
	// the user's credentials server-side.
	returnUnauthorized.Store(true)

	// Force a refresh — mirrors what happens when the in-memory cache TTL
	// expires (default 1h) and the next `thv registry list` triggers a
	// fresh fetch. The upstream API call will fail with 401.
	err = provider.ForceRefresh()

	require.Error(t, err,
		"expected auth error to propagate on refresh; got nil — "+
			"stale cache is masking the auth failure (bug)")
	require.True(t,
		errors.Is(err, api.ErrRegistryUnauthorized) ||
			errors.Is(err, auth.ErrRegistryAuthRequired),
		"expected ErrRegistryUnauthorized or ErrRegistryAuthRequired; got: %v", err)
}

// failingTokenSource is a test double for auth.TokenSource.
// It returns an empty token (no Authorization header added) when fail is
// false, and returns an error wrapped like oauthTokenSource.Token() would
// when its browser OAuth flow fails, when fail is true.
type failingTokenSource struct {
	fail atomic.Bool
}

// Token mirrors the exact error-wrapping oauthTokenSource.Token() applies
// when its browser flow times out / is cancelled. See:
//   - pkg/registry/auth/oauth_token_source.go:61-67 (outer wrap)
//   - pkg/registry/auth/oauth_token_source.go:110-113 (inner wrap)
//
// The point of matching the wrapping is to verify that the eventual
// refreshCache() fallback behavior does not depend on the specific error
// type — any error causes stale cache to be served.
func (s *failingTokenSource) Token(_ context.Context) (string, error) {
	if s.fail.Load() {
		inner := fmt.Errorf("oauth flow start failed: %w",
			errors.New("authorization timed out waiting for browser callback"))
		return "", fmt.Errorf("oauth flow failed: %w", inner)
	}
	// No auth header needed — the httptest server does not validate credentials.
	return "", nil
}

// TestCachedProvider_OAuthFlowFailureNotMaskedByStaleCache reproduces the
// same masking bug via the OAuth-browser-flow-failure code path — the one
// actually described in the bug report:
//
//	"thv registry list sent me a URL to OAuth. I did nothing. After a minute
//	 it exited the OAuth flow and went to another OAuth flow. I also did
//	 nothing. In another minute it exited that OAuth flow and then it
//	 showed me everything in the registry."
//
// This path differs from the 401/403 path because the error returned by
// oauthTokenSource.Token() when the browser flow times out is a generic
// wrapped error — it does NOT match errors.Is(err, auth.ErrRegistryAuthRequired)
// or errors.Is(err, api.ErrRegistryUnauthorized). It is propagated through:
//
//	oauthTokenSource.Token  ("oauth flow failed: ...")
//	  -> auth.Transport.RoundTrip  ("failed to get auth token: ...")
//	    -> api.Client.fetchServersPage  ("failed to fetch servers: ...")
//	      -> APIRegistryProvider.GetRegistry  (wrapped into *UnavailableError)
//	        -> refreshCache  (swallowed; stale cache returned with nil err)
//
// A sentinel-check fix like
//
//	if errors.Is(err, auth.ErrRegistryAuthRequired) ||
//	   errors.Is(err, api.ErrRegistryUnauthorized) { return nil, err }
//
// will NOT catch this path — the OAuth browser-flow error carries neither
// sentinel. A correct fix has to classify token-acquisition failures as
// auth errors before they are flattened into UnavailableError.
func TestCachedProvider_OAuthFlowFailureNotMaskedByStaleCache(t *testing.T) {
	t.Parallel()

	// Server always serves a valid empty list. All failures in this test
	// come from the token source, not the server — simulating an OAuth
	// flow that never completes while the registry itself is reachable.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{"next_cursor":""}}`))
	}))
	t.Cleanup(srv.Close)

	ts := &failingTokenSource{}

	// Non-nil tokenSource causes NewAPIRegistryProvider to skip its
	// construction-time validation probe (provider_api.go:57) — matching
	// the real OAuth-configured code path where the probe is skipped
	// because a browser flow cannot complete within 10 seconds.
	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, ts)
	require.NoError(t, err, "constructor should succeed (probe skipped when tokenSource != nil)")

	// Populate the in-memory cache with a successful fetch.
	// ts.fail == false, so Token() returns ("", nil) and the request passes
	// through the auth transport without an Authorization header.
	_, err = provider.ListServers()
	require.NoError(t, err, "first fetch should succeed and populate cache")

	// Simulate the OAuth browser flow failing / timing out on the next
	// token acquisition.
	ts.fail.Store(true)

	err = provider.ForceRefresh()

	// DESIRED: an OAuth-flow failure must not be hidden by stale cache.
	// CURRENT BUG: refreshCache returns cached data with nil error.
	require.Error(t, err,
		"expected OAuth flow failure to propagate on refresh; got nil — "+
			"stale cache is masking the OAuth failure (bug, "+
			"matches user-reported scenario)")
}
