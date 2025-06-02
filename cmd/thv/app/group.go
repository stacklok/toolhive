package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/config"
)

var groupCmd = &cobra.Command{
	Use:   "group",
	Short: "Manage groups of MCP servers",
	Long:  "The group command provides subcommands to manage groups of MCP servers.",
}

var createGroupCmd = &cobra.Command{
	Use:   "create [group-name]",
	Short: "Create a new group of MCP servers",
	Long:  "Create a new group of MCP servers managed by ToolHive by providing a group name.",
	RunE:  createGroupCmdFunc,
}

func init() {
	rootCmd.AddCommand(groupCmd)

	// Add subcommands to the group command
	groupCmd.AddCommand(createGroupCmd)
}

func createGroupCmdFunc(_ *cobra.Command, args []string) error {
	groupName := args[0]
	err := config.UpdateConfig(func(c *config.Config) {
		// Check if group already exists
		for _, group := range c.Groups {
			if group.Name == groupName {
				fmt.Printf("Group %s already exists, skipping...\n", groupName)
				return
			}
		}

		// Add the new group
		c.Groups = append(c.Groups, config.Group{Name: groupName})
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	fmt.Printf("Successfully registered group: %s\n", groupName)

	return nil
}
