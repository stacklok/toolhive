// Package helpers provides test utilities for vMCP integration tests.
package helpers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// BackendTool defines a tool for MCP backend servers.
// It provides a simplified interface for creating tools with handlers in tests.
//
// The handler function receives a context and arguments map, and returns a string
// result. The result should typically be valid JSON matching the tool's output schema.
type BackendTool struct {
	// Name is the unique identifier for the tool
	Name string

	// Description explains what the tool does
	Description string

	// InputSchema defines the expected input structure using JSON Schema.
	// The schema validates the arguments passed to the tool.
	InputSchema mcp.ToolInputSchema

	// Handler processes tool calls and returns results.
	// The handler receives the tool arguments as a map and should return
	// a string representation of the result (typically JSON).
	Handler func(ctx context.Context, args map[string]any) string
}

// NewBackendTool creates a new BackendTool with sensible defaults.
// The default InputSchema is an empty object schema that accepts any properties.
//
// Example:
//
//	tool := testkit.NewBackendTool(
//	    "create_issue",
//	    "Create a GitHub issue",
//	    func(ctx context.Context, args map[string]any) string {
//	        title := args["title"].(string)
//	        return fmt.Sprintf(`{"issue_id": 123, "title": %q}`, title)
//	    },
//	)
func NewBackendTool(name, description string, handler func(ctx context.Context, args map[string]any) string) BackendTool {
	return BackendTool{
		Name:        name,
		Description: description,
		InputSchema: mcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
		},
		Handler: handler,
	}
}

// contextKey is a private type for context keys to avoid collisions.
type contextKey string

// httpHeadersContextKey is the context key for storing HTTP headers.
const httpHeadersContextKey contextKey = "http-headers"

// GetHTTPHeadersFromContext retrieves HTTP headers from the context.
// Returns nil if headers are not present in the context.
func GetHTTPHeadersFromContext(ctx context.Context) http.Header {
	headers, _ := ctx.Value(httpHeadersContextKey).(http.Header)
	return headers
}

// BackendServerOption is a functional option for configuring a backend server.
type BackendServerOption func(*backendServerConfig)

// backendServerConfig holds configuration for creating a backend server.
type backendServerConfig struct {
	serverName      string
	serverVersion   string
	endpointPath    string
	withTools       bool
	withResources   bool
	withPrompts     bool
	captureHeaders  bool
	httpContextFunc server.HTTPContextFunc
}

// WithBackendName sets the backend server name.
// This name is reported in the server's initialize response.
//
// Default: "test-backend"
func WithBackendName(name string) BackendServerOption {
	return func(c *backendServerConfig) {
		c.serverName = name
	}
}

// WithCaptureHeaders enables capturing HTTP request headers in the context.
// When enabled, tool handlers can access request headers via GetHTTPHeadersFromContext(ctx).
// This is useful for testing authentication header injection.
//
// Default: false
func WithCaptureHeaders() BackendServerOption {
	return func(c *backendServerConfig) {
		c.captureHeaders = true
	}
}

// CreateBackendServer creates an MCP backend server using the mark3labs/mcp-go SDK.
// It returns an *httptest.Server ready to accept streamable-HTTP connections.
//
// The server automatically registers all provided tools with proper closure handling
// to avoid common Go loop variable capture bugs. Each tool's handler is invoked when
// the tool is called via the MCP protocol.
//
// The server uses the streamable-HTTP transport, which is compatible with ToolHive's
// vMCP server and supports both streaming and non-streaming requests.
//
// The returned httptest.Server should be closed after use with defer server.Close().
//
// Example:
//
//	// Create a simple echo tool
//	echoTool := testkit.NewBackendTool(
//	    "echo",
//	    "Echo back the input message",
//	    func(ctx context.Context, args map[string]any) string {
//	        msg := args["message"].(string)
//	        return fmt.Sprintf(`{"echoed": %q}`, msg)
//	    },
//	)
//
//	// Start backend server
//	backend := testkit.CreateBackendServer(t, []BackendTool{echoTool},
//	    testkit.WithBackendName("echo-server"),
//	    testkit.WithBackendEndpoint("/mcp"),
//	)
//	defer backend.Close()
//
//	// Use backend URL to connect MCP client
//	client := testkit.NewMCPClient(ctx, t, backend.URL+"/mcp")
//	defer client.Close()
func CreateBackendServer(tb testing.TB, tools []BackendTool, opts ...BackendServerOption) *httptest.Server {
	tb.Helper()

	// Apply default configuration
	config := &backendServerConfig{
		serverName:      "test-backend",
		serverVersion:   "1.0.0",
		endpointPath:    "/mcp",
		withTools:       true,
		withResources:   false,
		withPrompts:     false,
		captureHeaders:  false,
		httpContextFunc: nil,
	}

	// Apply functional options
	for _, opt := range opts {
		opt(config)
	}

	// If captureHeaders is enabled and no custom httpContextFunc is set, use default header capture
	if config.captureHeaders && config.httpContextFunc == nil {
		config.httpContextFunc = func(ctx context.Context, r *http.Request) context.Context {
			// Clone headers to avoid concurrent map access issues
			headers := make(http.Header, len(r.Header))
			for k, v := range r.Header {
				headers[k] = v
			}
			return context.WithValue(ctx, httpHeadersContextKey, headers)
		}
	}

	// Create MCP server with configured capabilities
	mcpServer := server.NewMCPServer(
		config.serverName,
		config.serverVersion,
		server.WithToolCapabilities(config.withTools),
		server.WithResourceCapabilities(config.withResources, config.withResources),
		server.WithPromptCapabilities(config.withPrompts),
	)

	// Register tools with proper closure handling to avoid loop variable capture
	for i := range tools {
		tool := tools[i] // Capture loop variable for closure
		mcpServer.AddTool(
			mcp.Tool{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			},
			func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				// Extract arguments from request, defaulting to empty map
				args, ok := req.Params.Arguments.(map[string]any)
				if !ok {
					args = make(map[string]any)
				}

				// Invoke the tool handler
				result := tool.Handler(ctx, args)

				// Return successful result with text content
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						mcp.NewTextContent(result),
					},
				}, nil
			},
		)
	}

	// Create streamable HTTP server with configured endpoint
	streamableOpts := []server.StreamableHTTPOption{
		server.WithEndpointPath(config.endpointPath),
	}

	// Add HTTP context function if configured
	if config.httpContextFunc != nil {
		streamableOpts = append(streamableOpts, server.WithHTTPContextFunc(config.httpContextFunc))
	}

	streamableServer := server.NewStreamableHTTPServer(
		mcpServer,
		streamableOpts...,
	)

	// Start HTTP test server
	httpServer := httptest.NewServer(streamableServer)

	tb.Logf("Created MCP backend server %q (v%s) at %s%s",
		config.serverName,
		config.serverVersion,
		httpServer.URL,
		config.endpointPath,
	)

	return httpServer
}
