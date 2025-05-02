package app

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/StacklokLabs/toolhive/pkg/api"
	"github.com/StacklokLabs/toolhive/pkg/api/factory"
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

	// Create stop options
	stopOpts := &api.StopOptions{
		Timeout: time.Duration(stopTimeout) * time.Second,
	}

	// Stop the server
	if err := apiClient.Server().Stop(ctx, containerName, stopOpts); err != nil {
		return fmt.Errorf("failed to stop server: %v", err)
	}

	fmt.Printf("Server %s stopped\n", containerName)
	return nil
}
