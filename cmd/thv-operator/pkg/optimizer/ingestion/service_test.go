// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ingestion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/db"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/embeddings"
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

	// Try to use Ollama if available, otherwise skip test
	// Check for the actual model we'll use: nomic-embed-text
	embeddingConfig := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "nomic-embed-text",
		Dimension:   768,
	}

	embeddingManager, err := embeddings.NewManager(embeddingConfig)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available or model not found. Error: %v. Run 'ollama serve && ollama pull nomic-embed-text'", err)
		return
	}
	_ = embeddingManager.Close()

	// Initialize service with Ollama embeddings
	config := &Config{
		DBConfig: &db.Config{
			PersistPath: filepath.Join(tmpDir, "test-db"),
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "nomic-embed-text",
			Dimension:   768,
		},
	}

	svc, err := NewService(config)
	if err != nil {
		t.Skipf("Skipping test: Failed to create service. Error: %v. Run 'ollama serve && ollama pull nomic-embed-text'", err)
		return
	}
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
	if err != nil {
		// Check if error is due to missing model
		errStr := err.Error()
		if strings.Contains(errStr, "model") || strings.Contains(errStr, "not found") || strings.Contains(errStr, "404") {
			t.Skipf("Skipping test: Model not available. Error: %v. Run 'ollama serve && ollama pull nomic-embed-text'", err)
			return
		}
		require.NoError(t, err)
	}

	// Query tools
	allTools, err := svc.database.ListToolsByServer(ctx, serverID)
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
	hybridConfig := &db.HybridSearchConfig{
		SemanticRatio: 70,
		Limit:         5,
		ServerID:      &serverID,
	}
	results, err := svc.database.SearchToolsHybrid(ctx, "weather information", hybridConfig)
	require.NoError(t, err)
	require.NotEmpty(t, results, "Should find at least one similar tool")

	require.NotEmpty(t, results, "Should return at least one result")

	// Weather tool should be most similar to weather query
	require.Equal(t, "get_weather", results[0].ToolName,
		"Weather tool should be most similar to weather query")
	toolNamesFound := make(map[string]bool)
	for _, result := range results {
		toolNamesFound[result.ToolName] = true
	}
	require.True(t, toolNamesFound["get_weather"], "get_weather should be in results")
	require.True(t, toolNamesFound["search_web"], "search_web should be in results")
}

// TestService_EmbeddingTimeTracking tests that embedding time is tracked correctly
func TestService_EmbeddingTimeTracking(t *testing.T) {
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

	// Initialize service
	config := &Config{
		DBConfig: &db.Config{
			PersistPath: filepath.Join(tmpDir, "test-db"),
		},
		EmbeddingConfig: &embeddings.Config{
			BackendType: "ollama",
			BaseURL:     "http://localhost:11434",
			Model:       "all-minilm",
			Dimension:   384,
		},
	}

	svc, err := NewService(config)
	require.NoError(t, err)
	defer func() { _ = svc.Close() }()

	// Initially, embedding time should be 0
	initialTime := svc.GetTotalEmbeddingTime()
	require.Equal(t, time.Duration(0), initialTime, "Initial embedding time should be 0")

	// Create test tools
	tools := []mcp.Tool{
		{
			Name:        "test_tool_1",
			Description: "First test tool for embedding",
		},
		{
			Name:        "test_tool_2",
			Description: "Second test tool for embedding",
		},
	}

	// Reset embedding time before ingestion
	svc.ResetEmbeddingTime()

	// Ingest server with tools (this will generate embeddings)
	err = svc.IngestServer(ctx, "test-server-id", "TestServer", nil, tools)
	require.NoError(t, err)

	// After ingestion, embedding time should be greater than 0
	totalEmbeddingTime := svc.GetTotalEmbeddingTime()
	require.Greater(t, totalEmbeddingTime, time.Duration(0),
		"Total embedding time should be greater than 0 after ingestion")

	// Reset and verify it's back to 0
	svc.ResetEmbeddingTime()
	resetTime := svc.GetTotalEmbeddingTime()
	require.Equal(t, time.Duration(0), resetTime, "Embedding time should be 0 after reset")
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
	hybridConfig := &db.HybridSearchConfig{
		SemanticRatio: 70,
		Limit:         5,
		ServerID:      nil,
	}
	results, err := svc.database.SearchToolsHybrid(ctx, "What's the temperature outside?", hybridConfig)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	require.Equal(t, "get_weather", results[0].ToolName,
		"Weather tool should be most similar to weather query")
}
