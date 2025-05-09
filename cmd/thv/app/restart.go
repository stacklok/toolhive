package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/lifecycle"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/process"
	"github.com/stacklok/toolhive/pkg/runner"
)

var restartCmd = &cobra.Command{
	Use:   "restart [container-name]",
	Short: "Restart a tooling server",
	Long:  `Restart a running tooling server managed by ToolHive. If the server is not running, it will be started.`,
	Args:  cobra.ExactArgs(1),
	RunE:  restartCmdFunc,
}

func init() {
	// No specific flags needed for restart command
}

func restartCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	// Get container name
	containerName := args[0]

	// Create container runtime
	runtime, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	// Try to find the container ID
	containerID, err := findContainerID(ctx, runtime, containerName)
	var containerBaseName string
	var running bool

	if err != nil {
		logger.Warnf("Warning: Failed to find container: %v", err)
		logger.Warnf("Trying to find state with name %s directly...", containerName)

		// Try to use the provided name as the base name
		containerBaseName = containerName
		running = false
	} else {
		// Container found, check if it's running
		running, err = runtime.IsContainerRunning(ctx, containerID)
		if err != nil {
			return fmt.Errorf("failed to check if container is running: %v", err)
		}

		// Get the base container name
		containerBaseName, err = getContainerBaseName(ctx, runtime, containerID)
		if err != nil {
			logger.Warnf("Warning: Could not find base container name in labels: %v", err)
			logger.Warnf("Using provided name %s as base name", containerName)
			containerBaseName = containerName
		}
	}

	// Check if the proxy process is running
	proxyRunning := isProxyRunning(containerBaseName)

	if running && proxyRunning {
		logger.Infof("Container %s and proxy are already running", containerName)
		return nil
	}

	// If the container is running but the proxy is not, stop the container first
	if containerID != "" && running && !proxyRunning {
		logger.Infof("Container %s is running but proxy is not. Stopping container...", containerName)
		if err := runtime.StopContainer(ctx, containerID); err != nil {
			return fmt.Errorf("failed to stop container: %v", err)
		}
		logger.Infof("Container %s stopped", containerName)
	}

	// Load the configuration from the state store
	mcpRunner, err := loadRunnerFromState(ctx, containerBaseName, runtime)
	if err != nil {
		return fmt.Errorf("failed to load state for %s: %v", containerBaseName, err)
	}

	logger.Infof("Loaded configuration from state for %s", containerBaseName)

	// Run the tooling server
	logger.Infof("Starting tooling server %s...", containerName)
	return RunMCPServer(ctx, cmd, mcpRunner.Config, false)
}

// isProxyRunning checks if the proxy process is running
func isProxyRunning(containerBaseName string) bool {
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
		logger.Warnf("Warning: Error checking process: %v", err)
		return false
	}

	return isRunning
}

// loadRunnerFromState attempts to load a Runner from the state store
func loadRunnerFromState(ctx context.Context, baseName string, runtime rt.Runtime) (*runner.Runner, error) {
	// Load the runner from the state store
	r, err := runner.LoadState(ctx, baseName)
	if err != nil {
		return nil, err
	}

	// Update the runtime in the loaded configuration
	r.Config.Runtime = runtime

	return r, nil
}

/*
 * The following functions are duplicated in container/manager.go until
 * we can refactor the code to avoid this duplication.
 */

// getContainerBaseName gets the base container name from the container labels
func getContainerBaseName(ctx context.Context, runtime rt.Runtime, containerID string) (string, error) {
	containers, err := runtime.ListContainers(ctx)
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

func findContainerID(ctx context.Context, runtime rt.Runtime, name string) (string, error) {
	c, err := findContainerByName(ctx, runtime, name)
	if err != nil {
		return "", err
	}
	return c.ID, nil
}

func findContainerByName(ctx context.Context, runtime rt.Runtime, name string) (*rt.ContainerInfo, error) {
	// List containers to find the one with the given name
	containers, err := runtime.ListContainers(ctx)
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

	return nil, fmt.Errorf("%w: %s", lifecycle.ErrContainerNotFound, name)
}
