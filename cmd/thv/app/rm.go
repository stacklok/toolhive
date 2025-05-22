package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/lifecycle"
	"github.com/stacklok/toolhive/pkg/logger"
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
	if err := manager.DeleteContainer(ctx, containerName, rmForce, true); err != nil {
		return fmt.Errorf("failed to delete container: %v", err)
	}

	// Delete associated egress container.
	egressContainerName := containerName + "-egress"
	if err := manager.DeleteContainer(ctx, egressContainerName, rmForce, false); err != nil {
		// just log the error and continue
		logger.Warnf("failed to delete egress container %q: %v", egressContainerName, err)
	}

	// Delete networks if there are no containers using them.
	toolHiveContainers, err := manager.ListContainers(ctx, listAll)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}
	fmt.Println("ToolHive containers:", toolHiveContainers)

	if len(toolHiveContainers) == 0 {
		// remove networks
		if err := manager.DeleteNetwork(ctx, "toolhive-internal"); err != nil {
			return fmt.Errorf("failed to delete network: %v", err)
		}
		if err := manager.DeleteNetwork(ctx, "toolhive-external"); err != nil {
			return fmt.Errorf("failed to delete network: %v", err)
		}
	}

	return nil
}
