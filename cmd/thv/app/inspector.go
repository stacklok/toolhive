package app

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/labels"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

var (
	inspectorUIPort       int
	inspectorMCPProxyPort int
)

func inspectorCommand() *cobra.Command {
	inspectorCommand := &cobra.Command{
		Use:   "inspector [container-name]",
		Short: "Launches the MCP Inspector UI and connects it to the specified MCP server",
		Long:  `Launches the MCP Inspector UI and connects it to the specified MCP server`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return inspectorCmdFunc(cmd, args)
		},
	}

	inspectorCommand.Flags().IntVarP(&inspectorUIPort, "ui-port", "u", 6274, "Port to run the MCP Inspector UI on")
	inspectorCommand.Flags().IntVarP(&inspectorMCPProxyPort, "mcp-proxy-port", "p", 6277, "Port to run the MCP Proxy on")

	return inspectorCommand
}

var (
	// TODO: This could probably be a flag with a sensible default
	// TODO: Additionally, when the inspector image has been published
	// TODO: to docker.io, we can use that instead of npx
	// TODO: https://github.com/modelcontextprotocol/inspector/issues/237
	inspectorImage = "npx://@modelcontextprotocol/inspector@latest"
)

func inspectorCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Get server name from args
	if len(args) == 0 || args[0] == "" {
		return fmt.Errorf("server name is required as an argument")
	}

	serverName := args[0]

	// find the port of the server if it is running / exists
	serverPort, err := getServerPort(ctx, serverName)
	if err != nil {
		return fmt.Errorf("failed to find server: %v", err)
	}

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	processedImage, err := runner.HandleProtocolScheme(ctx, rt, inspectorImage, "")
	if err != nil {
		return fmt.Errorf("failed to handle protocol scheme: %v", err)
	}

	inspectorUIPortStr := strconv.Itoa(inspectorUIPort)
	inspectorMCPProxyPortStr := strconv.Itoa(inspectorMCPProxyPort)

	// Setup container options with the required port configuration
	options := &runtime.DeployWorkloadOptions{
		ExposedPorts: map[string]struct{}{
			inspectorUIPortStr + "/tcp":       {},
			inspectorMCPProxyPortStr + "/tcp": {},
		},
		PortBindings: map[string][]runtime.PortBinding{
			inspectorUIPortStr + "/tcp": {
				{
					HostIP:   "127.0.0.1",
					HostPort: inspectorUIPortStr,
				},
			},
			inspectorMCPProxyPortStr + "/tcp": {
				{
					HostIP:   "127.0.0.1",
					HostPort: inspectorMCPProxyPortStr,
				},
			},
		},
		AttachStdio: false,
	}

	labelsMap := map[string]string{}
	labels.AddStandardLabels(labelsMap, "inspector", "inspector", string(types.TransportTypeInspector), inspectorUIPort)
	_, err = rt.DeployWorkload(
		ctx,
		processedImage,
		"inspector",
		[]string{}, // No custom command needed
		nil,
		labelsMap,              // Add toolhive label
		&permissions.Profile{}, // Empty profile as we don't need special permissions
		string(types.TransportTypeInspector),
		options,
	)
	if err != nil {
		return fmt.Errorf("failed to create inspector container: %v", err)
	}

	// Monitor inspector readiness by checking HTTP response
	statusChan := make(chan bool, 1)
	go func() {
		inspectorURL := fmt.Sprintf("http://localhost:%d", inspectorUIPort)
		for {
			resp, err := http.Get(inspectorURL) //nolint:gosec // URL is constructed from trusted local port
			if err == nil && resp.StatusCode == 200 {
				_ = resp.Body.Close()
				statusChan <- true
				return
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
			// Small delay before checking again
			select {
			case <-ctx.Done():
				return
			default:
				logger.Info("Waiting for MCP Inspector to be ready...")
				time.Sleep(3 * time.Second)
			}
		}
	}()

	// Wait for container to be running or context to be cancelled
	select {
	case <-statusChan:
		logger.Infof("Connected to MCP server: %s", serverName)
		inspectorURL := fmt.Sprintf(
			"http://localhost:%d?transport=sse&serverUrl=http://host.docker.internal:%d/sse",
			inspectorUIPort, serverPort)
		logger.Infof("Inspector UI is now available at %s", inspectorURL)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("context cancelled while waiting for container to start")
	}
}

func getServerPort(ctx context.Context, serverName string) (int, error) {
	// Instantiate the container manager
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to create container manager: %v", err)
	}

	// Get list of all containers
	containers, err := manager.ListWorkloads(ctx, true)
	if err != nil {
		return 0, fmt.Errorf("failed to list containers: %v", err)
	}

	for _, c := range containers {
		name := c.Name

		if name == serverName {
			// Get port from labels
			port := c.Port
			// Generate URL for the MCP server
			if port > 0 {
				return port, nil
			}
		}
	}

	return 0, fmt.Errorf("server with name %s not found", serverName)
}
