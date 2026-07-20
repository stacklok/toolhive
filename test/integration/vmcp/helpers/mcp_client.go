// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package helpers

import (
	"context"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/client"
	mcptransport "github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// MCPClient wraps the mcpcompat MCP client with test-friendly methods.
// It automatically handles initialization and provides semantic assertion helpers
// that integrate with Go's testing.TB interface.
//
// Example usage:
//
//	ctx := context.Background()
//	mcpClient := helpers.NewMCPClient(ctx, t, serverURL)
//	defer mcpClient.Close()
//
//	tools := mcpClient.ListTools(ctx)
//	toolNames := helpers.GetToolNames(tools)
//	assert.Contains(t, toolNames, "create_issue")
type MCPClient struct {
	client *client.Client
	tb     testing.TB

	// reqHeaders holds the caller-side HTTP headers sent on every outbound
	// request to the vMCP server. It is served per-request through a header func
	// and guarded by reqHeadersMu so SetHeader can change a header mid-session.
	reqHeaders   map[string]string
	reqHeadersMu *sync.Mutex
}

// MCPClientOption is a functional option for configuring an MCPClient.
type MCPClientOption func(*mcpClientConfig)

// mcpClientConfig holds configuration for creating an MCP client.
type mcpClientConfig struct {
	clientName    string
	clientVersion string
	extraHeaders  map[string]string
}

// WithClientHeader sets the initial value of an HTTP request header that the
// client sends on every outbound request to the vMCP server.  Call multiple
// times to set multiple headers; use MCPClient.SetHeader to change a value
// after the client is created.
//
// This is used by header-passthrough tests to inject caller-side headers so
// the capture middleware can forward them to the backend.
func WithClientHeader(name, value string) MCPClientOption {
	return func(c *mcpClientConfig) {
		if c.extraHeaders == nil {
			c.extraHeaders = make(map[string]string)
		}
		c.extraHeaders[name] = value
	}
}

// NewMCPClient creates and initializes a new MCP client for testing.
// It automatically starts the transport and performs the MCP handshake.
//
// The client is configured with sensible defaults suitable for testing:
//   - Protocol version: Latest (mcp.LATEST_PROTOCOL_VERSION)
//   - Client name: "testkit-client"
//   - Client version: "1.0.0"
//   - Transport: streamable-http (vMCP only supports streamable-http)
//
// The function fails the test immediately if initialization fails.
//
// Example:
//
//	client := helpers.NewMCPClient(ctx, t, "http://localhost:8080/mcp")
//	defer client.Close()
//
//	tools := client.ListTools(ctx)
//	assert.NotEmpty(t, helpers.GetToolNames(tools))
func NewMCPClient(ctx context.Context, tb testing.TB, serverURL string, opts ...MCPClientOption) *MCPClient {
	tb.Helper()

	// Default configuration
	config := &mcpClientConfig{
		clientName:    "testkit-client",
		clientVersion: "1.0.0",
	}

	// Apply options
	for _, opt := range opts {
		opt(config)
	}

	// Caller headers are served through a per-request header func backed by a
	// mutex-guarded map. This lets header-passthrough tests change a header
	// mid-session via SetHeader and assert the backend still observes the value
	// captured at backend-session creation.
	reqHeaders := map[string]string{}
	maps.Copy(reqHeaders, config.extraHeaders)
	reqHeadersMu := &sync.Mutex{}

	transportOpts := []mcptransport.StreamableHTTPCOption{
		mcptransport.WithHTTPHeaderFunc(func(context.Context) map[string]string {
			reqHeadersMu.Lock()
			defer reqHeadersMu.Unlock()
			return maps.Clone(reqHeaders)
		}),
	}

	// Create streamable-http client (vMCP only supports streamable-http)
	mcpClient, err := client.NewStreamableHttpClient(serverURL, transportOpts...)
	require.NoError(tb, err, "failed to create MCP client with streamable-http transport")

	// Start the transport
	err = mcpClient.Start(ctx)
	require.NoError(tb, err, "failed to start MCP transport")

	// Initialize the MCP session
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initRequest.Params.Capabilities = mcp.ClientCapabilities{}
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    config.clientName,
		Version: config.clientVersion,
	}

	_, err = mcpClient.Initialize(ctx, initRequest)
	require.NoError(tb, err, "failed to initialize MCP session")

	tb.Logf("MCP client initialized successfully: name=%s, version=%s, transport=streamable-http, url=%s",
		config.clientName, config.clientVersion, serverURL)

	return &MCPClient{
		client:       mcpClient,
		tb:           tb,
		reqHeaders:   reqHeaders,
		reqHeadersMu: reqHeadersMu,
	}
}

// SetHeader updates a caller-side HTTP header sent on subsequent requests to the
// vMCP server. Used by header-passthrough tests to change a header mid-session
// and assert the backend still observes the value captured at session creation.
func (c *MCPClient) SetHeader(name, value string) {
	c.reqHeadersMu.Lock()
	defer c.reqHeadersMu.Unlock()
	c.reqHeaders[name] = value
}

// Close closes the MCP client connection.
// This should typically be deferred immediately after client creation.
func (c *MCPClient) Close() error {
	c.tb.Helper()
	return c.client.Close()
}

