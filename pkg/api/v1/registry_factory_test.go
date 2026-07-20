// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
)

// writeFactorySentinelRegistry creates an upstream-format registry JSON file
// with a single server named sentinelName and a YAML config pointing to it.
// Returns the config file path.
func writeFactorySentinelRegistry(t *testing.T, sentinelName string) string {
	t.Helper()

	dir := t.TempDir()

	regData := []byte(`{
		"$schema": "https://example.com/schema.json",
		"version": "1.0.0",
		"meta": {"last_updated": "2025-01-01T00:00:00Z"},
		"data": {
			"servers": [
				{
					"name": "` + sentinelName + `",
					"description": "Factory sentinel server",
					"packages": [
						{
							"registryType": "oci",
							"identifier": "factory/server:latest",
							"transport": {"type": "stdio"}
						}
					]
				}
			]
		}
	}`)

	registryPath := filepath.Join(dir, "registry.json")
	require.NoError(t, os.WriteFile(registryPath, regData, 0600))

	// Write YAML config pointing to the registry JSON.
	type configFile struct {
		LocalRegistryPath string `yaml:"local_registry_path"`
	}

	cfgData, err := yaml.Marshal(configFile{LocalRegistryPath: registryPath})
	require.NoError(t, err)

	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, cfgData, 0600))

	return configPath
}

// makeListServersRequest builds an httptest request for GET /{name}/servers
// with the chi URL param "name" set to registryName.
func makeListServersRequest(registryName string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/"+registryName+"/servers", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", registryName)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// TestNewRegistryRoutes_RespectsRegisteredFactory is the critical regression test
// for the bug fix. Before the fix, NewRegistryRoutes called config.NewDefaultProvider(),
// which bypassed any registered ProviderFactory. The fix changed it to call
// config.NewProvider(), which checks the factory first.
//
// The test registers a factory that returns a PathProvider pointing at a local
// registry JSON containing a sentinel server name. If NewRegistryRoutes correctly
// forwards the factory-backed provider to getCurrentProvider, the listServers
// handler will return that sentinel server in its response.
//
//nolint:paralleltest // Mutates global state: config.registeredFactory and regpkg.defaultProviderOnce
func TestNewRegistryRoutes_RespectsRegisteredFactory(t *testing.T) {
	const sentinelName = "factory-sentinel-server"

	configPath := writeFactorySentinelRegistry(t, sentinelName)

	config.RegisterProviderFactory(func() config.Provider {
		return config.NewPathProvider(configPath)
	})
	t.Cleanup(func() {
		config.RegisterProviderFactory(nil)
		registry.ResetDefaultProvider()
	})

	routes := NewRegistryRoutes()

	// Clear provider cache so getCurrentProvider re-initialises using our factory.
	registry.ResetDefaultProvider()

	w := httptest.NewRecorder()
	routes.listServers(w, makeListServersRequest("default"))

	assert.Equal(t, http.StatusOK, w.Code,
		"listServers should return 200 when factory-backed provider is used")

	var body listServersResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body),
		"response body should be valid JSON")

	names := make([]string, 0, len(body.Servers))
	for _, s := range body.Servers {
		names = append(names, s.GetName())
	}
	assert.Contains(t, names, sentinelName,
		"sentinel server must be present; this would fail on the old code that called config.NewDefaultProvider()")
}

// TestNewRegistryRoutesForServe_RespectsRegisteredFactory verifies that the
// serve-mode constructor also honours the registered ProviderFactory. This
// mirrors TestNewRegistryRoutes_RespectsRegisteredFactory but exercises
// NewRegistryRoutesForServe and the serveMode code path.
//
//nolint:paralleltest // Mutates global state: config.registeredFactory and regpkg.defaultProviderOnce
func TestNewRegistryRoutesForServe_RespectsRegisteredFactory(t *testing.T) {
	const sentinelName = "factory-sentinel-server"

	configPath := writeFactorySentinelRegistry(t, sentinelName)

	config.RegisterProviderFactory(func() config.Provider {
		return config.NewPathProvider(configPath)
	})
	t.Cleanup(func() {
		config.RegisterProviderFactory(nil)
		registry.ResetDefaultProvider()
	})

	routes := NewRegistryRoutesForServe()

	// Clear provider cache so getCurrentProvider re-initialises using our factory.
	registry.ResetDefaultProvider()

	w := httptest.NewRecorder()
	routes.listServers(w, makeListServersRequest("default"))

	assert.Equal(t, http.StatusOK, w.Code,
		"listServers should return 200 when factory-backed provider is used in serve mode")

	var body listServersResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body),
		"response body should be valid JSON")

	names := make([]string, 0, len(body.Servers))
	for _, s := range body.Servers {
		names = append(names, s.GetName())
	}
	assert.Contains(t, names, sentinelName,
		"sentinel server must be present; this would fail on the old code that called config.NewDefaultProvider()")
}

