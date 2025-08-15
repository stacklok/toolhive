package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/client"
	runtime "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/validation"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Manage logical groupings of MCP servers",
	Long:  `The group command provides subcommands to manage logical groupings of MCP servers.`,
}

var groupCreateCmd = &cobra.Command{
	Use:   "create [group-name]",
	Short: "Create a new group of MCP servers",
	Long: `Create a new logical group of MCP servers.
		 The group can be used to organize and manage multiple MCP servers together.`,
	Args:    cobra.ExactArgs(1),
	PreRunE: validateGroupArg(),
	RunE:    groupCreateCmdFunc,
}

var groupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all groups",
	Long:  `List all logical groups of MCP servers.`,
	RunE:  groupListCmdFunc,
}

var groupRmCmd = &cobra.Command{
	Use:   "rm [group-name]",
	Short: "Remove a group and remove workloads from it",
	Long: "Remove a group and remove all MCP servers from it. By default, this only removes the group " +
		"membership from workloads without deleting them. Use --with-workloads to also delete the workloads. ",
	Args:    cobra.ExactArgs(1),
	PreRunE: validateGroupArg(),
	RunE:    groupRmCmdFunc,
}

func validateGroupArg() func(cmd *cobra.Command, args []string) error {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("group name is required")
		}
		if err := validation.ValidateGroupName(args[0]); err != nil {
			return fmt.Errorf("invalid group name: %w", err)
		}
		return nil
	}
}

var withWorkloadsFlag bool

func groupCreateCmdFunc(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	ctx := cmd.Context()

	manager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	if err := manager.Create(ctx, groupName); err != nil {
		return err
	}

	fmt.Printf("Group '%s' created successfully.\n", groupName)
	return nil
}

func groupListCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	manager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	allGroups, err := manager.List(ctx)
	if err != nil {
		return fmt.Errorf("failed to list groups: %w", err)
	}

	if len(allGroups) == 0 {
		fmt.Println("No groups configured.")
		return nil
	}

	// Create a tabwriter for table output
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME")

	// Print group names in table format
	for _, group := range allGroups {
		fmt.Fprintf(w, "%s\n", group.Name)
	}

	// Flush the tabwriter
	if err := w.Flush(); err != nil {
		return fmt.Errorf("failed to flush tabwriter: %w", err)
	}

	return nil
}

func groupRmCmdFunc(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	ctx := cmd.Context()

	if strings.EqualFold(groupName, groups.DefaultGroup) {
		return fmt.Errorf("cannot delete the %s group", groups.DefaultGroup)
	}
	manager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	// Check if group exists
	exists, err := manager.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("group '%s' does not exist", groupName)
	}

	// Create workloads manager
	workloadsManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workloads manager: %w", err)
	}

	// Get all workloads and filter for the group
	allWorkloads, err := workloadsManager.ListWorkloads(ctx, true) // listAll=true to include stopped workloads
	if err != nil {
		return fmt.Errorf("failed to list workloads: %w", err)
	}

	groupWorkloads, err := workloads.FilterByGroup(allWorkloads, groupName)
	if err != nil {
		return fmt.Errorf("failed to filter workloads by group: %w", err)
	}

	// Show warning and get user confirmation
	confirmed, err := showWarningAndGetConfirmation(groupName, groupWorkloads)
	if err != nil {
		return err
	}

	if !confirmed {
		return nil
	}

	// Handle workloads if any exist
	if len(groupWorkloads) > 0 {
		if withWorkloadsFlag {
			err = deleteWorkloadsInGroup(ctx, workloadsManager, groupWorkloads, groupName)
		} else {
			err = moveWorkloadsToGroup(ctx, workloadsManager, groupWorkloads, groupName, groups.DefaultGroup)
		}
	}
	if err != nil {
		return err
	}

	if err = manager.Delete(ctx, groupName); err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}

	fmt.Printf("Group '%s' deleted successfully\n", groupName)
	return nil
}

