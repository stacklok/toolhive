// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"net/http/httptest"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// MockMCPServer wraps an httptest.Server running a streamable-HTTP MCP server
// with a single SearchModelContextProtocol tool, suitable for e2e tests that
// previously depended on the external mcp-spec server.
type MockMCPServer struct {
	httpServer *httptest.Server
}

// NewMockMCPServer creates and starts a mock MCP server with a
// SearchModelContextProtocol tool that returns canned results.
func NewMockMCPServer() *MockMCPServer {
	mcpServer := server.NewMCPServer(
		"mock-mcp-spec",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	mcpServer.AddTool(
		mcp.Tool{
			Name:        "SearchModelContextProtocol",
			Description: "Search the Model Context Protocol documentation",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "The search query",
					},
				},
				Required: []string{"query"},
			},
		},
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, _ := req.Params.Arguments.(map[string]any)["query"].(string)
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					mcp.NewTextContent(fmt.Sprintf(
						"Mock search results for %q: The Model Context Protocol (MCP) defines transports including stdio and HTTP with SSE.",
						query,
					)),
				},
			}, nil
		},
	)

	streamableServer := server.NewStreamableHTTPServer(
		mcpServer,
		server.WithEndpointPath("/mcp"),
	)

	httpServer := httptest.NewServer(streamableServer)

	return &MockMCPServer{httpServer: httpServer}
}

// URL returns the full URL to the MCP endpoint (e.g. http://127.0.0.1:PORT/mcp).
func (m *MockMCPServer) URL() string {
	return m.httpServer.URL + "/mcp"
}

// Close shuts down the mock server.
func (m *MockMCPServer) Close() {
	m.httpServer.Close()
}
