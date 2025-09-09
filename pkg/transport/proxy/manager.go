// Package proxy contains code for managing proxy processes.
package proxy

import (
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
)

// We may want to move these operations behind an interface. For now, they
// have been moved to this package to keep proxy-related logic grouped together.

// IsRunning checks if the proxy process is running
func IsRunning(containerBaseName string) bool {
	logger.Debugf("Checking if proxy process is running for container %s", containerBaseName)
	if containerBaseName == "" {
		logger.Warnf("Warning: Could not find base container name in labels")
		return false
	}

	// Try to read the PID file
	logger.Debugf("Reading PID file for container %s", containerBaseName)
	pid, err := process.ReadPIDFile(containerBaseName)
	if err != nil {
		logger.Debugf("No PID file found for container %s", containerBaseName)
		return false
	}

	// Check if the process exists and is running
	logger.Debugf("Checking if process with PID %d is running", pid)
	isRunning, err := process.FindProcess(pid)
	if err != nil {
		logger.Warnf("Warning: Error checking process: %v", err)
		return false
	}

	return isRunning
}
