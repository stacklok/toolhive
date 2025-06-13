package app

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/workloads"
)

var stopCmd = &cobra.Command{
	Use:   "stop [container-name]",
	Short: "Stop an MCP server",
	Long:  `Stop a running MCP server managed by ToolHive.`,
	Args:  validateStopArgs,
	RunE:  stopCmdFunc,
}

var (
	stopTimeout int
	stopAll     bool
)

func init() {
	stopCmd.Flags().IntVar(&stopTimeout, "timeout", 30, "Timeout in seconds before forcibly stopping the container")
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "Stop all running MCP servers")
}

// validateStopArgs validates the arguments for the stop command
func validateStopArgs(cmd *cobra.Command, args []string) error {
	// Check if --all flag is set
	all, _ := cmd.Flags().GetBool("all")

	if all {
		// If --all is set, no arguments should be provided
		if len(args) > 0 {
			return fmt.Errorf("no arguments should be provided when --all flag is set")
		}
	} else {
		// If --all is not set, exactly one argument should be provided
		if len(args) != 1 {
			return fmt.Errorf("exactly one container name must be provided")
		}
	}

	return nil
}

func stopCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container manager: %v", err)
	}

	var group *errgroup.Group

	// Check if --all flag is set
	if stopAll {
		// Stop all workloads
		group, err = manager.StopAllWorkloads(ctx)
		if err != nil {
			return fmt.Errorf("failed to stop all containers: %v", err)
		}

		// Since the stop operation is asynchronous, wait for the group to finish.
		if err := group.Wait(); err != nil {
			return fmt.Errorf("failed to stop all containers: %v", err)
		}
		fmt.Println("All containers stopped successfully")
	} else {
		// Get container name
		containerName := args[0]

		// Stop a single workload
		group, err = manager.StopWorkload(ctx, containerName)
		if err != nil {
			// If the container is not found or not running, treat as a non-fatal error.
			if errors.Is(err, workloads.ErrContainerNotFound) || errors.Is(err, workloads.ErrContainerNotRunning) {
				fmt.Printf("Container %s is not running\n", containerName)
				return nil
			}
			return fmt.Errorf("unexpected error stopping container: %v", err)
		}

		// Since the stop operation is asynchronous, wait for the group to finish.
		if err := group.Wait(); err != nil {
			return fmt.Errorf("failed to stop container %s: %v", containerName, err)
		}
		fmt.Printf("Container %s stopped successfully\n", containerName)
	}

	return nil
}
