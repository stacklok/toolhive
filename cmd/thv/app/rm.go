package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	rmAll   bool
	rmGroup string
)

var rmCmd = &cobra.Command{
	Use:               "rm [workload-name]",
	Short:             "Remove an MCP server",
	Long:              `Remove an MCP server managed by ToolHive.`,
	Args:              cobra.MaximumNArgs(1),
	RunE:              rmCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

func init() {
	rmCmd.Flags().BoolVar(&rmAll, "all", false, "Delete all worloads")
	rmCmd.Flags().StringVarP(&rmGroup, "group", "", "", "Delete all workloads in the specified group")

	rmCmd.PreRunE = validateGroupFlag()
}

//nolint:gocyclo // This function is complex but manageable
func rmCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	workloadName := ""
	if len(args) > 0 {
		workloadName = args[0]
	}

	if workloadName != "" && ( rmGroup != "" || rmAll ) {
		return fmt.Errorf("workload name and group name cannot be used together")
	}

	if rmAll {
		return deleteAllWorkloads(ctx)
	}

	if rmGroup != "" {
		// Delete all workloads in the specified group
		return deleteAllWorkloadsInGroup(ctx, rmGroup)
	}

	// Delete specific workload
	if workloadName == "" {
		return fmt.Errorf("workload name is required when not using --group flag")
	}

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

	// List all workloads
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
		fmt.Println("No workloads to delete")
		return nil
	}

	// Delete all workloads
	group, err := workloadManager.DeleteWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to delete all workloads: %v", err)
	}

	// Wait for the deletion to complete
	if err := group.Wait(); err != nil {
		return fmt.Errorf("failed to delete all workloads: %v", err)
	}

	fmt.Println("All workloads delete successfully")
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