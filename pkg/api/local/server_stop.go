// Package local provides a local implementation of the ToolHive API.
package local

import (
	"context"
	"fmt"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/labels"
	"github.com/StacklokLabs/toolhive/pkg/process"
)

// Stop stops a running MCP server.
func (s *Server) Stop(ctx context.Context, name string, _ *api.StopOptions) error {
	// Get the container ID for the specified name
	containerID, err := s.getContainerID(ctx, name)
	if err != nil {
		return err
	}

	// Get the base container name from the container labels
	containerInfo, err := s.getContainerInfo(ctx, name)
	if err != nil {
		s.logDebug("Warning: Could not find container info: %v", err)
	} else {
		// Get the base container name
		containerBaseName := labels.GetContainerBaseName(containerInfo.Labels)
		if containerBaseName != "" {
			// Stop the proxy process
			s.stopProxyProcess(containerBaseName)
		}
	}

	// Stop the container
	s.logDebug("Stopping container %s...", name)
	if err := s.runtime.StopContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to stop container: %v", err)
	}

	s.logDebug("Container %s stopped", name)
	return nil
}

// stopProxyProcess stops the proxy process associated with the container
func (s *Server) stopProxyProcess(containerBaseName string) {
	if containerBaseName == "" {
		s.logDebug("Warning: Could not find base container name in labels")
		return
	}

	// Try to read the PID file and kill the process
	pid, err := process.ReadPIDFile(containerBaseName)
	if err != nil {
		s.logDebug("No PID file found for %s, proxy may not be running in detached mode", containerBaseName)
		return
	}

	// PID file found, try to kill the process
	s.logDebug("Stopping proxy process (PID: %d)...", pid)
	if err := process.KillProcess(pid); err != nil {
		s.logDebug("Warning: Failed to kill proxy process: %v", err)
	} else {
		s.logDebug("Proxy process stopped")
	}

	// Remove the PID file
	if err := process.RemovePIDFile(containerBaseName); err != nil {
		s.logDebug("Warning: Failed to remove PID file: %v", err)
	}
}
