// Package process provides utilities for managing process-related operations,
// such as PID file handling and process management.
package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/adrg/xdg"
)

// getOldPIDFilePath returns the legacy path to the PID file for a container (for backward compatibility)
func getOldPIDFilePath(containerBaseName string) string {
	// Use the system temporary directory (old behavior)
	tmpDir := os.TempDir()
	return filepath.Join(tmpDir, fmt.Sprintf("toolhive-%s.pid", containerBaseName))
}

// GetPIDFilePath returns the path to the PID file for a container
// It first tries the new XDG location, then falls back to the old temp directory location
func GetPIDFilePath(containerBaseName string) (string, error) {
	// Get the new XDG-based path
	pidPath, err := xdg.DataFile(filepath.Join("toolhive", "pids", fmt.Sprintf("toolhive-%s.pid", containerBaseName)))
	if err != nil {
		return "", fmt.Errorf("failed to get PID file path: %w", err)
	}
	return pidPath, nil
}

// GetPIDFilePathWithFallback returns the path to an existing PID file for a container
// It checks both the new XDG location and the old temp directory location
func GetPIDFilePathWithFallback(containerBaseName string) (string, error) {
	// First try the new XDG-based path
	newPath, err := GetPIDFilePath(containerBaseName)
	if err != nil {
		return "", err
	}

	// Check if file exists at new location
	if _, err := os.Stat(newPath); err == nil {
		return newPath, nil
	}

	// Fall back to old location
	oldPath := getOldPIDFilePath(containerBaseName)
	if _, err := os.Stat(oldPath); err == nil {
		return oldPath, nil
	}

	// If neither exists, return the new path (for new files)
	return newPath, nil
}

// WritePIDFile writes a process ID to a file
func WritePIDFile(containerBaseName string, pid int) error {
	// Get the PID file path
	pidFilePath, err := GetPIDFilePath(containerBaseName)
	if err != nil {
		return fmt.Errorf("failed to get PID file path: %v", err)
	}

	// Write the PID to the file
	return os.WriteFile(pidFilePath, []byte(fmt.Sprintf("%d", pid)), 0600)
}

// WriteCurrentPIDFile writes the current process ID to a file
func WriteCurrentPIDFile(containerBaseName string) error {
	return WritePIDFile(containerBaseName, os.Getpid())
}

// ReadPIDFile reads the process ID from a file
// It checks both the new XDG location and the old temp directory location
func ReadPIDFile(containerBaseName string) (int, error) {
	// Get the PID file path with fallback
	pidFilePath, err := GetPIDFilePathWithFallback(containerBaseName)
	if err != nil {
		return 0, fmt.Errorf("failed to get PID file path: %v", err)
	}

	// Read the PID from the file
	// #nosec G304 - This is safe as the path is constructed from a known prefix and container name
	pidBytes, err := os.ReadFile(pidFilePath)
	if err != nil {
		// If we can't read from the new location, try the old location explicitly
		oldPath := getOldPIDFilePath(containerBaseName)
		if oldPath != pidFilePath {
			// #nosec G304 - This is safe as the path is constructed from a known prefix and container name
			pidBytes, err = os.ReadFile(oldPath)
			if err != nil {
				return 0, fmt.Errorf("failed to read PID file from both new and old locations: %w", err)
			}
		} else {
			return 0, fmt.Errorf("failed to read PID file: %w", err)
		}
	}

	// Parse the PID
	pidStr := strings.TrimSpace(string(pidBytes))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse PID: %w", err)
	}

	return pid, nil
}

// RemovePIDFile removes the PID file
// It attempts to remove from both the new XDG location and the old temp directory location
func RemovePIDFile(containerBaseName string) error {
	var lastErr error

	// Try to remove from the new location
	newPath, err := GetPIDFilePath(containerBaseName)
	if err != nil {
		return fmt.Errorf("failed to get PID file path: %v", err)
	}

	if err := os.Remove(newPath); err != nil && !os.IsNotExist(err) {
		lastErr = err
	}

	// Also try to remove from the old location (cleanup legacy files)
	oldPath := getOldPIDFilePath(containerBaseName)
	if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
		// If we couldn't remove either file and both had errors, return the error
		if lastErr != nil {
			return fmt.Errorf("failed to remove PID files: new location: %v, old location: %v", lastErr, err)
		}
		lastErr = err
	}

	// If at least one was removed successfully (or didn't exist), consider it success
	if lastErr != nil && !os.IsNotExist(lastErr) {
		return lastErr
	}

	return nil
}
