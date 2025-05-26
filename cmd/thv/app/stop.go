package app

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/lifecycle"
)

var stopCmd = &cobra.Command{
	Use:   "stop [container-name]",
	Short: "Stop an MCP server",
	Long:  `Stop a running MCP server managed by ToolHive.`,
	Args:  cobra.ExactArgs(1),
	RunE:  stopCmdFunc,
}

var (
	stopTimeout int
)

func init() {
	stopCmd.Flags().IntVar(&stopTimeout, "timeout", 30, "Timeout in seconds before forcibly stopping the container")
}

func stopCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	// Get container name
	containerName := args[0]

	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container manager: %v", err)
	}

	err = manager.StopContainer(ctx, containerName)
	if err != nil {
		// If the container is not found, treat as a non-fatal error.
		if errors.Is(err, lifecycle.ErrContainerNotFound) {
			fmt.Printf("Container %s is not running\n", containerName)
		} else {
			return fmt.Errorf("failed to delete container: %v", err)
		}
	} else {
		fmt.Printf("Container %s stopped successfully\n", containerName)
	}

	return nil
}
