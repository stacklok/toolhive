// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"
	"github.com/modelcontextprotocol/registry/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
	regauth "github.com/stacklok/toolhive/pkg/registry/auth"
)

func CreateTestConfigProvider(t *testing.T, cfg *config.Config) (config.Provider, func()) {
	t.Helper()

	// Create a temporary directory for the test
	tempDir := t.TempDir()

	// Create the config directory structure
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	// Set up the config file path
	configPath := filepath.Join(configDir, "config.yaml")

	// Create a path-based config provider
	provider := config.NewPathProvider(configPath)

	// Write the config file if one is provided
	if cfg != nil {
		err = provider.UpdateConfig(func(c *config.Config) error { *c = *cfg; return nil })
		require.NoError(t, err)
	}

	return provider, func() {
		// Cleanup is handled by t.TempDir()
	}
}

// TestRegistryAPI_GetEndpoint_UnavailableUpstream tests that GET endpoints return
// 503 with a structured JSON response when the upstream registry API is unreachable
// or returns an unexpected error (e.g. 404 because the URL path is wrong).
//
//nolint:paralleltest // Uses global registry provider singleton
func TestRegistryAPI_GetEndpoint_UnavailableUpstream(t *testing.T) {
	// Mock server that returns 404 (simulates a wrong registry API URL)
	notFoundServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "404 page not found", http.StatusNotFound)
	}))
	defer notFoundServer.Close()

	// Configure registry to point at the mock 404 server
	cfg := &config.Config{
		RegistryApiUrl:         notFoundServer.URL,
		AllowPrivateRegistryIp: true,
	}
	configProvider, cleanup := CreateTestConfigProvider(t, cfg)
	defer cleanup()

	registry.ResetDefaultProvider()
	t.Cleanup(registry.ResetDefaultProvider)

	routes := &RegistryRoutes{
		configProvider: configProvider,
		configService:  registry.NewConfiguratorWithProvider(configProvider),
		serveMode:      true,
	}

	endpoints := []struct {
		name      string
		method    string
		path      string
		handler   http.HandlerFunc
		urlParams map[string]string
	}{
		{
			name:    "listRegistries",
			method:  http.MethodGet,
			path:    "/",
			handler: routes.listRegistries,
		},
		{
			name:      "getRegistry",
			method:    http.MethodGet,
			path:      "/default",
			handler:   routes.getRegistry,
			urlParams: map[string]string{"name": "default"},
		},
		{
			name:      "listServers",
			method:    http.MethodGet,
			path:      "/default/servers",
			handler:   routes.listServers,
			urlParams: map[string]string{"name": "default"},
		},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			registry.ResetDefaultProvider()

			req := httptest.NewRequest(ep.method, ep.path, nil)
			if ep.urlParams != nil {
				rctx := chi.NewRouteContext()
				for k, v := range ep.urlParams {
					rctx.URLParams.Add(k, v)
				}
				req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			}

			w := httptest.NewRecorder()
			ep.handler(w, req)

			assert.Equal(t, http.StatusServiceUnavailable, w.Code,
				"Expected 503 Service Unavailable for unreachable upstream registry")

			var body registryErrorResponse
			err := json.NewDecoder(w.Body).Decode(&body)
			require.NoError(t, err, "Response should be valid JSON")
			assert.Equal(t, RegistryUnavailableCode, body.Code,
				"Response code should be registry_unavailable")
			assert.Contains(t, body.Message, "unavailable",
				"Response message should indicate unavailability")
			assert.Contains(t, w.Header().Get("Content-Type"), "application/json",
				"Response Content-Type should be application/json")
		})
	}
}

