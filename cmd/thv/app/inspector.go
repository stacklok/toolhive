package app

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/cmd/thv/app/inspector"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/images"
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
		Use:   "inspector [workload-name]",
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

func buildInspectorContainerOptions(uiPortStr string, mcpPortStr string) *runtime.DeployWorkloadOptions {
	return &runtime.DeployWorkloadOptions{
		ExposedPorts: map[string]struct{}{
			uiPortStr + "/tcp":  {},
			mcpPortStr + "/tcp": {},
		},
		PortBindings: map[string][]runtime.PortBinding{
			uiPortStr + "/tcp": {
				{HostIP: "127.0.0.1", HostPort: uiPortStr},
			},
			mcpPortStr + "/tcp": {
				{HostIP: "127.0.0.1", HostPort: mcpPortStr},
			},
		},
		AttachStdio: false,
	}
}

func waitForInspectorReady(ctx context.Context, port int, statusChan chan bool) {
	go func() {
		url := fmt.Sprintf("http://localhost:%d", port)
		for {
			resp, err := http.Get(url) //nolint:gosec
			if err == nil && resp.StatusCode == 200 {
				_ = resp.Body.Close()
				statusChan <- true
				return
			}
			if resp != nil {
				_ = resp.Body.Close()
			}
			select {
			case <-ctx.Done():
				return
			default:
				logger.Info("Waiting for MCP Inspector to be ready...")
				time.Sleep(3 * time.Second)
			}
		}
	}()
}

func inspectorCmdFunc(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Get server name from args
	if len(args) == 0 || args[0] == "" {
		return fmt.Errorf("server name is required as an argument")
	}

	serverName := args[0]

	// find the port of the server if it is running / exists
	serverPort, transportType, err := getServerPortAndTransport(ctx, serverName)
	if err != nil {
		return fmt.Errorf("failed to find server: %v", err)
	}

	imageManager := images.NewImageManager(ctx)
	processedImage, err := runner.HandleProtocolScheme(ctx, imageManager, inspector.Image, "")
	if err != nil {
		return fmt.Errorf("failed to handle protocol scheme: %v", err)
	}

	// Setup workload options with the required port configuration
	uiPortStr := strconv.Itoa(inspectorUIPort)
	mcpPortStr := strconv.Itoa(inspectorMCPProxyPort)

	options := buildInspectorContainerOptions(uiPortStr, mcpPortStr)

	// Create workload runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create workload runtime: %v", err)
	}

	labelsMap := map[string]string{}
	labels.AddStandardLabels(labelsMap, "inspector", "inspector", string(types.TransportTypeInspector), inspectorUIPort)
	labelsMap["toolhive-auxiliary"] = "true"
	_, err = rt.DeployWorkload(
		ctx,
		processedImage,
		"inspector",
		[]string{}, // No custom command needed
		make(map[string]string),
		labelsMap,              // Add toolhive label
		&permissions.Profile{}, // Empty profile as we don't need special permissions
		string(types.TransportTypeInspector),
		options,
		false, // Do not isolate network
	)
	if err != nil {
		return fmt.Errorf("failed to create inspector workload: %v", err)
	}

	// Monitor inspector readiness by checking HTTP response
	statusChan := make(chan bool, 1)
	waitForInspectorReady(ctx, inspectorUIPort, statusChan)

	// Wait for workload to be running or context to be cancelled
	select {
	case <-statusChan:
		logger.Infof("Connected to MCP server: %s", serverName)

		var suffix string
		var transportTypeStr string
		if transportType == types.TransportTypeSSE || transportType == types.TransportTypeStdio {
			suffix = "sse"
			transportTypeStr = transportType.String()
		} else {
			suffix = "mcp/"
			transportTypeStr = "streamable-http"
		}
		inspectorURL := fmt.Sprintf(
			"http://localhost:%d?transport=%s&serverUrl=http://host.docker.internal:%d/%s",
			inspectorUIPort, transportTypeStr, serverPort, suffix)
		logger.Infof("Inspector UI is now available at %s", inspectorURL)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("context cancelled while waiting for workload to start")
	}
}

func getServerPortAndTransport(ctx context.Context, serverName string) (int, types.TransportType, error) {
	// Instantiate the status manager and list all workloads.
	manager, err := workloads.NewManager(ctx)
	if err != nil {
		return 0, types.TransportTypeSSE, fmt.Errorf("failed to create status manager: %v", err)
	}

	workloadList, err := manager.ListWorkloads(ctx, true)
	if err != nil {
		return 0, types.TransportTypeSSE, fmt.Errorf("failed to list workloads: %v", err)
	}

	for _, c := range workloadList {
		name := c.Name

		if name == serverName {
			// Get port from labels
			port := c.Port
			if port <= 0 {
				return 0, types.TransportTypeSSE, fmt.Errorf("server %s does not have a valid port", serverName)
			}

			// now get the transport type from labels
			transportType := c.TransportType
			return port, transportType, nil
		}
	}

	return 0, types.TransportTypeSSE, fmt.Errorf("server with name %s not found", serverName)
}
