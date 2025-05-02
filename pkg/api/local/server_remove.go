// Package local provides a local implementation of the ToolHive API.
package local

import (
	"context"
	"fmt"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/client"
	"github.com/StacklokLabs/toolhive/pkg/config"
	"github.com/StacklokLabs/toolhive/pkg/labels"
	"github.com/StacklokLabs/toolhive/pkg/runner"
)

// Remove removes a stopped MCP server.
func (s *Server) Remove(ctx context.Context, name string, opts *api.RemoveOptions) error {
	// Get the container ID for the specified name
	containerID, err := s.getContainerID(ctx, name)
	if err != nil {
		return err
	}

	// Check if the container is running and force is not specified
	running, err := s.runtime.IsContainerRunning(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to check if container is running: %v", err)
	}

	if running && (opts == nil || !opts.Force) {
		return fmt.Errorf("container %s is running. Use force option to force remove", name)
	}

	// Get the container info to extract labels
	containerInfo, err := s.getContainerInfo(ctx, name)
	if err != nil {
		s.logDebug("Warning: Could not find container info: %v", err)
	} else {
		// Get the base name from the container labels
		baseName := labels.GetContainerBaseName(containerInfo.Labels)
		if baseName != "" {
			// Delete the saved state if it exists
			if err := runner.DeleteSavedConfig(ctx, baseName); err != nil {
				s.logDebug("Warning: Failed to delete saved state: %v", err)
			} else {
				s.logDebug("Saved state for %s removed", baseName)
			}
		}
	}

	// Remove the container
	s.logDebug("Removing container %s...", name)
	if err := s.runtime.RemoveContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to remove container: %v", err)
	}

	s.logDebug("Container %s removed", name)

	// Check if we should remove client configurations
	if s.shouldRemoveClientConfig() {
		if err := s.removeClientConfigurations(name); err != nil {
			s.logDebug("Warning: Failed to remove client configurations: %v", err)
		} else {
			s.logDebug("Client configurations for %s removed", name)
		}
	}

	return nil
}

// shouldRemoveClientConfig checks if client configurations should be removed
func (*Server) shouldRemoveClientConfig() bool {
	c := config.GetConfig()
	return len(c.Clients.RegisteredClients) > 0 || c.Clients.AutoDiscovery
}

// removeClientConfigurations removes client configurations for the specified server
func (s *Server) removeClientConfigurations(containerName string) error {
	// Find client configuration files
	configs, err := client.FindClientConfigs()
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	if len(configs) == 0 {
		s.logDebug("No client configuration files found")
		return nil
	}

	for _, c := range configs {
		s.logDebug("Removing MCP server from client configuration: %s", c.Path)

		if err := c.ConfigUpdater.Remove(containerName); err != nil {
			s.logDebug("Warning: Failed to remove MCP server from client configuration %s: %v", c.Path, err)
			continue
		}

		s.logDebug("Successfully removed MCP server from client configuration: %s", c.Path)
	}

	return nil
}
