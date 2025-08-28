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

// GetPIDFilePath returns the path to the PID file for a container
func GetPIDFilePath(containerBaseName string) (string, error) {
	pidPath, err := xdg.DataFile(filepath.Join("toolhive", "pids", fmt.Sprintf("toolhive-%s.pid", containerBaseName)))
	if err != nil {
		return "", fmt.Errorf("failed to get PID file path: %w", err)
	}
	return pidPath, nil
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
	// Get the PID file path
	pidFilePath, err := GetPIDFilePath(containerBaseName)
	if err != nil {
		return 0, fmt.Errorf("failed to get PID file path: %v", err)
	}

	// Read the PID from the file
	// #nosec G304 - This is safe as the path is constructed from a known prefix and container name
	pidBytes, err := os.ReadFile(pidFilePath)
	if err != nil {
		return 0, fmt.Errorf("failed to read PID file: %w", err)
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
func RemovePIDFile(containerBaseName string) error {
	// Get the PID file path
	pidFilePath, err := GetPIDFilePath(containerBaseName)
	if err != nil {
		return fmt.Errorf("failed to get PID file path: %v", err)
	}

	// Remove the file
	return os.Remove(pidFilePath)
}
