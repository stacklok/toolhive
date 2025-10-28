package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	mcpServer  *mcp.Server
	httpServer *http.Server
	handler    *Handler
}

// New creates a new ToolHive MCP server
func New(ctx context.Context, config *Config) (*Server, error) {
	// Create the MCP server
	versionInfo := versions.GetVersionInfo()
	mcpServer := mcp.NewServer(
		&mcp.Implementation{
			Name:    "toolhive-mcp",
			Version: versionInfo.Version,
		},
		&mcp.ServerOptions{
			HasTools: false,
		},
	)

	// Create ToolHive handler
	handler, err := NewHandler(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create ToolHive handler: %w", err)
	}

	// Register tools
	registerTools(mcpServer, handler)

	// Create Streamable HTTP handler
	addr := fmt.Sprintf("%s:%s", config.Host, config.Port)
	streamableHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			return mcpServer
		},
		&mcp.StreamableHTTPOptions{},
	)

	// Create HTTP server with security settings
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           streamableHandler,
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
func registerTools(mcpServer *mcp.Server, handler *Handler) {
	mcpServer.AddTool(&mcp.Tool{
		Name:        "search_registry",
		Description: "Search the ToolHive registry for MCP servers",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"query": {
					Type:        "string",
					Description: "Search query to find MCP servers",
				},
			},
			Required: []string{"query"},
		},
	}, handler.SearchRegistry)

	mcpServer.AddTool(&mcp.Tool{
		Name:        "run_server",
		Description: "Run an MCP server from the ToolHive registry",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"server": {
					Type:        "string",
					Description: "Name of the server to run (e.g., 'fetch', 'github')",
				},
				"name": {
					Type:        "string",
					Description: "Optional custom name for the server instance",
				},
				"env": {
					Type:        "object",
					Description: "Environment variables to pass to the server",
					AdditionalProperties: &jsonschema.Schema{
						Type: "string",
					},
				},
				"secrets": {
					Type:        "array",
					Description: "Secrets to pass to the server as environment variables",
					Items: &jsonschema.Schema{
						Type: "object",
						Properties: map[string]*jsonschema.Schema{
							"name": {
								Type:        "string",
								Description: "Name of the secret in the ToolHive secrets store",
							},
							"target": {
								Type:        "string",
								Description: "Target environment variable name in the server container",
							},
						},
						Required: []string{"name", "target"},
					},
				},
			},
			Required: []string{"server"},
		},
	}, handler.RunServer)

	mcpServer.AddTool(&mcp.Tool{
		Name:        "list_servers",
		Description: "List all running ToolHive MCP servers",
		InputSchema: &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{},
		},
	}, handler.ListServers)

	mcpServer.AddTool(&mcp.Tool{
		Name:        "stop_server",
		Description: "Stop a running MCP server",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"name": {
					Type:        "string",
					Description: "Name of the server to stop",
				},
			},
			Required: []string{"name"},
		},
	}, handler.StopServer)

	mcpServer.AddTool(&mcp.Tool{
		Name:        "remove_server",
		Description: "Remove a stopped MCP server",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"name": {
					Type:        "string",
					Description: "Name of the server to remove",
				},
			},
			Required: []string{"name"},
		},
	}, handler.RemoveServer)

	mcpServer.AddTool(&mcp.Tool{
		Name:        "get_server_logs",
		Description: "Get logs from a running MCP server",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"name": {
					Type:        "string",
					Description: "Name of the server to get logs from",
				},
			},
			Required: []string{"name"},
		},
	}, handler.GetServerLogs)

	mcpServer.AddTool(&mcp.Tool{
		Name:        "list_secrets",
		Description: "List all available secrets in the ToolHive secrets store",
		InputSchema: &jsonschema.Schema{
			Type:       "object",
			Properties: map[string]*jsonschema.Schema{},
		},
	}, handler.ListSecrets)

	mcpServer.AddTool(&mcp.Tool{
		Name:        "set_secret",
		Description: "Set a secret by reading its value from a file",
		InputSchema: &jsonschema.Schema{
			Type: "object",
			Properties: map[string]*jsonschema.Schema{
				"name": {
					Type:        "string",
					Description: "Name of the secret to set",
				},
				"file_path": {
					Type:        "string",
					Description: "Path to the file containing the secret value",
				},
			},
			Required: []string{"name", "file_path"},
		},
	}, handler.SetSecret)
}
