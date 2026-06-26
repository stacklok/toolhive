// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestLocalStore creates a LocalStore rooted at a temp directory for testing.
func newTestLocalStore(t *testing.T) *LocalStore {
	t.Helper()
	dir := t.TempDir()
	return &LocalStore{basePath: dir}
}

func TestLocalStore_PathTraversalPrevented(t *testing.T) {
	t.Parallel()

	traversalNames := []string{
		"../escape",
		"../../etc/passwd",
		"../../../root/.ssh/authorized_keys",
		"./../escape",
		"subdir/../../escape",
	}

	for _, name := range traversalNames {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store := newTestLocalStore(t)

			_, err := store.GetReader(ctx, name)
			assert.ErrorContains(t, err, "path traversal detected", "GetReader should reject %q", name)

			_, err = store.GetWriter(ctx, name)
			assert.ErrorContains(t, err, "path traversal detected", "GetWriter should reject %q", name)

			_, err = store.CreateExclusive(ctx, name)
			assert.ErrorContains(t, err, "path traversal detected", "CreateExclusive should reject %q", name)

			err = store.Delete(ctx, name)
			assert.ErrorContains(t, err, "path traversal detected", "Delete should reject %q", name)

			_, err = store.Exists(ctx, name)
			assert.ErrorContains(t, err, "path traversal detected", "Exists should reject %q", name)
		})
	}
}

func TestLocalStore_ValidNamesWork(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestLocalStore(t)

	// Write via CreateExclusive
	w, err := store.CreateExclusive(ctx, "mystate")
	require.NoError(t, err)
	_, err = w.Write([]byte(`{"key":"value"}`))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// File should exist inside basePath
	exists, err := store.Exists(ctx, "mystate")
	require.NoError(t, err)
	assert.True(t, exists)

	// Read it back
	r, err := store.GetReader(ctx, "mystate")
	require.NoError(t, err)
	require.NoError(t, r.Close())

	// List should return it
	names, err := store.List(ctx)
	require.NoError(t, err)
	assert.Contains(t, names, "mystate")

	// Delete it
	require.NoError(t, store.Delete(ctx, "mystate"))
	exists, err = store.Exists(ctx, "mystate")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestLocalStore_FileStaysInsideBasePath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestLocalStore(t)

	w, err := store.GetWriter(ctx, "config")
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// The written file must be inside the base directory
	entries, err := os.ReadDir(store.basePath)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "config"+FileExtension, entries[0].Name())

	// Absolute path of the file must start with basePath
	absPath := filepath.Join(store.basePath, entries[0].Name())
	assert.True(t, filepath.IsAbs(absPath))
	assert.Contains(t, absPath, store.basePath)
}
