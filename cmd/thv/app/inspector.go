package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

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

func inspectorCommand() *cobra.Command {
	inspectorCommand := &cobra.Command{
		Use:   "inspector [container-name]",
		Short: "Output the logs of an MCP server",
		Long:  `Output the logs of an MCP server managed by Vibe Tool.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectorCmdFunc(cmd, args)
		},
	}

	return inspectorCommand
}

var (
	// TODO: This could probably be a flag with a sensible default
	// TODO: Additionally, when the inspector image has been published
	// TODO: to docker.io, we can use that instead of npx
	inspectorImage = "npx://@modelcontextprotocol/inspector@latest"
)

func inspectorCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Get server name from args
	if len(args) == 0 || args[0] == "" {
		return fmt.Errorf("server name is required as an argument")
	}

	serverName := args[0]

	// Instantiate the container manager
	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container manager: %v", err)
	}

	// Find the server with the matching name
	serverURL, err := getServerURL(ctx, manager, serverName)
	if err != nil || serverURL == "" {
		return fmt.Errorf("failed to get server URL: %v", err)
	}

	// Format the server URL for the inspector
	// TODO: We don't do anything with this at the moment.
	// TODO: When the inspector supports the search params we will use
	// TODO: and give it to to the user
	formattedURL, err := formatServerURL(serverURL)
	if err != nil {
		return fmt.Errorf("failed to format server URL: %v", err)
	}

	logger.Infof("Found MCP server: %s", serverName)
	logger.Infof("Server URL: %s", formattedURL)

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	processedImage, err := runner.HandleProtocolScheme(ctx, rt, inspectorImage, "")
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

	// Create environment variables with the server information
	envVars := map[string]string{
		"MCP_SERVER_URL":  formattedURL, // Pass the formatted server URL as an environment variable
		"MCP_SERVER_NAME": serverName,   // Pass the server name as an environment variable
	}

	containerId, err := rt.CreateContainer(
		ctx,
		processedImage,
		"inspector",
		[]string{},                            // No custom command needed
		envVars,                               // Set environment variables with server info
		map[string]string{"toolhive": "true"}, // Add toolhive label
		&permissions.Profile{},                // Empty profile as we don't need special permissions
		string(types.TransportTypeSSE),        // Use bridge network for port bindings
		options,
	)
	if err != nil {
		return fmt.Errorf("failed to create inspector container: %v", err)
	}

	// TODO: We should output the URL that the user can use to connect to the inspector
	// TODO: that has their SSE search params already applied
	logger.Infof("MCP Inspector launched with container ID: %s", containerId)
	logger.Infof("Inspector UI is now available at http://localhost:6274")
	logger.Infof("Inspector API is available at http://localhost:6277")
	logger.Infof("Connected to MCP server: %s", serverName)
	logger.Infof("Using server URL: %s", formattedURL)

	return nil
}

// formatServerURL ensures the URL is properly formatted for the inspector
// It removes the fragment part (#container-name) as it's not needed for the inspector
func formatServerURL(serverURL string) (string, error) {
	// Parse the URL
	parsedURL, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse server URL: %w", err)
	}

	// Clear the fragment as it's not needed for the inspector
	parsedURL.Fragment = ""

	// Ensure the URL has the /sse endpoint
	if !strings.HasSuffix(parsedURL.Path, "/sse") {
		if parsedURL.Path == "" {
			parsedURL.Path = "/sse"
		} else if !strings.HasSuffix(parsedURL.Path, "/") {
			parsedURL.Path += "/sse"
		} else {
			parsedURL.Path += "sse"
		}
	}

	// Return the formatted URL
	return parsedURL.String(), nil
}

// getServerURL gets the URL of a running MCP server with the given name
func getServerURL(ctx context.Context, manager lifecycle.Manager, serverName string) (string, error) {
	// Get list of all containers
	containers, err := manager.ListContainers(ctx, true)
	if err != nil {
		return "", fmt.Errorf("failed to list containers: %v", err)
	}

	for _, c := range containers {
		name := labels.GetContainerName(c.Labels)
		if name == "" {
			name = c.Name // Fallback to container name
		}

		if name == serverName {
			// Get port from labels
			port, err := labels.GetPort(c.Labels)
			if err != nil {
				return "", fmt.Errorf("failed to get port for server %s: %v", serverName, err)
			}

			// Generate URL for the MCP server
			if port > 0 {
				return client.GenerateMCPServerURL(defaultHost, port, serverName), nil
			}
		}
	}

	return "", fmt.Errorf("server with name %s not found", serverName)
}
