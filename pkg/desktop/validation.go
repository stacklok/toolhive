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

// envSkipDesktopCheck is the environment variable name that can be set to
// skip the desktop validation check. Set to "1" or "true" to skip.
const envSkipDesktopCheck = "TOOLHIVE_SKIP_DESKTOP_CHECK"

// ErrDesktopConflict is returned when a conflict is detected between the
// current CLI and the desktop-managed CLI.
var ErrDesktopConflict = errors.New("CLI conflict detected")

// IsDesktopManagedCLI reports whether the current CLI binary is the one
// managed by the ToolHive Desktop application. It returns false on any error
// (fail open: show updates when uncertain).
func IsDesktopManagedCLI() bool {
	result, err := checkDesktopAlignment()
	if err != nil {
		return false
	}
	// No conflict + DesktopCLIPath populated = paths matched, we ARE the desktop binary
	return !result.HasConflict && result.DesktopCLIPath != ""
}

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

	result, err := checkDesktopAlignment()
	if err != nil {
		// Treat errors during validation as non-fatal - don't block the CLI
		return nil
	}

	if result.HasConflict {
		return fmt.Errorf("%w\n\n%s", ErrDesktopConflict, result.Message)
	}

	return nil
}

// checkDesktopAlignment performs the desktop alignment check and returns
// a detailed result.
func checkDesktopAlignment() (*validationResult, error) {
	result := &validationResult{}

	// Read the marker file
	marker, err := readMarkerFile()
	if err != nil {
		if errors.Is(err, errMarkerNotFound) || errors.Is(err, errInvalidMarker) {
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
	val := os.Getenv(envSkipDesktopCheck)
	val = strings.ToLower(strings.TrimSpace(val))
	return val == "1" || val == "true"
}

// getTargetPath extracts the target binary path from the marker based on
// the installation method and platform.
func getTargetPath(marker *cliSourceMarker) string {
	if marker.InstallMethod == "symlink" && marker.SymlinkTarget != "" {
		return marker.SymlinkTarget
	}

	// For Flatpak installations, the target is the host-visible path to the
	// CLI binary inside the Flatpak app directory. The validation logic is
	// the same as symlink: check if the target exists and compare paths.
	if marker.InstallMethod == "flatpak" && marker.FlatpakTarget != "" {
		return marker.FlatpakTarget
	}

	// For Windows/copy method, construct the path to the desktop-managed CLI
	// from the known installation location: %LOCALAPPDATA%\ToolHive\bin\thv.exe
	// Note: copy method is only used on Windows; on other platforms, return empty.
	if marker.InstallMethod == "copy" && runtime.GOOS == "windows" {
		return filepath.Join(getWindowsLocalAppData(), "ToolHive", "bin", "thv.exe")
	}

	return ""
}

// getWindowsLocalAppData returns the LocalAppData path on Windows.
// Falls back to %USERPROFILE%\AppData\Local if LOCALAPPDATA is not set.
func getWindowsLocalAppData() string {
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		return localAppData
	}
	// Fallback: construct from home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, "AppData", "Local")
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
func buildConflictMessage(desktopPath, currentPath string, marker *cliSourceMarker) string {
	var sb strings.Builder

	sb.WriteString("The ToolHive Desktop application manages a CLI installation at:\n")
	fmt.Fprintf(&sb, "  %s\n\n", desktopPath)

	sb.WriteString("You are running a different CLI binary at:\n")
	fmt.Fprintf(&sb, "  %s\n\n", currentPath)

	sb.WriteString("To avoid conflicts, please use the desktop-managed CLI or uninstall\n")
	sb.WriteString("the ToolHive Desktop application.\n\n")

	// Provide actionable guidance with platform-specific paths
	binPath, exeName := getDesktopBinPath()

	sb.WriteString("To use the desktop-managed CLI, ensure your PATH includes:\n")
	fmt.Fprintf(&sb, "  %s\n\n", binPath)

	sb.WriteString("Or run the desktop CLI directly:\n")
	fmt.Fprintf(&sb, "  %s [command]\n", filepath.Join(binPath, exeName))

	if marker.DesktopVersion != "" {
		fmt.Fprintf(&sb, "\nDesktop version: %s\n", marker.DesktopVersion)
	}

	return sb.String()
}

// getDesktopBinPath returns the platform-specific path to the desktop-managed
// CLI bin directory and the executable name.
func getDesktopBinPath() (binPath string, exeName string) {
	if runtime.GOOS == "windows" {
		// Windows: %LOCALAPPDATA%\ToolHive\bin\thv.exe
		return filepath.Join(getWindowsLocalAppData(), "ToolHive", "bin"), "thv.exe"
	}
	// macOS/Linux: ~/.toolhive/bin/thv
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, toolhiveDir, "bin"), "thv"
}
