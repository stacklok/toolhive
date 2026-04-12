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
// registry via the EmbeddedSource.
func TestValidateEmbeddedRegistryCanLoadData(t *testing.T) {
	t.Parallel()

	source := &EmbeddedSource{}
	result, err := source.Load(t.Context())
	require.NoError(t, err, "Embedded upstream registry should load successfully")

	assert.NotEmpty(t, result.Servers, "Should have at least one server")
}
