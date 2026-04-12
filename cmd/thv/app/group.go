// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	groupval "github.com/stacklok/toolhive-core/validation/group"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// mcpOptimizerGroup is an internal group created by the UI to support the MCP optimizer feature.
const mcpOptimizerGroup = "__mcp-optimizer__"

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

var groupRunCmd = &cobra.Command{
	Use:   "run [group-name]",
	Short: "Deploy all MCP servers from a registry group",
	Long: `Deploy all MCP servers defined in a registry group.
		 This creates a new runtime group and starts all MCP servers within it.`,
	Args:    cobra.ExactArgs(1),
	PreRunE: validateGroupArg(),
	RunE:    groupRunCmdFunc,
}

func validateGroupArg() func(cmd *cobra.Command, args []string) error {
	return func(_ *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("group name is required. Hint: use 'thv group list' to see available groups")
		}
		if err := groupval.ValidateName(args[0]); err != nil {
			return fmt.Errorf("invalid group name: %w", err)
		}
		return nil
	}
}

var (
	withWorkloadsFlag bool
	groupSecrets      []string
	groupEnvVars      []string
)

func groupCreateCmdFunc(cmd *cobra.Command, args []string) error {
	groupName := args[0]
	ctx := cmd.Context()

	manager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	return manager.Create(ctx, groupName)
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
	if _, err := fmt.Fprintln(w, "NAME"); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}

	// Print group names in table format
	for _, group := range allGroups {
		// Hide the MCP optimizer internal group
		if group.Name == mcpOptimizerGroup {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s\n", group.Name); err != nil {
			slog.Debug(fmt.Sprintf("Failed to write group name: %v", err))
		}
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
		return fmt.Errorf(
			"cannot delete the %s group. "+
				"Hint: the 'default' group is reserved for workloads that are not assigned to any other group",
			groups.DefaultGroup)
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
		return fmt.Errorf("group '%s' does not exist. Hint: use 'thv group list' to see available groups", groupName)
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
			err = deleteWorkloadsInGroup(ctx, workloadsManager, groupWorkloads)
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
) error {
	// Extract workload names for deletion
	var workloadNames []string
	for _, workload := range groupWorkloads {
		workloadNames = append(workloadNames, workload.Name)
	}

	// Delete all workloads in the group
	complete, err := workloadManager.DeleteWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to delete workloads in group: %w", err)
	}

	// Wait for the deletion to complete
	if err := complete(); err != nil {
		return fmt.Errorf("failed to delete workloads in group: %w", err)
	}

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

func groupRunCmdFunc(_ *cobra.Command, _ []string) error {
	return fmt.Errorf("registry-based groups are no longer supported; use workload groups instead")
}

func init() {
	groupCmd.AddCommand(groupCreateCmd)
	groupCmd.AddCommand(groupListCmd)
	groupCmd.AddCommand(groupRmCmd)
	groupCmd.AddCommand(groupRunCmd)

	// Add --with-workloads flag to group rm command
	groupRmCmd.Flags().BoolVar(&withWorkloadsFlag, "with-workloads", false,
		"Delete all workloads in the group along with the group (default false)")

	// Add flags to group run command
	groupRunCmd.Flags().StringArrayVar(&groupSecrets, "secret", []string{},
		"Secrets to be fetched from the secrets manager and set as environment variables (format: NAME,target=SERVER_NAME.TARGET)")
	groupRunCmd.Flags().StringArrayVar(&groupEnvVars, "env", []string{},
		"Environment variables to pass to an MCP server in the group (format: SERVER_NAME.KEY=VALUE)")
}
