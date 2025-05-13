package app

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func inspectorCommand() *cobra.Command {
	inspectorCommand := &cobra.Command{
		Use:   "inspector [container-name]",
		Short: "Output the logs of an MCP server",
		Long:  `Output the logs of an MCP server managed by Vibe Tool.`,
		Args:  cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectorCmdFunc(cmd, args)
		},
	}

	inspectorCommand.Flags().BoolVarP(&tailFlag, "tail", "t", false, "Tail the logs")
	err := viper.BindPFlag("tail", inspectorCommand.Flags().Lookup("tail"))
	if err != nil {
		logger.Errorf("failed to bind flag: %v", err)
	}

	return inspectorCommand
}

func inspectorCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	// // Get container name
	// containerName := args[0]

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	processedImage, err := runner.HandleProtocolScheme(ctx, rt, "npx://@modelcontextprotocol/inspector", "")
	if err != nil {
		return fmt.Errorf("failed to handle protocol scheme: %v", err)
	}

	// Define fixed port configuration for the inspector container
	clientUIPort := "6274"
	mcpProxyPort := "6277"

	// Setup container options with the required port configuration
	options := &runtime.CreateContainerOptions{
		ExposedPorts: map[string]struct{}{
			clientUIPort + "/tcp": {},
			mcpProxyPort + "/tcp": {},
		},
		PortBindings: map[string][]runtime.PortBinding{
			clientUIPort + "/tcp": {
				{
					HostIP:   "0.0.0.0",
					HostPort: clientUIPort,
				},
			},
			mcpProxyPort + "/tcp": {
				{
					HostIP:   "0.0.0.0",
					HostPort: mcpProxyPort,
				},
			},
		},
		AttachStdio: false,
	}

	containerId, err := rt.CreateContainer(
		ctx,
		processedImage,
		"inspector",
		[]string{},                            // No custom command needed
		map[string]string{},                   // No special environment variables
		map[string]string{"toolhive": "true"}, // Add toolhive label
		&permissions.Profile{},                // Empty profile as we don't need special permissions
		string(types.TransportTypeSSE),        // Use bridge network for port bindings
		options,
	)
	if err != nil {
		return fmt.Errorf("failed to create inspector container: %v", err)
	}

	logger.Infof("MCP Inspector launched with container ID: %s", containerId)
	logger.Infof("Inspector UI is now available at http://localhost:6274")
	logger.Infof("Inspector API is available at http://localhost:6277")

	return nil
}