func showWarningAndGetConfirmation(groupName string, groupWorkloads []core.Workload) (bool, error) {
	if len(groupWorkloads) == 0 {
		return true, nil
	}

	// Show warning and get user confirmation
	if withWorkloadsFlag {
		fmt.Printf("⚠️  WARNING: This will delete group '%s' and DELETE all workloads belonging to it.\n", groupName)
	} else {
		fmt.Printf("⚠️  WARNING: This will delete group '%s' and move all workloads to the 'default' group\n", groupName)
	}

	fmt.Printf("   The following %d workload(s) will be affected:\n", len(groupWorkloads))
	for _, workload := range groupWorkloads {
		if withWorkloadsFlag {
			fmt.Printf("   - %s (will be DELETED)\n", workload.Name)
		} else {
			fmt.Printf("   - %s (will be moved to the 'default' group)\n", workload.Name)
		}
	}

	if withWorkloadsFlag {
		fmt.Printf("\nThis action cannot be undone. Are you sure you want to continue? [y/N]: ")
	} else {
		fmt.Printf("\nAre you sure you want to continue? [y/N]: ")
	}

	// Read user input
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("failed to read user input: %w", err)
	}

	// Check if user confirmed
	response = strings.TrimSpace(strings.ToLower(response))
	if response != "y" && response != "yes" {
		fmt.Println("Group deletion cancelled.")
		return false, nil
	}

	return true, nil
}

func deleteWorkloadsInGroup(
	ctx context.Context,
	workloadManager workloads.Manager,
	groupWorkloads []core.Workload,
	groupName string,
) error {
	// Extract workload names for deletion
	var workloadNames []string
	for _, workload := range groupWorkloads {
		workloadNames = append(workloadNames, workload.Name)
	}

	// Delete all workloads in the group
	group, err := workloadManager.DeleteWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to delete workloads in group: %w", err)
	}

	// Wait for the deletion to complete
	if err := group.Wait(); err != nil {
		return fmt.Errorf("failed to delete workloads in group: %w", err)
	}

	fmt.Printf("Deleted %d workload(s) from group '%s'\n", len(groupWorkloads), groupName)
	return nil
}

// moveWorkloadsToGroup moves all workloads in the specified group to a new group.
func moveWorkloadsToGroup(
	ctx context.Context,
	workloadManager workloads.Manager,
	groupWorkloads []core.Workload,
	groupFrom string,
	groupTo string,
) error {

	// Extract workload names for the move operation
	var workloadNames []string
	for _, workload := range groupWorkloads {
		workloadNames = append(workloadNames, workload.Name)
	}

	// Update workload runconfigs to point to the new group
	if err := workloadManager.MoveToGroup(ctx, workloadNames, groupFrom, groupTo); err != nil {
		return fmt.Errorf("failed to move workloads to default group: %w", err)
	}

	// Update client configurations for the moved workloads
	err := updateClientConfigurations(ctx, groupWorkloads, groupFrom, groupTo)
	if err != nil {
		return fmt.Errorf("failed to update client configurations with new group: %w", err)
	}

	fmt.Printf("Moved %d workload(s) from group '%s' to group '%s'\n", len(groupWorkloads), groupFrom, groupTo)
	return nil
}

func updateClientConfigurations(ctx context.Context, groupWorkloads []core.Workload, groupFrom string, groupTo string) error {
	clientManager, err := client.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create client manager: %w", err)
	}

	for _, w := range groupWorkloads {
		// Only update client configurations for running workloads
		if w.Status != runtime.WorkloadStatusRunning {
			continue
		}

		if err := clientManager.RemoveServerFromClients(ctx, w.Name, groupFrom); err != nil {
			return fmt.Errorf("failed to remove server %s from client configurations: %w", w.Name, err)
		}
		if err := clientManager.AddServerToClients(ctx, w.Name, w.URL, string(w.TransportType), groupTo); err != nil {
			return fmt.Errorf("failed to add server %s to client configurations: %w", w.Name, err)
		}
	}

	return nil
}

func init() {
	groupCmd.AddCommand(groupCreateCmd)
	groupCmd.AddCommand(groupListCmd)
	groupCmd.AddCommand(groupRmCmd)

	// Add --with-workloads flag to group rm command
	groupRmCmd.Flags().BoolVar(&withWorkloadsFlag, "with-workloads", false,
		"Delete all workloads in the group along with the group")
}
