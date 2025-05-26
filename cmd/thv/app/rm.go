package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/lifecycle"
)

var rmCmd = &cobra.Command{
	Use:   "rm [container-name]",
	Short: "Remove an MCP server",
	Long:  `Remove an MCP server managed by ToolHive.`,
	Args:  cobra.ExactArgs(1),
	RunE:  rmCmdFunc,
}

var (
	rmForce bool
)

func init() {
	rmCmd.Flags().BoolVarP(&rmForce, "force", "f", false, "Force removal of a running container")
}

//nolint:gocyclo // This function is complex but manageable
func rmCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	// Get container name
	containerName := args[0]

	// Create container manager.
	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container manager: %v", err)
	}

	// Delete container.
	if err := manager.DeleteContainer(ctx, containerName, rmForce); err != nil {
		return fmt.Errorf("failed to delete container: %v", err)
	}

	fmt.Printf("Container %s removed successfully\n", containerName)
	return nil
}