// TestRegistryAPI_GetEndpoint_LegacyFormat tests that GET endpoints return 502
// with a structured "registry_legacy_format" code when the configured custom
// registry URL serves data in the legacy ToolHive format. 502 is correct per
// RFC 9110 §15.6.3: thv serve acts as a gateway to the upstream registry and
// the upstream returned a response we cannot process.
//
//nolint:paralleltest // Uses global registry provider singleton
func TestRegistryAPI_GetEndpoint_LegacyFormat(t *testing.T) {
	// Mock server that returns a valid HTTP 200 but with legacy ToolHive
	// registry JSON (top-level "servers" instead of nested "data.servers").
	// validateConnectivity() must detect this and reject the provider with a
	// *LegacyFormatError.
	legacyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"version": "1.0.0",
			"servers": {"example": {"image": "example:latest"}}
		}`))
	}))
	defer legacyServer.Close()

	// Configure registry to point at the mock legacy-format server via the
	// remote URL provider path (RegistryUrl, not RegistryApiUrl).
	cfg := &config.Config{
		RegistryUrl:            legacyServer.URL,
		AllowPrivateRegistryIp: true,
	}
	configProvider, cleanup := CreateTestConfigProvider(t, cfg)
	defer cleanup()

	registry.ResetDefaultProvider()
	t.Cleanup(registry.ResetDefaultProvider)

	routes := &RegistryRoutes{
		configProvider: configProvider,
		configService:  registry.NewConfiguratorWithProvider(configProvider),
		serveMode:      true,
	}

	endpoints := []struct {
		name      string
		method    string
		path      string
		handler   http.HandlerFunc
		urlParams map[string]string
	}{
		{
			name:    "listRegistries",
			method:  http.MethodGet,
			path:    "/",
			handler: routes.listRegistries,
		},
		{
			name:      "getRegistry",
			method:    http.MethodGet,
			path:      "/default",
			handler:   routes.getRegistry,
			urlParams: map[string]string{"name": "default"},
		},
		{
			name:      "listServers",
			method:    http.MethodGet,
			path:      "/default/servers",
			handler:   routes.listServers,
			urlParams: map[string]string{"name": "default"},
		},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			registry.ResetDefaultProvider()

			req := httptest.NewRequest(ep.method, ep.path, nil)
			if ep.urlParams != nil {
				rctx := chi.NewRouteContext()
				for k, v := range ep.urlParams {
					rctx.URLParams.Add(k, v)
				}
				req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			}

			w := httptest.NewRecorder()
			ep.handler(w, req)

			assert.Equal(t, http.StatusBadGateway, w.Code,
				"Expected 502 Bad Gateway when registry is in legacy format")

			var body registryErrorResponse
			err := json.NewDecoder(w.Body).Decode(&body)
			require.NoError(t, err, "Response should be valid JSON")
			assert.Equal(t, RegistryLegacyFormatCode, body.Code,
				"Response code should be registry_legacy_format")
			// The body must carry the actionable migration hint so CLI users
			// and desktop clients both have something to act on.
			assert.Contains(t, body.Message, "legacy ToolHive format",
				"Response message should mention the legacy format")
			assert.Contains(t, body.Message, "thv registry convert",
				"Response message should include the migration command hint")
			assert.Contains(t, body.Message, legacyServer.URL,
				"Response message should identify the offending source URL")
			assert.Contains(t, w.Header().Get("Content-Type"), "application/json",
				"Response Content-Type should be application/json")
		})
	}
}

func TestRegistryRouter(t *testing.T) {
	t.Parallel()

	// Create a test config provider to avoid using the singleton
	provider, _ := CreateTestConfigProvider(t, nil)
	routes := NewRegistryRoutesWithProvider(provider)
	assert.NotNil(t, routes)
}

//nolint:paralleltest // Uses global registry provider singleton
func TestRefreshRegistry_RefreshesServerSideCache(t *testing.T) {
	var (
		mu          sync.RWMutex
		serverNames = []string{"io.example/old-server"}
	)

	setServerNames := func(names ...string) {
		mu.Lock()
		defer mu.Unlock()
		serverNames = append([]string(nil), names...)
	}
	getServerNames := func() []string {
		mu.RLock()
		defer mu.RUnlock()
		return append([]string(nil), serverNames...)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0.1/servers" {
			http.NotFound(w, r)
			return
		}

		names := getServerNames()
		servers := make([]v0.ServerResponse, 0, len(names))
		for _, name := range names {
			servers = append(servers, v0.ServerResponse{
				Server: v0.ServerJSON{
					Schema:      "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
					Name:        name,
					Description: "Test registry server",
					Version:     "1.0.0",
					Packages: []model.Package{
						{
							RegistryType: model.RegistryTypeOCI,
							Identifier:   "ghcr.io/example/" + strings.ReplaceAll(name, "/", "-") + ":1.0.0",
							Transport: model.Transport{
								Type: "stdio",
							},
						},
					},
				},
			})
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v0.ServerListResponse{
			Servers: servers,
			Metadata: v0.Metadata{
				Count: len(servers),
			},
		}); err != nil {
			t.Errorf("failed to encode registry response: %v", err)
		}
	}))
	defer upstream.Close()

	cacheFile, err := regauth.RegistryCacheFilePath(upstream.URL)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(cacheFile))
	t.Cleanup(func() {
		_ = os.Remove(cacheFile)
	})

	cfg := &config.Config{
		RegistryApiUrl:         upstream.URL,
		AllowPrivateRegistryIp: true,
	}
	configProvider, cleanup := CreateTestConfigProvider(t, cfg)
	defer cleanup()

	registry.ResetDefaultProvider()
	t.Cleanup(registry.ResetDefaultProvider)

	routes := &RegistryRoutes{
		configProvider: configProvider,
		configService:  registry.NewConfiguratorWithProvider(configProvider),
		serveMode:      true,
	}

	first := getRegistryForTest(t, routes)
	require.Equal(t, 1, first.ServerCount)
	require.Contains(t, first.Registry.Servers, "io.example/old-server")

	setServerNames("io.example/old-server", "io.example/new-server")

	staleRegistry := getRegistryForTest(t, routes)
	require.Equal(t, 1, staleRegistry.ServerCount)
	require.NotContains(t, staleRegistry.Registry.Servers, "io.example/new-server")

	staleServers := listServersForTest(t, routes)
	require.Len(t, staleServers.Servers, 1)

	refreshReq := requestWithRegistryName(http.MethodPost, "/default/refresh", "default")
	refreshRecorder := httptest.NewRecorder()
	routes.refreshRegistry(refreshRecorder, refreshReq)
	require.Equal(t, http.StatusOK, refreshRecorder.Code)

	var refreshBody map[string]string
	require.NoError(t, json.NewDecoder(refreshRecorder.Body).Decode(&refreshBody))
	require.Equal(t, "refreshed", refreshBody["status"])

	refreshedRegistry := getRegistryForTest(t, routes)
	require.Equal(t, 2, refreshedRegistry.ServerCount)
	require.Contains(t, refreshedRegistry.Registry.Servers, "io.example/new-server")

	refreshedServers := listServersForTest(t, routes)
	require.Len(t, refreshedServers.Servers, 2)
}

//nolint:paralleltest // Uses global registry provider singleton
func TestRefreshRegistry_NonDefaultRegistryNotFound(t *testing.T) {
	registry.ResetDefaultProvider()
	t.Cleanup(registry.ResetDefaultProvider)

	routes := &RegistryRoutes{}
	req := requestWithRegistryName(http.MethodPost, "/other/refresh", "other")
	w := httptest.NewRecorder()

	routes.refreshRegistry(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "Registry not found")
}

//nolint:paralleltest // Uses global registry provider singleton
func TestRefreshRegistry_UnavailableUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	cacheFile, err := regauth.RegistryCacheFilePath(upstream.URL)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(cacheFile))
	t.Cleanup(func() {
		_ = os.Remove(cacheFile)
	})

	cfg := &config.Config{
		RegistryApiUrl:         upstream.URL,
		AllowPrivateRegistryIp: true,
	}
	configProvider, cleanup := CreateTestConfigProvider(t, cfg)
	defer cleanup()

	registry.ResetDefaultProvider()
	t.Cleanup(registry.ResetDefaultProvider)

	routes := &RegistryRoutes{
		configProvider: configProvider,
		configService:  registry.NewConfiguratorWithProvider(configProvider),
		serveMode:      true,
	}

	req := requestWithRegistryName(http.MethodPost, "/default/refresh", "default")
	w := httptest.NewRecorder()

	routes.refreshRegistry(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)

	var body registryErrorResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	require.Equal(t, RegistryUnavailableCode, body.Code)
}

func requestWithRegistryName(method, target, name string) *http.Request {
	req := httptest.NewRequest(method, target, http.NoBody)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", name)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func getRegistryForTest(t *testing.T, routes *RegistryRoutes) getRegistryResponse {
	t.Helper()

	req := requestWithRegistryName(http.MethodGet, "/default", "default")
	w := httptest.NewRecorder()
	routes.getRegistry(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response getRegistryResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&response))
	return response
}

func listServersForTest(t *testing.T, routes *RegistryRoutes) listServersResponse {
	t.Helper()

	req := requestWithRegistryName(http.MethodGet, "/default/servers", "default")
	w := httptest.NewRecorder()
	routes.listServers(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var response listServersResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&response))
	return response
}

//nolint:paralleltest // Cannot use t.Parallel() with t.Setenv() in Go 1.24+
func TestGetRegistryInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         *config.Config
		expectedType   RegistryType
		expectedSource string
	}{
		{
			name: "default registry",
			config: &config.Config{
				RegistryUrl:       "",
				LocalRegistryPath: "",
			},
			expectedType:   RegistryTypeDefault,
			expectedSource: "",
		},
		{
			name: "URL registry",
			config: &config.Config{
				RegistryUrl:            "https://test.com/registry.json",
				AllowPrivateRegistryIp: false,
				LocalRegistryPath:      "",
			},
			expectedType:   RegistryTypeURL,
			expectedSource: "https://test.com/registry.json",
		},
		{
			name: "file registry",
			config: &config.Config{
				RegistryUrl:       "",
				LocalRegistryPath: "/tmp/test-registry.json",
			},
			expectedType:   RegistryTypeFile,
			expectedSource: "/tmp/test-registry.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			configProvider, cleanup := CreateTestConfigProvider(t, tt.config)
			defer cleanup()

			service := registry.NewConfiguratorWithProvider(configProvider)
			registryType, source := service.GetRegistryInfo()
			assert.Equal(t, string(tt.expectedType), registryType, "Registry type should match expected")
			assert.Equal(t, tt.expectedSource, source, "Registry source should match expected")
		})
	}
}

//nolint:paralleltest,tparallel // Subtests cannot run in parallel as they share a mock HTTP server
func TestRegistryAPI_PutEndpoint(t *testing.T) {
	t.Parallel()

	// Create a mock HTTP server that serves valid registry JSON
	validRegistryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		registryData := map[string]interface{}{
			"$schema": "https://example.com/schema.json",
			"version": "1.0.0",
			"meta":    map[string]interface{}{"last_updated": "2025-01-01T00:00:00Z"},
			"data": map[string]interface{}{
				"servers": []interface{}{
					map[string]interface{}{"name": "io.example.test-server"},
				},
			},
		}
		if err := json.NewEncoder(w).Encode(registryData); err != nil {
			t.Logf("Failed to encode registry data: %v", err)
		}
	}))
	defer validRegistryServer.Close()

	tests := []struct {
		name         string
		setupFunc    func(t *testing.T) string // Returns the request body
		expectedCode int
		description  string
	}{
		{
			name: "valid URL registry",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				// Use the mock server URL with allow_private_ip to enable HTTP for localhost
				return `{"url":"` + validRegistryServer.URL + `","allow_private_ip":true}`
			},
			expectedCode: http.StatusOK,
			description:  "Valid URL with actual registry data should be accepted",
		},
		{
			name: "valid local file registry",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				// Create a temporary file with valid registry JSON
				tempFile := filepath.Join(t.TempDir(), "valid-registry.json")
				validJSON := `{"data": {"servers": [{"name": "io.example.test-server"}]}}`
				err := os.WriteFile(tempFile, []byte(validJSON), 0600)
				require.NoError(t, err)
				return `{"local_path":"` + tempFile + `"}`
			},
			expectedCode: http.StatusOK,
			description:  "Valid local file with proper registry structure should be accepted",
		},
		{
			name: "invalid local file - non-existent",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{"local_path":"/tmp/non-existent-registry-file-12345.json"}`
			},
			expectedCode: http.StatusBadRequest,
			description:  "Non-existent local file should return 400",
		},
		{
			name: "invalid local file - wrong structure",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				// Create a file with invalid registry structure
				tempFile := filepath.Join(t.TempDir(), "invalid-registry.json")
				invalidJSON := `{"test": "registry"}`
				err := os.WriteFile(tempFile, []byte(invalidJSON), 0600)
				require.NoError(t, err)
				return `{"local_path":"` + tempFile + `"}`
			},
			expectedCode: http.StatusBadGateway,
			description:  "Local file with invalid registry structure should return 502 (validation failure)",
		},
		{
			name: "invalid URL - unreachable",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{"url":"https://invalid-url-that-does-not-exist-12345.example.com/test.json"}`
			},
			expectedCode: http.StatusGatewayTimeout,
			description:  "Unreachable URL should return 504 Gateway Timeout",
		},
		{
			name: "invalid JSON",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{"invalid":json}`
			},
			expectedCode: http.StatusBadRequest,
			description:  "Invalid JSON should return 400",
		},
		{
			name: "empty body",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				return `{}`
			},
			expectedCode: http.StatusOK,
			description:  "Empty request resets registry (returns 200)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: Not using t.Parallel() here because subtests share the mock server

			// Create a temporary config for this test
			tempDir := t.TempDir()
			configPath := filepath.Join(tempDir, "toolhive", "config.yaml")

			// Ensure the directory exists
			err := os.MkdirAll(filepath.Dir(configPath), 0755)
			require.NoError(t, err)

			// Create a test config provider
			configProvider := config.NewPathProvider(configPath)

			// Create routes with the test config provider
			routes := NewRegistryRoutesWithProvider(configProvider)

			// Get the request body from the setup function
			requestBody := tt.setupFunc(t)

			req := httptest.NewRequest("PUT", "/default", strings.NewReader(requestBody))
			req.Header.Set("Content-Type", "application/json")
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("name", "default")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

			w := httptest.NewRecorder()
			routes.updateRegistry(w, req)

			assert.Equal(t, tt.expectedCode, w.Code, tt.description)

			if w.Code == http.StatusOK {
				var response map[string]interface{}
				err := json.NewDecoder(w.Body).Decode(&response)
				require.NoError(t, err, "Success response should be valid JSON")
			}
		})
	}
}

