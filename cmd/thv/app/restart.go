package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	restartAll bool
)

var restartCmd = &cobra.Command{
	Use:               "restart [workload-name]",
	Short:             "Restart a tooling server",
	Long:              `Restart a running tooling server managed by ToolHive. If the server is not running, it will be started.`,
	Args:              cobra.RangeArgs(0, 1),
	RunE:              restartCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

func init() {
	restartCmd.Flags().BoolVarP(&restartAll, "all", "a", false, "Restart all MCP servers")
}

func restartCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Validate arguments
	if restartAll && len(args) > 0 {
		return fmt.Errorf("cannot specify both --all flag and workload name")
	}
	if !restartAll && len(args) == 0 {
		return fmt.Errorf("must specify either --all flag or workload name")
	}

	// Create workload managers.
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}

	if restartAll {
		return restartAllContainers(ctx, workloadManager)
	}

	// Restart single workload
	workloadName := args[0]
	restartGroup, err := workloadManager.RestartWorkloads(ctx, []string{workloadName})
	if err != nil {
		return err
	}

	// Wait for the restart group to complete
	if err := restartGroup.Wait(); err != nil {
		return fmt.Errorf("failed to restart workload %s: %v", workloadName, err)
	}

	fmt.Printf("Container %s restarted successfully\n", workloadName)
	return nil
}

func restartAllContainers(ctx context.Context, workloadManager workloads.Manager) error {
	// Get all containers (including stopped ones since restart can start stopped containers)
	allWorkloads, err := workloadManager.ListWorkloads(ctx, true)
	if err != nil {
		return fmt.Errorf("failed to list allWorkloads: %v", err)
	}

	if len(allWorkloads) == 0 {
		fmt.Println("No MCP servers found to restart")
		return nil
	}

	var restartedCount int
	var failedCount int
	var errors []string

	fmt.Printf("Restarting %d MCP server(s)...\n", len(allWorkloads))

	var restartRequests []*errgroup.Group
	// First, trigger the restarts concurrently.
	for _, workload := range allWorkloads {
		workloadName := workload.Name
		fmt.Printf("Restarting %s...", workloadName)
		restart, err := workloadManager.RestartWorkloads(ctx, []string{workloadName})
		if err != nil {
			fmt.Printf(" failed: %v\n", err)
			failedCount++
			errors = append(errors, fmt.Sprintf("%s: %v", workloadName, err))
		} else {
			// If it didn't fail during the synchronous part of the operation,
			// append to the list of restart requests in flight.
			restartRequests = append(restartRequests, restart)
		}
	}

	// Wait for all restarts to complete.
	for _, restart := range restartRequests {
		err = restart.Wait()
		if err != nil {
			fmt.Printf(" failed: %v\n", err)
			failedCount++
			// Unfortunately we don't have the workload name here, so we just log a generic error.
			errors = append(errors, fmt.Sprintf("Error restarting workload: %v", err))
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
		return fmt.Errorf("%d workload(s) failed to restart", failedCount)
	}

	return nil
}
