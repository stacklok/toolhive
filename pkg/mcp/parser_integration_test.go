package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParsingMiddlewareWithRealMCPClients tests the parsing middleware with real MCP clients and servers
// This ensures the middleware works correctly in actual usage scenarios
func TestParsingMiddlewareWithRealMCPClients(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		ssePath     string
		messagePath string
		endpoint    string // for streamable-http transport
		transport   string // "sse" or "streamable-http"
	}{
		{
			name:        "Standard SSE paths",
			ssePath:     "/sse",
			messagePath: "/messages",
			transport:   "sse",
		},
		{
			name:        "Custom SSE paths",
			ssePath:     "/events",
			messagePath: "/rpc",
			transport:   "sse",
		},
		{
			name:        "Tenant-specific SSE paths",
			ssePath:     "/tenant/123/sse",
			messagePath: "/tenant/123/messages",
			transport:   "sse",
		},
		{
			name:      "Streamable HTTP with standard path",
			endpoint:  "/mcp",
			transport: "streamable-http",
		},
		{
			name:      "Streamable HTTP with custom path",
			endpoint:  "/api/v1/rpc",
			transport: "streamable-http",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a real MCP server with test tools and resources
			mcpServer := createTestMCPServer()

			// Track if parsing occurred
			var parsedRequests []*ParsedMCPRequest
			parsingCaptureMiddleware := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if parsed := GetParsedMCPRequest(r.Context()); parsed != nil {
						parsedRequests = append(parsedRequests, parsed)
					}
					next.ServeHTTP(w, r)
				})
			}

			// Create and start the test server based on transport type
			var testServerURL string
			var mcpClient *client.Client
			var err error

			if tc.transport == "sse" {
				testServerURL = setupSSEServer(t, mcpServer, tc.ssePath, tc.messagePath, parsingCaptureMiddleware)
				mcpClient, err = client.NewSSEMCPClient(testServerURL + tc.ssePath)
			} else {
				// For streamable HTTP, use the specified endpoint
				testServerURL = setupStreamableHTTPServer(t, mcpServer, tc.endpoint, parsingCaptureMiddleware)
				mcpClient, err = client.NewStreamableHttpClient(testServerURL + tc.endpoint)
			}
			require.NoError(t, err)

			// Start the client
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err = mcpClient.Start(ctx)
			require.NoError(t, err)
			defer mcpClient.Close()

			// Initialize the client
			initReq := mcp.InitializeRequest{}
			initReq.Params.ProtocolVersion = "2024-11-05"
			initReq.Params.ClientInfo = mcp.Implementation{
				Name:    "test-client",
				Version: "1.0.0",
			}
			initReq.Params.Capabilities = mcp.ClientCapabilities{}

			_, err = mcpClient.Initialize(ctx, initReq)
			require.NoError(t, err)

			// Test 1: List tools
			toolsReq := mcp.ListToolsRequest{}
			toolsResult, err := mcpClient.ListTools(ctx, toolsReq)
			require.NoError(t, err)
			assert.NotEmpty(t, toolsResult.Tools)
			assert.Equal(t, "test_tool", toolsResult.Tools[0].Name)

			// Test 2: Call a tool
			callReq := mcp.CallToolRequest{}
			callReq.Params.Name = "test_tool"
			callReq.Params.Arguments = map[string]interface{}{
				"message": "hello from test",
			}
			callResult, err := mcpClient.CallTool(ctx, callReq)
			require.NoError(t, err)
			assert.NotNil(t, callResult)

			// Test 3: List resources
			resourcesReq := mcp.ListResourcesRequest{}
			resourcesResult, err := mcpClient.ListResources(ctx, resourcesReq)
			require.NoError(t, err)
			assert.NotEmpty(t, resourcesResult.Resources)

			// Test 4: Read a resource
			readReq := mcp.ReadResourceRequest{}
			readReq.Params.URI = "test://resource"
			readResult, err := mcpClient.ReadResource(ctx, readReq)
			require.NoError(t, err)
			assert.NotEmpty(t, readResult.Contents)

			// Verify that all requests were parsed by the middleware
			assert.GreaterOrEqual(t, len(parsedRequests), 4, "Expected at least 4 parsed requests (initialize, list tools, call tool, list resources, read resource)")

			// Verify specific parsed requests
			foundInitialize := false
			foundToolCall := false
			foundResourceRead := false
			for _, parsed := range parsedRequests {
				switch parsed.Method {
				case "initialize":
					foundInitialize = true
					assert.Equal(t, "test-client", parsed.ResourceID)
				case "tools/call":
					foundToolCall = true
					assert.Equal(t, "test_tool", parsed.ResourceID)
				case "resources/read":
					foundResourceRead = true
					assert.Equal(t, "test://resource", parsed.ResourceID)
				}
			}
			assert.True(t, foundInitialize, "Initialize request should have been parsed")
			assert.True(t, foundToolCall, "Tool call request should have been parsed")
			assert.True(t, foundResourceRead, "Resource read request should have been parsed")
		})
	}
}

