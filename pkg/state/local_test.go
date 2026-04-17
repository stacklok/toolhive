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

// newTestStore creates a LocalStore backed by a resolved temp directory.
func newTestStore(t *testing.T) *LocalStore {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	return &LocalStore{basePath: resolved}
}

func TestGetFilePath(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)

	tests := []struct {
		name        string
		input       string
		expectError bool
	}{
		// Valid names
		{name: "simple name", input: "my-workload", expectError: false},
		{name: "with dots", input: "workload.v2", expectError: false},
		{name: "with underscores", input: "my_workload", expectError: false},
		{name: "alphanumeric", input: "abc123", expectError: false},
		{name: "already has extension", input: "config.json", expectError: false},

		// Path traversal attacks
		{name: "parent directory", input: "..", expectError: true},
		{name: "relative escape", input: "../secret", expectError: true},
		{name: "nested escape", input: "../../etc/passwd", expectError: true},
		{name: "mid-path traversal", input: "foo/../../../etc/shadow", expectError: true},
		{name: "absolute unix", input: "/etc/passwd", expectError: true},

		// Path separators
		{name: "forward slash subdirectory", input: "sub/file", expectError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := store.getFilePath(tt.input)

			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, result)
				assert.Contains(t, err.Error(), "path traversal detected")
			} else {
				require.NoError(t, err)
				assert.True(t, filepath.IsAbs(result), "result should be an absolute path")
				// Verify the result is inside basePath
				dir := filepath.Dir(result)
				assert.Equal(t, store.basePath, dir, "file should be inside basePath")
			}
		})
	}
}

func TestGetFilePathSecurityCases(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)

	// Real-world attack patterns that must always be rejected.
	attacks := []string{
		"../../../etc/passwd",
		"./../../../etc/shadow",
		"../../../../../../etc/passwd",
		"..\\..\\Windows\\System32",
		"foo/../../bar",
	}

	for _, pattern := range attacks {
		t.Run("reject_"+pattern, func(t *testing.T) {
			t.Parallel()

			result, err := store.getFilePath(pattern)
			assert.Error(t, err, "should reject attack pattern: %q", pattern)
			assert.Empty(t, result)
			assert.Contains(t, err.Error(), "path traversal detected")
		})
	}
}

func TestLocalStoreOperationsRejectTraversal(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()
	malicious := "../../../etc/passwd"

	t.Run("GetReader", func(t *testing.T) {
		t.Parallel()
		reader, err := store.GetReader(ctx, malicious)
		assert.Error(t, err)
		assert.Nil(t, reader)
		assert.Contains(t, err.Error(), "path traversal detected")
	})

	t.Run("GetWriter", func(t *testing.T) {
		t.Parallel()
		writer, err := store.GetWriter(ctx, malicious)
		assert.Error(t, err)
		assert.Nil(t, writer)
		assert.Contains(t, err.Error(), "path traversal detected")
	})

	t.Run("CreateExclusive", func(t *testing.T) {
		t.Parallel()
		writer, err := store.CreateExclusive(ctx, malicious)
		assert.Error(t, err)
		assert.Nil(t, writer)
		assert.Contains(t, err.Error(), "path traversal detected")
	})

	t.Run("Delete", func(t *testing.T) {
		t.Parallel()
		err := store.Delete(ctx, malicious)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "path traversal detected")
	})

	t.Run("Exists", func(t *testing.T) {
		t.Parallel()
		exists, err := store.Exists(ctx, malicious)
		assert.Error(t, err)
		assert.False(t, exists)
		assert.Contains(t, err.Error(), "path traversal detected")
	})
}

func TestLocalStoreRoundTrip(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	// Write data
	writer, err := store.GetWriter(ctx, "test-roundtrip")
	require.NoError(t, err)
	_, err = writer.Write([]byte(`{"key":"value"}`))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	// Verify it exists
	exists, err := store.Exists(ctx, "test-roundtrip")
	require.NoError(t, err)
	assert.True(t, exists)

	// Read it back
	reader, err := store.GetReader(ctx, "test-roundtrip")
	require.NoError(t, err)
	buf := make([]byte, 256)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, `{"key":"value"}`, string(buf[:n]))
	require.NoError(t, reader.Close())

	// Verify it appears in list
	names, err := store.List(ctx)
	require.NoError(t, err)
	assert.Contains(t, names, "test-roundtrip")

	// Delete and verify
	require.NoError(t, store.Delete(ctx, "test-roundtrip"))
	exists, err = store.Exists(ctx, "test-roundtrip")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestLocalStoreCreateExclusiveConflict(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	// First create succeeds
	writer, err := store.CreateExclusive(ctx, "exclusive-test")
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	// Second create fails with conflict
	writer, err = store.CreateExclusive(ctx, "exclusive-test")
	assert.Error(t, err)
	assert.Nil(t, writer)
	assert.Contains(t, err.Error(), "already exists")

	// Cleanup
	require.NoError(t, os.Remove(filepath.Join(store.basePath, "exclusive-test.json")))
}
