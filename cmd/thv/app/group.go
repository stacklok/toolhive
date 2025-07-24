package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/groups"
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
	Long:  `Create a new logical group of MCP servers. The group can be used to organize and manage multiple MCP servers together.`,
	Args:  cobra.ExactArgs(1),
	RunE:  groupCreateCmdFunc,
}

var groupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all groups",
	Long:  `List all logical groups of MCP servers.`,
	RunE:  groupListCmdFunc,
}

var groupDeleteCmd = &cobra.Command{
	Use:   "delete [group-name]",
	Short: "Delete a group and remove workloads from it",
	Long: "Delete a group and remove all MCP servers from it. By default, this only removes the group " +
		"membership from workloads without deleting them. Use --with-workloads to also delete the workloads. " +
		"The command will show a warning and require user confirmation before proceeding.",
	Args: cobra.ExactArgs(1),
	RunE: groupDeleteCmdFunc,
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

	// Sort groups alphanumerically by name (handles mixed characters, numbers, etc.)
	sort.Slice(allGroups, func(i, j int) bool {
		return strings.Compare(allGroups[i].Name, allGroups[j].Name) < 0
	})

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

func groupDeleteCmdFunc(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	ctx := cmd.Context()

	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	// Check if group exists
	exists, err := groupManager.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return fmt.Errorf("group '%s' does not exist", groupName)
	}

	// Get all workloads in the group
	workloadsInGroup, err := groupManager.ListWorkloadsInGroup(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to list workloads in group: %w", err)
	}

	// Show warning and get user confirmation
	confirmed, err := showWarningAndGetConfirmation(groupName, workloadsInGroup)
	if err != nil {
		return err
	}

	if !confirmed {
		return nil
	}

	// Handle workloads if any exist
	if len(workloadsInGroup) > 0 {
		if withWorkloadsFlag {
			err = deleteWorkloadsInGroup(ctx, workloadsInGroup, groupName)
		} else {
			err = removeWorkloadsMembershipFromGroup(ctx, workloadsInGroup, groupName)
		}
	}
	if err != nil {
		return err
	}

	if err = groupManager.Delete(ctx, groupName); err != nil {
		return fmt.Errorf("failed to delete group: %w", err)
	}

	fmt.Printf("Group '%s' deleted successfully\n", groupName)
	return nil
}

func showWarningAndGetConfirmation(groupName string, workloadsInGroup []*workloads.Workload) (bool, error) {
	// Show warning and get user confirmation
	if withWorkloadsFlag {
		fmt.Printf("⚠️  WARNING: This will delete group '%s' and DELETE all workloads belonging to it.\n", groupName)
	} else {
		fmt.Printf("⚠️  WARNING: This will delete group '%s' and remove all workloads from it "+
			"(workloads will not be deleted).\n", groupName)
	}

	if len(workloadsInGroup) > 0 {
		fmt.Printf("   The following %d workload(s) will be affected:\n", len(workloadsInGroup))
		for _, workload := range workloadsInGroup {
			if withWorkloadsFlag {
				fmt.Printf("   - %s (will be DELETED)\n", workload.Name)
			} else {
				fmt.Printf("   - %s (will be removed from group)\n", workload.Name)
			}
		}
	} else {
		fmt.Printf("   No workloads are currently in this group.\n")
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

func deleteWorkloadsInGroup(ctx context.Context, workloadsInGroup []*workloads.Workload, groupName string) error {
	// Delete workloads
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Extract workload names
	var workloadNames []string
	for _, workload := range workloadsInGroup {
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

	fmt.Printf("Deleted %d workload(s) from group '%s'\n", len(workloadNames), groupName)
	return nil
}

// removeWorkloadsFromGroup removes the group membership from the workloads
// in the group.
func removeWorkloadsMembershipFromGroup(ctx context.Context, workloadsInGroup []*workloads.Workload, groupName string) error {
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Extract workload names
	var workloadNames []string
	for _, workload := range workloadsInGroup {
		workloadNames = append(workloadNames, workload.Name)
	}

	// Remove group membership from all workloads
	if err := workloadManager.RemoveFromGroup(ctx, workloadNames, groupName); err != nil {
		return fmt.Errorf("failed to remove workloads from group: %w", err)
	}

	fmt.Printf("Removed %d workload(s) from group '%s'\n", len(workloadNames), groupName)
	return nil
}

func init() {
	groupCmd.AddCommand(groupCreateCmd)
	groupCmd.AddCommand(groupListCmd)
	groupCmd.AddCommand(groupDeleteCmd)

	// Add --with-workloads flag to group delete command
	groupDeleteCmd.Flags().BoolVar(&withWorkloadsFlag, "with-workloads", false,
		"Delete all workloads in the group along with the group")
}
