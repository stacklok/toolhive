// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var rmCmd = &cobra.Command{
	Use:   "rm [workload-name...]",
	Short: "Remove one or more MCP servers",
	Long: `Remove one or more MCP servers managed by ToolHive. 
Examples:
  # Remove a single MCP server
  thv rm filesystem

  # Remove multiple MCP servers
  thv rm filesystem github slack

  # Remove all workloads
  thv rm --all

  # Remove all workloads in a group
  thv rm --group production`,
	Args:              validateRmArgs,
	RunE:              rmCmdFunc,
	ValidArgsFunction: completeMCPServerNames,
}

var (
	rmAll   bool
	rmGroup string
)

func init() {
	AddAllFlag(rmCmd, &rmAll, false, "Delete all workloads")
	AddGroupFlag(rmCmd, &rmGroup, true)

	// Mark the flags as mutually exclusive
	rmCmd.MarkFlagsMutuallyExclusive("all", "group")

	rmCmd.PreRunE = validateGroupFlag()
}

// validateRmArgs validates the arguments for the remove command
func validateRmArgs(cmd *cobra.Command, args []string) error {
	// Check if --all or --group flags are set
	all, _ := cmd.Flags().GetBool("all")
	group, _ := cmd.Flags().GetString("group")

	if all || group != "" {
		// If --all or --group is set, no arguments should be provided
		if len(args) > 0 {
			return fmt.Errorf(
				"no arguments should be provided when --all or --group flag is set. " +
					"Hint: remove the workload names or remove the flag")
		}
	} else {
		// If neither --all nor --group is set, at least one argument should be provided
		if len(args) < 1 {
			return fmt.Errorf(
				"at least one workload name must be provided. " +
					"Hint: use 'thv list' to see available workloads, or use --all to remove all")
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

	// Delete specified workloads
	workloadNames := args
	// Create workload manager.
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}
	// Delete workloads.
	complete, err := manager.DeleteWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to delete workloads: %w", err)
	}

	// Wait for the deletion to complete
	if err := complete(); err != nil {
		return fmt.Errorf("failed to delete workloads: %w", err)
	}

	if len(workloadNames) == 1 {
		fmt.Printf("Workload %s removed successfully\n", workloadNames[0])
	} else {
		formattedNames := strings.Join(workloadNames, ", ")
		fmt.Printf("Workloads %s removed successfully\n", formattedNames)
	}
	return nil
}

func deleteAllWorkloads(ctx context.Context) error {

	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	// List all workloads
	workloadList, err := workloadManager.ListWorkloads(ctx, true) // true = all workloads
	if err != nil {
		return fmt.Errorf("failed to list workloads: %w", err)
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

	// Delete all workloads
	complete, err := workloadManager.DeleteWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to delete all workloads: %w", err)
	}

	// Wait for the deletion to complete
	if err := complete(); err != nil {
		return fmt.Errorf("failed to delete all workloads: %w", err)
	}

	fmt.Println("All workloads deleted successfully")
	return nil
}

func deleteAllWorkloadsInGroup(ctx context.Context, groupName string) error {
	// Create group manager
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
		return fmt.Errorf("group '%s' does not exist. Hint: use 'thv group list' to see available groups", groupName)
	}

	// Create workload manager
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Get all workloads in the group
	groupWorkloads, err := workloadManager.ListWorkloadsInGroup(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to list workloads in group: %w", err)
	}

	if len(groupWorkloads) == 0 {
		fmt.Printf("No workloads found in group '%s'\n", groupName)
		return nil
	}

	// Delete all workloads in the group
	complete, err := workloadManager.DeleteWorkloads(ctx, groupWorkloads)
	if err != nil {
		return fmt.Errorf("failed to delete workloads in group: %w", err)
	}

	// Wait for the deletion to complete
	if err := complete(); err != nil {
		return fmt.Errorf("failed to delete workloads in group: %w", err)
	}

	fmt.Printf("Successfully removed %d workload(s) from group '%s'\n", len(groupWorkloads), groupName)
	return nil
}
