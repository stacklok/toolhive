// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package state

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/httperr"
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

	// A published entry must reject another exclusive create immediately.
	_, err = store.CreateExclusive(ctx, "mystate")
	require.Error(t, err)
	assert.Equal(t, http.StatusConflict, httperr.Code(err))

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

func TestLocalStore_GetWriterPublishesOnClose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestLocalStore(t)
	original := []byte("complete original state")
	replacement := []byte("incomplete replacement")

	writer, err := store.GetWriter(ctx, "state")
	require.NoError(t, err)
	_, err = writer.Write(original)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	writer, err = store.GetWriter(ctx, "state")
	require.NoError(t, err)
	_, err = writer.Write(replacement)
	require.NoError(t, err)

	names, err := store.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"state"}, names)

	reader, err := store.GetReader(ctx, "state")
	require.NoError(t, err)
	contents, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, original, contents)

	abortLocalWriter(t, writer)

	reader, err = store.GetReader(ctx, "state")
	require.NoError(t, err)
	contents, err = io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	assert.Equal(t, original, contents)
	assertNoTemporaryFiles(t, store)
}

func TestLocalStore_CreateExclusivePublishesOnClose(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestLocalStore(t)
	writer, err := store.CreateExclusive(ctx, "state")
	require.NoError(t, err)
	_, err = writer.Write([]byte("incomplete state"))
	require.NoError(t, err)

	names, err := store.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, names)

	reader, err := store.GetReader(ctx, "state")
	require.Error(t, err)
	assert.Nil(t, reader)
	assert.Equal(t, http.StatusNotFound, httperr.Code(err))
	abortLocalWriter(t, writer)

	exists, err := store.Exists(ctx, "state")
	require.NoError(t, err)
	assert.False(t, exists)
	assertNoTemporaryFiles(t, store)
}

func TestLocalStore_CompetingExclusiveWriters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestLocalStore(t)
	first, err := store.CreateExclusive(ctx, "state")
	require.NoError(t, err)
	second, err := store.CreateExclusive(ctx, "state")
	require.NoError(t, err)
	_, err = first.Write([]byte("first"))
	require.NoError(t, err)
	_, err = second.Write([]byte("second"))
	require.NoError(t, err)

	results := make(chan error, 2)
	go func() { results <- first.Close() }()
	go func() { results <- second.Close() }()

	firstErr := receiveCloseResult(t, results)
	secondErr := receiveCloseResult(t, results)
	assert.True(t, (firstErr == nil && httperr.Code(secondErr) == http.StatusConflict) ||
		(secondErr == nil && httperr.Code(firstErr) == http.StatusConflict))

	contents, err := os.ReadFile(filepath.Join(store.basePath, "state"+FileExtension))
	require.NoError(t, err)
	assert.Contains(t, [][]byte{[]byte("first"), []byte("second")}, contents)
	assertNoTemporaryFiles(t, store)
}

func receiveCloseResult(t *testing.T, results <-chan error) error {
	t.Helper()
	select {
	case err := <-results:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for exclusive writer to close")
		return nil
	}
}

func abortLocalWriter(t *testing.T, writer io.WriteCloser) {
	t.Helper()
	aborter, ok := writer.(interface{ Abort() error })
	require.True(t, ok)
	require.NoError(t, aborter.Abort())
}

func assertNoTemporaryFiles(t *testing.T, store *LocalStore) {
	t.Helper()
	entries, err := os.ReadDir(store.basePath)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), ".tmp-")
	}
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
