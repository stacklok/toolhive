package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/process"
)

var stopCmd = &cobra.Command{
	Use:   "stop [container-name]",
	Short: "Stop an MCP server",
	Long:  `Stop a running MCP server managed by ToolHive.`,
	Args:  cobra.ExactArgs(1),
	RunE:  stopCmdFunc,
}

var (
	stopTimeout int
)

func init() {
	stopCmd.Flags().IntVar(&stopTimeout, "timeout", 30, "Timeout in seconds before forcibly stopping the container")
}

// findContainerID finds the container ID by name or ID prefix
func findContainerID(ctx context.Context, runtime rt.Runtime, containerName string) (string, error) {
	// List containers to find the one with the given name
	containers, err := runtime.ListContainers(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the container with the given name
	for _, c := range containers {
		// Check if the container is managed by ToolHive
		if !labels.IsToolHiveContainer(c.Labels) {
			continue
		}

		// Check if the container name matches
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Check if the name matches (exact match or prefix match)
		if name == containerName || strings.HasPrefix(c.ID, containerName) {
			return c.ID, nil
		}
	}

	return "", fmt.Errorf("container %s not found", containerName)
}

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

// stopProxyProcess stops the proxy process associated with the container
func stopProxyProcess(containerBaseName string) {
	if containerBaseName == "" {
		fmt.Printf("Warning: Could not find base container name in labels\n")
		return
	}

	// Try to read the PID file and kill the process
	pid, err := process.ReadPIDFile(containerBaseName)
	if err != nil {
		fmt.Printf("No PID file found for %s, proxy may not be running in detached mode\n", containerBaseName)
		return
	}

	// PID file found, try to kill the process
	fmt.Printf("Stopping proxy process (PID: %d)...\n", pid)
	if err := process.KillProcess(pid); err != nil {
		fmt.Printf("Warning: Failed to kill proxy process: %v\n", err)
	} else {
		fmt.Printf("Proxy process stopped\n")
	}

	// Remove the PID file
	if err := process.RemovePIDFile(containerBaseName); err != nil {
		fmt.Printf("Warning: Failed to remove PID file: %v\n", err)
	}
}

// stopContainer stops the container
func stopContainer(ctx context.Context, runtime rt.Runtime, containerID, containerName string) error {
	fmt.Printf("Stopping container %s...\n", containerName)
	if err := runtime.StopContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to stop container: %v", err)
	}

	fmt.Printf("Container %s stopped\n", containerName)
	return nil
}

func stopCmdFunc(_ *cobra.Command, args []string) error {
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

	// Find the container ID
	containerID, err := findContainerID(ctx, runtime, containerName)
	if err != nil {
		return err
	}

	// Check if the container is running
	running, err := runtime.IsContainerRunning(ctx, containerID)
	if err != nil {
		return fmt.Errorf("failed to check if container is running: %v", err)
	}

	if !running {
		fmt.Printf("Container %s is not running\n", containerName)
		return nil
	}

	// Get the base container name
	containerBaseName, _ := getContainerBaseName(ctx, runtime, containerID)

	// Stop the proxy process
	stopProxyProcess(containerBaseName)

	// Stop the container
	return stopContainer(ctx, runtime, containerID, containerName)
}
