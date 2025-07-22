package app

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/labels"
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

// Shared flags for group add command (same as run command except --foreground)
var (
	groupAddConfig RunConfig
)

var groupAddCmd = &cobra.Command{
	Use:   "add [group-name] [flags] SERVER_OR_IMAGE_OR_PROTOCOL [-- ARGS...]",
	Short: "Add an MCP server to a group",
	Long: `Add an MCP server to a logical group. The server will be created in "Stopped" state and can be started later.

This command shares the same flags as 'thv run' except for --foreground, as the server is always created in stopped state.

Examples:
  # Add a server from registry to a group
  thv group add mygroup fetch

  # Add a custom image to a group
  thv group add mygroup ghcr.io/example/mcp-server:latest

  # Add a server with specific transport and port
  thv group add mygroup fetch --transport sse --proxy-port 8080`,
	Args: cobra.MinimumNArgs(2),
	RunE: groupAddCmdFunc,
	// Ignore unknown flags to allow passing flags to the MCP server
	FParseErrWhitelist: cobra.FParseErrWhitelist{
		UnknownFlags: true,
	},
}

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

func groupAddCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Validate arguments
	if len(args) < 2 {
		return fmt.Errorf("group add requires at least 2 arguments: group name and server/image")
	}

	groupName := args[0]
	serverOrImage := args[1]

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Process command arguments using os.Args to find everything after --
	cmdArgs := parseCommandArguments(os.Args)

	// Validate that the group exists
	groupManager, err := groups.NewManager()
	if err != nil {
		return fmt.Errorf("failed to create group manager: %v", err)
	}

	exists, err := groupManager.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %v", err)
	}
	if !exists {
		return fmt.Errorf("group '%s' does not exist", groupName)
	}

	// Set the group name in the config
	groupAddConfig.Group = groupName

	// Add the group label to the labels
	groupLabels := make([]string, len(groupAddConfig.Labels))
	copy(groupLabels, groupAddConfig.Labels)
	groupLabels = append(groupLabels, fmt.Sprintf("%s=%s", labels.LabelGroup, groupName))
	groupAddConfig.Labels = groupLabels

	// Build the run configuration using shared logic
	runnerConfig, err := BuildRunnerConfig(ctx, &groupAddConfig, serverOrImage, cmdArgs, debugMode)
	if err != nil {
		return fmt.Errorf("failed to create RunConfig: %v", err)
	}

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}
	workloadManager := workloads.NewManagerFromRuntime(rt)

	// Create the workload in stopped state
	if err := workloadManager.CreateWorkload(ctx, runnerConfig); err != nil {
		return fmt.Errorf("failed to create workload: %v", err)
	}

	// Add the workload to the group
	if err := groupManager.AddWorkloadToGroup(ctx, groupName, runnerConfig.BaseName); err != nil {
		return fmt.Errorf("failed to add workload to group: %v", err)
	}

	fmt.Printf("MCP server '%s' added to group '%s' in 'Stopped' state.\n", runnerConfig.BaseName, groupName)
	fmt.Printf("Use 'thv start %s' to start the server.\n", runnerConfig.BaseName)

	return nil
}

func init() {
	groupCmd.AddCommand(groupCreateCmd)
	groupCmd.AddCommand(groupAddCmd)

	// Add run flags from run command (excluding --foreground)
	AddRunFlags(groupAddCmd, &groupAddConfig)
}
