// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry/api"
	regmocks "github.com/stacklok/toolhive/pkg/registry/mocks"
)

func TestFilterPluginsV01(t *testing.T) {
	t.Parallel()

	plugins := []types.Plugin{
		{Namespace: "stacklok", Name: "code-review", Description: "Reviews code for issues"},
		{Namespace: "stacklok", Name: "commit", Description: "Creates git commits"},
		{Namespace: "other", Name: "weather", Description: "Weather data"},
	}

	tests := []struct {
		query     string
		wantCount int
	}{
		{"code", 1},
		{"CODE", 1},        // case-insensitive
		{"Code-Review", 1}, // mixed case
		{"stacklok", 2},
		{"weather", 1},
		{"commits", 1},
		{"nonexistent", 0},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			t.Parallel()
			result := filterPluginsV01(plugins, tt.query)
			assert.Len(t, result, tt.wantCount)
		})
	}
}

func TestFilterPluginsV01_EmptyResult_NotNull(t *testing.T) {
	t.Parallel()

	plugins := []types.Plugin{
		{Namespace: "stacklok", Name: "test", Description: "A test plugin"},
	}

	result := filterPluginsV01(plugins, "nonexistent")
	assert.NotNil(t, result, "Filter result should be empty slice, not nil")
	assert.Empty(t, result)

	// Verify JSON encoding produces [] not null
	data, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Equal(t, "[]", string(data))
}

func TestRegistryV01Router_ListPlugins(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/default/v0.1/x/dev.toolhive/plugins")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var body pluginsV01Response
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	// Should return plugins from the embedded catalog (may be empty in test env)
	assert.NotNil(t, body.Plugins)
	assert.GreaterOrEqual(t, body.Metadata.Total, 0)
}

func TestRegistryV01Router_GetPlugin_NotFound(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/default/v0.1/x/dev.toolhive/plugins/nonexistent/noplugin")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json",
		"Error responses should be JSON")

	var body registryErrorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "not_found", body.Code)
}

func TestRegistryV01Router_ListPlugins_PaginationBeyondResults(t *testing.T) {
	t.Parallel()

	handler := RegistryV01Router()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/default/v0.1/x/dev.toolhive/plugins?page=999&limit=10")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body pluginsV01Response
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Empty(t, body.Plugins, "Page beyond results should return empty plugins")
	assert.Equal(t, 999, body.Metadata.Page)
	assert.GreaterOrEqual(t, body.Metadata.Total, 0)
}

// TestRegistryV01Router_GetPlugin_RegistryAuthError confirms that an upstream
// *api.RegistryHTTPError with a 401/403 status is mapped to the structured
// registry_auth_required 503 response (writeRegistryAuthRequiredError), so
// desktop clients can branch on the code. The skills handler has the same
// mapping but lacks this test; plugins get explicit coverage here.
//
// The test wires getPluginV01WithProvider directly against a mock provider via
// a dedicated chi route, avoiding the process-wide default provider singleton
// so it stays deterministic and parallel-safe.
func TestRegistryV01Router_GetPlugin_RegistryAuthError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
	}{
		{"unauthorized maps to registry_auth_required", http.StatusUnauthorized},
		{"forbidden maps to registry_auth_required", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)

			provider := regmocks.NewMockProvider(ctrl)
			provider.EXPECT().GetPlugin("io.test", "auth-plugin").
				Return(nil, &api.RegistryHTTPError{StatusCode: tt.statusCode, URL: "https://registry.test/plugin"})

			r := chi.NewRouter()
			r.Get("/x/dev.toolhive/plugins/{namespace}/{pluginName}",
				func(w http.ResponseWriter, req *http.Request) {
					getPluginV01WithProvider(w, req, provider)
				})
			srv := httptest.NewServer(r)
			t.Cleanup(srv.Close)

			resp, err := http.Get(srv.URL + "/x/dev.toolhive/plugins/io.test/auth-plugin")
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

			var body registryErrorResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			assert.Equal(t, RegistryAuthRequiredCode, body.Code)
			assert.Contains(t, body.Message, "Registry authentication required")
		})
	}
}
