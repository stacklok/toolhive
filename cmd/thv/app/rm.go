package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/workloads"
)

var rmCmd = &cobra.Command{
	Use:               "rm [workload-name]",
	Short:             "Remove an MCP server",
	Long:              `Remove an MCP server managed by ToolHive.`,
	Args:              cobra.ExactArgs(1),
	RunE:              rmCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

//nolint:gocyclo // This function is complex but manageable
func rmCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	// Get workload name
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
