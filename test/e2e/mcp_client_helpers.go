package e2e

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2" //nolint:staticcheck // Standard practice for Ginkgo
	. "github.com/onsi/gomega"    //nolint:staticcheck // Standard practice for Gomega
)

// MCPClientHelper provides high-level MCP client operations for e2e tests
type MCPClientHelper struct {
	client *client.Client
	config *TestConfig
}

// NewMCPClientForSSE creates a new MCP client for SSE transport
func NewMCPClientForSSE(config *TestConfig, serverURL string) (*MCPClientHelper, error) {
	mcpClient, err := client.NewSSEMCPClient(serverURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create SSE MCP client: %w", err)
	}

	return &MCPClientHelper{
		client: mcpClient,
		config: config,
	}, nil
}

// Initialize initializes the MCP connection
func (h *MCPClientHelper) Initialize(ctx context.Context) error {
	// Start the transport first
	err := h.client.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start MCP transport: %w", err)
	}

	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = "2024-11-05"
	initRequest.Params.Capabilities = mcp.ClientCapabilities{
		// Basic client capabilities
	}
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "toolhive-e2e-test",
		Version: "1.0.0",
	}

	_, err = h.client.Initialize(ctx, initRequest)
	if err != nil {
		return fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	return nil
}

// Close closes the MCP client connection
func (h *MCPClientHelper) Close() error {
	return h.client.Close()
}

// ListTools lists all available tools from the MCP server
func (h *MCPClientHelper) ListTools(ctx context.Context) (*mcp.ListToolsResult, error) {
	request := mcp.ListToolsRequest{}
	return h.client.ListTools(ctx, request)
}

// CallTool calls a specific tool with the given arguments
func (h *MCPClientHelper) CallTool(
	ctx context.Context, toolName string, arguments map[string]interface{},
) (*mcp.CallToolResult, error) {
	request := mcp.CallToolRequest{}
	request.Params.Name = toolName
	request.Params.Arguments = arguments
	return h.client.CallTool(ctx, request)
}

// ListResources lists all available resources from the MCP server
func (h *MCPClientHelper) ListResources(ctx context.Context) (*mcp.ListResourcesResult, error) {
	request := mcp.ListResourcesRequest{}
	return h.client.ListResources(ctx, request)
}

// ReadResource reads a specific resource
func (h *MCPClientHelper) ReadResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, error) {
	request := mcp.ReadResourceRequest{}
	request.Params.URI = uri
	return h.client.ReadResource(ctx, request)
}

// Ping sends a ping to test connectivity
func (h *MCPClientHelper) Ping(ctx context.Context) error {
	return h.client.Ping(ctx)
}

// ExpectToolExists verifies that a tool with the given name exists
func (h *MCPClientHelper) ExpectToolExists(ctx context.Context, toolName string) {
	tools, err := h.ListTools(ctx)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to list tools")

	found := false
	for _, tool := range tools.Tools {
		if tool.Name == toolName {
			found = true
			break
		}
	}
	ExpectWithOffset(1, found).To(BeTrue(), fmt.Sprintf("Tool '%s' should exist", toolName))
}

// ExpectToolCall verifies that a tool can be called successfully
func (h *MCPClientHelper) ExpectToolCall(
	ctx context.Context, toolName string, arguments map[string]interface{},
) *mcp.CallToolResult {
	result, err := h.CallTool(ctx, toolName, arguments)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), fmt.Sprintf("Should be able to call tool '%s'", toolName))
	ExpectWithOffset(1, result).ToNot(BeNil(), "Tool result should not be nil")
	return result
}

// ExpectResourceExists verifies that a resource with the given URI exists
func (h *MCPClientHelper) ExpectResourceExists(ctx context.Context, uri string) {
	resources, err := h.ListResources(ctx)
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "Should be able to list resources")

	found := false
	for _, resource := range resources.Resources {
		if resource.URI == uri {
			found = true
			break
		}
	}
	ExpectWithOffset(1, found).To(BeTrue(), fmt.Sprintf("Resource '%s' should exist", uri))
}

// WaitForMCPServerReady waits for an MCP server to be ready and responsive
func WaitForMCPServerReady(config *TestConfig, serverURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Extract server name from URL for debugging
	serverName := extractServerNameFromURL(serverURL)

	for {
		select {
		case <-ctx.Done():
			// Before timing out, debug the server state
			GinkgoWriter.Printf("MCP server connection timed out, debugging server state...\n")
			DebugServerState(config, serverName)
			return fmt.Errorf("timeout waiting for MCP server to be ready at %s", serverURL)
		case <-ticker.C:
			// Try to create a client and initialize
			mcpClient, err := NewMCPClientForSSE(config, serverURL)
			if err != nil {
				GinkgoWriter.Printf("Failed to create MCP client for %s: %v\n", serverURL, err)
				continue
			}

			initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
			err = mcpClient.Initialize(initCtx)
			initCancel()

			if err == nil {
				// Successfully initialized, server is ready
				GinkgoWriter.Printf("MCP server ready at %s\n", serverURL)
				_ = mcpClient.Close()
				return nil
			}

			GinkgoWriter.Printf("MCP initialization failed for %s: %v\n", serverURL, err)
			_ = mcpClient.Close()
		}
	}
}

// extractServerNameFromURL extracts the server name from a URL like http://127.0.0.1:8080/sse#server-name
func extractServerNameFromURL(serverURL string) string {
	if idx := strings.Index(serverURL, "#"); idx != -1 {
		return serverURL[idx+1:]
	}
	return "unknown"
}

// TestMCPServerBasicFunctionality tests basic MCP server functionality
func TestMCPServerBasicFunctionality(config *TestConfig, serverURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create MCP client
	mcpClient, err := NewMCPClientForSSE(config, serverURL)
	if err != nil {
		return fmt.Errorf("failed to create MCP client: %w", err)
	}
	defer mcpClient.Close()

	// Initialize the connection
	if err := mcpClient.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize MCP connection: %w", err)
	}

	// Test ping
	if err := mcpClient.Ping(ctx); err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}

	// List tools
	tools, err := mcpClient.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("failed to list tools: %w", err)
	}

	if len(tools.Tools) == 0 {
		return fmt.Errorf("no tools available from MCP server")
	}

	// List resources (if supported)
	// Note: Not all MCP servers support resources, so we don't fail on this
	if _, err := mcpClient.ListResources(ctx); err != nil {
		GinkgoWriter.Printf("Note: Server does not support resources: %v\n", err)
	}

	return nil
}
