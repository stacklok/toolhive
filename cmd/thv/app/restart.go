package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/lifecycle"
)

var (
	restartAll bool
)

var restartCmd = &cobra.Command{
	Use:   "restart [container-name]",
	Short: "Restart a tooling server",
	Long:  `Restart a running tooling server managed by ToolHive. If the server is not running, it will be started.`,
	Args:  cobra.RangeArgs(0, 1),
	RunE:  restartCmdFunc,
}

func init() {
	restartCmd.Flags().BoolVarP(&restartAll, "all", "a", false, "Restart all MCP servers")
}

func restartCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Validate arguments
	if restartAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both --all flag and container name")
	}
	if !restartAll && len(args) == 0 {
		return fmt.Errorf("must specify either --all flag or container name")
	}

	// Create lifecycle manager.
	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create lifecycle manager: %v", err)
	}

	if restartAll {
		return restartAllContainers(ctx, manager)
	}

	// Restart single container
	containerName := args[0]
	err = manager.RestartContainer(ctx, containerName)
	if err != nil {
		return err
	}

	fmt.Printf("Container %s restarted successfully\n", containerName)
	return nil
}

func restartAllContainers(ctx context.Context, manager lifecycle.Manager) error {
	// Get all containers (including stopped ones since restart can start stopped containers)
	containers, err := manager.ListContainers(ctx, true)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	if len(containers) == 0 {
		fmt.Println("No MCP servers found to restart")
		return nil
	}

	var restartedCount int
	var failedCount int
	var errors []string

	fmt.Printf("Restarting %d MCP server(s)...\n", len(containers))

	for _, container := range containers {
		// Get container name from labels
		containerName := labels.GetContainerName(container.Labels)
		if containerName == "" {
			containerName = container.Name // Fallback to container name
		}

		fmt.Printf("Restarting %s...", containerName)
		err := manager.RestartContainer(ctx, containerName)
		if err != nil {
			fmt.Printf(" failed: %v\n", err)
			failedCount++
			errors = append(errors, fmt.Sprintf("%s: %v", containerName, err))
		} else {
			fmt.Printf(" success\n")
			restartedCount++
		}
	}

	// Print summary
	fmt.Printf("\nRestart summary: %d succeeded, %d failed\n", restartedCount, failedCount)

	if failedCount > 0 {
		fmt.Println("\nFailed restarts:")
		for _, errMsg := range errors {
			fmt.Printf("  - %s\n", errMsg)
		}
		return fmt.Errorf("%d container(s) failed to restart", failedCount)
	}

	return nil
}
