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
	Args:              cobra.MaximumNArgs(1),
	RunE:              rmCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

func init() {
	rmCmd.Flags().String("group", "", "Remove all workloads in the specified group")
}

//nolint:gocyclo // This function is complex but manageable
func rmCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Check if group flag is provided
	groupName, _ := cmd.Flags().GetString("group")

	if groupName != "" {
		// Remove all workloads in the specified group
		return removeWorkloadsInGroup(ctx, groupName)
	}

	// Original behavior: remove specific workload
	if len(args) == 0 {
		return fmt.Errorf("workload name is required when not using --group flag")
	}

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

func removeWorkloadsInGroup(ctx context.Context, groupName string) error {
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

	// Get all workloads in the group
	workloadsInGroup, err := groupManager.ListWorkloadsInGroup(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to list workloads in group: %v", err)
	}

	if len(workloadsInGroup) == 0 {
		fmt.Printf("No workloads found in group '%s'\n", groupName)
		return nil
	}

	// Create workload manager
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %v", err)
	}

	// Extract workload names
	var workloadNames []string
	for _, workload := range workloadsInGroup {
		workloadNames = append(workloadNames, workload.Name)
	}

	// Delete all workloads in the group
	group, err := workloadManager.DeleteWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to delete workloads in group: %v", err)
	}

	// Wait for the deletion to complete
	if err := group.Wait(); err != nil {
		return fmt.Errorf("failed to delete workloads in group: %v", err)
	}

	fmt.Printf("Successfully removed %d workload(s) from group '%s'\n", len(workloadNames), groupName)
	return nil
}