// denyRegistryGate is a test helper that blocks all registry mutations.
type denyRegistryGate struct {
	registry.NoopPolicyGate
	err error
}

func (g *denyRegistryGate) CheckUpdateRegistry(_ context.Context, _ *registry.UpdateRegistryConfig) error {
	return g.err
}

func (g *denyRegistryGate) CheckDeleteRegistry(_ context.Context, _ *registry.DeleteRegistryConfig) error {
	return g.err
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestUpdateRegistry_BlockedByPolicyGate(t *testing.T) {
	original := registry.ActivePolicyGate()
	t.Cleanup(func() { registry.RegisterPolicyGate(original) })

	sentinel := errors.New("[ToolHive Policy] Registry is managed by organization policy")
	registry.RegisterPolicyGate(&denyRegistryGate{err: sentinel})

	provider, cleanup := CreateTestConfigProvider(t, nil)
	defer cleanup()
	routes := NewRegistryRoutesWithProvider(provider)

	body := `{"url":"https://example.com/registry.json"}`
	req := httptest.NewRequest(http.MethodPut, "/default", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	routes.updateRegistry(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "Blocked PUT should return 403")
	assert.Contains(t, w.Body.String(), "organization policy")
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestRemoveRegistry_BlockedByPolicyGate(t *testing.T) {
	original := registry.ActivePolicyGate()
	t.Cleanup(func() { registry.RegisterPolicyGate(original) })

	sentinel := errors.New("[ToolHive Policy] Registry is managed by organization policy")
	registry.RegisterPolicyGate(&denyRegistryGate{err: sentinel})

	routes := &RegistryRoutes{}

	req := httptest.NewRequest(http.MethodDelete, "/default", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	routes.removeRegistry(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "Blocked DELETE should return 403")
	assert.Contains(t, w.Body.String(), "organization policy")
}

//nolint:paralleltest // Mutates global registry policy gate singleton
func TestUpdateRegistry_AllowedByDefaultGate(t *testing.T) {
	original := registry.ActivePolicyGate()
	t.Cleanup(func() { registry.RegisterPolicyGate(original) })

	// Reset to default (allow-all) gate
	registry.RegisterPolicyGate(registry.NoopPolicyGate{})

	provider, cleanup := CreateTestConfigProvider(t, nil)
	defer cleanup()
	routes := NewRegistryRoutesWithProvider(provider)

	// Empty body resets registry — should return 200 when gate allows
	body := `{}`
	req := httptest.NewRequest(http.MethodPut, "/default", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "default")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	routes.updateRegistry(w, req)

	assert.NotEqual(t, http.StatusForbidden, w.Code,
		"Default gate should not return 403")
}
