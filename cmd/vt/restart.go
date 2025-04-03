package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/container"
	rt "github.com/stacklok/vibetool/pkg/container/runtime"
	"github.com/stacklok/vibetool/pkg/process"
	"github.com/stacklok/vibetool/pkg/runner"
)

var restartCmd = &cobra.Command{
	Use:   "restart [container-name]",
	Short: "Restart a tooling server",
	Long:  `Restart a running tooling server managed by Vibe Tool. If the server is not running, it will be started.`,
	Args:  cobra.ExactArgs(1),
	RunE:  restartCmdFunc,
}

func init() {
	// No specific flags needed for restart command
}

func restartCmdFunc(cmd *cobra.Command, args []string) error {
	// Get container name
	containerName := args[0]

	// Create context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
		fmt.Printf("Warning: Failed to find container: %v\n", err)
		fmt.Printf("Trying to find state with name %s directly...\n", containerName)

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
			fmt.Printf("Warning: Could not find base container name in labels: %v\n", err)
			fmt.Printf("Using provided name %s as base name\n", containerName)
			containerBaseName = containerName
		}
	}

	// Check if the proxy process is running
	proxyRunning := isProxyRunning(containerBaseName)

	if running && proxyRunning {
		fmt.Printf("Container %s and proxy are already running\n", containerName)
		return nil
	}

	// If the container is running but the proxy is not, stop the container first
	if containerID != "" && running && !proxyRunning {
		fmt.Printf("Container %s is running but proxy is not. Stopping container...\n", containerName)
		if err := runtime.StopContainer(ctx, containerID); err != nil {
			return fmt.Errorf("failed to stop container: %v", err)
		}
		fmt.Printf("Container %s stopped\n", containerName)
	}

	// Load the configuration from the state store
	mcpRunner, err := loadRunnerFromState(ctx, containerBaseName, runtime)
	if err != nil {
		return fmt.Errorf("failed to load state for %s: %v", containerBaseName, err)
	}

	fmt.Printf("Loaded configuration from state for %s\n", containerBaseName)

	// Run the tooling server
	fmt.Printf("Starting tooling server %s...\n", containerName)
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
		fmt.Printf("Warning: Error checking process: %v\n", err)
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
