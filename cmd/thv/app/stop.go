package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/workloads"
	"github.com/stacklok/toolhive/pkg/workloads/types"
)

var stopCmd = &cobra.Command{
	Use:               "stop [workload-name]",
	Short:             "Stop an MCP server",
	Long:              `Stop a running MCP server managed by ToolHive.`,
	Args:              validateStopArgs,
	RunE:              stopCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

var (
	stopTimeout int
	stopAll     bool
	stopGroup   string
)

func init() {
	stopCmd.Flags().IntVar(&stopTimeout, "timeout", 30, "Timeout in seconds before forcibly stopping the workload")
	stopCmd.Flags().BoolVar(&stopAll, "all", false, "Stop all running MCP servers")
	// TODO: Re-enable group flag when groups are implemented
	//stopCmd.Flags().StringVarP(&stopGroup, "group", "g", "", "Stop all MCP servers in a specific group")
	//
	//// Mark the flags as mutually exclusive
	//stopCmd.MarkFlagsMutuallyExclusive("all", "group")
	//
	//stopCmd.PreRunE = validateGroupFlag()
}

// validateStopArgs validates the arguments for the stop command
func validateStopArgs(cmd *cobra.Command, args []string) error {
	// Check if --all or --group flags are set
	all, _ := cmd.Flags().GetBool("all")
	group, _ := cmd.Flags().GetString("group")

	if all || group != "" {
		// If --all or --group is set, no arguments should be provided
		if len(args) > 0 {
			return fmt.Errorf("no arguments should be provided when --all or --group flag is set")
		}
	} else {
		// If neither --all nor --group is set, exactly one argument should be provided
		if len(args) != 1 {
			return fmt.Errorf("exactly one workload name must be provided")
		}
	}

	return nil
}

func stopCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}

	var group *errgroup.Group

	if stopAll {
		return stopAllWorkloads(ctx, workloadManager)
	}

	if stopGroup != "" {
		return stopWorkloadsByGroup(ctx, workloadManager, stopGroup)
	}

	// Stop single workload
	workloadName := args[0]
	group, err = workloadManager.StopWorkloads(ctx, []string{workloadName})
	if err != nil {
		// If the workload is not found or not running, treat as a non-fatal error.
		if errors.Is(err, rt.ErrWorkloadNotFound) ||
			errors.Is(err, workloads.ErrWorkloadNotRunning) ||
			errors.Is(err, types.ErrInvalidWorkloadName) {
			fmt.Printf("workload %s is not running\n", workloadName)
			return nil
		}
		return fmt.Errorf("unexpected error stopping workload: %v", err)
	}

	// Since the stop operation is asynchronous, wait for the group to finish.
	if err := group.Wait(); err != nil {
		return fmt.Errorf("failed to stop workload %s: %v", workloadName, err)
	}
	fmt.Printf("workload %s stopped successfully\n", workloadName)

	return nil
}

func stopAllWorkloads(ctx context.Context, workloadManager workloads.Manager) error {
	// Get list of all running workloads first
	workloadList, err := workloadManager.ListWorkloads(ctx, false) // false = only running workloads
	if err != nil {
		return fmt.Errorf("failed to list workloads: %v", err)
	}

	// Extract workload names
	var workloadNames []string
	for _, workload := range workloadList {
		workloadNames = append(workloadNames, workload.Name)
	}

	if len(workloadNames) == 0 {
		fmt.Println("No running workloads to stop")
		return nil
	}

	// Stop all workloads using the bulk method
	group, err := workloadManager.StopWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to stop all workloads: %v", err)
	}

	// Since the stop operation is asynchronous, wait for the group to finish.
	if err := group.Wait(); err != nil {
		return fmt.Errorf("failed to stop all workloads: %v", err)
	}
	fmt.Println("All workloads stopped successfully")
	return nil
}

func stopWorkloadsByGroup(ctx context.Context, workloadManager workloads.Manager, groupName string) error {
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

	// Get list of running workloads and filter by group
	workloadList, err := workloadManager.ListWorkloads(ctx, false) // false = only running workloads
	if err != nil {
		return fmt.Errorf("failed to list running workloads: %v", err)
	}

	// Filter workloads by group
	groupWorkloads, err := workloads.FilterByGroup(workloadList, groupName)
	if err != nil {
		return fmt.Errorf("failed to filter workloads by group: %v", err)
	}

	if len(groupWorkloads) == 0 {
		fmt.Printf("No running MCP servers found in group '%s'\n", groupName)
		return nil
	}

	// Extract workload names from the filtered list
	var workloadNames []string
	for _, workload := range groupWorkloads {
		workloadNames = append(workloadNames, workload.Name)
	}

	// Stop workloads in the group
	subtasks, err := workloadManager.StopWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to stop workloads in group '%s': %v", groupName, err)
	}

	// Wait for the stop operation to complete
	if err := subtasks.Wait(); err != nil {
		return fmt.Errorf("failed to stop workloads in group '%s': %v", groupName, err)
	}

	fmt.Printf("Successfully stopped %d workload(s) in group '%s'\n", len(workloadNames), groupName)
	return nil
}