// TestParsingMiddlewareWithComplexMCPInteractions tests more complex MCP interactions
func TestParsingMiddlewareWithComplexMCPInteractions(t *testing.T) {
	t.Parallel()

	// Create MCP server with prompts
	mcpServer := server.NewMCPServer(
		"test-server",
		"1.0.0",
		server.WithPromptCapabilities(true),
		server.WithToolCapabilities(true),
	)

	// Add a prompt
	mcpServer.AddPrompt(
		mcp.Prompt{
			Name:        "greeting",
			Description: "Generate a greeting",
			Arguments: []mcp.PromptArgument{
				{
					Name:        "name",
					Description: "Name to greet",
					Required:    true,
				},
			},
		},
		func(_ context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			name := request.Params.Arguments["name"]
			return &mcp.GetPromptResult{
				Messages: []mcp.PromptMessage{
					{
						Role: "assistant",
						Content: mcp.TextContent{
							Type: "text",
							Text: "Hello, " + name + "!",
						},
					},
				},
			}, nil
		},
	)

	// Track parsed requests
	var parsedRequests []*ParsedMCPRequest
	middleware := ParsingMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if parsed := GetParsedMCPRequest(r.Context()); parsed != nil {
			parsedRequests = append(parsedRequests, parsed)
		}
	}))

	// Setup server with custom endpoint
	streamableServer := server.NewStreamableHTTPServer(
		mcpServer,
		server.WithEndpointPath("/custom/api"),
	)

	// Apply middleware and create test server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First capture the parsed request
		middleware.ServeHTTP(w, r)
		// Then handle with the actual server
		streamableServer.ServeHTTP(w, r)
	})

	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	// Create client
	mcpClient, err := client.NewStreamableHttpClient(testServer.URL + "/custom/api")
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = mcpClient.Start(ctx)
	require.NoError(t, err)
	defer mcpClient.Close()

	// Initialize
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = "2024-11-05"
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "test-client",
		Version: "1.0.0",
	}
	_, err = mcpClient.Initialize(ctx, initReq)
	require.NoError(t, err)

	// Test prompt operations
	// List prompts
	promptsReq := mcp.ListPromptsRequest{}
	promptsResult, err := mcpClient.ListPrompts(ctx, promptsReq)
	require.NoError(t, err)
	assert.NotEmpty(t, promptsResult.Prompts)

	// Get prompt
	getPromptReq := mcp.GetPromptRequest{}
	getPromptReq.Params.Name = "greeting"
	getPromptReq.Params.Arguments = map[string]string{
		"name": "World",
	}
	promptResult, err := mcpClient.GetPrompt(ctx, getPromptReq)
	require.NoError(t, err)
	assert.NotEmpty(t, promptResult.Messages)

	// Verify parsing
	foundPromptGet := false
	for _, parsed := range parsedRequests {
		if parsed.Method == "prompts/get" {
			foundPromptGet = true
			assert.Equal(t, "greeting", parsed.ResourceID)
			assert.Equal(t, map[string]interface{}{"name": "World"}, parsed.Arguments)
		}
	}
	assert.True(t, foundPromptGet, "Prompt get request should have been parsed")
}