// ListTools lists all available tools from the MCP server.
// The method logs the operation and fails the test if the request fails.
//
// Example:
//
//	tools := client.ListTools(ctx)
//	toolNames := helpers.GetToolNames(tools)
//	assert.Contains(t, toolNames, "expected_tool")
func (c *MCPClient) ListTools(ctx context.Context) *mcp.ListToolsResult {
	c.tb.Helper()

	// Follow MCP pagination cursors so the returned set is the complete tool
	// list across every page, not just the first. This is spec-conforming and
	// harmless to callers that only assert containment.
	var allTools []mcp.Tool
	var cursor mcp.Cursor
	for {
		tools, next := c.ListToolsPage(ctx, cursor)
		allTools = append(allTools, tools...)
		if next == "" {
			break
		}
		cursor = next
	}

	c.tb.Logf("Listed %d tools from MCP server", len(allTools))
	return &mcp.ListToolsResult{Tools: allTools}
}

// ListToolsPage lists a single page of tools starting at the given cursor
// (pass an empty cursor for the first page). It returns that page's tools and
// the NextCursor to fetch the following page (empty when no more pages remain).
//
// Use this when a test needs to assert page-level behavior (e.g. that the first
// vMCP page is bounded by the server page size and carries a NextCursor,
// proving pagination actually occurred). Most tests should use ListTools, which
// accumulates across pages.
func (c *MCPClient) ListToolsPage(ctx context.Context, cursor mcp.Cursor) ([]mcp.Tool, mcp.Cursor) {
	c.tb.Helper()

	request := mcp.ListToolsRequest{}
	request.Params.Cursor = cursor
	result, err := c.client.ListTools(ctx, request)
	require.NoError(c.tb, err, "failed to list tools")

	return result.Tools, result.NextCursor
}

// CallTool calls the specified tool with the given arguments.
// The method logs the operation and fails the test if the request fails.
//
// Example:
//
//	result := client.CallTool(ctx, "create_issue", map[string]any{
//	    "title": "Bug report",
//	    "body": "Description",
//	})
//	text := helpers.AssertToolCallSuccess(t, result)
//	assert.Contains(t, text, "issue_id")
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]any) *mcp.CallToolResult {
	c.tb.Helper()

	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = args

	result, err := c.client.CallTool(ctx, request)
	require.NoError(c.tb, err, "failed to call tool %q", name)

	c.tb.Logf("Called tool %q with %d arguments", name, len(args))
	return result
}

// GetToolNames extracts tool names from a ListToolsResult.
// This is a convenience function for common test assertions.
//
// Example:
//
//	tools := client.ListTools(ctx)
//	names := helpers.GetToolNames(tools)
//	assert.ElementsMatch(t, []string{"tool1", "tool2"}, names)
func GetToolNames(result *mcp.ListToolsResult) []string {
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// AssertToolCallSuccess asserts that a tool call succeeded (IsError=false)
// and returns the concatenated text content from all content items.
//
// The function uses require assertions, so it will fail the test immediately
// if the tool call was an error.
//
// Example:
//
//	result := client.CallTool(ctx, "get_user", map[string]any{"id": 123})
//	text := helpers.AssertToolCallSuccess(t, result)
//	assert.Contains(t, text, "username")
func AssertToolCallSuccess(tb testing.TB, result *mcp.CallToolResult) string {
	tb.Helper()

	require.NotNil(tb, result, "tool call result should not be nil")
	require.False(tb, result.IsError, "tool call should not return an error, got: %v", result.Content)

	var textParts []string
	for _, content := range result.Content {
		if textContent, ok := mcp.AsTextContent(content); ok {
			textParts = append(textParts, textContent.Text)
		}
	}

	text := strings.Join(textParts, "\n")
	tb.Logf("Tool call succeeded with %d content items, total text length: %d", len(result.Content), len(text))

	return text
}

// AssertTextContains asserts that text contains all expected substrings.
// This is a variadic helper for checking multiple content expectations in tool results.
//
// The function uses assert (not require), so multiple failures can be reported together.
//
// Example:
//
//	text := helpers.AssertToolCallSuccess(t, result)
//	helpers.AssertTextContains(t, text, "user_id", "username", "email")
func AssertTextContains(tb testing.TB, text string, expected ...string) {
	tb.Helper()

	for _, exp := range expected {
		if !assert.Contains(tb, text, exp) {
			tb.Logf("Expected substring %q not found in text (length: %d)", exp, len(text))
		}
	}
}

// AssertTextNotContains asserts that text does not contain any of the forbidden substrings.
// This is a variadic helper for checking that certain content is absent from tool results.
//
// The function uses assert (not require), so multiple failures can be reported together.
//
// Example:
//
//	text := helpers.AssertToolCallSuccess(t, result)
//	helpers.AssertTextNotContains(t, text, "password", "secret", "api_key")
func AssertTextNotContains(tb testing.TB, text string, forbidden ...string) {
	tb.Helper()

	for _, forb := range forbidden {
		if !assert.NotContains(tb, text, forb) {
			tb.Logf("Forbidden substring %q found in text (length: %d)", forb, len(text))
		}
	}
}
