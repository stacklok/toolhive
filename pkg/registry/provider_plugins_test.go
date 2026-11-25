// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upstreamRegistryWithPlugins is a minimal upstream-format registry payload
// carrying a single server (so validateConnectivity accepts it for the remote
// provider) plus two plugins used to exercise List/Get/Search on both the
// local and remote providers.
const upstreamRegistryWithPlugins = `{
  "$schema": "https://example.com/schema.json",
  "version": "1.0.0",
  "meta": {"last_updated": "2025-01-01T00:00:00Z"},
  "data": {
    "servers": [
      {
        "name": "io.example.test-server",
        "description": "Test server",
        "packages": [
          {
            "registryType": "oci",
            "identifier": "example/test-server:latest",
            "transport": {"type": "stdio"}
          }
        ]
      }
    ],
    "plugins": [
      {
        "namespace": "io.github.stacklok",
        "name": "code-reviewer",
        "description": "Reviews code for bugs",
        "version": "1.0.0"
      },
      {
        "namespace": "io.github.acme",
        "name": "doc-generator",
        "description": "Generates documentation",
        "version": "0.2.0"
      }
    ]
  }
}`

// pluginsOnlyRegistry is an upstream-format payload that carries plugins but
// no servers or skills. The remote provider's validateConnectivity guard must
// accept it (regression for the widened empty-data check).
const pluginsOnlyRegistry = `{
  "$schema": "https://example.com/schema.json",
  "version": "1.0.0",
  "meta": {"last_updated": "2025-01-01T00:00:00Z"},
  "data": {
    "servers": [],
    "plugins": [
      {
        "namespace": "io.github.stacklok",
        "name": "code-reviewer",
        "description": "Reviews code for bugs",
        "version": "1.0.0"
      }
    ]
  }
}`

func TestParseRegistryData_Plugins(t *testing.T) {
	t.Parallel()

	_, _, plugins, err := parseRegistryData([]byte(upstreamRegistryWithPlugins))
	require.NoError(t, err)
	require.Len(t, plugins, 2)
	assert.Equal(t, "io.github.stacklok", plugins[0].Namespace)
	assert.Equal(t, "code-reviewer", plugins[0].Name)
	assert.Equal(t, "Reviews code for bugs", plugins[0].Description)
}

func TestLocalRegistryProvider_Plugins(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	require.NoError(t, os.WriteFile(path, []byte(upstreamRegistryWithPlugins), 0o600))

	provider := NewLocalRegistryProvider(path)

	// ListAvailablePlugins yields the plugins from data.plugins.
	plugins, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, plugins, 2)

	// GetPlugin returns the matching plugin.
	got, err := provider.GetPlugin("io.github.stacklok", "code-reviewer")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "code-reviewer", got.Name)
	assert.Equal(t, "1.0.0", got.Version)

	// GetPlugin returns nil for a missing plugin (no error).
	missing, err := provider.GetPlugin("io.github.nope", "absent")
	require.NoError(t, err)
	assert.Nil(t, missing)

	// SearchPlugins matches by name.
	byName, err := provider.SearchPlugins("reviewer")
	require.NoError(t, err)
	require.Len(t, byName, 1)
	assert.Equal(t, "code-reviewer", byName[0].Name)

	// SearchPlugins matches by namespace.
	byNs, err := provider.SearchPlugins("acme")
	require.NoError(t, err)
	require.Len(t, byNs, 1)
	assert.Equal(t, "doc-generator", byNs[0].Name)

	// SearchPlugins matches by description.
	byDesc, err := provider.SearchPlugins("documentation")
	require.NoError(t, err)
	require.Len(t, byDesc, 1)
	assert.Equal(t, "doc-generator", byDesc[0].Name)

	// SearchPlugins returns nil for a non-matching query.
	none, err := provider.SearchPlugins("non-existent-plugin")
	require.NoError(t, err)
	assert.Nil(t, none)
}

func TestRemoteRegistryProvider_Plugins(t *testing.T) {
	t.Parallel()

	server := createTestServer(upstreamRegistryWithPlugins, 200)
	defer server.Close()

	provider, err := NewRemoteRegistryProvider(server.URL, true)
	require.NoError(t, err)
	require.NotNil(t, provider)

	plugins, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, plugins, 2)

	got, err := provider.GetPlugin("io.github.acme", "doc-generator")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "doc-generator", got.Name)

	byName, err := provider.SearchPlugins("reviewer")
	require.NoError(t, err)
	require.Len(t, byName, 1)
	assert.Equal(t, "code-reviewer", byName[0].Name)
}

// TestRemoteRegistryProvider_PluginsOnlyNotRejected confirms the widened
// validateConnectivity guard accepts a plugins-only remote registry.
func TestRemoteRegistryProvider_PluginsOnlyNotRejected(t *testing.T) {
	t.Parallel()

	server := createTestServer(pluginsOnlyRegistry, 200)
	defer server.Close()

	provider, err := NewRemoteRegistryProvider(server.URL, true)
	require.NoError(t, err)
	require.NotNil(t, provider)

	plugins, err := provider.ListAvailablePlugins()
	require.NoError(t, err)
	require.Len(t, plugins, 1)
	assert.Equal(t, "code-reviewer", plugins[0].Name)
}

// TestBaseProvider_PluginsNoOp confirms the BaseProvider no-op defaults return
// nil, nil (providers that don't support plugins inherit these).
func TestBaseProvider_PluginsNoOp(t *testing.T) {
	t.Parallel()

	var p *BaseProvider
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

// Compile-time assertions that the concrete providers satisfy the interface.
var (
	_ Provider = (*LocalRegistryProvider)(nil)
	_ Provider = (*RemoteRegistryProvider)(nil)
	_ Provider = (*APIRegistryProvider)(nil)
	_ Provider = (*CachedAPIRegistryProvider)(nil)
)
