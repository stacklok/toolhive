package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/vibetool/pkg/container"
	"github.com/stacklok/vibetool/pkg/labels"
	"github.com/stacklok/vibetool/pkg/process"
)

var stopCmd = &cobra.Command{
	Use:   "stop [container-name]",
	Short: "Stop an MCP server",
	Long:  `Stop a running MCP server managed by Vibe Tool.`,
	Args:  cobra.ExactArgs(1),
	RunE:  stopCmdFunc,
}

var (
	stopTimeout int
)

func init() {
	stopCmd.Flags().IntVar(&stopTimeout, "timeout", 30, "Timeout in seconds before forcibly stopping the container")
}

func stopCmdFunc(cmd *cobra.Command, args []string) error {
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

	// List containers to find the one with the given name
	containers, err := runtime.ListContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the container with the given name
	var containerID string
	for _, c := range containers {
		// Check if the container is managed by Vibe Tool
		if !labels.IsVibeToolContainer(c.Labels) {
			continue
		}

		// Check if the container name matches
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		// Check if the name matches (exact match or prefix match)
		if name == containerName || strings.HasPrefix(c.ID, containerName) {
			containerID = c.ID
			break
		}
	}

	if containerID == "" {
		return fmt.Errorf("container %s not found", containerName)
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

	// Get the base container name from the labels
	var containerBaseName string
	for _, c := range containers {
		if c.ID == containerID {
			containerBaseName = labels.GetContainerBaseName(c.Labels)
			break
		}
	}

	if containerBaseName != "" {
		// Try to read the PID file and kill the process
		pid, err := process.ReadPIDFile(containerBaseName)
		if err == nil {
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
		} else {
			fmt.Printf("No PID file found for %s, proxy may not be running in detached mode\n", containerBaseName)
		}
	} else {
		fmt.Printf("Warning: Could not find base container name in labels\n")
	}

	// Stop the container
	fmt.Printf("Stopping container %s...\n", containerName)
	if err := runtime.StopContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to stop container: %v", err)
	}

	fmt.Printf("Container %s stopped\n", containerName)
	return nil
}