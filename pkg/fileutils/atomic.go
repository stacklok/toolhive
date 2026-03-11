// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package fileutils provides file operation utilities including atomic writes.
package fileutils

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// AtomicWriteFile writes data to a file atomically by writing to a temporary file
// and then renaming it. This ensures that readers either see the complete old file
// or the complete new file, never a partially written file.
func AtomicWriteFile(targetPath string, data []byte, perm os.FileMode) error {
	return atomicWriteFile(targetPath, data, perm, os.Rename)
}

type renameFunc func(oldPath, newPath string) error

func atomicWriteFile(targetPath string, data []byte, perm os.FileMode, rename renameFunc) error {
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
	// #nosec G703 -- tmpPath is from os.CreateTemp in the same directory as targetPath
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("failed to set permissions on temp file: %w", err)
	}

	// Atomically rename temp file to target file.
	// #nosec G703 -- tmpPath is from os.CreateTemp, targetPath is caller-controlled
	if renameErr := rename(tmpPath, targetPath); renameErr != nil {
		if !errors.Is(renameErr, syscall.EBUSY) {
			return fmt.Errorf("failed to rename temp file: %w", renameErr)
		}

		// Some sandbox setups bind-mount individual files (for example Flatpak
		// --filesystem=~/file). Renaming over that mountpoint fails with EBUSY.
		// Fallback to in-place overwrite to preserve functionality.
		if overwriteErr := overwriteFileInPlace(tmpPath, targetPath, perm); overwriteErr != nil {
			return fmt.Errorf("failed to rename temp file: %w (fallback overwrite failed: %v)", renameErr, overwriteErr)
		}
	} else {
		// Rename succeeded, no temp file remains.
		success = true
		return nil
	}

	// Fallback path: remove temporary source file now that data has been copied.
	if err := os.Remove(tmpPath); err != nil {
		return fmt.Errorf("failed to clean up temp file after fallback overwrite: %w", err)
	}

	success = true
	return nil
}

func overwriteFileInPlace(srcPath, dstPath string, perm os.FileMode) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open temp file: %w", err)
	}
	defer src.Close() //nolint:errcheck,gosec // best effort cleanup

	// #nosec G304 -- dstPath is caller-controlled target path.
	dst, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("open target file: %w", err)
	}
	defer dst.Close() //nolint:errcheck,gosec // best effort cleanup

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}
	if err := dst.Sync(); err != nil {
		return fmt.Errorf("sync target file: %w", err)
	}
	if err := dst.Chmod(perm); err != nil {
		return fmt.Errorf("set target permissions: %w", err)
	}
	return nil
}
