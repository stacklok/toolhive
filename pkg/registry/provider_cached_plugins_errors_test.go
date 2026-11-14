// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	thvregistry "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry/api"
)

// newPluginsStatusServer returns an httptest.Server whose plugins list endpoint
// responds with statusCode when status.Load() is non-zero, and a normal page of
// plugins otherwise. The servers list endpoint always answers an empty list so
// the constructor's validation probe succeeds.
func newPluginsStatusServer(t *testing.T, plugins []*thvregistry.Plugin, status *atomic.Int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var callCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/x/dev.toolhive/plugins", func(w http.ResponseWriter, _ *http.Request) {
		callCount.Add(1)
		if s := status.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		payload := pluginsListPayload{Plugins: plugins}
		payload.Metadata.Count = len(plugins)
		require.NoError(t, json.NewEncoder(w).Encode(payload))
	})
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{"next_cursor":""}}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &callCount
}

// TestCachedProvider_PluginsAuthErrorWithWarmCache verifies that a 401 from the
// registry is propagated even when a warm cache could otherwise mask it. A
// revoked token must not silently serve stale data.
func TestCachedProvider_PluginsAuthErrorWithWarmCache(t *testing.T) {
	t.Parallel()

	plugins := []*thvregistry.Plugin{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
	}
	var status atomic.Int32 // 0 = healthy
	srv, callCount := newPluginsStatusServer(t, plugins, &status)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	// Warm the cache.
	got, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, got, 1)
	firstCount := callCount.Load()

	// Flip the registry to 401 and expire the cache.
	status.Store(http.StatusUnauthorized)
	provider.pluginsMu.Lock()
	provider.pluginsTime = provider.pluginsTime.Add(-defaultCacheTTL - 1)
	provider.pluginsMu.Unlock()

	got2, err := provider.ListAvailablePlugins()
	require.Error(t, err, "401 must propagate, not be masked by stale cache")
	require.Nil(t, got2)
	require.True(t,
		errors.Is(err, api.ErrRegistryUnauthorized),
		"expected errors.Is(err, ErrRegistryUnauthorized); got: %v", err)
	require.Greater(t, callCount.Load(), firstCount, "expired cache should trigger a re-fetch attempt")
}

// TestCachedProvider_PluginsTransient500WithWarmCache verifies that a transient
// 500 with a warm cache degrades to stale data (the auth path is the exception,
// not the rule).
func TestCachedProvider_PluginsTransient500WithWarmCache(t *testing.T) {
	t.Parallel()

	plugins := []*thvregistry.Plugin{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
	}
	var status atomic.Int32
	srv, callCount := newPluginsStatusServer(t, plugins, &status)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	// Warm the cache.
	got, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, got, 1)

	// Flip to 500 and expire the cache.
	status.Store(http.StatusInternalServerError)
	provider.pluginsMu.Lock()
	provider.pluginsTime = provider.pluginsTime.Add(-defaultCacheTTL - 1)
	provider.pluginsMu.Unlock()

	got2, err := provider.ListAvailablePlugins()
	require.NoError(t, err, "transient 500 with warm cache should serve stale data")
	require.Len(t, got2, 1)
	require.Equal(t, "code-reviewer", got2[0].Name)
	_ = callCount // re-fetch attempt made (not asserting exact count here)
}

// TestCachedProvider_PluginsTransient500WithColdCache verifies that a transient
// 500 with no cache returns the error rather than (nil, nil), so the v0.1
// registry route does not answer 200 [] on a real failure.
func TestCachedProvider_PluginsTransient500WithColdCache(t *testing.T) {
	t.Parallel()

	var status atomic.Int32
	status.Store(http.StatusInternalServerError)
	srv, _ := newPluginsStatusServer(t, nil, &status)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	got, err := provider.ListAvailablePlugins()
	require.Error(t, err, "transient 500 with cold cache must return the error, not nil,nil")
	require.Nil(t, got)
}

// TestCachedProvider_ForceRefreshInvalidatesPluginsCache verifies that
// ForceRefresh invalidates the plugins cache so the next ListAvailablePlugins
// re-fetches from the API instead of serving the pre-refresh cached value.
func TestCachedProvider_ForceRefreshInvalidatesPluginsCache(t *testing.T) {
	t.Parallel()

	plugins := []*thvregistry.Plugin{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
	}
	srv, callCount := newPluginsStatusServer(t, plugins, &atomic.Int32{})

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	// Prime the plugins cache.
	got, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, got, 1)
	afterFirst := callCount.Load()

	// A second call within TTL must be served from cache (no re-fetch).
	got2, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Equal(t, got, got2)
	require.Equal(t, afterFirst, callCount.Load(), "second call should be served from cache")

	// ForceRefresh must invalidate the plugins cache so the next call refetches.
	require.NoError(t, provider.ForceRefresh())

	got3, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, got3, 1)
	require.Greater(t, callCount.Load(), afterFirst, "ForceRefresh should cause the next ListAvailablePlugins to refetch")
}
