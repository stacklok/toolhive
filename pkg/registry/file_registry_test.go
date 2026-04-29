// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFixtureFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}

func TestNewFileRegistry(t *testing.T) {
	t.Parallel()

	t.Run("loads upstream-format file", func(t *testing.T) {
		t.Parallel()
		path := writeFixtureFile(t, upstreamWithServersAndSkills)

		r, err := NewFileRegistry("local", path)
		require.NoError(t, err)
		assert.Equal(t, "local", r.Name())

		entries, err := r.List(Filter{})
		require.NoError(t, err)
		assert.Len(t, entries, 4)
	})

	t.Run("rejects empty path", func(t *testing.T) {
		t.Parallel()
		_, err := NewFileRegistry("local", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "path must not be empty")
	})

	t.Run("rejects missing file", func(t *testing.T) {
		t.Parallel()
		_, err := NewFileRegistry("local", filepath.Join(t.TempDir(), "missing.json"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read")
	})

	t.Run("rejects invalid JSON", func(t *testing.T) {
		t.Parallel()
		path := writeFixtureFile(t, "not json")
		_, err := NewFileRegistry("local", path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse upstream registry")
	})

	t.Run("returns ErrLegacyFormat for legacy input", func(t *testing.T) {
		t.Parallel()
		path := writeFixtureFile(t, legacyFormat)
		_, err := NewFileRegistry("local", path)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrLegacyFormat))
	})
}
