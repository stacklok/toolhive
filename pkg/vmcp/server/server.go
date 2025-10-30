// Package server implements the Virtual MCP Server that aggregates
// multiple backend MCP servers into a unified interface.
//
// The server exposes aggregated capabilities (tools, resources, prompts)
// and routes incoming MCP protocol requests to appropriate backend workloads.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

const (
	// defaultReadHeaderTimeout prevents slowloris attacks by limiting time to read request headers.
	defaultReadHeaderTimeout = 10 * time.Second

	// defaultShutdownTimeout is the maximum time to wait for graceful shutdown.
	defaultShutdownTimeout = 10 * time.Second
)

// Config holds the Virtual MCP Server configuration.
type Config struct {
	// Name is the server name exposed in MCP protocol
	Name string

	// Version is the server version
	Version string

	// Host is the bind address (default: "127.0.0.1")
	Host string

	// Port is the bind port (default: 4483)
	Port int

	// EndpointPath is the MCP endpoint path (default: "/mcp")
	EndpointPath string
}

// Server is the Virtual MCP Server that aggregates multiple backends.
type Server struct {
	config *Config

	// MCP protocol server (mark3labs/mcp-go)
	mcpServer *server.MCPServer

	// HTTP server for Streamable HTTP transport
	httpServer *http.Server

	// Router for forwarding requests to backends
	router router.Router

	// Backend client for making requests to backends
	backendClient vmcp.BackendClient

	// Aggregated capabilities (cached)
	aggregatedCapabilities *aggregator.AggregatedCapabilities
}

// New creates a new Virtual MCP Server instance.
func New(
	cfg *Config,
	rt router.Router,
	backendClient vmcp.BackendClient,
) *Server {
	// Apply defaults
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 4483
	}
	if cfg.EndpointPath == "" {
		cfg.EndpointPath = "/mcp"
	}
	if cfg.Name == "" {
		cfg.Name = "toolhive-vmcp"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}

	// Create mark3labs MCP server
	mcpServer := server.NewMCPServer(
		cfg.Name,
		cfg.Version,
		server.WithToolCapabilities(false), // We'll register tools dynamically
		server.WithLogging(),
	)

	return &Server{
		config:        cfg,
		mcpServer:     mcpServer,
		router:        rt,
		backendClient: backendClient,
	}
}

// RegisterCapabilities registers the aggregated capabilities with the MCP server.
// This must be called before starting the server.
func (s *Server) RegisterCapabilities(ctx context.Context, capabilities *aggregator.AggregatedCapabilities) error {
	logger.Infof("Registering %d tools, %d resources, %d prompts",
		len(capabilities.Tools), len(capabilities.Resources), len(capabilities.Prompts))

	// Cache the aggregated capabilities
	s.aggregatedCapabilities = capabilities

	// Update router with routing table
	if err := s.router.UpdateRoutingTable(ctx, capabilities.RoutingTable); err != nil {
		return fmt.Errorf("failed to update routing table: %w", err)
	}

	// Register all tools
	for _, tool := range capabilities.Tools {
		if err := s.registerTool(tool); err != nil {
			return fmt.Errorf("failed to register tool %s: %w", tool.Name, err)
		}
	}

	// Register all resources
	for _, resource := range capabilities.Resources {
		if err := s.registerResource(resource); err != nil {
			return fmt.Errorf("failed to register resource %s: %w", resource.URI, err)
		}
	}

	// Register all prompts
	for _, prompt := range capabilities.Prompts {
		if err := s.registerPrompt(prompt); err != nil {
			return fmt.Errorf("failed to register prompt %s: %w", prompt.Name, err)
		}
	}

	logger.Infof("Successfully registered all capabilities")
	return nil
}

// Start starts the Virtual MCP Server and begins serving requests.
func (s *Server) Start(ctx context.Context) error {
	if s.aggregatedCapabilities == nil {
		return fmt.Errorf("capabilities not registered, call RegisterCapabilities first")
	}

	// Create Streamable HTTP server
	streamableServer := server.NewStreamableHTTPServer(
		s.mcpServer,
		server.WithEndpointPath(s.config.EndpointPath),
	)

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           streamableServer,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
	}

	logger.Infof("Starting Virtual MCP Server at %s%s", addr, s.config.EndpointPath)

	// Start server in background
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	// Wait for either context cancellation or server error
	select {
	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down server")
		return s.Stop(context.Background())
	case err := <-errCh:
		return err
	}
}

