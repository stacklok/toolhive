// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// resetCatalogLookup clears the package-level catalog lookup after the test.
func resetCatalogLookup(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { catalogLookup.Store(nil) })
}

//nolint:paralleltest // Mutates the package-level catalog lookup.
func TestIsBuiltinCatalogServer(t *testing.T) {
	resetCatalogLookup(t)

	catalogLookup.Store(nil)
	assert.False(t, IsBuiltinCatalogServer("fetch"), "no lookup registered")

	var looked []string
	SetCatalogLookup(func(name string) bool {
		looked = append(looked, name)
		return name == "fetch"
	})

	assert.True(t, IsBuiltinCatalogServer("fetch"))
	assert.False(t, IsBuiltinCatalogServer("my-private-server"))
	assert.False(t, IsBuiltinCatalogServer(""), "empty name is never builtin")
	assert.Equal(t, []string{"fetch", "my-private-server"}, looked,
		"lookup is not consulted for an empty name")
}
