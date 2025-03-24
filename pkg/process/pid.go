package process

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// GetPIDFilePath returns the path to the PID file for a container
func GetPIDFilePath(containerBaseName string) string {
	// Use the system temporary directory
	tmpDir := os.TempDir()
	return filepath.Join(tmpDir, fmt.Sprintf("vibetool-%s.pid", containerBaseName))
}

// WritePIDFile writes a process ID to a file
func WritePIDFile(containerBaseName string, pid int) error {
	// Get the PID file path
	pidFilePath := GetPIDFilePath(containerBaseName)
	
	// Write the PID to the file
	return ioutil.WriteFile(pidFilePath, []byte(fmt.Sprintf("%d", pid)), 0644)
}

// WriteCurrentPIDFile writes the current process ID to a file
func WriteCurrentPIDFile(containerBaseName string) error {
	return WritePIDFile(containerBaseName, os.Getpid())
}

// ReadPIDFile reads the process ID from a file
func ReadPIDFile(containerBaseName string) (int, error) {
	// Get the PID file path
	pidFilePath := GetPIDFilePath(containerBaseName)
	
	// Read the PID from the file
	pidBytes, err := ioutil.ReadFile(pidFilePath)
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
	pidFilePath := GetPIDFilePath(containerBaseName)
	
	// Remove the file
	return os.Remove(pidFilePath)
}

// KillProcess kills a process by its ID
func KillProcess(pid int) error {
	// Check if the process exists
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}
	
	// Send a SIGTERM signal to the process
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("failed to send SIGTERM to process: %w", err)
	}
	
	return nil
}