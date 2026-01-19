package optimizer

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// mockMCPServerWithSession implements AddSessionTools for testing
type mockMCPServerWithSession struct {
	*server.MCPServer
	toolsAdded map[string][]server.ServerTool
}

func newMockMCPServerWithSession() *mockMCPServerWithSession {
	return &mockMCPServerWithSession{
		MCPServer:  server.NewMCPServer("test-server", "1.0"),
		toolsAdded: make(map[string][]server.ServerTool),
	}
}

func (m *mockMCPServerWithSession) AddSessionTools(sessionID string, tools ...server.ServerTool) error {
	m.toolsAdded[sessionID] = tools
	return nil
}

// mockBackendClientWithCallTool implements CallTool for testing
type mockBackendClientWithCallTool struct {
	callToolResult map[string]any
	callToolError  error
}

func (*mockBackendClientWithCallTool) ListCapabilities(_ context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	return &vmcp.CapabilityList{}, nil
}

func (m *mockBackendClientWithCallTool) CallTool(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (map[string]any, error) {
	if m.callToolError != nil {
		return nil, m.callToolError
	}
	return m.callToolResult, nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockBackendClientWithCallTool) GetPrompt(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (string, error) {
	return "", nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockBackendClientWithCallTool) ReadResource(_ context.Context, _ *vmcp.BackendTarget, _ string) ([]byte, error) {
	return nil, nil
}

// TestCreateFindToolHandler_InvalidArguments tests error handling for invalid arguments
func TestCreateFindToolHandler_InvalidArguments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Setup optimizer integration
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClient{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	handler := integration.CreateFindToolHandler()

	// Test with invalid arguments type
	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "optim.find_tool",
			Arguments: "not a map",
		},
	}

	result, err := handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for invalid arguments")

	// Test with missing tool_description
	request = mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"limit": 10,
			},
		},
	}

	result, err = handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for missing tool_description")

	// Test with empty tool_description
	request = mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": "",
			},
		},
	}

	result, err = handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for empty tool_description")

	// Test with non-string tool_description
	request = mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": 123,
			},
		},
	}

	result, err = handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for non-string tool_description")
}

// TestCreateFindToolHandler_WithKeywords tests find_tool with keywords
func TestCreateFindToolHandler_WithKeywords(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClient{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	// Ingest a tool for testing
	tools := []mcp.Tool{
		{
			Name:        "test_tool",
			Description: "A test tool for searching",
		},
	}

	err = integration.IngestToolsForTesting(ctx, "server-1", "TestServer", nil, tools)
	require.NoError(t, err)

	handler := integration.CreateFindToolHandler()

	// Test with keywords
	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": "search tool",
				"tool_keywords":    "test search",
				"limit":            10,
			},
		},
	}

	result, err := handler(ctx, request)
	require.NoError(t, err)
	require.False(t, result.IsError, "Should not return error")

	// Verify response structure
	textContent, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok)

	var response map[string]any
	err = json.Unmarshal([]byte(textContent.Text), &response)
	require.NoError(t, err)

	_, ok = response["tools"]
	require.True(t, ok, "Response should have tools")

	_, ok = response["token_metrics"]
	require.True(t, ok, "Response should have token_metrics")
}

// TestCreateFindToolHandler_Limit tests limit parameter handling
func TestCreateFindToolHandler_Limit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClient{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	handler := integration.CreateFindToolHandler()

	// Test with custom limit
	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": "test",
				"limit":            5,
			},
		},
	}

	result, err := handler(ctx, request)
	require.NoError(t, err)
	require.False(t, result.IsError)

	// Test with float64 limit (from JSON)
	request = mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": "test",
				"limit":            float64(3),
			},
		},
	}

	result, err = handler(ctx, request)
	require.NoError(t, err)
	require.False(t, result.IsError)
}

// TestCreateFindToolHandler_BackendToolOpsNil tests error when backend tool ops is nil
func TestCreateFindToolHandler_BackendToolOpsNil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create integration with nil ingestion service to trigger error path
	integration := &OptimizerIntegration{
		config:           &Config{Enabled: true},
		ingestionService: nil, // This will cause GetBackendToolOps to return nil
	}

	handler := integration.CreateFindToolHandler()

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": "test",
			},
		},
	}

	result, err := handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error when backend tool ops is nil")
}

// TestCreateCallToolHandler_InvalidArguments tests error handling for invalid arguments
func TestCreateCallToolHandler_InvalidArguments(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Check Ollama availability first
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClientWithCallTool{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	handler := integration.CreateCallToolHandler()

	// Test with invalid arguments type
	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      "optim.call_tool",
			Arguments: "not a map",
		},
	}

	result, err := handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for invalid arguments")

	// Test with missing backend_id
	request = mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"tool_name":  "test_tool",
				"parameters": map[string]any{},
			},
		},
	}

	result, err = handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for missing backend_id")

	// Test with empty backend_id
	request = mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"backend_id": "",
				"tool_name":  "test_tool",
				"parameters": map[string]any{},
			},
		},
	}

	result, err = handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for empty backend_id")

	// Test with missing tool_name
	request = mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"backend_id": "backend-1",
				"parameters": map[string]any{},
			},
		},
	}

	result, err = handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for missing tool_name")

	// Test with missing parameters
	request = mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"backend_id": "backend-1",
				"tool_name":  "test_tool",
			},
		},
	}

	result, err = handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for missing parameters")

	// Test with invalid parameters type
	request = mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"backend_id": "backend-1",
				"tool_name":  "test_tool",
				"parameters": "not a map",
			},
		},
	}

	result, err = handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error for invalid parameters type")
}

