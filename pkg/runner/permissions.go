// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive-core/permissions"
)

// This was moved from the CLI to allow it to be shared with the lifecycle manager.
// It will likely be moved elsewhere in a future PR.

// CreatePermissionProfileFile creates a temporary file with the permission profile
func CreatePermissionProfileFile(serverName string, permProfile *permissions.Profile) (string, error) {
	tempFile, err := os.CreateTemp("", fmt.Sprintf("toolhive-%s-permissions-*.json", serverName))
	if err != nil {
		return "", fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer func() {
		if err := tempFile.Close(); err != nil {
			// Non-fatal: temp file cleanup failure
			slog.Warn("Failed to close temp file", "error", err)
		}
	}()

	// Get the temporary file path
	permProfilePath := tempFile.Name()

	// Serialize the permission profile to JSON
	permProfileJSON, err := json.Marshal(permProfile)
	if err != nil {
		return "", fmt.Errorf("failed to serialize permission profile: %w", err)
	}

	// Write the permission profile to the temporary file
	if _, err := tempFile.Write(permProfileJSON); err != nil {
		return "", fmt.Errorf("failed to write permission profile to file: %w", err)
	}

	//nolint:gosec // G706: path is a temp file created by us
	slog.Debug("Wrote permission profile to temporary file", "path", permProfilePath)

	return permProfilePath, nil
}

// CleanupTempPermissionProfile removes a temporary permission profile file if it was created by toolhive
func CleanupTempPermissionProfile(permissionProfilePath string) error {
	if permissionProfilePath == "" {
		return nil
	}

	// Check if this is a temporary file created by toolhive
	if !isTempPermissionProfile(permissionProfilePath) {
		//nolint:gosec // G706: path is user-provided file, not secret
		slog.Debug("Permission profile is not a temporary file, skipping cleanup", "path", permissionProfilePath)
		return nil
	}

	// Check if the file exists
	// #nosec G703 -- permissionProfilePath is validated by isTempPermissionProfile above
	if _, err := os.Stat(permissionProfilePath); os.IsNotExist(err) {
		//nolint:gosec // G706: path is validated by isTempPermissionProfile
		slog.Debug("Temporary permission profile file does not exist, skipping cleanup", "path", permissionProfilePath)
		return nil
	}

	// Remove the temporary file
	// #nosec G703 -- permissionProfilePath is validated by isTempPermissionProfile above
	if err := os.Remove(permissionProfilePath); err != nil {
		return fmt.Errorf("failed to remove temporary permission profile file %s: %w", permissionProfilePath, err)
	}

	//nolint:gosec // G706: path is validated by isTempPermissionProfile
	slog.Debug("Removed temporary permission profile file", "path", permissionProfilePath)
	return nil
}

// isTempPermissionProfile checks if a file path is a temporary permission profile created by toolhive
func isTempPermissionProfile(filePath string) bool {
	if filePath == "" {
		return false
	}

	// Get the base name of the file
	fileName := filepath.Base(filePath)

	// Check if it matches the pattern: toolhive-*-permissions-*.json
	if !strings.HasPrefix(fileName, "toolhive-") ||
		!strings.Contains(fileName, "-permissions-") ||
		!strings.HasSuffix(fileName, ".json") {
		return false
	}

	// Check if it's in a temporary directory (os.TempDir() or similar)
	tempDir := os.TempDir()
	fileDir := filepath.Dir(filePath)

	// Check if the file is in the system temp directory or a subdirectory of it
	relPath, err := filepath.Rel(tempDir, fileDir)
	if err != nil {
		return false
	}

	// If the relative path doesn't start with "..", then it's within the temp directory
	return !strings.HasPrefix(relPath, "..")
}