// Helper function to create a test MCP server with tools and resources
func createTestMCPServer() *server.MCPServer {
	mcpServer := server.NewMCPServer(
		"test-server",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, true),
	)

	// Add a test tool
	mcpServer.AddTool(
		mcp.Tool{
			Name:        "test_tool",
			Description: "A test tool",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]interface{}{
					"message": map[string]interface{}{
						"type":        "string",
						"description": "Test message",
					},
				},
			},
		},
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.TextContent{
						Type: "text",
						Text: "Tool called successfully",
					},
				},
			}, nil
		},
	)

	// Add a test resource
	mcpServer.AddResource(
		mcp.Resource{
			URI:         "test://resource",
			Name:        "Test Resource",
			Description: "A test resource",
			MIMEType:    "text/plain",
		},
		func(_ context.Context, _ mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "test://resource",
					MIMEType: "text/plain",
					Text:     "Resource content",
				},
			}, nil
		},
	)

	return mcpServer
}

// Helper function to setup SSE server with middleware
func setupSSEServer(t *testing.T, mcpServer *server.MCPServer, ssePath, messagePath string, captureMiddleware func(http.Handler) http.Handler) string {
	t.Helper()
	sseServer := server.NewSSEServer(
		mcpServer,
		server.WithSSEEndpoint(ssePath),
		server.WithMessageEndpoint(messagePath),
	)

	mux := http.NewServeMux()

	// Create a handler that applies parsing middleware and then the actual server handler
	messageHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Apply parsing middleware
		ParsingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Capture the parsed request
			captureMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Then handle with the actual server
				sseServer.MessageHandler().ServeHTTP(w, r)
			})).ServeHTTP(w, r)
		})).ServeHTTP(w, r)
	})

	mux.Handle(messagePath, messageHandler)

	// SSE handler doesn't need parsing middleware
	mux.Handle(ssePath, sseServer.SSEHandler())

	testServer := httptest.NewServer(mux)
	t.Cleanup(func() { testServer.Close() })

	return testServer.URL
}

// Helper function to setup Streamable HTTP server with middleware
func setupStreamableHTTPServer(t *testing.T, mcpServer *server.MCPServer, endpoint string, captureMiddleware func(http.Handler) http.Handler) string {
	t.Helper()
	streamableServer := server.NewStreamableHTTPServer(
		mcpServer,
		server.WithEndpointPath(endpoint),
	)

	// Create a handler that applies parsing middleware and then the actual server handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Apply parsing middleware
		ParsingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Capture the parsed request
			captureMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Then handle with the actual server
				streamableServer.ServeHTTP(w, r)
			})).ServeHTTP(w, r)
		})).ServeHTTP(w, r)
	})

	testServer := httptest.NewServer(handler)
	t.Cleanup(func() { testServer.Close() })

	return testServer.URL
}

// TestParsingMiddlewareDoesNotParseSSEEstablishment verifies SSE endpoint establishment is not parsed
func TestParsingMiddlewareDoesNotParseSSEEstablishment(t *testing.T) {
	t.Parallel()

	// Create a handler that captures parsing attempts
	var parsedRequest *ParsedMCPRequest
	handler := ParsingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parsedRequest = GetParsedMCPRequest(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	// Try to establish SSE connection (GET request to /sse)
	resp, err := http.Get(testServer.URL + "/sse")
	require.NoError(t, err)
	defer resp.Body.Close()

	// Verify that SSE establishment was NOT parsed
	assert.Nil(t, parsedRequest, "SSE establishment endpoint should not be parsed")
}
