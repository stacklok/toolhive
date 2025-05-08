// Package lifecycle contains high-level logic for managing the lifecycle of
// ToolHive-managed containers.
package lifecycle

import (
	"context"
	"fmt"

	"github.com/StacklokLabs/toolhive/pkg/client"
	"github.com/StacklokLabs/toolhive/pkg/config"
	ct "github.com/StacklokLabs/toolhive/pkg/container"
	rt "github.com/StacklokLabs/toolhive/pkg/container/runtime"
	"github.com/StacklokLabs/toolhive/pkg/labels"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/process"
	"github.com/StacklokLabs/toolhive/pkg/runner"
)

// Manager is responsible for managing the state of ToolHive-managed containers.
// TODO: add Run and Restart here. This requires refactoring of the run code.
type Manager interface {
	// ListContainers lists all ToolHive-managed containers.
	ListContainers(ctx context.Context, listAll bool) ([]rt.ContainerInfo, error)
	// DeleteContainer deletes a container and its associated proxy process.
	DeleteContainer(ctx context.Context, name string, forceDelete bool) error
	// StopContainer stops a container and its associated proxy process.
	StopContainer(ctx context.Context, name string) error
}

type defaultManager struct {
	runtime rt.Runtime
}

// ErrContainerNotFound is returned when a container cannot be found by name.
var (
	ErrContainerNotFound   = fmt.Errorf("container not found")
	ErrContainerNotRunning = fmt.Errorf("container not running")
)

// NewManager creates a new container manager instance.
func NewManager(ctx context.Context) (Manager, error) {
	runtime, err := ct.NewFactory().Create(ctx)
	if err != nil {
		return nil, err
	}

	return &defaultManager{
		runtime: runtime,
	}, nil
}

func (d *defaultManager) ListContainers(ctx context.Context, listAll bool) ([]rt.ContainerInfo, error) {
	// List containers
	containers, err := d.runtime.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Filter containers to only show those managed by ToolHive
	var toolHiveContainers []rt.ContainerInfo
	for _, c := range containers {
		// If the caller did not set `listAll` to true, only include running containers.
		if labels.IsToolHiveContainer(c.Labels) && (c.State == "running" || listAll) {
			toolHiveContainers = append(toolHiveContainers, c)
		}
	}

	return toolHiveContainers, nil
}

func (d *defaultManager) DeleteContainer(ctx context.Context, name string, forceDelete bool) error {
	// We need several fields from the container struct for deletion.
	container, err := d.findContainerByName(ctx, name)
	if err != nil {
		return err
	}

	containerID := container.ID
	isRunning := container.State == "running"
	containerLabels := container.Labels

	// Check if the container is running and force is not specified
	if isRunning && !forceDelete {
		return fmt.Errorf("container %s is running. Use -f to force remove", name)
	}

	// Remove the container
	logger.Log.Infof("Removing container %s...", name)
	if err := d.runtime.RemoveContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to remove container: %v", err)
	}

	// Get the base name from the container labels
	baseName := labels.GetContainerBaseName(containerLabels)
	if baseName != "" {
		// Delete the saved state if it exists
		if err := runner.DeleteSavedConfig(ctx, baseName); err != nil {
			logger.Log.Warnf("Warning: Failed to delete saved state: %v", err)
		} else {
			logger.Log.Infof("Saved state for %s removed", baseName)
		}
	}

	logger.Log.Infof("Container %s removed", name)

	if shouldRemoveClientConfig() {
		if err := removeClientConfigurations(name); err != nil {
			logger.Log.Warnf("Warning: Failed to remove client configurations: %v", err)
		} else {
			logger.Log.Infof("Client configurations for %s removed", name)
		}
	}

	return nil
}

