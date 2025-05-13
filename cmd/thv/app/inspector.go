package app

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/lifecycle"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// Constants for inspector command
// const (
// 	defaultHost = "localhost"
// )

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

	inspectorCommand.Flags().StringP("server-name", "s", "", "Name of an existing MCP server to connect to")
	err = viper.BindPFlag("server-name", inspectorCommand.Flags().Lookup("server-name"))
	if err != nil {
		logger.Errorf("failed to bind flag: %v", err)
	}

	return inspectorCommand
}

func inspectorCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Get server name from flag
	serverName, err := cmd.Flags().GetString("server-name")
	if err != nil {
		return fmt.Errorf("failed to get server-name flag: %v", err)
	}

	if serverName == "" {
		return fmt.Errorf("server-name flag is required")
	}

	// Instantiate the container manager
	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container manager: %v", err)
	}

	// Get list of all containers
	containers, err := manager.ListContainers(ctx, true)
	if err != nil {
		return fmt.Errorf("failed to list containers: %v", err)
	}

	// Find the server with the matching name
	var serverURL string
	for _, c := range containers {
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		if name == serverName {
			// Get port from labels
			port, err := labels.GetPort(c.Labels)
			if err != nil {
				return fmt.Errorf("failed to get port for server %s: %v", serverName, err)
			}

			// Generate URL for the MCP server
			if port > 0 {
				serverURL = client.GenerateMCPServerURL(defaultHost, port, serverName)
				break
			}
		}
	}

	logger.Infof("Found MCP server: %s", serverName)
	logger.Infof("Server URL: %s", serverURL)

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