// Stop gracefully stops the Virtual MCP Server.
func (s *Server) Stop(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}

	logger.Info("Stopping Virtual MCP Server")

	// Create shutdown context with timeout
	shutdownCtx, cancel := context.WithTimeout(ctx, defaultShutdownTimeout)
	defer cancel()

	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("failed to shutdown HTTP server: %w", err)
	}

	logger.Info("Virtual MCP Server stopped")
	return nil
}

// Address returns the server's listen address.
func (s *Server) Address() string {
	return fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
}

// registerTool registers a single tool with the MCP server.
// The tool handler routes the request to the appropriate backend.
//
//nolint:unparam // Error return kept for future extensibility
func (s *Server) registerTool(tool vmcp.Tool) error {
	// Convert vmcp.Tool to mcp.Tool
	mcpTool := mcp.Tool{
		Name:        tool.Name,
		Description: tool.Description,
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: tool.InputSchema,
		},
	}

	// Create handler that routes to backend
	handler := s.createToolHandler(tool.Name)

	// Register with MCP server
	s.mcpServer.AddTool(mcpTool, handler)

	logger.Debugf("Registered tool: %s", tool.Name)
	return nil
}

// registerResource registers a single resource with the MCP server.
// The resource handler routes the request to the appropriate backend.
//
//nolint:unparam // Error return kept for future extensibility
func (s *Server) registerResource(resource vmcp.Resource) error {
	// Convert vmcp.Resource to mcp.Resource
	mcpResource := mcp.Resource{
		URI:         resource.URI,
		Name:        resource.Name,
		Description: resource.Description,
		MIMEType:    resource.MimeType,
	}

	// Create handler that routes to backend
	handler := s.createResourceHandler(resource.URI)

	// Register with MCP server
	s.mcpServer.AddResource(mcpResource, handler)

	logger.Debugf("Registered resource: %s (MIME: %s)", resource.URI, resource.MimeType)
	return nil
}

// registerPrompt registers a single prompt with the MCP server.
// The prompt handler routes the request to the appropriate backend.
//
//nolint:unparam // Error return kept for future extensibility
func (s *Server) registerPrompt(prompt vmcp.Prompt) error {
	// Convert vmcp.Prompt to mcp.Prompt
	mcpArguments := make([]mcp.PromptArgument, len(prompt.Arguments))
	for i, arg := range prompt.Arguments {
		mcpArguments[i] = mcp.PromptArgument{
			Name:        arg.Name,
			Description: arg.Description,
			Required:    arg.Required,
		}
	}

	mcpPrompt := mcp.Prompt{
		Name:        prompt.Name,
		Description: prompt.Description,
		Arguments:   mcpArguments,
	}

	// Create handler that routes to backend
	handler := s.createPromptHandler(prompt.Name)

	// Register with MCP server
	s.mcpServer.AddPrompt(mcpPrompt, handler)

	logger.Debugf("Registered prompt: %s", prompt.Name)
	return nil
}

// createToolHandler creates a tool handler that routes to the appropriate backend.
func (s *Server) createToolHandler(toolName string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Debugf("Handling tool call: %s", toolName)

		// Route to backend
		target, err := s.router.RouteTool(ctx, toolName)
		if err != nil {
			// Wrap routing errors with domain error
			if errors.Is(err, router.ErrToolNotFound) {
				wrappedErr := fmt.Errorf("%w: tool %s", vmcp.ErrNotFound, toolName)
				logger.Warnf("Routing failed: %v", wrappedErr)
				return mcp.NewToolResultError(wrappedErr.Error()), nil
			}
			logger.Warnf("Failed to route tool %s: %v", toolName, err)
			return mcp.NewToolResultError(fmt.Sprintf("Routing error: %v", err)), nil
		}

		// Convert arguments to map[string]any
		args, ok := request.Params.Arguments.(map[string]any)
		if !ok {
			wrappedErr := fmt.Errorf("%w: arguments must be object, got %T", vmcp.ErrInvalidInput, request.Params.Arguments)
			logger.Warnf("Invalid arguments for tool %s: %v", toolName, wrappedErr)
			return mcp.NewToolResultError(wrappedErr.Error()), nil
		}

		// Forward request to backend
		result, err := s.backendClient.CallTool(ctx, target, toolName, args)
		if err != nil {
			// Distinguish between domain errors (tool execution failed) and operational errors (backend unavailable)
			if errors.Is(err, vmcp.ErrToolExecutionFailed) {
				// Tool ran but returned error - forward transparently to client
				logger.Debugf("Tool execution failed for %s: %v", toolName, err)
				return mcp.NewToolResultError(err.Error()), nil
			}
			if errors.Is(err, vmcp.ErrBackendUnavailable) {
				// Operational error - backend unreachable
				logger.Warnf("Backend unavailable for tool %s: %v", toolName, err)
				return mcp.NewToolResultError(fmt.Sprintf("Backend unavailable: %v", err)), nil
			}
			// Unknown error type
			logger.Warnf("Backend tool call failed for %s: %v", toolName, err)
			return mcp.NewToolResultError(fmt.Sprintf("Tool call failed: %v", err)), nil
		}

		// Convert result to MCP format
		return mcp.NewToolResultStructuredOnly(result), nil
	}
}

