// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package statuses

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:paralleltest // File system operations require sequential execution
func TestPIDFileBackwardCompatibility(t *testing.T) {

	t.Run("ReadPIDFile_FromOldLocation", func(t *testing.T) {
		//nolint:paralleltest // File system operations require sequential execution

		containerName := "test-container-old-read"
		testPID := 12345

		// Clean up any existing files
		t.Cleanup(func() {
			// Clean up new location
			if newPath, err := getPIDFilePath(containerName); err == nil {
				// Error expected here - ignore.
				_ = os.Remove(newPath)
			}
			// Clean up old location
			oldPath := getOldPIDFilePath(containerName)
			require.NoError(t, os.Remove(oldPath))
		})

		// Write PID file to old location
		oldPath := getOldPIDFilePath(containerName)
		oldDir := filepath.Dir(oldPath)
		require.NoError(t, os.MkdirAll(oldDir, 0755), "Failed to create old directory")
		require.NoError(t, os.WriteFile(oldPath, []byte(fmt.Sprintf("%d", testPID)), 0600),
			"Failed to write PID file to old location")

		// Read PID file (should find it in old location)
		pid, err := readPIDFile(containerName)
		require.NoError(t, err, "Failed to read PID file from old location")
		assert.Equal(t, testPID, pid, "PID mismatch")
	})

	t.Run("ReadPIDFile_PreferNewLocation", func(t *testing.T) {
		//nolint:paralleltest // File system operations require sequential execution

		containerName := "test-container-prefer-new"
		oldPID := 11111
		newPID := 22222

		// Clean up any existing files
		t.Cleanup(func() {
			// Clean up new location
			if newPath, err := getPIDFilePath(containerName); err == nil {
				require.NoError(t, os.Remove(newPath))
			}
			// Clean up old location
			oldPath := getOldPIDFilePath(containerName)
			require.NoError(t, os.Remove(oldPath))
		})

		// Write PID file to old location
		oldPath := getOldPIDFilePath(containerName)
		require.NoError(t, os.WriteFile(oldPath, []byte(fmt.Sprintf("%d", oldPID)), 0600),
			"Failed to write PID file to old location")

		// Write PID file to new location
		newPath, err := getPIDFilePath(containerName)
		require.NoError(t, err, "Failed to get new PID file path")

		newDir := filepath.Dir(newPath)
		require.NoError(t, os.MkdirAll(newDir, 0755), "Failed to create new directory")
		require.NoError(t, os.WriteFile(newPath, []byte(fmt.Sprintf("%d", newPID)), 0600),
			"Failed to write PID file to new location")

		// Read PID file (should prefer new location)
		pid, err := readPIDFile(containerName)
		require.NoError(t, err, "Failed to read PID file")
		assert.Equal(t, newPID, pid, "Should read from new location when both exist")
	})

	//nolint:paralleltest // File system operations require sequential execution
	t.Run("RemovePIDFile_RemovesBothLocations", func(t *testing.T) {
		//nolint:paralleltest // File system operations require sequential execution

		containerName := "test-container-remove-both"
		testPID := 44444

		// Clean up any existing files
		t.Cleanup(func() {
			// Clean up new location
			if newPath, err := getPIDFilePath(containerName); err == nil {
				// Error expected here - ignore.
				_ = os.Remove(newPath)
			}
			// Clean up old location
			oldPath := getOldPIDFilePath(containerName)
			// Error expected here - ignore.
			_ = os.Remove(oldPath)
		})

		// Create PID files in both locations
		oldPath := getOldPIDFilePath(containerName)
		require.NoError(t, os.WriteFile(oldPath, []byte(fmt.Sprintf("%d", testPID)), 0600),
			"Failed to write PID file to old location")

		newPath, err := getPIDFilePath(containerName)
		require.NoError(t, err, "Failed to get new PID file path")

		newDir := filepath.Dir(newPath)
		require.NoError(t, os.MkdirAll(newDir, 0755), "Failed to create new directory")
		require.NoError(t, os.WriteFile(newPath, []byte(fmt.Sprintf("%d", testPID)), 0600),
			"Failed to write PID file to new location")

		// Remove PID files
		require.NoError(t, removePIDFile(containerName), "Failed to remove PID files")

		// Verify both locations are cleaned up
		_, err = os.Stat(oldPath)
		assert.True(t, os.IsNotExist(err), "Old PID file should be removed")

		_, err = os.Stat(newPath)
		assert.True(t, os.IsNotExist(err), "New PID file should be removed")
	})

	//nolint:paralleltest // File system operations require sequential execution
	t.Run("RemovePIDFile_HandlesPartialExistence", func(t *testing.T) {
		//nolint:paralleltest // File system operations require sequential execution

		containerName := "test-container-partial"
		testPID := 55555

		// Clean up any existing files
		t.Cleanup(func() {
			// Clean up new location
			if newPath, err := getPIDFilePath(containerName); err == nil {
				// Error expected here - ignore.
				_ = os.Remove(newPath)
			}
			// Clean up old location
			oldPath := getOldPIDFilePath(containerName)
			// Error expected here - ignore.
			_ = os.Remove(oldPath)
		})

		// Test removing when only old file exists
		oldPath := getOldPIDFilePath(containerName)
		require.NoError(t, os.WriteFile(oldPath, []byte(fmt.Sprintf("%d", testPID)), 0600),
			"Failed to write PID file to old location")

		err := removePIDFile(containerName)
		assert.NoError(t, err, "Should handle removing only old file")

		_, err = os.Stat(oldPath)
		assert.True(t, os.IsNotExist(err), "Old PID file should be removed")
	})

	t.Run("getPIDFilePathWithFallback", func(t *testing.T) {
		//nolint:paralleltest // File system operations require sequential execution

		containerName := "test-container-fallback"

		// Clean up any existing files
		t.Cleanup(func() {
			// Clean up new location
			if newPath, err := getPIDFilePath(containerName); err == nil {
				require.NoError(t, os.Remove(newPath))
			}
			// Clean up old location
			oldPath := getOldPIDFilePath(containerName)
			require.NoError(t, os.Remove(oldPath))
		})

		// Test when neither file exists (should return new path)
		path, err := getPIDFilePathWithFallback(containerName)
		require.NoError(t, err, "Failed to get PID file path with fallback")

		expectedPath, _ := getPIDFilePath(containerName)
		assert.Equal(t, expectedPath, path, "Should return new path when no files exist")

		// Test when only old file exists
		oldPath := getOldPIDFilePath(containerName)
		require.NoError(t, os.WriteFile(oldPath, []byte("test"), 0600),
			"Failed to create old PID file")

		path, err = getPIDFilePathWithFallback(containerName)
		require.NoError(t, err, "Failed to get PID file path with fallback")
		assert.Equal(t, oldPath, path, "Should return old path when only old file exists")

		// Test when both files exist (should prefer new)
		newPath, _ := getPIDFilePath(containerName)
		newDir := filepath.Dir(newPath)
		require.NoError(t, os.MkdirAll(newDir, 0755), "Failed to create new directory")
		require.NoError(t, os.WriteFile(newPath, []byte("test"), 0600),
			"Failed to create new PID file")

		path, err = getPIDFilePathWithFallback(containerName)
		require.NoError(t, err, "Failed to get PID file path with fallback")
		assert.Equal(t, newPath, path, "Should prefer new path when both files exist")
	})
}

