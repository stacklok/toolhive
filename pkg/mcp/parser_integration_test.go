package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

			// Skip SSE tests - the new SDK's SSE transport uses Server-Sent Events for bidirectional
			// communication and doesn't send HTTP requests that can be intercepted by the parsing middleware.
			// The parsing middleware is designed for HTTP-based transports like streamable HTTP.
			if tc.transport == "sse" {
				t.Skip("SSE transport doesn't use HTTP requests - parsing middleware not applicable")
			}

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
			var transport mcp.Transport

			if tc.transport == "sse" {
				testServerURL = setupSSEServer(t, mcpServer, tc.ssePath, tc.messagePath, parsingCaptureMiddleware)
				transport = &mcp.SSEClientTransport{
					Endpoint: testServerURL + tc.ssePath,
				}
			} else {
				// For streamable HTTP, use the specified endpoint
				testServerURL = setupStreamableHTTPServer(t, mcpServer, tc.endpoint, parsingCaptureMiddleware)
				transport = &mcp.StreamableClientTransport{
					Endpoint: testServerURL + tc.endpoint,
				}
			}

			// Create MCP client using the new SDK pattern
			mcpClient := mcp.NewClient(
				&mcp.Implementation{
					Name:    "test-client",
					Version: "1.0.0",
				},
				&mcp.ClientOptions{},
			)

			// Connect using the transport
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			session, err := mcpClient.Connect(ctx, transport, nil)
			require.NoError(t, err)
			defer session.Close()

			// Test 1: List tools
			toolsResult, err := session.ListTools(ctx, &mcp.ListToolsParams{})
			require.NoError(t, err)
			assert.NotEmpty(t, toolsResult.Tools)
			assert.Equal(t, "test_tool", toolsResult.Tools[0].Name)

			// Test 2: Call a tool
			// Convert arguments to JSON
			argJSON, err := json.Marshal(map[string]interface{}{
				"message": "hello from test",
			})
			require.NoError(t, err)

			callResult, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      "test_tool",
				Arguments: json.RawMessage(argJSON),
			})
			require.NoError(t, err)
			assert.NotNil(t, callResult)

			// Test 3: List resources
			resourcesResult, err := session.ListResources(ctx, &mcp.ListResourcesParams{})
			require.NoError(t, err)
			assert.NotEmpty(t, resourcesResult.Resources)

			// Test 4: Read a resource
			readResult, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
				URI: "test://resource",
			})
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
	mcpServer := mcp.NewServer(
		&mcp.Implementation{
			Name:    "test-server",
			Version: "1.0.0",
		},
		&mcp.ServerOptions{
			HasPrompts: true,
			HasTools:   true,
		},
	)

	// Add a prompt
	mcpServer.AddPrompt(
		&mcp.Prompt{
			Name:        "greeting",
			Description: "Generate a greeting",
			Arguments: []*mcp.PromptArgument{
				{
					Name:        "name",
					Description: "Name to greet",
					Required:    true,
				},
			},
		},
		func(_ context.Context, request *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			nameStr := "unknown"
			if name, ok := request.Params.Arguments["name"]; ok {
				nameStr = name
			}
			return &mcp.GetPromptResult{
				Messages: []*mcp.PromptMessage{
					{
						Role: mcp.Role("assistant"),
						Content: &mcp.TextContent{
							Text: "Hello, " + nameStr + "!",
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
	streamableHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			return mcpServer
		},
		&mcp.StreamableHTTPOptions{},
	)

	// Apply middleware and create test server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First capture the parsed request
		middleware.ServeHTTP(w, r)
		// Then handle with the actual server
		streamableHandler.ServeHTTP(w, r)
	})

	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	// Create MCP client using the new SDK pattern
	mcpClient := mcp.NewClient(
		&mcp.Implementation{
			Name:    "test-client",
			Version: "1.0.0",
		},
		&mcp.ClientOptions{},
	)

	// Create streamable transport
	transport := &mcp.StreamableClientTransport{
		Endpoint: testServer.URL + "/custom/api",
	}

	// Connect using the transport
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := mcpClient.Connect(ctx, transport, nil)
	require.NoError(t, err)
	defer session.Close()

	// Client is already initialized during Connect
	// Test prompt operations
	// List prompts
	promptsResult, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{})
	require.NoError(t, err)
	assert.NotEmpty(t, promptsResult.Prompts)

	// Get prompt
	promptResult, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name: "greeting",
		Arguments: map[string]string{
			"name": "World",
		},
	})
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
func createTestMCPServer() *mcp.Server {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{
			Name:    "test-server",
			Version: "1.0.0",
		},
		&mcp.ServerOptions{
			HasTools:     true,
			HasResources: true,
		},
	)

	// Add a test tool
	mcpServer.AddTool(
		&mcp.Tool{
			Name:        "test_tool",
			Description: "A test tool",
			InputSchema: &jsonschema.Schema{
				Type: "object",
				Properties: map[string]*jsonschema.Schema{
					"message": {
						Type:        "string",
						Description: "Test message",
					},
				},
				Required: []string{"message"},
			},
		},
		func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: "Tool called successfully",
					},
				},
			}, nil
		},
	)

	// Add a test resource
	mcpServer.AddResource(
		&mcp.Resource{
			URI:         "test://resource",
			Name:        "Test Resource",
			Description: "A test resource",
			MIMEType:    "text/plain",
		},
		func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{
				Contents: []*mcp.ResourceContents{
					{
						URI:      "test://resource",
						MIMEType: "text/plain",
						Text:     "Resource content",
					},
				},
			}, nil
		},
	)

	return mcpServer
}

// Helper function to setup SSE server with middleware
func setupSSEServer(t *testing.T, mcpServer *mcp.Server, ssePath, messagePath string, captureMiddleware func(http.Handler) http.Handler) string {
	t.Helper()

	// Create SSE handler for the MCP server
	sseHandler := mcp.NewSSEHandler(
		func(*http.Request) *mcp.Server {
			return mcpServer
		},
		&mcp.SSEOptions{},
	)

	mux := http.NewServeMux()

	// Set up the SSE endpoint
	mux.Handle(ssePath, sseHandler)

	// For SSE with message endpoint, we need to handle messages separately
	// This is a simplified setup - in production you'd need proper message handling
	messageHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Apply parsing middleware
		ParsingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Capture the parsed request
			captureMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Handle the message - simplified for testing
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(w, r)
		})).ServeHTTP(w, r)
	})

	mux.Handle(messagePath, messageHandler)

	testServer := httptest.NewServer(mux)
	t.Cleanup(func() { testServer.Close() })

	return testServer.URL
}

// Helper function to setup Streamable HTTP server with middleware
func setupStreamableHTTPServer(t *testing.T, mcpServer *mcp.Server, endpoint string, captureMiddleware func(http.Handler) http.Handler) string {
	t.Helper()

	// Create streamable HTTP handler for the MCP server
	streamableHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server {
			return mcpServer
		},
		&mcp.StreamableHTTPOptions{},
	)

	// Create a handler that applies parsing middleware and then the actual server handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Apply parsing middleware
		ParsingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Capture the parsed request
			captureMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Then handle with the actual server
				streamableHandler.ServeHTTP(w, r)
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
