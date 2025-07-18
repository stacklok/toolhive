package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/groups"
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

func init() {
	groupCmd.AddCommand(groupCreateCmd)
}
