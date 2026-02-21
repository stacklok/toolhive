// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalog "github.com/stacklok/toolhive-catalog/pkg/catalog/toolhive"
	types "github.com/stacklok/toolhive-core/registry/types"
)

// TestEmbeddedRegistrySchemaValidation validates that the embedded registry.json
// conforms to the registry schema. This is the main test that ensures our
// registry data is always valid.
func TestEmbeddedRegistrySchemaValidation(t *testing.T) {
	t.Parallel()

	err := types.ValidateRegistrySchema(catalog.Legacy())
	require.NoError(t, err, "Embedded registry.json must conform to the registry schema")
}

// TestValidateEmbeddedRegistryCanLoadData tests that we can actually load the embedded registry
func TestValidateEmbeddedRegistryCanLoadData(t *testing.T) {
	t.Parallel()

	// Verify it's valid JSON
	var registry types.Registry
	err := json.Unmarshal(catalog.Legacy(), &registry)
	require.NoError(t, err, "Embedded registry should be valid JSON")

	// Verify basic structure
	assert.NotEmpty(t, registry.Version, "Registry should have a version")
	assert.NotEmpty(t, registry.LastUpdated, "Registry should have a last_updated timestamp")
	assert.NotNil(t, registry.Servers, "Registry should have a servers map")
}
