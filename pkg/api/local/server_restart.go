// Package local provides a local implementation of the ToolHive API.
package local

import (
	"context"
	"fmt"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/labels"
	"github.com/StacklokLabs/toolhive/pkg/process"
	"github.com/StacklokLabs/toolhive/pkg/runner"
)

// Restart restarts a running MCP server.
func (s *Server) Restart(ctx context.Context, name string, _ *api.RestartOptions) error {
	// Get the container ID for the specified name
	containerID, err := s.getContainerID(ctx, name)
	var containerBaseName string
	var running bool

	if err != nil {
		s.logDebug("Warning: Failed to find container: %v", err)
		s.logDebug("Trying to find state with name %s directly...", name)

		// Try to use the provided name as the base name
		containerBaseName = name
		running = false
	} else {
		// Container found, check if it's running
		running, err = s.runtime.IsContainerRunning(ctx, containerID)
		if err != nil {
			return fmt.Errorf("failed to check if container is running: %v", err)
		}

		// Get the base container name from the container labels
		containerInfo, err := s.getContainerInfo(ctx, name)
		if err != nil {
			s.logDebug("Warning: Could not find container info: %v", err)
			s.logDebug("Using provided name %s as base name", name)
			containerBaseName = name
		} else {
			containerBaseName = labels.GetContainerBaseName(containerInfo.Labels)
			if containerBaseName == "" {
				s.logDebug("Warning: Could not find base container name in labels")
				s.logDebug("Using provided name %s as base name", name)
				containerBaseName = name
			}
		}
	}

	// Check if the proxy process is running
	proxyRunning := s.isProxyRunning(containerBaseName)

	if running && proxyRunning {
		s.logDebug("Container %s and proxy are already running", name)
		return nil
	}

	// If the container is running but the proxy is not, stop the container first
	if containerID != "" && running && !proxyRunning {
		s.logDebug("Container %s is running but proxy is not. Stopping container...", name)
		if err := s.runtime.StopContainer(ctx, containerID); err != nil {
			return fmt.Errorf("failed to stop container: %v", err)
		}
		s.logDebug("Container %s stopped", name)
	}

	// Load the configuration from the state store
	mcpRunner, err := runner.LoadState(ctx, containerBaseName)
	if err != nil {
		return fmt.Errorf("failed to load state for %s: %v", containerBaseName, err)
	}

	s.logDebug("Loaded configuration from state for %s", containerBaseName)

	// Update the runtime in the loaded configuration
	mcpRunner.Config.Runtime = s.runtime

	// Run the MCP server
	s.logDebug("Starting MCP server %s...", name)

	// We need to call the original Run method in cmd/thv/app/restart.go
	// This is a temporary solution until we refactor the code to use the client API
	return nil
}

// isProxyRunning checks if the proxy process is running
func (s *Server) isProxyRunning(containerBaseName string) bool {
	if containerBaseName == "" {
		return false
	}

	// Try to read the PID file
	pid, err := process.ReadPIDFile(containerBaseName)
	if err != nil {
		return false
	}

	// Check if the process exists and is running
	isRunning, err := process.FindProcess(pid)
	if err != nil {
		s.logDebug("Warning: Error checking process: %v", err)
		return false
	}

	return isRunning
}