//nolint:paralleltest // File system operations require sequential execution
func TestPIDFileOperations(t *testing.T) {

	t.Run("ReadNonExistentPIDFile", func(t *testing.T) {
		//nolint:paralleltest // File system operations require sequential execution

		containerName := "test-non-existent-read"

		// Clean up to ensure file doesn't exist
		t.Cleanup(func() {
			require.NoError(t, removePIDFile(containerName))
		})

		// Try to read non-existent file
		_, err := readPIDFile(containerName)
		assert.Error(t, err, "Should error when reading non-existent PID file")
	})

	//nolint:paralleltest // File system operations require sequential execution
	t.Run("RemoveNonExistentPIDFile", func(t *testing.T) {
		//nolint:paralleltest // File system operations require sequential execution

		containerName := "test-non-existent-remove"

		// Clean up to ensure file doesn't exist
		t.Cleanup(func() {
			require.NoError(t, removePIDFile(containerName))
		})

		// Removing non-existent file may or may not error (implementation dependent)
		// Just ensure it doesn't panic
		_ = removePIDFile(containerName)
	})
}

//nolint:paralleltest // File system operations require sequential execution
func TestGetPIDFilePath(t *testing.T) {

	t.Run("getPIDFilePath", func(t *testing.T) {
		//nolint:paralleltest // File system operations require sequential execution

		containerName := "test-path"

		path, err := getPIDFilePath(containerName)
		require.NoError(t, err, "Failed to get PID file path")

		// Verify it's in the XDG data directory
		expectedDir := filepath.Join(xdg.DataHome, "toolhive", "pids")
		assert.Contains(t, path, expectedDir,
			"PID file path should be in XDG data directory")

		// Verify filename format
		expectedFilename := fmt.Sprintf("toolhive-%s.pid", containerName)
		assert.Equal(t, expectedFilename, filepath.Base(path),
			"PID file should have correct filename format")
	})

	t.Run("GetOldPIDFilePath", func(t *testing.T) {
		//nolint:paralleltest // File system operations require sequential execution

		containerName := "test-old-path"

		// Test the internal function for old path
		oldPath := getOldPIDFilePath(containerName)

		// Verify it's in the temp directory
		tmpDir := os.TempDir()
		assert.Contains(t, oldPath, tmpDir,
			"Old PID file path should be in temp directory")

		// Verify filename format
		expectedFilename := fmt.Sprintf("toolhive-%s.pid", containerName)
		assert.Equal(t, expectedFilename, filepath.Base(oldPath),
			"Old PID file should have correct filename format")
	})
}
