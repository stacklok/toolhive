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

	// Configure optimizer
	optimizerConfig := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
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
