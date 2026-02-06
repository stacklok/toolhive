// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package fileutils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAtomicWriteFile(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()

	tests := []struct {
		name        string
		data        []byte
		perm        os.FileMode
		expectError bool
	}{
		{
			name:        "successful write",
			data:        []byte(`{"test": "data"}`),
			perm:        0o600,
			expectError: false,
		},
		{
			name:        "empty data",
			data:        []byte{},
			perm:        0o600,
			expectError: false,
		},
		{
			name:        "large data",
			data:        []byte(strings.Repeat("x", 10000)),
			perm:        0o644,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Use different file for each test to avoid conflicts
			testPath := filepath.Join(tempDir, tt.name+".json")

			err := AtomicWriteFile(testPath, tt.data, tt.perm)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify file exists and has correct content
				content, readErr := os.ReadFile(testPath)
				require.NoError(t, readErr)
				assert.Equal(t, tt.data, content)

				// Verify permissions
				info, statErr := os.Stat(testPath)
				require.NoError(t, statErr)
				assert.Equal(t, tt.perm, info.Mode().Perm())
			}
		})
	}
}

func TestAtomicWriteFile_Overwrite(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "test.json")

	// Write initial data
	initialData := []byte(`{"initial": "data with more content to ensure truncation"}`)
	err := AtomicWriteFile(targetPath, initialData, 0o600)
	require.NoError(t, err)

	// Verify initial write
	content, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, initialData, content)

	// Overwrite with smaller data
	newData := []byte(`{"new": "data"}`)
	err = AtomicWriteFile(targetPath, newData, 0o600)
	require.NoError(t, err)

	// Verify overwrite - should be only the new data, not appended
	content, err = os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, newData, content)
	assert.Len(t, content, len(newData), "file should be truncated to new data size")
}

func TestAtomicWriteFile_NoTempFileLeftBehind(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "test.json")

	// Write data successfully
	err := AtomicWriteFile(targetPath, []byte(`{"test": "data"}`), 0o600)
	require.NoError(t, err)

	// Check that no temp files remain in the directory
	entries, err := os.ReadDir(tempDir)
	require.NoError(t, err)

	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".tmp-"),
			"temp file should not remain: %s", entry.Name())
	}
}

func TestAtomicWriteFile_InvalidDirectory(t *testing.T) {
	t.Parallel()

	// Try to write to a non-existent directory
	targetPath := "/nonexistent/directory/test.json"
	err := AtomicWriteFile(targetPath, []byte(`{"test": "data"}`), 0o600)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create temp file")
}