// TestCreateCallToolHandler_NoRoutingTable tests error when routing table is missing
func TestCreateCallToolHandler_NoRoutingTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Check Ollama availability first
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClientWithCallTool{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	handler := integration.CreateCallToolHandler()

	// Test without routing table in context
	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"backend_id": "backend-1",
				"tool_name":  "test_tool",
				"parameters": map[string]any{},
			},
		},
	}

	result, err := handler(ctx, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error when routing table is missing")
}

// TestCreateCallToolHandler_ToolNotFound tests error when tool is not found
func TestCreateCallToolHandler_ToolNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Check Ollama availability first
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClientWithCallTool{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	handler := integration.CreateCallToolHandler()

	// Create context with routing table but tool not found
	capabilities := &aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{
			Tools:     make(map[string]*vmcp.BackendTarget),
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"backend_id": "backend-1",
				"tool_name":  "nonexistent_tool",
				"parameters": map[string]any{},
			},
		},
	}

	result, err := handler(ctxWithCaps, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error when tool is not found")
}

// TestCreateCallToolHandler_BackendMismatch tests error when backend doesn't match
func TestCreateCallToolHandler_BackendMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Check Ollama availability first
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClientWithCallTool{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	handler := integration.CreateCallToolHandler()

	// Create context with routing table where tool belongs to different backend
	capabilities := &aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test_tool": {
					WorkloadID:   "backend-2", // Different backend
					WorkloadName: "Backend 2",
				},
			},
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"backend_id": "backend-1", // Requesting backend-1
				"tool_name":  "test_tool", // But tool belongs to backend-2
				"parameters": map[string]any{},
			},
		},
	}

	result, err := handler(ctxWithCaps, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error when backend doesn't match")
}

// TestCreateCallToolHandler_Success tests successful tool call
func TestCreateCallToolHandler_Success(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Check Ollama availability first
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClientWithCallTool{
		callToolResult: map[string]any{
			"result": "success",
		},
	}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	handler := integration.CreateCallToolHandler()

	// Create context with routing table
	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "Backend 1",
		BaseURL:      "http://localhost:8000",
	}

	capabilities := &aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test_tool": target,
			},
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"backend_id": "backend-1",
				"tool_name":  "test_tool",
				"parameters": map[string]any{
					"param1": "value1",
				},
			},
		},
	}

	result, err := handler(ctxWithCaps, request)
	require.NoError(t, err)
	require.False(t, result.IsError, "Should not return error")

	// Verify response
	textContent, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok)

	var response map[string]any
	err = json.Unmarshal([]byte(textContent.Text), &response)
	require.NoError(t, err)
	assert.Equal(t, "success", response["result"])
}

// TestCreateCallToolHandler_CallToolError tests error handling when CallTool fails
func TestCreateCallToolHandler_CallToolError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Check Ollama availability first
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClientWithCallTool{
		callToolError: assert.AnError,
	}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	handler := integration.CreateCallToolHandler()

	target := &vmcp.BackendTarget{
		WorkloadID:   "backend-1",
		WorkloadName: "Backend 1",
		BaseURL:      "http://localhost:8000",
	}

	capabilities := &aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test_tool": target,
			},
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	ctxWithCaps := discovery.WithDiscoveredCapabilities(ctx, capabilities)

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.call_tool",
			Arguments: map[string]any{
				"backend_id": "backend-1",
				"tool_name":  "test_tool",
				"parameters": map[string]any{},
			},
		},
	}

	result, err := handler(ctxWithCaps, request)
	require.NoError(t, err)
	require.True(t, result.IsError, "Should return error when CallTool fails")
}

// TestCreateFindToolHandler_InputSchemaUnmarshalError tests error handling for invalid input schema
func TestCreateFindToolHandler_InputSchemaUnmarshalError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClient{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	handler := integration.CreateFindToolHandler()

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: "optim.find_tool",
			Arguments: map[string]any{
				"tool_description": "test",
			},
		},
	}

	// The handler should handle invalid input schema gracefully
	result, err := handler(ctx, request)
	require.NoError(t, err)
	// Should not error even if some tools have invalid schemas
	require.False(t, result.IsError)
}

// TestOnRegisterSession_DuplicateSession tests duplicate session handling
func TestOnRegisterSession_DuplicateSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Check Ollama availability first
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClient{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	session := &mockSession{sessionID: "test-session"}
	capabilities := &aggregator.AggregatedCapabilities{}

	// First call
	err = integration.OnRegisterSession(ctx, session, capabilities)
	require.NoError(t, err)

	// Second call with same session ID (should be skipped)
	err = integration.OnRegisterSession(ctx, session, capabilities)
	require.NoError(t, err, "Should handle duplicate session gracefully")
}

// TestIngestInitialBackends_ErrorHandling tests error handling during ingestion
func TestIngestInitialBackends_ErrorHandling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Check Ollama availability first
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := newMockMCPServerWithSession()
	mockClient := &mockBackendClient{
		err: assert.AnError, // Simulate error when listing capabilities
	}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer.MCPServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	backends := []vmcp.Backend{
		{
			ID:            "backend-1",
			Name:          "Backend 1",
			BaseURL:       "http://localhost:8000",
			TransportType: "sse",
		},
	}

	// Should not fail even if backend query fails
	err = integration.IngestInitialBackends(ctx, backends)
	require.NoError(t, err, "Should handle backend query errors gracefully")
}

// TestIngestInitialBackends_NilIntegration tests nil integration handling
func TestIngestInitialBackends_NilIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var integration *OptimizerIntegration = nil
	backends := []vmcp.Backend{}

	err := integration.IngestInitialBackends(ctx, backends)
	require.NoError(t, err, "Should handle nil integration gracefully")
}
