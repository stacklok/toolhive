package optimizer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// mockBackendClient implements vmcp.BackendClient for integration testing
type mockIntegrationBackendClient struct {
	backends map[string]*vmcp.CapabilityList
}

func newMockIntegrationBackendClient() *mockIntegrationBackendClient {
	return &mockIntegrationBackendClient{
		backends: make(map[string]*vmcp.CapabilityList),
	}
}

func (m *mockIntegrationBackendClient) addBackend(backendID string, caps *vmcp.CapabilityList) {
	m.backends[backendID] = caps
}

func (m *mockIntegrationBackendClient) ListCapabilities(_ context.Context, target *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	if caps, exists := m.backends[target.WorkloadID]; exists {
		return caps, nil
	}
	return &vmcp.CapabilityList{}, nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockIntegrationBackendClient) CallTool(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (map[string]any, error) {
	return nil, nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockIntegrationBackendClient) GetPrompt(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (string, error) {
	return "", nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockIntegrationBackendClient) ReadResource(_ context.Context, _ *vmcp.BackendTarget, _ string) ([]byte, error) {
	return nil, nil
}

// mockIntegrationSession implements server.ClientSession for testing
type mockIntegrationSession struct {
	sessionID string
}

func (m *mockIntegrationSession) SessionID() string {
	return m.sessionID
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockIntegrationSession) Send(_ interface{}) error {
	return nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockIntegrationSession) Close() error {
	return nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockIntegrationSession) Initialize() {
	// No-op for testing
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockIntegrationSession) Initialized() bool {
	return true
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockIntegrationSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	// Return a dummy channel for testing
	ch := make(chan mcp.JSONRPCNotification, 1)
	return ch
}

// TestOptimizerIntegration_WithVMCP tests the complete integration with vMCP
func TestOptimizerIntegration_WithVMCP(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create MCP server
	mcpServer := server.NewMCPServer("vmcp-test", "1.0")

	// Create mock backend client
	mockClient := newMockIntegrationBackendClient()
	mockClient.addBackend("github", &vmcp.CapabilityList{
		Tools: []vmcp.Tool{
			{
				Name:        "create_issue",
				Description: "Create a GitHub issue",
			},
		},
	})

	// Try to use Ollama if available, otherwise skip test
	embeddingConfig := &embeddings.Config{
		BackendType: embeddings.BackendTypeOllama,
		BaseURL:     "http://localhost:11434",
		Model:       embeddings.DefaultModelAllMiniLM,
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull %s'", err, embeddings.DefaultModelAllMiniLM)
		return
	}
	t.Cleanup(func() { _ = embeddingManager.Close() })

	// Configure optimizer
	optimizerConfig := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: embeddings.BackendTypeOllama,
			BaseURL:     "http://localhost:11434",
			Model:       embeddings.DefaultModelAllMiniLM,
			Dimension:   384,
		},
	}

	// Create optimizer integration
	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, optimizerConfig, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	// Ingest backends
	backends := []vmcp.Backend{
		{
			ID:            "github",
			Name:          "GitHub",
			BaseURL:       "http://localhost:8000",
			TransportType: "sse",
		},
	}

	err = integration.IngestInitialBackends(ctx, backends)
	require.NoError(t, err)

	// Simulate session registration
	session := &mockIntegrationSession{sessionID: "test-session"}
	capabilities := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{
			{
				Name:        "create_issue",
				Description: "Create a GitHub issue",
				BackendID:   "github",
			},
		},
		RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"create_issue": {
					WorkloadID:   "github",
					WorkloadName: "GitHub",
				},
			},
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	err = integration.OnRegisterSession(ctx, session, capabilities)
	require.NoError(t, err)

	// Note: We don't test RegisterTools here because it requires the session
	// to be properly registered with the MCP server, which is beyond the scope
	// of this integration test. The RegisterTools method is tested separately
	// in unit tests where we can properly mock the MCP server behavior.
}

// TestOptimizerIntegration_EmbeddingTimeTracking tests that embedding time is tracked and logged
func TestOptimizerIntegration_EmbeddingTimeTracking(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create MCP server
	mcpServer := server.NewMCPServer("vmcp-test", "1.0")

	// Create mock backend client
	mockClient := newMockIntegrationBackendClient()
	mockClient.addBackend("github", &vmcp.CapabilityList{
		Tools: []vmcp.Tool{
			{
				Name:        "create_issue",
				Description: "Create a GitHub issue",
			},
			{
				Name:        "get_repo",
				Description: "Get repository information",
			},
		},
	})

	// Try to use Ollama if available, otherwise skip test
	embeddingConfig := &embeddings.Config{
		BackendType: embeddings.BackendTypeOllama,
		BaseURL:     "http://localhost:11434",
		Model:       embeddings.DefaultModelAllMiniLM,
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull %s'", err, embeddings.DefaultModelAllMiniLM)
		return
	}
	t.Cleanup(func() { _ = embeddingManager.Close() })

	// Configure optimizer
	optimizerConfig := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: embeddings.BackendTypeOllama,
			BaseURL:     "http://localhost:11434",
			Model:       embeddings.DefaultModelAllMiniLM,
			Dimension:   384,
		},
	}

	// Create optimizer integration
	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, optimizerConfig, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	// Verify embedding time starts at 0
	embeddingTime := integration.ingestionService.GetTotalEmbeddingTime()
	require.Equal(t, time.Duration(0), embeddingTime, "Initial embedding time should be 0")

	// Ingest backends
	backends := []vmcp.Backend{
		{
			ID:            "github",
			Name:          "GitHub",
			BaseURL:       "http://localhost:8000",
			TransportType: "sse",
		},
	}

	err = integration.IngestInitialBackends(ctx, backends)
	require.NoError(t, err)

	// After ingestion, embedding time should be tracked
	// Note: The actual time depends on Ollama performance, but it should be > 0
	finalEmbeddingTime := integration.ingestionService.GetTotalEmbeddingTime()
	require.Greater(t, finalEmbeddingTime, time.Duration(0), 
		"Embedding time should be tracked after ingestion")
}

// TestOptimizerIntegration_DisabledEmbeddingTime tests that embedding time is 0 when optimizer is disabled
func TestOptimizerIntegration_DisabledEmbeddingTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create optimizer integration with disabled optimizer
	optimizerConfig := &Config{
		Enabled: false,
	}

	mcpServer := server.NewMCPServer("vmcp-test", "1.0")
	mockClient := newMockIntegrationBackendClient()
	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	
	integration, err := NewIntegration(ctx, optimizerConfig, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	require.Nil(t, integration, "Integration should be nil when optimizer is disabled")

	// Try to ingest backends - should return nil without error
	backends := []vmcp.Backend{
		{
			ID:            "github",
			Name:          "GitHub",
			BaseURL:       "http://localhost:8000",
			TransportType: "sse",
		},
	}

	// This should handle nil integration gracefully
	var nilIntegration *OptimizerIntegration
	err = nilIntegration.IngestInitialBackends(ctx, backends)
	require.NoError(t, err, "Should handle nil integration gracefully")
}
