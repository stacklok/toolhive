package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/workloads"
)

const (
	// DefaultMCPPort is the default port for the MCP server
	// 4483 represents "HIVE" on a phone keypad (4=HI, 8=V, 3=E)
	DefaultMCPPort = "4483"
)

var (
	mcpServePort string
	mcpServeHost string
)

// newMCPServeCommand creates the 'mcp serve' subcommand
func newMCPServeCommand() *cobra.Command {
	// Check for MCP_PORT environment variable
	defaultPort := DefaultMCPPort
	if envPort := os.Getenv("MCP_PORT"); envPort != "" {
		defaultPort = envPort
	}

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start an MCP server to control ToolHive",
		Long: `Start an MCP (Model Context Protocol) server that allows external clients to control ToolHive.
The server provides tools to search the registry, run MCP servers, and remove servers.
The server runs in privileged mode and can access the Docker socket directly.

The port can be configured via the --port flag or the MCP_PORT environment variable.`,
		RunE: mcpServeCmdFunc,
	}

	// Add flags
	cmd.Flags().StringVar(&mcpServePort, "port", defaultPort, "Port to listen on (can also be set via MCP_PORT env var)")
	cmd.Flags().StringVar(&mcpServeHost, "host", "localhost", "Host to listen on")

	return cmd
}

// mcpServeCmdFunc is the main function for the MCP serve command
func mcpServeCmdFunc(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Create the MCP server
	versionInfo := versions.GetVersionInfo()
	mcpServer := server.NewMCPServer(
		"toolhive-mcp",
		versionInfo.Version,
		server.WithToolCapabilities(false),
		server.WithLogging(),
	)

	// Create ToolHive handler
	handler, err := newToolHiveHandler(ctx)
	if err != nil {
		return fmt.Errorf("failed to create ToolHive handler: %w", err)
	}

	// Register tools
	mcpServer.AddTool(mcp.Tool{
		Name:        "search_registry",
		Description: "Search the ToolHive registry for MCP servers",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Search query to find MCP servers",
				},
			},
			Required: []string{"query"},
		},
	}, handler.searchRegistry)

	mcpServer.AddTool(mcp.Tool{
		Name:        "run_server",
		Description: "Run an MCP server from the ToolHive registry",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"server": map[string]interface{}{
					"type":        "string",
					"description": "Name of the server to run (e.g., 'fetch', 'github')",
				},
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Optional custom name for the server instance",
				},
				"env": map[string]interface{}{
					"type":        "object",
					"description": "Environment variables to pass to the server",
					"additionalProperties": map[string]interface{}{
						"type": "string",
					},
				},
			},
			Required: []string{"server"},
		},
	}, handler.runServer)

	mcpServer.AddTool(mcp.Tool{
		Name:        "list_servers",
		Description: "List all running ToolHive MCP servers",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, handler.listServers)

	mcpServer.AddTool(mcp.Tool{
		Name:        "stop_server",
		Description: "Stop a running MCP server",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Name of the server to stop",
				},
			},
			Required: []string{"name"},
		},
	}, handler.stopServer)

	mcpServer.AddTool(mcp.Tool{
		Name:        "remove_server",
		Description: "Remove a stopped MCP server",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Name of the server to remove",
				},
			},
			Required: []string{"name"},
		},
	}, handler.removeServer)

	mcpServer.AddTool(mcp.Tool{
		Name:        "get_server_logs",
		Description: "Get logs from a running MCP server",
		InputSchema: mcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Name of the server to get logs from",
				},
			},
			Required: []string{"name"},
		},
	}, handler.getServerLogs)

	// Create Streamable HTTP server
	addr := fmt.Sprintf("%s:%s", mcpServeHost, mcpServePort)
	streamableServer := server.NewStreamableHTTPServer(
		mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithHTTPContextFunc(func(_ context.Context, _ *http.Request) context.Context {
			return ctx
		}),
	)

	// Create HTTP server with security settings
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           streamableServer,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Start server in goroutine
	go func() {
		logger.Infof("Starting ToolHive MCP server on http://%s/mcp", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("MCP server error: %v", err)
			cancel()
		}
	}()

	// Wait for shutdown signal
	<-sigChan
	logger.Info("Shutting down MCP server...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	return httpServer.Shutdown(shutdownCtx)
}

// toolHiveHandler handles MCP tool requests for ToolHive
type toolHiveHandler struct {
	ctx              context.Context
	workloadManager  workloads.Manager
	registryProvider registry.Provider
}

// newToolHiveHandler creates a new ToolHive handler
func newToolHiveHandler(ctx context.Context) (*toolHiveHandler, error) {
	// Create workload manager
	workloadManager, err := workloads.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Create registry provider
	registryProvider, err := registry.GetDefaultProvider()
	if err != nil {
		return nil, fmt.Errorf("failed to get registry provider: %w", err)
	}

	return &toolHiveHandler{
		ctx:              ctx,
		workloadManager:  workloadManager,
		registryProvider: registryProvider,
	}, nil
}

