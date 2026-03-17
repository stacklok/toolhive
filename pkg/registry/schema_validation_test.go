// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalog "github.com/stacklok/toolhive-catalog/pkg/catalog/toolhive"
	types "github.com/stacklok/toolhive-core/registry/types"
)

// TestEmbeddedRegistrySchemaValidation validates that the embedded upstream registry
// conforms to the upstream registry schema.
func TestEmbeddedRegistrySchemaValidation(t *testing.T) {
	t.Parallel()

	err := types.ValidateUpstreamRegistryBytes(catalog.Upstream())
	require.NoError(t, err, "Embedded upstream registry must conform to the upstream registry schema")
}

// TestValidateEmbeddedRegistryCanLoadData tests that we can load the embedded upstream
// registry and convert it to the internal format.
func TestValidateEmbeddedRegistryCanLoadData(t *testing.T) {
	t.Parallel()

	registry, skills, err := parseUpstreamRegistry(catalog.Upstream())
	require.NoError(t, err, "Embedded upstream registry should parse successfully")

	// Verify basic structure
	assert.NotEmpty(t, registry.Version, "Registry should have a version")
	assert.NotEmpty(t, registry.LastUpdated, "Registry should have a last_updated timestamp")
	assert.True(t, len(registry.Servers) > 0 || len(registry.RemoteServers) > 0,
		"Registry should have at least one server")

	// Skills may or may not be present in the catalog, just verify no error
	_ = skills
}

func TestIsUpstreamFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "upstream format with data object",
			input:    `{"$schema": "https://example.com/schema.json", "data": {"servers": []}}`,
			expected: true,
		},
		{
			name:     "upstream format data only, no schema",
			input:    `{"data": {"servers": []}}`,
			expected: true,
		},
		{
			name:     "legacy format with schema but no data object",
			input:    `{"$schema": "https://example.com/legacy.json", "version": "1.0", "servers": {}}`,
			expected: false,
		},
		{
			name:     "legacy format no schema",
			input:    `{"version": "1.0", "servers": {"osv": {}}}`,
			expected: false,
		},
		{
			name:     "data is a string not object",
			input:    `{"data": "not an object"}`,
			expected: false,
		},
		{
			name:     "data is an array not object",
			input:    `{"data": [1, 2, 3]}`,
			expected: false,
		},
		{
			name:     "data is null",
			input:    `{"data": null}`,
			expected: false,
		},
		{
			name:     "empty JSON object",
			input:    `{}`,
			expected: false,
		},
		{
			name:     "invalid JSON",
			input:    `not json`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, isUpstreamFormat([]byte(tt.input)))
		})
	}
}

func TestParseRegistryAutoDetect(t *testing.T) {
	t.Parallel()

	t.Run("upstream format returns isLegacy=false", func(t *testing.T) {
		t.Parallel()
		input := `{
			"$schema": "https://example.com/schema.json",
			"version": "1.0.0",
			"meta": {"last_updated": "2025-01-01T00:00:00Z"},
			"data": {"servers": []}
		}`
		_, _, isLegacy, err := parseRegistryAutoDetect([]byte(input))
		require.NoError(t, err)
		assert.False(t, isLegacy)
	})

	t.Run("legacy format returns isLegacy=true", func(t *testing.T) {
		t.Parallel()
		input := `{
			"version": "1.0.0",
			"servers": {"test": {"image": "test:latest"}}
		}`
		_, _, isLegacy, err := parseRegistryAutoDetect([]byte(input))
		require.NoError(t, err)
		assert.True(t, isLegacy)
	})

	t.Run("legacy format with schema returns isLegacy=true", func(t *testing.T) {
		t.Parallel()
		input := `{
			"$schema": "https://example.com/legacy.json",
			"version": "1.0.0",
			"servers": {"test": {"image": "test:latest"}}
		}`
		_, _, isLegacy, err := parseRegistryAutoDetect([]byte(input))
		require.NoError(t, err)
		assert.True(t, isLegacy)
	})
}

// TestUpstreamRegistryParsing verifies that parseUpstreamRegistry correctly converts
// the embedded upstream catalog data.
func TestUpstreamRegistryParsing(t *testing.T) {
	t.Parallel()

	registry, _, err := parseUpstreamRegistry(catalog.Upstream())
	require.NoError(t, err)

	// Verify servers have names set (from conversion)
	for _, server := range registry.Servers {
		assert.NotEmpty(t, server.Name, "Server should have a name")
		assert.NotEmpty(t, server.Image, "Container server should have an image")
	}
	for _, server := range registry.RemoteServers {
		assert.NotEmpty(t, server.Name, "Remote server should have a name")
	}
}
