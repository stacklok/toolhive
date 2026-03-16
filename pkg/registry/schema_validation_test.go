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
