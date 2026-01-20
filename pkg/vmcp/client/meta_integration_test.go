package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
)

// TestMetaPreservation_CallTool tests that _meta fields are preserved when calling tools.
func TestMetaPreservation_CallTool(t *testing.T) {
	t.Parallel()

	// Create and start a real MCP server that returns _meta
	port, cleanup := startTestMCPServer(t)
	defer cleanup()

	// Create vMCP backend client with unauthenticated strategy
	registry := auth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	// Create backend target pointing to our test server
	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Call tool through vMCP backend client
	result, err := backendClient.CallTool(ctx, target, "test_tool_with_meta", map[string]any{
		"input": "test-value",
	})

	// Verify call succeeded
	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify _meta field is preserved
	assert.NotNil(t, result.Meta, "_meta field should be preserved")
	assert.Equal(t, "test-progress-token-123", result.Meta["progressToken"], "progressToken should be preserved")
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-fedcba9876543210-01", result.Meta["traceparent"], "traceparent should be preserved")
	assert.Equal(t, "custom-value", result.Meta["customField"], "custom metadata should be preserved")

	// Verify content is also correct
	assert.NotNil(t, result.Content)
	assert.Len(t, result.Content, 1)
	assert.Equal(t, "text", result.Content[0].Type)
	assert.Equal(t, "Response from test tool", result.Content[0].Text)
}

// TestMetaPreservation_CallTool_NoMeta tests that tools without _meta don't break.
func TestMetaPreservation_CallTool_NoMeta(t *testing.T) {
	t.Parallel()

	port, cleanup := startTestMCPServer(t)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Call tool that doesn't return _meta
	result, err := backendClient.CallTool(ctx, target, "test_tool_no_meta", map[string]any{
		"input": "test-value",
	})

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify _meta is nil (not present)
	assert.Nil(t, result.Meta, "_meta should be nil when backend doesn't provide it")

	// Verify content is still correct
	assert.NotNil(t, result.Content)
	assert.Len(t, result.Content, 1)
	assert.Equal(t, "text", result.Content[0].Type)
}

// TestMetaPreservation_GetPrompt tests that _meta fields are preserved when getting prompts.
func TestMetaPreservation_GetPrompt(t *testing.T) {
	t.Parallel()

	port, cleanup := startTestMCPServer(t)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{})
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       "http://127.0.0.1:" + port,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get prompt through vMCP backend client
	result, err := backendClient.GetPrompt(ctx, target, "test_prompt_with_meta", map[string]any{
		"name": "World",
	})

	require.NoError(t, err)
	require.NotNil(t, result)

	// Verify _meta field is preserved
	assert.NotNil(t, result.Meta, "_meta field should be preserved for prompts")
	assert.Equal(t, "prompt-token-456", result.Meta["progressToken"])
	assert.Equal(t, "prompt-trace-id", result.Meta["traceId"])

	// Verify prompt content
	assert.Contains(t, result.Messages, "Hello, World!")
}

// startTestMCPServer creates and starts a test MCP server with tools that return _meta.
// Returns the port and cleanup function.
func startTestMCPServer(t *testing.T) (string, func()) {
	t.Helper()

	// Create MCP server
	mcpServer := server.NewMCPServer("test-backend", "1.0.0")

	// Add tool that returns _meta
	mcpServer.AddTool(
		mcp.NewTool("test_tool_with_meta",
			mcp.WithDescription("Test tool that returns metadata"),
			mcp.WithString("input", mcp.Required()),
		),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Result: mcp.Result{
					Meta: &mcp.Meta{
						ProgressToken: "test-progress-token-123",
						AdditionalFields: map[string]any{
							"traceparent": "00-0123456789abcdef0123456789abcdef-fedcba9876543210-01",
							"customField": "custom-value",
						},
					},
				},
				Content: []mcp.Content{
					mcp.NewTextContent("Response from test tool"),
				},
			}, nil
		},
	)

	// Add tool that doesn't return _meta (backward compatibility test)
	mcpServer.AddTool(
		mcp.NewTool("test_tool_no_meta",
			mcp.WithDescription("Test tool without metadata"),
			mcp.WithString("input", mcp.Required()),
		),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent("Response without meta"),
				},
			}, nil
		},
	)

	// Add prompt that returns _meta
	mcpServer.AddPrompt(
		mcp.NewPrompt("test_prompt_with_meta",
			mcp.WithPromptDescription("Test prompt with metadata"),
		),
		func(_ context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			name := "there"
			if nameArg, ok := request.Params.Arguments["name"]; ok {
				name = nameArg
			}
			return &mcp.GetPromptResult{
				Result: mcp.Result{
					Meta: &mcp.Meta{
						ProgressToken: "prompt-token-456",
						AdditionalFields: map[string]any{
							"traceId": "prompt-trace-id",
						},
					},
				},
				Messages: []mcp.PromptMessage{
					{
						Role:    "user",
						Content: mcp.NewTextContent("Hello, " + name + "!"),
					},
				},
			}, nil
		},
	)

	// Create HTTP handler for the MCP server
	httpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// MCP over HTTP uses POST requests with JSON-RPC
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Read the request body
		rawMessage, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		// Handle message through MCP server
		response := mcpServer.HandleMessage(r.Context(), rawMessage)

		// Marshal response to JSON
		responseBytes, err := json.Marshal(response)
		if err != nil {
			http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBytes)
	})

	// Start HTTP server on random available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)

	httpServer := &http.Server{
		Handler: httpHandler,
	}

	// Start server in background
	go func() {
		_ = httpServer.Serve(listener)
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	cleanup := func() {
		_ = httpServer.Close()
		_ = listener.Close()
	}

	return port, cleanup
}
