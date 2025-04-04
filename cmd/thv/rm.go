package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/runner"
)

var rmCmd = &cobra.Command{
	Use:   "rm [container-name]",
	Short: "Remove an MCP server",
	Long:  `Remove an MCP server managed by ToolHive.`,
	Args:  cobra.ExactArgs(1),
	RunE:  rmCmdFunc,
}

var (
	rmForce bool
)

func init() {
	rmCmd.Flags().BoolVarP(&rmForce, "force", "f", false, "Force removal of a running container")
}

func rmCmdFunc(_ *cobra.Command, args []string) error {
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
	var isRunning bool
	var containerLabels map[string]string
	for _, c := range containers {
		// Check if the container is managed by ToolHive
		if !labels.IstoolhiveContainer(c.Labels) {
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
			isRunning = strings.Contains(strings.ToLower(c.State), "running")
			containerLabels = c.Labels
			break
		}
	}

	if containerID == "" {
		return fmt.Errorf("container %s not found", containerName)
	}

	// Check if the container is running and force is not specified
	if isRunning && !rmForce {
		return fmt.Errorf("container %s is running. Use -f to force remove", containerName)
	}

	// Remove the container
	fmt.Printf("Removing container %s...\n", containerName)
	if err := runtime.RemoveContainer(ctx, containerID); err != nil {
		return fmt.Errorf("failed to remove container: %v", err)
	}

	// Get the base name from the container labels
	baseName := labels.GetContainerBaseName(containerLabels)
	if baseName != "" {
		// Delete the saved state if it exists
		if err := runner.DeleteSavedConfig(ctx, baseName); err != nil {
			fmt.Printf("Warning: Failed to delete saved state: %v\n", err)
		} else {
			fmt.Printf("Saved state for %s removed\n", baseName)
		}
	}

	fmt.Printf("Container %s removed\n", containerName)
	return nil
}
