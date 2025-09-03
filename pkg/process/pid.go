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

	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// getOldPIDFilePath returns the legacy path to the PID file for a container (for backward compatibility)
// Note: containerBaseName is pre-sanitized by the caller
func getOldPIDFilePath(containerBaseName string) string {
	// Use the system temporary directory (old behavior)
	tmpDir := os.TempDir()
	// Clean the path to satisfy security scanners (containerBaseName is already sanitized)
	return filepath.Clean(filepath.Join(tmpDir, fmt.Sprintf("toolhive-%s.pid", containerBaseName)))
}

// GetPIDFilePath returns the path to the PID file for a container
// It first tries the new XDG location, then falls back to the old temp directory location
func GetPIDFilePath(containerBaseName string) (string, error) {
	// Return empty path in Kubernetes runtime since PID files are not used
	if runtime.IsKubernetesRuntime() {
		return "", fmt.Errorf("PID file operations are not supported in Kubernetes runtime")
	}

	// Get the new XDG-based path
	pidPath, err := xdg.DataFile(filepath.Join("toolhive", "pids", fmt.Sprintf("toolhive-%s.pid", containerBaseName)))
	if err != nil {
		return "", fmt.Errorf("failed to get PID file path: %w", err)
	}
	return pidPath, nil
}

// GetPIDFilePathWithFallback returns the path to an existing PID file for a container
// It checks both the new XDG location and the old temp directory location
// Note: containerBaseName is pre-sanitized by the caller
func GetPIDFilePathWithFallback(containerBaseName string) (string, error) {
	// Return empty path in Kubernetes runtime since PID files are not used
	if runtime.IsKubernetesRuntime() {
		return "", fmt.Errorf("PID file operations are not supported in Kubernetes runtime")
	}

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
	// Clean the path to satisfy security scanners (containerBaseName is already sanitized)
	oldPath := filepath.Clean(getOldPIDFilePath(containerBaseName))
	if _, err := os.Stat(oldPath); err == nil {
		return oldPath, nil
	}

	// If neither exists, return the new path (for new files)
	return newPath, nil
}

// WritePIDFile writes a process ID to a file
// For full version compatibility, it writes to both the new XDG location and the old temp location
func WritePIDFile(containerBaseName string, pid int) error {
	// Skip PID file operations in Kubernetes runtime
	if runtime.IsKubernetesRuntime() {
		return nil
	}

	pidContent := []byte(fmt.Sprintf("%d", pid))

	// Write to the new XDG location first
	newPath, err := GetPIDFilePath(containerBaseName)
	if err != nil {
		return fmt.Errorf("failed to get PID file path: %v", err)
	}

	if err := os.WriteFile(newPath, pidContent, 0600); err != nil {
		return fmt.Errorf("failed to write PID file: %v", err)
	}

	// Also write to the old temp location for backward/forward compatibility
	// This is best-effort - don't fail the operation
	oldPath := getOldPIDFilePath(containerBaseName)

	_ = os.WriteFile(oldPath, pidContent, 0600)

	return nil
}

// WriteCurrentPIDFile writes the current process ID to a file
func WriteCurrentPIDFile(containerBaseName string) error {
	return WritePIDFile(containerBaseName, os.Getpid())
}

// ReadPIDFile reads the process ID from a file
// It checks both the new XDG location and the old temp directory location
// Note: containerBaseName is pre-sanitized by the caller
func ReadPIDFile(containerBaseName string) (int, error) {
	// Skip PID file operations in Kubernetes runtime
	if runtime.IsKubernetesRuntime() {
		return 0, fmt.Errorf("PID file operations are not supported in Kubernetes runtime")
	}

	// Get the PID file path with fallback
	pidFilePath, err := GetPIDFilePathWithFallback(containerBaseName)
	if err != nil {
		return 0, fmt.Errorf("failed to get PID file path: %v", err)
	}

	// Read the PID from the file
	// Clean the path to satisfy security scanners (containerBaseName is already sanitized)
	cleanPidPath := filepath.Clean(pidFilePath)
	pidBytes, err := os.ReadFile(cleanPidPath)
	if err != nil {
		// If we can't read from the new location, try the old location explicitly
		oldPath := getOldPIDFilePath(containerBaseName)
		if oldPath != pidFilePath {
			// Clean the path to satisfy security scanners (containerBaseName is already sanitized)
			cleanOldPath := filepath.Clean(oldPath)
			pidBytes, err = os.ReadFile(cleanOldPath)
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
	// Skip PID file operations in Kubernetes runtime
	if runtime.IsKubernetesRuntime() {
		return nil
	}

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
