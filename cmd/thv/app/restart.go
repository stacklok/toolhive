package app

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/lifecycle"
)

var restartCmd = &cobra.Command{
	Use:   "restart [container-name]",
	Short: "Restart a tooling server",
	Long:  `Restart a running tooling server managed by ToolHive. If the server is not running, it will be started.`,
	Args:  cobra.ExactArgs(1),
	RunE:  restartCmdFunc,
}

func init() {
	// No specific flags needed for restart command
}

func restartCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	// Get container name
	containerName := args[0]

	// Create lifecycle manager.
	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create lifecycle manager: %v", err)
	}

	// TODO: IMPLEMENT ME
	var envVars []string
	return manager.RestartContainer(ctx, containerName, envVars)
}
