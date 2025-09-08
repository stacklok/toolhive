package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var rmCmd = &cobra.Command{
	Use:               "rm [workload-name]",
	Short:             "Remove an MCP server",
	Long:              `Remove an MCP server managed by ToolHive.`,
	Args:              validateRmArgs,
	RunE:              rmCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

var (
	rmAll   bool
	rmGroup string
)

func init() {
	rmCmd.Flags().BoolVar(&rmAll, "all", false, "Delete all running MCP servers")
	rmCmd.Flags().StringVarP(&rmGroup, "group", "g", "", "Delete all MCP servers in a specific group")

	// Mark the flags as mutually exclusive
	rmCmd.MarkFlagsMutuallyExclusive("all", "group")

	rmCmd.PreRunE = validateGroupFlag()
}

// validateStopArgs validates the arguments for the remove command
func validateRmArgs(cmd *cobra.Command, args []string) error {
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

//nolint:gocyclo // This function is complex but manageable
func rmCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	if rmAll {
		return deleteAllWorkloads(ctx)
	}

	if rmGroup != "" {
		return deleteAllWorkloadsInGroup(ctx, rmGroup)
	}

	// Stop single workload
	workloadName := args[0]
	// Create workload manager.
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}
	// Delete workload.
	group, err := manager.DeleteWorkloads(ctx, []string{workloadName})
	if err != nil {
		return fmt.Errorf("failed to delete workload: %v", err)
	}

	// Wait for the deletion to complete.
	if err := group.Wait(); err != nil {
		return fmt.Errorf("failed to delete workload: %v", err)
	}

	fmt.Printf("Container %s removed successfully\n", workloadName)
	return nil
}

func deleteAllWorkloads(ctx context.Context) error {

	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}

	// Get list of all running workloads first
	workloadList, err := workloadManager.ListWorkloads(ctx, true) // true = all workloads
	if err != nil {
		return fmt.Errorf("failed to list workloads: %v", err)
	}

	// Extract workload names
	var workloadNames []string
	for _, workload := range workloadList {
		workloadNames = append(workloadNames, workload.Name)
	}

	if len(workloadNames) == 0 {
		fmt.Println("No running workloads to delete")
		return nil
	}

	// Stop all workloads using the bulk method
	group, err := workloadManager.DeleteWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to delete all workloads: %v", err)
	}

	// Since the stop operation is asynchronous, wait for the group to finish.
	if err := group.Wait(); err != nil {
		return fmt.Errorf("failed to delete all workloads: %v", err)
	}

	fmt.Println("All workloads stopped successfully")
	return nil
}

func deleteAllWorkloadsInGroup(ctx context.Context, groupName string) error {
	// Create group manager
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %v", err)
	}

	// Check if group exists
	exists, err := groupManager.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %v", err)
	}
	if !exists {
		return fmt.Errorf("group '%s' does not exist", groupName)
	}

	// Create workload manager
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}

	// Get all workloads in the group
	groupWorkloads, err := workloadManager.ListWorkloadsInGroup(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to list workloads in group: %v", err)
	}

	if len(groupWorkloads) == 0 {
		fmt.Printf("No workloads found in group '%s'\n", groupName)
		return nil
	}

	// Delete all workloads in the group
	group, err := workloadManager.DeleteWorkloads(ctx, groupWorkloads)
	if err != nil {
		return fmt.Errorf("failed to delete workloads in group: %v", err)
	}

	// Wait for the deletion to complete
	if err := group.Wait(); err != nil {
		return fmt.Errorf("failed to delete workloads in group: %v", err)
	}

	fmt.Printf("Successfully removed %d workload(s) from group '%s'\n", len(groupWorkloads), groupName)
	return nil
}
