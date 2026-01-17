package optimizer

import (
	"context"
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
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// mockBackendClient implements vmcp.BackendClient for testing
type mockBackendClient struct {
	capabilities *vmcp.CapabilityList
	err          error
}

func (m *mockBackendClient) ListCapabilities(_ context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.capabilities, nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockBackendClient) CallTool(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (map[string]any, error) {
	return nil, nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockBackendClient) GetPrompt(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (string, error) {
	return "", nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockBackendClient) ReadResource(_ context.Context, _ *vmcp.BackendTarget, _ string) ([]byte, error) {
	return nil, nil
}

// mockSession implements server.ClientSession for testing
type mockSession struct {
	sessionID string
}

func (m *mockSession) SessionID() string {
	return m.sessionID
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockSession) Send(_ interface{}) error {
	return nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockSession) Close() error {
	return nil
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockSession) Initialize() {
	// No-op for testing
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockSession) Initialized() bool {
	return true
}

//nolint:revive // Receiver unused in mock implementation
func (m *mockSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	// Return a dummy channel for testing
	ch := make(chan mcp.JSONRPCNotification, 1)
	return ch
}

// TestNewIntegration_Disabled tests that nil is returned when optimizer is disabled
func TestNewIntegration_Disabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Test with nil config
	integration, err := NewIntegration(ctx, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, integration, "Should return nil when config is nil")

	// Test with disabled config
	config := &Config{Enabled: false}
	integration, err = NewIntegration(ctx, config, nil, nil, nil)
	require.NoError(t, err)
	assert.Nil(t, integration, "Should return nil when optimizer is disabled")
}

// TestNewIntegration_Enabled tests successful creation
func TestNewIntegration_Enabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Try to use Ollama if available, otherwise skip test
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull all-minilm'", err)
		return
	}
	_ = embeddingManager.Close()

	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "nomic-embed-text",
			Dimension:   768,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	require.NotNil(t, integration)
	defer func() { _ = integration.Close() }()
}

// TestOnRegisterSession tests session registration
func TestOnRegisterSession(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	// Try to use Ollama if available, otherwise skip test
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull all-minilm'", err)
		return
	}
	_ = embeddingManager.Close()

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "nomic-embed-text",
			Dimension:   768,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	session := &mockSession{sessionID: "test-session"}
	capabilities := &aggregator.AggregatedCapabilities{
		Tools: []vmcp.Tool{
			{
				Name:        "test_tool",
				Description: "A test tool",
				BackendID:   "backend-1",
			},
		},
		RoutingTable: &vmcp.RoutingTable{
			Tools: map[string]*vmcp.BackendTarget{
				"test_tool": {
					WorkloadID:   "backend-1",
					WorkloadName: "Test Backend",
				},
			},
			Resources: map[string]*vmcp.BackendTarget{},
			Prompts:   map[string]*vmcp.BackendTarget{},
		},
	}

	err = integration.OnRegisterSession(ctx, session, capabilities)
	assert.NoError(t, err)
}

// TestOnRegisterSession_NilIntegration tests nil integration handling
func TestOnRegisterSession_NilIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var integration *OptimizerIntegration = nil
	session := &mockSession{sessionID: "test-session"}
	capabilities := &aggregator.AggregatedCapabilities{}

	err := integration.OnRegisterSession(ctx, session, capabilities)
	assert.NoError(t, err)
}

// TestRegisterTools tests tool registration behavior
func TestRegisterTools(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	// Try to use Ollama if available, otherwise skip test
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull all-minilm'", err)
		return
	}
	_ = embeddingManager.Close()

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "nomic-embed-text",
			Dimension:   768,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)
	defer func() { _ = integration.Close() }()

	session := &mockSession{sessionID: "test-session"}
	// RegisterTools will fail with "session not found" because the mock session
	// is not actually registered with the MCP server. This is expected behavior.
	// We're just testing that the method executes without panicking.
	_ = integration.RegisterTools(ctx, session)
}

// TestRegisterTools_NilIntegration tests nil integration handling
func TestRegisterTools_NilIntegration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var integration *OptimizerIntegration = nil
	session := &mockSession{sessionID: "test-session"}

	err := integration.RegisterTools(ctx, session)
	assert.NoError(t, err)
}

// TestClose tests cleanup
func TestClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tmpDir := t.TempDir()

	mcpServer := server.NewMCPServer("test-server", "1.0")
	mockClient := &mockBackendClient{}

	// Try to use Ollama if available, otherwise skip test
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull all-minilm'", err)
		return
	}
	_ = embeddingManager.Close()

	config := &Config{
		Enabled:     true,
		PersistPath: filepath.Join(tmpDir, "optimizer-db"),
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "nomic-embed-text",
			Dimension:   768,
		},
	}

	sessionMgr := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
	integration, err := NewIntegration(ctx, config, mcpServer, mockClient, sessionMgr)
	require.NoError(t, err)

	err = integration.Close()
	assert.NoError(t, err)

	// Multiple closes should be safe
	err = integration.Close()
	assert.NoError(t, err)
}

// TestClose_NilIntegration tests nil integration close
func TestClose_NilIntegration(t *testing.T) {
	t.Parallel()

	var integration *OptimizerIntegration = nil
	err := integration.Close()
	assert.NoError(t, err)
}
