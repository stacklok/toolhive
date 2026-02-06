// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package fileutils provides file operation utilities including atomic writes.
package fileutils

import (
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteFile writes data to a file atomically by writing to a temporary file
// and then renaming it. This ensures that readers either see the complete old file
// or the complete new file, never a partially written file.
func AtomicWriteFile(targetPath string, data []byte, perm os.FileMode) error {
	// Create a temporary file in the same directory as the target file
	// This ensures the temp file is on the same filesystem for atomic rename
	dir := filepath.Dir(targetPath)
	tmpFile, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Ensure cleanup of temp file on error
	success := false
	defer func() {
		if !success {
			tmpFile.Close()    //nolint:errcheck,gosec // best effort cleanup
			os.Remove(tmpPath) //nolint:errcheck,gosec // best effort cleanup
		}
	}()

	// Write data to temp file
	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}

	// Sync to ensure data is written to disk
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	// Close the temp file before renaming
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Set the correct permissions on the temp file
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("failed to set permissions on temp file: %w", err)
	}

	// Atomically rename temp file to target file
	// This is atomic on POSIX systems (Linux, macOS, etc.)
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	success = true
	return nil
}
