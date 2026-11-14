// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	thvregistry "github.com/stacklok/toolhive-core/registry/types"
)

// pluginsListPayload is the wire format served by the test registry for the
// plugins list endpoint. Field names mirror pluginsListResponse in
// pkg/registry/api/plugins_client.go so the client decodes them cleanly.
type pluginsListPayload struct {
	Plugins  []*thvregistry.Plugin `json:"plugins"`
	Metadata struct {
		Count      int    `json:"count"`
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

// newPluginsTestServer returns an httptest.Server that serves the plugins list
// endpoint. The server endpoint is /v0.1/x/dev.toolhive/plugins. If fail is
// non-nil and returns true, the server responds 500 to exercise stale-cache
// fallback. callCount tracks how many times the list endpoint was hit.
func newPluginsTestServer(t *testing.T, plugins []*thvregistry.Plugin, fail *atomic.Bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var callCount atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/v0.1/x/dev.toolhive/plugins", func(w http.ResponseWriter, _ *http.Request) {
		callCount.Add(1)
		if fail != nil && fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		payload := pluginsListPayload{Plugins: plugins}
		payload.Metadata.Count = len(plugins)
		require.NoError(t, json.NewEncoder(w).Encode(payload))
	})
	// The constructor's validation probe hits the servers list endpoint; serve
	// an empty server list so the probe succeeds and construction completes.
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{"next_cursor":""}}`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &callCount
}

// TestCachedProvider_PluginsCacheHit verifies that a second ListAvailablePlugins
// call within the TTL is served from cache without hitting the registry.
func TestCachedProvider_PluginsCacheHit(t *testing.T) {
	t.Parallel()

	plugins := []*thvregistry.Plugin{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
		{Namespace: "io.github.acme", Name: "doc-generator", Version: "0.2.0"},
	}
	srv, callCount := newPluginsTestServer(t, plugins, nil)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	// First call: cache miss, fetches from API.
	got, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "code-reviewer", got[0].Name)
	require.Equal(t, int32(1), callCount.Load())

	// Second call: cache hit, must not hit the API again.
	got2, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Equal(t, got, got2)
	require.Equal(t, int32(1), callCount.Load(), "second call should be served from cache")
}

// TestCachedProvider_PluginsCacheMissFetch verifies that an empty cache
// triggers a fetch from the registry API.
func TestCachedProvider_PluginsCacheMissFetch(t *testing.T) {
	t.Parallel()

	plugins := []*thvregistry.Plugin{
		{Namespace: "io.github.stacklok", Name: "linter", Version: "1.0.0"},
	}
	srv, callCount := newPluginsTestServer(t, plugins, nil)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	got, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "linter", got[0].Name)
	require.Equal(t, int32(1), callCount.Load())
}

// TestCachedProvider_PluginsStaleOnFailure verifies that when the registry
// returns a transient error (500) on a cache miss, ListAvailablePlugins
// returns nil (plugins are optional) rather than propagating the error —
// mirroring the skills behavior in provider_cached.go.
func TestCachedProvider_PluginsStaleOnFailure(t *testing.T) {
	t.Parallel()

	plugins := []*thvregistry.Plugin{
		{Namespace: "io.github.stacklok", Name: "code-reviewer", Version: "1.0.0"},
	}
	var fail atomic.Bool
	srv, callCount := newPluginsTestServer(t, plugins, &fail)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	// First call: succeeds and populates the cache.
	got, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, got, 1)
	firstCount := callCount.Load()

	// Force the registry to fail, then expire the cache so the next call must
	// re-fetch. The stale cache should be served instead of the error.
	fail.Store(true)
	provider.pluginsMu.Lock()
	provider.pluginsTime = provider.pluginsTime.Add(-defaultCacheTTL - 1)
	provider.pluginsMu.Unlock()

	got2, err := provider.ListAvailablePlugins()
	require.NoError(t, err, "transient failure should return stale cache, not error")
	require.Len(t, got2, 1, "stale cache should still hold the previously fetched plugin")
	require.Equal(t, "code-reviewer", got2[0].Name)
	require.Greater(t, callCount.Load(), firstCount, "expired cache should trigger a re-fetch attempt")
}

// TestCachedProvider_PluginsStaleOnFailureEmptyCache verifies that a transient
// failure with NO cached data returns the error (not nil,nil) so the v0.1
// registry route surfaces a real failure instead of an empty 200 [].
func TestCachedProvider_PluginsStaleOnFailureEmptyCache(t *testing.T) {
	t.Parallel()

	var fail atomic.Bool
	fail.Store(true)
	srv, _ := newPluginsTestServer(t, nil, &fail)

	provider, err := NewCachedAPIRegistryProvider(srv.URL, true, false, nil)
	require.NoError(t, err)

	got, err := provider.ListAvailablePlugins()
	require.Error(t, err, "transient failure with empty cache should return the error, not nil,nil")
	require.Nil(t, got)
}
