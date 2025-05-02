package app

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/api/factory"
	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/runner"
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
	// Get container name
	containerName := args[0]

	// Create context
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Get debug mode flag
	debugMode, _ := cmd.Flags().GetBool("debug")

	// Create API client factory
	apiFactory, err := factory.New(
		factory.WithClientType(factory.LocalClientType),
		factory.WithDebug(debugMode),
	)
	if err != nil {
		return fmt.Errorf("failed to create API client factory: %v", err)
	}

	// Create API client
	apiClient, err := apiFactory.Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create API client: %v", err)
	}
	defer apiClient.Close()

	// Get the server information to verify it exists
	_, err = apiClient.Server().Get(ctx, containerName)
	if err != nil {
		return fmt.Errorf("failed to get server information: %v", err)
	}

	// Create restart options
	restartOpts := &api.RestartOptions{}

	// Restart the server
	if err := apiClient.Server().Restart(ctx, containerName, restartOpts); err != nil {
		return fmt.Errorf("failed to restart server: %v", err)
	}

	// Load the configuration from the state store
	mcpRunner, err := runner.LoadState(ctx, containerName)
	if err != nil {
		return fmt.Errorf("failed to load state for %s: %v", containerName, err)
	}

	// Run the MCP server in a detached process
	logger.Log.Infof("Starting MCP server %s...", containerName)
	return RunMCPServer(ctx, cmd, mcpRunner.Config, false)
}