// searchRegistry searches the ToolHive registry
func (h *toolHiveHandler) searchRegistry(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := struct {
		Query string `json:"query"`
	}{}

	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Search the registry
	servers, err := h.registryProvider.SearchServers(args.Query)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to search registry: %v", err)), nil
	}

	// Format results with all available information
	type ServerInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Transport   string   `json:"transport"`
		Image       string   `json:"image,omitempty"`
		Args        []string `json:"args,omitempty"`
		Tools       []string `json:"tools,omitempty"`
		Tags        []string `json:"tags,omitempty"`
	}

	var results []ServerInfo
	for _, srv := range servers {
		info := ServerInfo{
			Name:        srv.GetName(),
			Description: srv.GetDescription(),
			Transport:   srv.GetTransport(),
		}

		// Add image-specific fields if it's an ImageMetadata
		if imgMeta, ok := srv.(*registry.ImageMetadata); ok {
			info.Image = imgMeta.Image
			info.Args = imgMeta.Args
			info.Tools = imgMeta.Tools
			info.Tags = imgMeta.Tags
		}

		results = append(results, info)
	}

	// Use StructuredOnly to get JSON serialization automatically
	return mcp.NewToolResultStructuredOnly(results), nil
}

// runServer runs an MCP server
func (h *toolHiveHandler) runServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse and validate arguments
	args, err := parseRunServerArgs(request)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Use retriever to properly fetch and prepare the MCP server
	// TODO: make this configurable so we could warn or even fail
	imageURL, imageMetadata, err := retriever.GetMCPServer(ctx, args.Server, "", "disabled")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get MCP server: %v", err)), nil
	}

	// Build run configuration
	runConfig, err := buildServerConfig(ctx, args, imageURL, imageMetadata)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to build run configuration: %v", err)), nil
	}

	// Save and run the server
	if err := h.saveAndRunServer(ctx, runConfig, args.Name); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to run server: %v", err)), nil
	}

	// Build and return result
	result := h.getServerResult(ctx, args)
	return mcp.NewToolResultStructuredOnly(result), nil
}

// listServers lists all running MCP servers
func (h *toolHiveHandler) listServers(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// List all workloads (including stopped ones)
	wklds, err := h.workloadManager.ListWorkloads(ctx, true)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to list workloads: %v", err)), nil
	}

	// Format results with structured data
	type WorkloadInfo struct {
		Name      string `json:"name"`
		Server    string `json:"server,omitempty"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at"`
		URL       string `json:"url,omitempty"`
	}

	var results []WorkloadInfo
	for _, workload := range wklds {
		info := WorkloadInfo{
			Name:      workload.Name,
			Status:    string(workload.Status),
			CreatedAt: workload.CreatedAt.Format("2006-01-02 15:04:05"),
		}

		// Add server name from labels if available
		if serverName, ok := workload.Labels["toolhive.server"]; ok {
			info.Server = serverName
		}

		// Add URL if port is available
		if workload.Port > 0 {
			info.URL = fmt.Sprintf("http://localhost:%d", workload.Port)
		}

		results = append(results, info)
	}

	return mcp.NewToolResultStructuredOnly(results), nil
}

// stopServer stops a running MCP server
func (h *toolHiveHandler) stopServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := struct {
		Name string `json:"name"`
	}{}

	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Stop the workload
	group, err := h.workloadManager.StopWorkloads(ctx, []string{args.Name})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to stop server: %v", err)), nil
	}

	// Wait for the stop operation to complete
	if err := group.Wait(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to stop server: %v", err)), nil
	}

	result := map[string]interface{}{
		"status": "stopped",
		"name":   args.Name,
	}

	return mcp.NewToolResultStructuredOnly(result), nil
}

// removeServer removes a stopped MCP server
func (h *toolHiveHandler) removeServer(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := struct {
		Name string `json:"name"`
	}{}

	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Delete the workload
	group, err := h.workloadManager.DeleteWorkloads(ctx, []string{args.Name})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to remove server: %v", err)), nil
	}

	// Wait for the delete operation to complete
	if err := group.Wait(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to remove server: %v", err)), nil
	}

	result := map[string]interface{}{
		"status": "removed",
		"name":   args.Name,
	}

	return mcp.NewToolResultStructuredOnly(result), nil
}

// getServerLogs gets logs from a running MCP server
func (h *toolHiveHandler) getServerLogs(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// Parse arguments using BindArguments
	args := struct {
		Name string `json:"name"`
	}{}

	if err := request.BindArguments(&args); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to parse arguments: %v", err)), nil
	}

	// Get logs
	logs, err := h.workloadManager.GetLogs(ctx, args.Name, false)
	if err != nil {
		// Check if it's a not found error
		if strings.Contains(err.Error(), "not found") {
			return mcp.NewToolResultError(fmt.Sprintf("Server '%s' not found", args.Name)), nil
		}
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get server logs: %v", err)), nil
	}

	return mcp.NewToolResultText(logs), nil
}
