// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	thvregistry "github.com/stacklok/toolhive-core/registry/types"
)

// apiRegistryTestServer serves the MCP Registry API endpoints needed to exercise
// APIRegistryProvider plugin methods. It serves the servers list (for the
// constructor's validation probe) plus the ToolHive plugins extension endpoints.
func apiRegistryTestServer(t *testing.T, plugins []*thvregistry.Plugin) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	// Servers list endpoint — required by the constructor's validation probe.
	mux.HandleFunc("/v0.1/servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{"next_cursor":""}}`))
	})

	// List plugins (auto-pagination) — required by ListAvailablePlugins.
	mux.HandleFunc("/v0.1/x/dev.toolhive/plugins", func(w http.ResponseWriter, r *http.Request) {
		// If there's a search query param, this is the SearchPlugins endpoint.
		if r.URL.Query().Get("search") != "" {
			searchHandler(w, r, plugins)
			return
		}
		listHandler(w, r, plugins)
	})

	// Get plugin by namespace/name — required by GetPlugin.
	mux.HandleFunc("/v0.1/x/dev.toolhive/plugins/", func(w http.ResponseWriter, _ *http.Request) {
		// The path is /v0.1/x/dev.toolhive/plugins/{namespace}/{name}
		// We serve a fixed plugin for any request (the handler extracts the
		// namespace/name from the path but we just return the first plugin
		// that matches, or 404).
		w.Header().Set("Content-Type", "application/json")

		// Simple match: if plugins has at least one, return the first one.
		// The GetPlugin implementation extracts namespace/name from the
		// path; we don't need to validate them here — we just need to
		// return a valid plugin payload so the caller can assert it.
		if len(plugins) > 0 {
			require.NoError(t, json.NewEncoder(w).Encode(plugins[0]))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("plugin not found"))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func searchHandler(w http.ResponseWriter, _ *http.Request, plugins []*thvregistry.Plugin) {
	w.Header().Set("Content-Type", "application/json")
	payload := pluginsListPayload{Plugins: plugins}
	payload.Metadata.Count = len(plugins)
	_ = json.NewEncoder(w).Encode(payload)
}

func listHandler(w http.ResponseWriter, _ *http.Request, plugins []*thvregistry.Plugin) {
	w.Header().Set("Content-Type", "application/json")
	payload := pluginsListPayload{Plugins: plugins}
	payload.Metadata.Count = len(plugins)
	_ = json.NewEncoder(w).Encode(payload)
}

func TestAPIRegistryProvider_ListAvailablePlugins(t *testing.T) {
	t.Parallel()

	plugins := []*thvregistry.Plugin{
		{
			Namespace:   "io.github.stacklok",
			Name:        "code-reviewer",
			Description: "Reviews code for bugs",
			Version:     "1.0.0",
		},
		{
			Namespace:   "io.github.acme",
			Name:        "doc-generator",
			Description: "Generates documentation",
			Version:     "0.2.0",
		},
	}

	srv := apiRegistryTestServer(t, plugins)
	provider, err := NewAPIRegistryProvider(srv.URL, true, nil)
	require.NoError(t, err)
	require.NotNil(t, provider)

	got, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, "code-reviewer", got[0].Name)
	assert.Equal(t, "io.github.stacklok", got[0].Namespace)
	assert.Equal(t, "Reviews code for bugs", got[0].Description)
	assert.Equal(t, "1.0.0", got[0].Version)

	assert.Equal(t, "doc-generator", got[1].Name)
	assert.Equal(t, "io.github.acme", got[1].Namespace)
	assert.Equal(t, "0.2.0", got[1].Version)
}

func TestAPIRegistryProvider_GetPlugin(t *testing.T) {
	t.Parallel()

	plugins := []*thvregistry.Plugin{
		{
			Namespace:   "io.github.stacklok",
			Name:        "code-reviewer",
			Description: "Reviews code for bugs",
			Version:     "1.0.0",
		},
	}

	srv := apiRegistryTestServer(t, plugins)
	provider, err := NewAPIRegistryProvider(srv.URL, true, nil)
	require.NoError(t, err)

	got, err := provider.GetPlugin("io.github.stacklok", "code-reviewer")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "code-reviewer", got.Name)
	assert.Equal(t, "io.github.stacklok", got.Namespace)
	assert.Equal(t, "1.0.0", got.Version)
}

func TestAPIRegistryProvider_SearchPlugins(t *testing.T) {
	t.Parallel()

	plugins := []*thvregistry.Plugin{
		{
			Namespace:   "io.github.stacklok",
			Name:        "code-reviewer",
			Description: "Reviews code for bugs",
			Version:     "1.0.0",
		},
	}

	srv := apiRegistryTestServer(t, plugins)
	provider, err := NewAPIRegistryProvider(srv.URL, true, nil)
	require.NoError(t, err)

	got, err := provider.SearchPlugins("reviewer")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "code-reviewer", got[0].Name)
	assert.Equal(t, "Reviews code for bugs", got[0].Description)
}

func TestAPIRegistryProvider_SearchPlugins_Empty(t *testing.T) {
	t.Parallel()

	srv := apiRegistryTestServer(t, nil)
	provider, err := NewAPIRegistryProvider(srv.URL, true, nil)
	require.NoError(t, err)

	got, err := provider.SearchPlugins("nonexistent")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestAPIRegistryProvider_PluginsNilClient(t *testing.T) {
	t.Parallel()

	// Construct an APIRegistryProvider with a nil pluginsClient (simulating
	// the case where NewPluginsClient fails, e.g. due to HTTP transport setup).
	// The constructor does best-effort plugin client creation, so a nil client
	// is possible. The methods must return nil, nil rather than panicking.
	p := &APIRegistryProvider{
		pluginsClient: nil,
	}

	plugins, err := p.ListAvailablePlugins()
	require.NoError(t, err)
	assert.Nil(t, plugins)

	got, err := p.GetPlugin("ns", "name")
	require.NoError(t, err)
	assert.Nil(t, got)

	results, err := p.SearchPlugins("query")
	require.NoError(t, err)
	assert.Nil(t, results)
}
