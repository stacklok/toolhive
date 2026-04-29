// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEmbeddedRegistry(t *testing.T) {
	t.Parallel()

	t.Run("loads with default name", func(t *testing.T) {
		t.Parallel()
		r, err := NewEmbeddedRegistry("")
		require.NoError(t, err)
		assert.Equal(t, EmbeddedRegistryName, r.Name())

		// Embedded asset must contain at least one entry — sanity check that
		// the build-time bundle is wired up correctly.
		entries, err := r.List(Filter{})
		require.NoError(t, err)
		assert.NotEmpty(t, entries, "embedded registry should ship with entries")
	})

	t.Run("accepts name override", func(t *testing.T) {
		t.Parallel()
		r, err := NewEmbeddedRegistry("custom")
		require.NoError(t, err)
		assert.Equal(t, "custom", r.Name())
	})
}
