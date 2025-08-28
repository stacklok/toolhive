// Package process provides utilities for managing process-related operations,
// such as PID file handling and process management.
package process

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/adrg/xdg"
)

// GetPIDFilePath returns the path to the PID file for a container
func GetPIDFilePath(containerBaseName string) (string, error) {
	pidPath, err := xdg.DataFile(filepath.Join("toolhive", "pids", fmt.Sprintf("toolhive-%s.pid", containerBaseName)))
	if err != nil {
		return "", fmt.Errorf("failed to get PID file path: %w", err)
	}
	return pidPath, nil
}

// legacyTempPIDFilePath returns the legacy path under os.TempDir().
func legacyTempPIDFilePath(containerBaseName string) (string, error) {
	base := filepath.Clean(os.TempDir())
	if base == "" || !filepath.IsAbs(base) {
		return "", fmt.Errorf("invalid temp dir: %q", base)
	}
	return filepath.Join(base, fmt.Sprintf("toolhive-%s.pid", containerBaseName)), nil
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
func ReadPIDFile(containerBaseName string) (int, error) {
	primaryPath, err := GetPIDFilePath(containerBaseName)
	if err != nil {
		return 0, fmt.Errorf("failed to get PID file path: %v", err)
	}

	// #nosec G304: This is a fixed path, not user input
	pidBytes, err := os.ReadFile(primaryPath)
	if err != nil {
		// If it's specifically "not exist", try the legacy tempdir location
		if errors.Is(err, os.ErrNotExist) {
			fallbackPath, err := legacyTempPIDFilePath(containerBaseName)
			if err != nil {
				return 0, fmt.Errorf("failed to get fallback PID file path: %v", err)
			}

			// #nosec G304: This is a fixed path, not user input
			pidBytes, err = os.ReadFile(fallbackPath)
			if err != nil {
				return 0, fmt.Errorf("failed to read PID file: tried %q and fallback %q: %w", primaryPath, fallbackPath, err)
			}
		} else {
			return 0, fmt.Errorf("failed to read PID file %q: %w", primaryPath, err)
		}
	}

	pidStr := strings.TrimSpace(string(pidBytes))
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return 0, fmt.Errorf("failed to parse PID: %w", err)
	}
	return pid, nil
}

// RemovePIDFile removes the PID file
func RemovePIDFile(containerBaseName string) error {
	// Get the PID file path
	pidFilePath, err := GetPIDFilePath(containerBaseName)
	if err != nil {
		return fmt.Errorf("failed to get PID file path: %v", err)
	}

	// Remove the file
	return os.Remove(pidFilePath)
}
