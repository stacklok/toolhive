// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package desktop

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvSkipDesktopCheck is the environment variable name that can be set to
// skip the desktop validation check. Set to "1" or "true" to skip.
const EnvSkipDesktopCheck = "TOOLHIVE_SKIP_DESKTOP_CHECK"

// ErrDesktopConflict is returned when a conflict is detected between the
// current CLI and the desktop-managed CLI.
var ErrDesktopConflict = errors.New("CLI conflict detected")

// ValidateDesktopAlignment checks if the current CLI binary conflicts with
// a desktop-managed CLI installation.
//
// Returns nil if:
//   - No marker file exists (no desktop installation)
//   - Marker file is invalid or unreadable (treat as no desktop installation)
//   - The target binary in the marker doesn't exist (desktop was uninstalled)
//   - The current CLI is the desktop-managed CLI (paths match)
//
// Returns an error if:
//   - A valid marker file exists pointing to an existing binary
//   - The current CLI is NOT the desktop-managed binary
func ValidateDesktopAlignment() error {
	// Check for skip override
	if shouldSkipValidation() {
		return nil
	}

	result, err := CheckDesktopAlignment()
	if err != nil {
		// Treat errors during validation as non-fatal - don't block the CLI
		return nil
	}

	if result.HasConflict {
		return fmt.Errorf("%w\n\n%s", ErrDesktopConflict, result.Message)
	}

	return nil
}

// CheckDesktopAlignment performs the desktop alignment check and returns
// a detailed result. This is useful for programmatic inspection.
func CheckDesktopAlignment() (*ValidationResult, error) {
	result := &ValidationResult{}

	// Read the marker file
	marker, err := ReadMarkerFile()
	if err != nil {
		if errors.Is(err, ErrMarkerNotFound) || errors.Is(err, ErrInvalidMarker) {
			// No marker or invalid marker - no conflict
			return result, nil
		}
		return nil, fmt.Errorf("failed to read marker file: %w", err)
	}

	// Get the target path from the marker
	targetPath := getTargetPath(marker)
	if targetPath == "" {
		// No target path available - can't validate
		return result, nil
	}

	// Check if the target binary exists
	if !fileExists(targetPath) {
		// Target doesn't exist - desktop was likely uninstalled but marker
		// wasn't cleaned up. Proceed normally.
		return result, nil
	}

	// Get the current executable path
	currentExePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to get current executable path: %w", err)
	}

	// Resolve and normalize both paths for comparison
	resolvedCurrent, err := resolvePath(currentExePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve current executable path: %w", err)
	}

	resolvedTarget, err := resolvePath(targetPath)
	if err != nil {
		// If we can't resolve the target, we can't compare properly
		// Treat as no conflict to avoid blocking legitimate use
		return result, nil
	}

	result.CurrentCLIPath = resolvedCurrent
	result.DesktopCLIPath = resolvedTarget

	// Compare paths
	if pathsEqual(resolvedCurrent, resolvedTarget) {
		// We ARE the desktop-managed CLI - no conflict
		return result, nil
	}

	// Conflict detected!
	result.HasConflict = true
	result.Message = buildConflictMessage(resolvedTarget, resolvedCurrent, marker)

	return result, nil
}

// shouldSkipValidation checks if the validation should be skipped via
// environment variable.
func shouldSkipValidation() bool {
	val := os.Getenv(EnvSkipDesktopCheck)
	val = strings.ToLower(strings.TrimSpace(val))
	return val == "1" || val == "true"
}

// getTargetPath extracts the target binary path from the marker based on
// the installation method and platform.
func getTargetPath(marker *CliSourceMarker) string {
	if marker.InstallMethod == "symlink" && marker.SymlinkTarget != "" {
		return marker.SymlinkTarget
	}
	// For Windows/copy method, we'd need to get the desktop CLI path
	// from a known location. For now, return empty to skip validation.
	return ""
}

// resolvePath resolves symlinks and normalizes the path for comparison.
func resolvePath(path string) (string, error) {
	// First, evaluate any symlinks
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}

	// Clean and convert to absolute path
	resolved = filepath.Clean(resolved)
	if !filepath.IsAbs(resolved) {
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return "", err
		}
	}

	return resolved, nil
}

// pathsEqual compares two paths accounting for platform-specific
// case sensitivity.
func pathsEqual(path1, path2 string) bool {
	// On Windows and macOS, file systems are typically case-insensitive
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(path1, path2)
	}
	// On Linux and other platforms, use case-sensitive comparison
	return path1 == path2
}

// fileExists checks if a file exists at the given path.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// buildConflictMessage creates a user-friendly conflict error message.
func buildConflictMessage(desktopPath, currentPath string, marker *CliSourceMarker) string {
	var sb strings.Builder

	sb.WriteString("The ToolHive Desktop application manages a CLI installation at:\n")
	sb.WriteString(fmt.Sprintf("  %s\n\n", desktopPath))

	sb.WriteString("You are running a different CLI binary at:\n")
	sb.WriteString(fmt.Sprintf("  %s\n\n", currentPath))

	sb.WriteString("To avoid conflicts, please use the desktop-managed CLI or uninstall\n")
	sb.WriteString("the ToolHive Desktop application.\n\n")

	// Provide actionable guidance
	homeDir, _ := os.UserHomeDir()
	binPath := filepath.Join(homeDir, ".toolhive", "bin")

	sb.WriteString("To use the desktop-managed CLI, ensure your PATH includes:\n")
	sb.WriteString(fmt.Sprintf("  %s\n\n", binPath))

	sb.WriteString("Or run the desktop CLI directly:\n")
	sb.WriteString(fmt.Sprintf("  %s [command]\n", filepath.Join(binPath, "thv")))

	if marker.DesktopVersion != "" {
		sb.WriteString(fmt.Sprintf("\nDesktop version: %s\n", marker.DesktopVersion))
	}

	return sb.String()
}
