package ingestion

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/optimizer/db"
	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
)

// TestServiceCreationAndIngestion demonstrates the complete chromem-go workflow:
// 1. Create in-memory database
// 2. Initialize ingestion service
// 3. Ingest server and tools
// 4. Query the database
func TestServiceCreationAndIngestion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Create temporary directory for persistence (optional)
	tmpDir := t.TempDir()

	// Initialize service with placeholder embeddings (no dependencies)
	config := &Config{
		DBConfig: &db.Config{
			PersistPath: filepath.Join(tmpDir, "test-db"),
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "placeholder", // Use placeholder for testing
			Dimension:   384,
		},
	}

	svc, err := NewService(config)
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()

	// Create test tools
	tools := []mcp.Tool{
		{
			Name:        "get_weather",
			Description: "Get the current weather for a location",
		},
		{
			Name:        "search_web",
			Description: "Search the web for information",
		},
	}

	// Ingest server with tools
	serverName := "test-server"
	serverID := "test-server-id"
	description := "A test MCP server"

	err = svc.IngestServer(ctx, serverID, serverName, &description, tools)
	require.NoError(t, err)

	// Query tools
	allTools, err := svc.backendToolOps.ListByServer(ctx, serverID)
	require.NoError(t, err)
	require.Len(t, allTools, 2, "Expected 2 tools to be ingested")

	// Verify tool names
	toolNames := make(map[string]bool)
	for _, tool := range allTools {
		toolNames[tool.ToolName] = true
	}
	require.True(t, toolNames["get_weather"], "get_weather tool should be present")
	require.True(t, toolNames["search_web"], "search_web tool should be present")

	// Search for similar tools
	results, err := svc.backendToolOps.Search(ctx, "weather information", 5, &serverID)
	require.NoError(t, err)
	require.NotEmpty(t, results, "Should find at least one similar tool")

	// With placeholder embeddings (hash-based), semantic similarity isn't guaranteed
	// Just verify we got results back
	require.Len(t, results, 2, "Should return both tools")
	
	// Verify both tools are present (order doesn't matter with placeholder embeddings)
	toolNamesFound := make(map[string]bool)
	for _, result := range results {
		toolNamesFound[result.ToolName] = true
	}
	require.True(t, toolNamesFound["get_weather"], "get_weather should be in results")
	require.True(t, toolNamesFound["search_web"], "search_web should be in results")
}

// TestServiceWithOllama demonstrates using real embeddings (requires Ollama running)
// This test can be enabled locally to verify Ollama integration
func TestServiceWithOllama(t *testing.T) {
	t.Parallel()

	// Skip if not explicitly enabled or Ollama is not available
	if os.Getenv("TEST_OLLAMA") != "true" {
		t.Skip("Skipping Ollama integration test (set TEST_OLLAMA=true to enable)")
	}

	ctx := context.Background()
	tmpDir := t.TempDir()

	// Initialize service with Ollama embeddings
	config := &Config{
		DBConfig: &db.Config{
			PersistPath: filepath.Join(tmpDir, "ollama-db"),
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "nomic-embed-text",
			Dimension:   384,
		},
	}

	svc, err := NewService(config)
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()

	// Create test tools
	tools := []mcp.Tool{
		{
			Name:        "get_weather",
			Description: "Get current weather conditions for any location worldwide",
		},
		{
			Name:        "send_email",
			Description: "Send an email message to a recipient",
		},
	}

	// Ingest server
	err = svc.IngestServer(ctx, "server-1", "TestServer", nil, tools)
	require.NoError(t, err)

	// Search for weather-related tools
	results, err := svc.backendToolOps.Search(ctx, "What's the temperature outside?", 5, nil)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// With real embeddings, weather tool should be most similar
	require.Equal(t, "get_weather", results[0].ToolName, 
		"Weather tool should be most similar to weather query")
}
