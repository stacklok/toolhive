package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	restartAll        bool
	restartGroup      string
	restartForeground bool
)

var restartCmd = &cobra.Command{
	Use:               "restart [workload-name]",
	Short:             "Restart a tooling server",
	Long:              `Restart a running tooling server managed by ToolHive. If the server is not running, it will be started. Supports both container-based and remote MCP servers.`,
	Args:              cobra.RangeArgs(0, 1),
	RunE:              restartCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

func init() {
	restartCmd.Flags().BoolVarP(&restartAll, "all", "a", false, "Restart all MCP servers")
	restartCmd.Flags().BoolVarP(&restartForeground, "foreground", "f", false, "Run the restarted workload in foreground mode")
	restartCmd.Flags().StringVarP(&restartGroup, "group", "g", "", "Restart all MCP servers in a specific group")

	// Mark the flags as mutually exclusive
	restartCmd.MarkFlagsMutuallyExclusive("all", "group")

	restartCmd.PreRunE = validateGroupFlag()
}

func restartCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Validate arguments - check mutual exclusivity with positional arguments
	// Cobra already handles mutual exclusivity between --all and --group
	if (restartAll || restartGroup != "") && len(args) > 0 {
		return fmt.Errorf("cannot specify both flags and workload name")
	}

	if !restartAll && restartGroup == "" && len(args) == 0 {
		return fmt.Errorf("must specify either --all flag, --group flag, or workload name")
	}

	// Create workload managers.
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}

	if restartAll {
		return restartAllContainers(ctx, workloadManager, restartForeground)
	}

	if restartGroup != "" {
		return restartWorkloadsByGroup(ctx, workloadManager, restartGroup, restartForeground)
	}

	// Restart single workload
	workloadName := args[0]
	restartGroup, err := workloadManager.RestartWorkloads(ctx, []string{workloadName}, restartForeground)
	if err != nil {
		return err
	}

	// Wait for the restart group to complete
	if err := restartGroup.Wait(); err != nil {
		return fmt.Errorf("failed to restart workload %s: %v", workloadName, err)
	}

	fmt.Printf("Workload %s restarted successfully\n", workloadName)
	return nil
}

func restartAllContainers(ctx context.Context, workloadManager workloads.Manager, foreground bool) error {
	// Get all containers (including stopped ones since restart can start stopped containers)
	allWorkloads, err := workloadManager.ListWorkloads(ctx, true)
	if err != nil {
		return fmt.Errorf("failed to list allWorkloads: %v", err)
	}

	if len(allWorkloads) == 0 {
		fmt.Println("No workloads found to restart")
		return nil
	}

	// Extract workload names
	workloadNames := make([]string, len(allWorkloads))
	for i, workload := range allWorkloads {
		workloadNames[i] = workload.Name
	}

	return restartMultipleWorkloads(ctx, workloadManager, workloadNames, foreground)
}

func restartWorkloadsByGroup(ctx context.Context, workloadManager workloads.Manager, groupName string, foreground bool) error {
	// Create a groups manager to list workloads in the group
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %v", err)
	}

	// Check if the group exists
	exists, err := groupManager.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group '%s' exists: %v", groupName, err)
	}
	if !exists {
		return fmt.Errorf("group '%s' does not exist", groupName)
	}

	// Get all workload names in the group
	workloadNames, err := workloadManager.ListWorkloadsInGroup(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to list workloads in group '%s': %v", groupName, err)
	}

	if len(workloadNames) == 0 {
		fmt.Printf("No workloads found in group '%s' to restart\n", groupName)
		return nil
	}

	return restartMultipleWorkloads(ctx, workloadManager, workloadNames, foreground)
}

// restartMultipleWorkloads handles restarting multiple workloads and reporting results
func restartMultipleWorkloads(
	ctx context.Context,
	workloadManager workloads.Manager,
	workloadNames []string,
	foreground bool,
) error {
	restartedCount := 0
	failedCount := 0
	var errors []string

	fmt.Printf("Restarting %d workload(s)...\n", len(workloadNames))

	var restartRequests []*errgroup.Group
	// First, trigger the restarts concurrently.
	for _, workloadName := range workloadNames {
		fmt.Printf("Restarting %s...", workloadName)
		restart, err := workloadManager.RestartWorkloads(ctx, []string{workloadName}, foreground)
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
		err := restart.Wait()
		if err != nil {
			fmt.Printf(" failed: %v\n", err)
			failedCount++
			// Unfortunately we don't have the workload name here, so we just log a generic error.
			errors = append(errors, fmt.Sprintf("Error restarting workload: %v", err))
		} else {
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
