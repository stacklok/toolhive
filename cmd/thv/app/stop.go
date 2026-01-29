// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/workloads"
	"github.com/stacklok/toolhive/pkg/workloads/types"
)

var stopCmd = &cobra.Command{
	Use:   "stop [workload-name...]",
	Short: "Stop one or more MCP servers",
	Long: `Stop one or more running MCP servers managed by ToolHive. Examples:
  # Stop a single MCP server
  thv stop filesystem

  # Stop multiple MCP servers
  thv stop filesystem github slack

  # Stop all running MCP servers
  thv stop --all

  # Stop all servers in a group
  thv stop --group production`,
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
	AddAllFlag(stopCmd, &stopAll, true, "Stop all running MCP servers")
	AddGroupFlag(stopCmd, &stopGroup, true)

	// Mark the flags as mutually exclusive
	stopCmd.MarkFlagsMutuallyExclusive("all", "group")

	stopCmd.PreRunE = validateGroupFlag()
}

// validateStopArgs validates the arguments for the stop command
func validateStopArgs(cmd *cobra.Command, args []string) error {
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
					"Hint: use 'thv list' to see available workloads, or use --all to stop all")
		}
	}

	return nil
}

func stopCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload manager: %w", err)
	}

	if stopAll {
		return stopAllWorkloads(ctx, workloadManager)
	}

	if stopGroup != "" {
		return stopWorkloadsByGroup(ctx, workloadManager, stopGroup)
	}

	// Stop specified workloads
	workloadNames := args
	complete, err := workloadManager.StopWorkloads(ctx, workloadNames)
	if err != nil {
		// If the workload is not found or not running, treat as a non-fatal error.
		if errors.Is(err, rt.ErrWorkloadNotFound) ||
			errors.Is(err, workloads.ErrWorkloadNotRunning) ||
			errors.Is(err, types.ErrInvalidWorkloadName) {
			fmt.Println("one or more workloads are not running")
			return nil
		}
		return fmt.Errorf("unexpected error stopping workloads: %w", err)
	}

	// Wait for the stop operation to complete
	if err := complete(); err != nil {
		return fmt.Errorf("failed to stop workloads %v: %w", workloadNames, err)
	}

	return nil
}

func stopAllWorkloads(ctx context.Context, workloadManager workloads.Manager) error {
	// Get list of all running workloads first
	workloadList, err := workloadManager.ListWorkloads(ctx, false) // false = only running workloads
	if err != nil {
		return fmt.Errorf("failed to list workloads: %w", err)
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
	complete, err := workloadManager.StopWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to stop all workloads: %w", err)
	}

	// Wait for the stop operation to complete
	if err := complete(); err != nil {
		return fmt.Errorf("failed to stop all workloads: %w", err)
	}
	return nil
}

func stopWorkloadsByGroup(ctx context.Context, workloadManager workloads.Manager, groupName string) error {
	// Create a groups manager to list workloads in the group
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %w", err)
	}

	// Check if the group exists
	exists, err := groupManager.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group '%s' exists: %w", groupName, err)
	}
	if !exists {
		return fmt.Errorf("group '%s' does not exist. Hint: use 'thv group list' to see available groups", groupName)
	}

	// Get list of running workloads and filter by group
	workloadList, err := workloadManager.ListWorkloads(ctx, false) // false = only running workloads
	if err != nil {
		return fmt.Errorf("failed to list running workloads: %w", err)
	}

	// Filter workloads by group
	groupWorkloads, err := workloads.FilterByGroup(workloadList, groupName)
	if err != nil {
		return fmt.Errorf("failed to filter workloads by group: %w", err)
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
	complete, err := workloadManager.StopWorkloads(ctx, workloadNames)
	if err != nil {
		return fmt.Errorf("failed to stop workloads in group '%s': %w", groupName, err)
	}

	// Wait for the stop operation to complete
	if err := complete(); err != nil {
		return fmt.Errorf("failed to stop workloads in group '%s': %w", groupName, err)
	}

	return nil
}