// createResourceHandler creates a resource handler that routes to the appropriate backend.
func (s *Server) createResourceHandler(uri string) func(
	context.Context, mcp.ReadResourceRequest,
) ([]mcp.ResourceContents, error) {
	return func(ctx context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		logger.Debugf("Handling resource read: %s", uri)

		// Route to backend
		target, err := s.router.RouteResource(ctx, uri)
		if err != nil {
			// Wrap routing errors with domain error
			if errors.Is(err, router.ErrResourceNotFound) {
				wrappedErr := fmt.Errorf("%w: resource %s", vmcp.ErrNotFound, uri)
				logger.Warnf("Routing failed: %v", wrappedErr)
				return nil, wrappedErr
			}
			logger.Warnf("Failed to route resource %s: %v", uri, err)
			return nil, fmt.Errorf("routing error: %w", err)
		}

		// Forward request to backend
		data, err := s.backendClient.ReadResource(ctx, target, uri)
		if err != nil {
			// Check if backend is unavailable (operational error)
			if errors.Is(err, vmcp.ErrBackendUnavailable) {
				logger.Warnf("Backend unavailable for resource %s: %v", uri, err)
				return nil, fmt.Errorf("backend unavailable: %w", err)
			}
			// Other errors
			logger.Warnf("Backend resource read failed for %s: %v", uri, err)
			return nil, fmt.Errorf("resource read failed: %w", err)
		}

		// Get resource MIME type from aggregated capabilities
		mimeType := "application/octet-stream" // Default for unknown resources
		if s.aggregatedCapabilities != nil {
			for _, res := range s.aggregatedCapabilities.Resources {
				if res.URI == uri && res.MimeType != "" {
					mimeType = res.MimeType
					break
				}
			}
		}

		// Convert to MCP ResourceContents
		contents := []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      uri,
				MIMEType: mimeType,
				Text:     string(data),
			},
		}

		return contents, nil
	}
}

// createPromptHandler creates a prompt handler that routes to the appropriate backend.
func (s *Server) createPromptHandler(promptName string) func(
	context.Context, mcp.GetPromptRequest,
) (*mcp.GetPromptResult, error) {
	return func(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		logger.Debugf("Handling prompt request: %s", promptName)

		// Route to backend
		target, err := s.router.RoutePrompt(ctx, promptName)
		if err != nil {
			// Wrap routing errors with domain error
			if errors.Is(err, router.ErrPromptNotFound) {
				wrappedErr := fmt.Errorf("%w: prompt %s", vmcp.ErrNotFound, promptName)
				logger.Warnf("Routing failed: %v", wrappedErr)
				return nil, wrappedErr
			}
			logger.Warnf("Failed to route prompt %s: %v", promptName, err)
			return nil, fmt.Errorf("routing error: %w", err)
		}

		// Convert arguments to map[string]any
		args := make(map[string]any)
		for k, v := range request.Params.Arguments {
			args[k] = v
		}

		// Forward request to backend
		promptText, err := s.backendClient.GetPrompt(ctx, target, promptName, args)
		if err != nil {
			// Check if backend is unavailable (operational error)
			if errors.Is(err, vmcp.ErrBackendUnavailable) {
				logger.Warnf("Backend unavailable for prompt %s: %v", promptName, err)
				return nil, fmt.Errorf("backend unavailable: %w", err)
			}
			// Other errors
			logger.Warnf("Backend prompt request failed for %s: %v", promptName, err)
			return nil, fmt.Errorf("prompt request failed: %w", err)
		}

		// Convert to MCP GetPromptResult
		result := &mcp.GetPromptResult{
			Description: fmt.Sprintf("Prompt: %s", promptName),
			Messages: []mcp.PromptMessage{
				{
					Role:    "assistant",
					Content: mcp.NewTextContent(promptText),
				},
			},
		}

		return result, nil
	}
}
