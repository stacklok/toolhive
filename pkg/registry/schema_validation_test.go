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

	registry, skills, err := parseRegistryData(catalog.Upstream())
	require.NoError(t, err, "Embedded upstream registry should parse successfully")

	// Verify basic structure
	assert.NotEmpty(t, registry.Version, "Registry should have a version")
	assert.NotEmpty(t, registry.LastUpdated, "Registry should have a last_updated timestamp")
	assert.True(t, len(registry.Servers) > 0 || len(registry.RemoteServers) > 0,
		"Registry should have at least one server")

	// Skills may or may not be present in the catalog, just verify no error
	_ = skills
}

// TestUpstreamRegistryParsing verifies that parseRegistryData correctly converts
// the embedded upstream catalog data.
func TestUpstreamRegistryParsing(t *testing.T) {
	t.Parallel()

	registry, _, err := parseRegistryData(catalog.Upstream())
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

// TestParseRegistryData_LegacyFormatDetection verifies that legacy ToolHive
// registry files are rejected with ErrLegacyFormat instead of silently
// producing an empty UpstreamRegistry. Without this check, Go's JSON decoder
// drops the legacy top-level "servers"/"remote_servers"/"groups" fields and
// the caller ends up with an empty registry and no actionable error.
func TestParseRegistryData_LegacyFormatDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		wantLegacy bool
	}{
		{
			name: "legacy with top-level servers is rejected",
			input: `{
				"version": "1.0.0",
				"servers": {"test": {"image": "test:latest"}}
			}`,
			wantLegacy: true,
		},
		{
			name: "legacy with top-level remote_servers is rejected",
			input: `{
				"version": "1.0.0",
				"remote_servers": {"test": {"url": "https://example.com"}}
			}`,
			wantLegacy: true,
		},
		{
			name: "legacy with top-level groups is rejected",
			input: `{
				"version": "1.0.0",
				"groups": [{"name": "g", "servers": {}}]
			}`,
			wantLegacy: true,
		},
		{
			name: "legacy with $schema header is still detected",
			input: `{
				"$schema": "https://example.com/legacy.json",
				"version": "1.0.0",
				"servers": {"test": {"image": "test:latest"}}
			}`,
			wantLegacy: true,
		},
		{
			name: "upstream format passes through",
			input: `{
				"$schema": "https://example.com/schema.json",
				"version": "1.0.0",
				"meta": {"last_updated": "2025-01-01T00:00:00Z"},
				"data": {"servers": []}
			}`,
			wantLegacy: false,
		},
		{
			name:       "empty object is not classified as legacy",
			input:      `{}`,
			wantLegacy: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := parseRegistryData([]byte(tt.input))
			if tt.wantLegacy {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrLegacyFormat)
				assert.Contains(t, err.Error(), "thv registry convert")
				return
			}
			assert.NotErrorIs(t, err, ErrLegacyFormat)
		})
	}
}