// TestNewRegistryRoutes_NoFactory_ReturnsValidRoutes verifies that NewRegistryRoutes
// returns a fully-initialised struct when no ProviderFactory is registered.
//
//nolint:paralleltest // Mutates global state: config.registeredFactory
func TestNewRegistryRoutes_NoFactory_ReturnsValidRoutes(t *testing.T) {
	config.RegisterProviderFactory(nil)
	t.Cleanup(func() { config.RegisterProviderFactory(nil) })

	routes := NewRegistryRoutes()

	require.NotNil(t, routes, "NewRegistryRoutes must return a non-nil value")
	assert.NotNil(t, routes.configProvider, "configProvider must be initialised")
	assert.NotNil(t, routes.configService, "configService must be initialised")
	assert.False(t, routes.serveMode, "serveMode must be false for NewRegistryRoutes")
}

// TestNewRegistryRoutesForServe_NoFactory_ReturnsValidRoutes verifies that
// NewRegistryRoutesForServe returns a fully-initialised struct with serveMode
// set to true when no ProviderFactory is registered.
//
//nolint:paralleltest // Mutates global state: config.registeredFactory
func TestNewRegistryRoutesForServe_NoFactory_ReturnsValidRoutes(t *testing.T) {
	config.RegisterProviderFactory(nil)
	t.Cleanup(func() { config.RegisterProviderFactory(nil) })

	routes := NewRegistryRoutesForServe()

	require.NotNil(t, routes, "NewRegistryRoutesForServe must return a non-nil value")
	assert.NotNil(t, routes.configProvider, "configProvider must be initialised")
	assert.NotNil(t, routes.configService, "configService must be initialised")
	assert.True(t, routes.serveMode, "serveMode must be true for NewRegistryRoutesForServe")
}

// TestNewRegistryRoutes_ConfigServiceAndProviderAreConsistent verifies that
// configService (which drives the type/source fields) and getCurrentProvider
// (which drives the server list) both draw from the same config provider instance.
// Before the fix, configService used config.NewDefaultProvider() independently,
// causing type/source to reflect local config while the server list could reflect
// a factory-backed config (or vice versa) — inconsistency within a single response.
//
//nolint:paralleltest // Mutates global state: config.registeredFactory and regpkg.defaultProviderOnce
func TestNewRegistryRoutes_ConfigServiceAndProviderAreConsistent(t *testing.T) {
	const sentinelName = "consistency-sentinel-server"

	configPath := writeFactorySentinelRegistry(t, sentinelName)

	config.RegisterProviderFactory(func() config.Provider {
		return config.NewPathProvider(configPath)
	})
	t.Cleanup(func() {
		config.RegisterProviderFactory(nil)
		registry.ResetDefaultProvider()
	})

	routes := NewRegistryRoutes()
	registry.ResetDefaultProvider()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/registry", nil)
	routes.listRegistries(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "listRegistries should return 200")

	var body registryListResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body), "response body should be valid JSON")
	require.Len(t, body.Registries, 1, "should return exactly one registry")

	reg := body.Registries[0]
	// configService reads Type/Source from the shared provider. On the old code,
	// configService used config.NewDefaultProvider() which bypassed the factory,
	// so Type would be "default" and Source would be "" even when a factory was set.
	assert.Equal(t, RegistryTypeFile, reg.Type,
		"Type must be 'file' — proves configService uses the factory-backed provider, not an independent one")
	assert.NotEmpty(t, reg.Source,
		"Source must be non-empty for a file registry — proves configService reads from the shared provider")

	// getCurrentProvider also uses the shared provider, so it loads servers from the same registry.
	// ServerCount > 0 proves both data sources are in sync.
	assert.Greater(t, reg.ServerCount, 0,
		"ServerCount must be > 0 — proves getCurrentProvider uses the same factory-backed provider as configService")
}

// TestNewRegistryRoutesForServe_ConfigServiceAndProviderAreConsistent is the
// serve-mode equivalent of TestNewRegistryRoutes_ConfigServiceAndProviderAreConsistent.
//
//nolint:paralleltest // Mutates global state: config.registeredFactory and regpkg.defaultProviderOnce
func TestNewRegistryRoutesForServe_ConfigServiceAndProviderAreConsistent(t *testing.T) {
	const sentinelName = "consistency-sentinel-server"

	configPath := writeFactorySentinelRegistry(t, sentinelName)

	config.RegisterProviderFactory(func() config.Provider {
		return config.NewPathProvider(configPath)
	})
	t.Cleanup(func() {
		config.RegisterProviderFactory(nil)
		registry.ResetDefaultProvider()
	})

	routes := NewRegistryRoutesForServe()
	registry.ResetDefaultProvider()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/registry", nil)
	routes.listRegistries(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "listRegistries should return 200 in serve mode")

	var body registryListResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body), "response body should be valid JSON")
	require.Len(t, body.Registries, 1, "should return exactly one registry")

	reg := body.Registries[0]
	assert.Equal(t, RegistryTypeFile, reg.Type,
		"Type must be 'file' in serve mode — proves configService uses the factory-backed provider")
	assert.NotEmpty(t, reg.Source,
		"Source must be non-empty for a file registry in serve mode")
	assert.Greater(t, reg.ServerCount, 0,
		"ServerCount must be > 0 in serve mode — proves getCurrentProvider uses the same factory-backed provider as configService")
}