func (d *defaultManager) StopContainer(ctx context.Context, name string) error {
	// Find the container ID
	containerID, err := d.findContainerID(ctx, name)
	if err != nil {
		return err
	}

	// Check if the container is running
	running, err := d.runtime.IsContainerRunning(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to check if container is running: %v", err)
	}

	if !running {
		return fmt.Errorf("%w: %s", ErrContainerNotRunning, name)
	}

	// Get the base container name
	containerBaseName, _ := d.getContainerBaseName(ctx, containerID)

	// Stop the proxy process
	stopProxyProcess(containerBaseName)

	// Stop the container
	return d.stopContainer(ctx, containerID, name)
}

func (d *defaultManager) findContainerID(ctx context.Context, name string) (string, error) {
	c, err := d.findContainerByName(ctx, name)
	if err != nil {
		return "", err
	}
	return c.ID, nil
}

func (d *defaultManager) findContainerByName(ctx context.Context, name string) (*rt.ContainerInfo, error) {
	// List containers to find the one with the given name
	containers, err := d.runtime.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the container with the given name
	for _, c := range containers {
		// Check if the container is managed by ToolHive
		if !labels.IsToolHiveContainer(c.Labels) {
			continue
		}

		// Check if the container name matches
		containerName := labels.GetContainerName(c.Labels)
		if containerName == "" {
			name = c.Name // Fallback to container name
		}

		// Check if the name matches (exact match or prefix match)
		if containerName == name || c.ID == name {
			return &c, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrContainerNotFound, name)
}

// stopProxyProcess stops the proxy process associated with the container
func stopProxyProcess(containerBaseName string) {
	if containerBaseName == "" {
		logger.Log.Warnf("Warning: Could not find base container name in labels")
		return
	}

	// Try to read the PID file and kill the process
	pid, err := process.ReadPIDFile(containerBaseName)
	if err != nil {
		logger.Log.Errorf("No PID file found for %s, proxy may not be running in detached mode", containerBaseName)
		return
	}

	// PID file found, try to kill the process
	logger.Log.Infof("Stopping proxy process (PID: %d)...", pid)
	if err := process.KillProcess(pid); err != nil {
		logger.Log.Warnf("Warning: Failed to kill proxy process: %v", err)
	} else {
		logger.Log.Infof("Proxy process stopped")
	}

	// Remove the PID file
	if err := process.RemovePIDFile(containerBaseName); err != nil {
		logger.Log.Warnf("Warning: Failed to remove PID file: %v", err)
	}
}

// getContainerBaseName gets the base container name from the container labels
func (d *defaultManager) getContainerBaseName(ctx context.Context, containerID string) (string, error) {
	containers, err := d.runtime.ListContainers(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %v", err)
	}

	for _, c := range containers {
		if c.ID == containerID {
			return labels.GetContainerBaseName(c.Labels), nil
		}
	}

	return "", fmt.Errorf("container %s not found", containerID)
}

// stopContainer stops the container
func (d *defaultManager) stopContainer(ctx context.Context, containerID, containerName string) error {
	logger.Log.Infof("Stopping container %s...", containerName)
	if err := d.runtime.StopContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to stop container: %w", err)
	}

	logger.Log.Infof("Container %s stopped", containerName)
	return nil
}

func shouldRemoveClientConfig() bool {
	c := config.GetConfig()
	return len(c.Clients.RegisteredClients) > 0 || c.Clients.AutoDiscovery
}

// TODO: Move to dedicated config management interface.
// updateClientConfigurations updates client configuration files with the MCP server URL
func removeClientConfigurations(containerName string) error {
	// Find client configuration files
	configs, err := client.FindClientConfigs()
	if err != nil {
		return fmt.Errorf("failed to find client configurations: %w", err)
	}

	if len(configs) == 0 {
		logger.Log.Infof("No client configuration files found")
		return nil
	}

	for _, c := range configs {
		logger.Log.Infof("Removing MCP server from client configuration: %s", c.Path)

		if err := c.ConfigUpdater.Remove(containerName); err != nil {
			logger.Log.Warnf("Warning: Failed to remove MCP server from client configurationn %s: %v", c.Path, err)
			continue
		}

		logger.Log.Infof("Successfully removed MCP server from client configuration: %s", c.Path)
	}

	return nil
}
