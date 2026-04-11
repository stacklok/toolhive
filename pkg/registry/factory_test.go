// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/config"
)

// resetGlobalState resets both the registry factory and default provider cache.
// It must be called via t.Cleanup in every test that touches global state.
func resetGlobalState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		config.RegisterProviderFactory(nil)
		ResetDefaultProvider()
	})
}

// writeTempRegistryJSON writes a legacy-format registry JSON file to dir and
// returns its path. serverName is used as the only server key.
func writeTempRegistryJSON(t *testing.T, dir, serverName string) string {
	t.Helper()

	type serverEntry struct {
		Image       string `json:"image"`
		Description string `json:"description"`
	}
	type registryFile struct {
		Version     string                 `json:"version"`
		LastUpdated string                 `json:"last_updated"`
		Servers     map[string]serverEntry `json:"servers"`
	}

	data, err := json.Marshal(registryFile{
		Version:     "1.0.0",
		LastUpdated: "2025-01-01T00:00:00Z",
		Servers: map[string]serverEntry{
			serverName: {
				Image:       "enterprise/server:latest",
				Description: "Enterprise test server",
			},
		},
	})
	require.NoError(t, err)

	registryPath := filepath.Join(dir, "registry.json")
	require.NoError(t, os.WriteFile(registryPath, data, 0600))
	return registryPath
}

// writeTempConfigYAML writes a YAML config file that sets local_registry_path
// and returns the config file path.
func writeTempConfigYAML(t *testing.T, dir, localRegistryPath string) string {
	t.Helper()

	type configFile struct {
		LocalRegistryPath string `yaml:"local_registry_path"`
	}

	data, err := yaml.Marshal(configFile{LocalRegistryPath: localRegistryPath})
	require.NoError(t, err)

	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, data, 0600))
	return configPath
}

// TestGetDefaultProvider_NoFactoryRegistered verifies that when no factory is
// registered, GetDefaultProvider returns a non-nil provider backed by the
// embedded registry data (which must contain at least one server).
//
//nolint:paralleltest // Mutates global config factory and provider state singletons
func TestGetDefaultProvider_NoFactoryRegistered(t *testing.T) {
	resetGlobalState(t)
	// Ensure no factory is active and the cache is clear before the call.
	config.RegisterProviderFactory(nil)
	ResetDefaultProvider()

	// Ensure the test does not accidentally run in a Kubernetes environment.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	provider, err := GetDefaultProvider()
	require.NoError(t, err)
	require.NotNil(t, provider)

	servers, err := provider.ListServers()
	require.NoError(t, err)
	assert.NotEmpty(t, servers, "embedded registry must contain at least one server")
}

// TestGetDefaultProvider_RespectsRegisteredFactory is the critical regression
// test for the bug fix. Before the fix, GetDefaultProvider called
// config.NewDefaultProvider(), which bypassed any registered factory. The fix
// changed the call to config.NewProvider(), which checks the registered factory
// first.
//
// This test:
//  1. Writes a local registry JSON with a sentinel server name.
//  2. Writes a config YAML pointing to that registry.
//  3. Registers a factory that returns a PathProvider for that config file.
//  4. Asserts that GetDefaultProvider() returns a provider whose ListServers
//     includes the sentinel server — proving the factory was honoured.
//
//nolint:paralleltest // Mutates global config factory and provider state singletons
func TestGetDefaultProvider_RespectsRegisteredFactory(t *testing.T) {
	resetGlobalState(t)

	dir := t.TempDir()
	const sentinelName = "enterprise-test-server"

	registryPath := writeTempRegistryJSON(t, dir, sentinelName)
	configPath := writeTempConfigYAML(t, dir, registryPath)

	config.RegisterProviderFactory(func() config.Provider {
		return config.NewPathProvider(configPath)
	})
	// Reset after factory is registered so the next call re-initialises.
	ResetDefaultProvider()

	provider, err := GetDefaultProvider()
	require.NoError(t, err)
	require.NotNil(t, provider)

	servers, err := provider.ListServers()
	require.NoError(t, err)

	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.GetName())
	}
	assert.Contains(t, names, sentinelName,
		"provider must expose the sentinel server from the custom registry; "+
			"this would fail on the old code that called config.NewDefaultProvider()")
}

// TestGetDefaultProvider_FactoryReturnsNil_FallsThrough verifies that when the
// registered factory returns nil, GetDefaultProvider falls through to the
// embedded registry (non-nil provider, non-empty server list).
//
//nolint:paralleltest // Mutates global config factory and provider state singletons
func TestGetDefaultProvider_FactoryReturnsNil_FallsThrough(t *testing.T) {
	resetGlobalState(t)

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	config.RegisterProviderFactory(func() config.Provider {
		return nil
	})
	ResetDefaultProvider()

	provider, err := GetDefaultProvider()
	require.NoError(t, err)
	require.NotNil(t, provider)

	servers, err := provider.ListServers()
	require.NoError(t, err)
	assert.NotEmpty(t, servers, "fallback to embedded registry must yield at least one server")
}

// TestGetDefaultProvider_CachesResult verifies that two consecutive calls to
// GetDefaultProvider (without a reset in between) return the exact same
// provider pointer, confirming the sync.Once caching semantics.
//
//nolint:paralleltest // Mutates global config factory and provider state singletons
func TestGetDefaultProvider_CachesResult(t *testing.T) {
	resetGlobalState(t)

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	config.RegisterProviderFactory(nil)
	ResetDefaultProvider()

	first, err := GetDefaultProvider()
	require.NoError(t, err)
	require.NotNil(t, first)

	second, err := GetDefaultProvider()
	require.NoError(t, err)
	require.NotNil(t, second)

	assert.Same(t, first, second, "consecutive calls must return the same cached provider instance")
}

// TestResetDefaultProvider_AllowsReinit verifies that calling ResetDefaultProvider
// clears the sync.Once cache so the next call to GetDefaultProvider creates a
// fresh provider instance (a different pointer).
//
//nolint:paralleltest // Mutates global config factory and provider state singletons
func TestResetDefaultProvider_AllowsReinit(t *testing.T) {
	resetGlobalState(t)

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")

	config.RegisterProviderFactory(nil)
	ResetDefaultProvider()

	first, err := GetDefaultProvider()
	require.NoError(t, err)
	require.NotNil(t, first)

	ResetDefaultProvider()

	second, err := GetDefaultProvider()
	require.NoError(t, err)
	require.NotNil(t, second)

	assert.NotSame(t, first, second, "after ResetDefaultProvider the next call must return a new instance")
}
