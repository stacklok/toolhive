package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/stacklok/toolhive/pkg/state"
)

// Group represents a logical grouping of MCP servers.
type Group struct {
	Name string `json:"name"`
	// MCP servers will be added in a followup story.
}

// WriteJSON serializes the Group to JSON and writes it to the provided writer
func (g *Group) WriteJSON(w *os.File) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(g)
}

// SaveGroup saves the group to the group state store
func SaveGroup(ctx context.Context, group *Group) error {
	store, err := state.NewGroupConfigStore("toolhive")
	if err != nil {
		return fmt.Errorf("failed to create group state store: %w", err)
	}

	writer, err := store.GetWriter(ctx, group.Name)
	if err != nil {
		return fmt.Errorf("failed to get writer for group: %w", err)
	}
	defer writer.Close()

	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(group); err != nil {
		return fmt.Errorf("failed to write group: %w", err)
	}

	return nil
}

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

	// Check if group already exists
	store, err := state.NewGroupConfigStore("toolhive")
	if err != nil {
		return fmt.Errorf("failed to create group state store: %w", err)
	}
	exists, err := store.Exists(ctx, groupName)
	if err != nil {
		return fmt.Errorf("failed to check if group exists: %w", err)
	}
	if exists {
		return fmt.Errorf("group '%s' already exists", groupName)
	}

	group := &Group{Name: groupName}
	if err := SaveGroup(ctx, group); err != nil {
		return err
	}

	fmt.Printf("Group '%s' created successfully.\n", groupName)
	return nil
}

func init() {
	rootCmd.AddCommand(groupCmd)
	groupCmd.AddCommand(groupCreateCmd)
}
