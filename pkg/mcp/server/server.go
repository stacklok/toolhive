package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/versions"
)

const (
	// DefaultMCPPort is the default port for the MCP server
	// 4483 represents "HIVE" on a phone keypad (4=HI, 8=V, 3=E)
	DefaultMCPPort = "4483"
)

// Config holds the configuration for the MCP server
type Config struct {
	Host string
	Port string
}

// Server represents the ToolHive MCP server
type Server struct {
	config     *Config
	mcpServer  *server.MCPServer
	httpServer *http.Server
	handler    *Handler
}

// New creates a new ToolHive MCP server
func New(ctx context.Context, config *Config) (*Server, error) {
	// Create the MCP server
	versionInfo := versions.GetVersionInfo()
	mcpServer := server.NewMCPServer(
		"toolhive-mcp",
		versionInfo.Version,
		server.WithToolCapabilities(false),
		server.WithLogging(),
	)

	// Create ToolHive handler
	handler, err := NewHandler(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create ToolHive handler: %w", err)
	}

	// Register tools
	registerTools(mcpServer, handler)

	// Create Streamable HTTP server
	addr := fmt.Sprintf("%s:%s", config.Host, config.Port)
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

	return &Server{
		config:     config,
		mcpServer:  mcpServer,
		httpServer: httpServer,
		handler:    handler,
	}, nil
}

// Start starts the MCP server
func (s *Server) Start() error {
	logger.Infof("Starting ToolHive MCP server on http://%s:%s/mcp", s.config.Host, s.config.Port)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("MCP server error: %w", err)
	}
	return nil
}

// Shutdown gracefully shuts down the MCP server
func (s *Server) Shutdown(ctx context.Context) error {
	logger.Info("Shutting down MCP server...")
	return s.httpServer.Shutdown(ctx)
}

// GetAddress returns the server address
func (s *Server) GetAddress() string {
	return fmt.Sprintf("http://%s:%s/mcp", s.config.Host, s.config.Port)
}

// registerTools registers all MCP tools with the server
func registerTools(mcpServer *server.MCPServer, handler *Handler) {
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
	}, handler.SearchRegistry)

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
	}, handler.RunServer)

	mcpServer.AddTool(mcp.Tool{
		Name:        "list_servers",
		Description: "List all running ToolHive MCP servers",
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]interface{}{},
		},
	}, handler.ListServers)

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
	}, handler.StopServer)

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
	}, handler.RemoveServer)

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
	}, handler.GetServerLogs)
}
