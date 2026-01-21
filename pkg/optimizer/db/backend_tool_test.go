// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/optimizer/embeddings"
	"github.com/stacklok/toolhive/pkg/optimizer/models"
)

// createTestDB creates a test database
func createTestDB(t *testing.T) *DB {
	t.Helper()
	tmpDir := t.TempDir()

	config := &Config{
		PersistPath: filepath.Join(tmpDir, "test-db"),
	}

	db, err := NewDB(config)
	require.NoError(t, err)

	return db
}

// createTestEmbeddingFunc creates a test embedding function using Ollama embeddings
func createTestEmbeddingFunc(t *testing.T) func(ctx context.Context, text string) ([]float32, error) {
	t.Helper()

	// Try to use Ollama if available, otherwise skip test
	config := &embeddings.Config{
		BackendType: "ollama",
		BaseURL:     "http://localhost:11434",
		Model:       "all-minilm",
		Dimension:   384,
	}

	manager, err := embeddings.NewManager(config)
	if err != nil {
		t.Skipf("Skipping test: Ollama not available. Error: %v. Run 'ollama serve && ollama pull all-minilm'", err)
		return nil
	}
	t.Cleanup(func() { _ = manager.Close() })

	return func(_ context.Context, text string) ([]float32, error) {
		results, err := manager.GenerateEmbedding([]string{text})
		if err != nil {
			return nil, err
		}
		if len(results) == 0 {
			return nil, assert.AnError
		}
		return results[0], nil
	}
}

// TestBackendToolOps_Create tests creating a backend tool
func TestBackendToolOps_Create(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	description := "Get current weather information"
	tool := &models.BackendTool{
		ID:          "tool-1",
		MCPServerID: "server-1",
		ToolName:    "get_weather",
		Description: &description,
		InputSchema: []byte(`{"type":"object","properties":{"location":{"type":"string"}}}`),
		TokenCount:  100,
	}

	err := ops.Create(ctx, tool, "Test Server")
	require.NoError(t, err)

	// Verify tool was created by retrieving it
	retrieved, err := ops.Get(ctx, "tool-1")
	require.NoError(t, err)
	assert.Equal(t, "get_weather", retrieved.ToolName)
	assert.Equal(t, "server-1", retrieved.MCPServerID)
	assert.Equal(t, description, *retrieved.Description)
}

// TestBackendToolOps_CreateWithPrecomputedEmbedding tests creating tool with existing embedding
func TestBackendToolOps_CreateWithPrecomputedEmbedding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	description := "Search the web"
	// Generate a precomputed embedding
	precomputedEmbedding := make([]float32, 384)
	for i := range precomputedEmbedding {
		precomputedEmbedding[i] = 0.1
	}

	tool := &models.BackendTool{
		ID:            "tool-2",
		MCPServerID:   "server-1",
		ToolName:      "search_web",
		Description:   &description,
		InputSchema:   []byte(`{}`),
		ToolEmbedding: precomputedEmbedding,
		TokenCount:    50,
	}

	err := ops.Create(ctx, tool, "Test Server")
	require.NoError(t, err)

	// Verify tool was created
	retrieved, err := ops.Get(ctx, "tool-2")
	require.NoError(t, err)
	assert.Equal(t, "search_web", retrieved.ToolName)
}

// TestBackendToolOps_Get tests retrieving a backend tool
func TestBackendToolOps_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Create a tool first
	description := "Send an email"
	tool := &models.BackendTool{
		ID:          "tool-3",
		MCPServerID: "server-1",
		ToolName:    "send_email",
		Description: &description,
		InputSchema: []byte(`{}`),
		TokenCount:  75,
	}

	err := ops.Create(ctx, tool, "Test Server")
	require.NoError(t, err)

	// Test Get
	retrieved, err := ops.Get(ctx, "tool-3")
	require.NoError(t, err)
	assert.Equal(t, "tool-3", retrieved.ID)
	assert.Equal(t, "send_email", retrieved.ToolName)
}

// TestBackendToolOps_Get_NotFound tests retrieving non-existent tool
func TestBackendToolOps_Get_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Try to get a non-existent tool
	_, err := ops.Get(ctx, "non-existent")
	assert.Error(t, err)
}

// TestBackendToolOps_Update tests updating a backend tool
func TestBackendToolOps_Update(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Create initial tool
	description := "Original description"
	tool := &models.BackendTool{
		ID:          "tool-4",
		MCPServerID: "server-1",
		ToolName:    "test_tool",
		Description: &description,
		InputSchema: []byte(`{}`),
		TokenCount:  50,
	}

	err := ops.Create(ctx, tool, "Test Server")
	require.NoError(t, err)

	// Update the tool
	const updatedDescription = "Updated description"
	updatedDescriptionCopy := updatedDescription
	tool.Description = &updatedDescriptionCopy
	tool.TokenCount = 75

	err = ops.Update(ctx, tool)
	require.NoError(t, err)

	// Verify update
	retrieved, err := ops.Get(ctx, "tool-4")
	require.NoError(t, err)
	assert.Equal(t, "Updated description", *retrieved.Description)
}

// TestBackendToolOps_Delete tests deleting a backend tool
func TestBackendToolOps_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Create a tool
	description := "Tool to delete"
	tool := &models.BackendTool{
		ID:          "tool-5",
		MCPServerID: "server-1",
		ToolName:    "delete_me",
		Description: &description,
		InputSchema: []byte(`{}`),
		TokenCount:  25,
	}

	err := ops.Create(ctx, tool, "Test Server")
	require.NoError(t, err)

	// Delete the tool
	err = ops.Delete(ctx, "tool-5")
	require.NoError(t, err)

	// Verify deletion
	_, err = ops.Get(ctx, "tool-5")
	assert.Error(t, err, "Should not find deleted tool")
}

// TestBackendToolOps_Delete_NonExistent tests deleting non-existent tool
func TestBackendToolOps_Delete_NonExistent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Try to delete a non-existent tool - should not error
	err := ops.Delete(ctx, "non-existent")
	// Delete may or may not error depending on implementation
	// Just ensure it doesn't panic
	_ = err
}

// TestBackendToolOps_ListByServer tests listing tools for a server
func TestBackendToolOps_ListByServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Create multiple tools for different servers
	desc1 := "Tool 1"
	tool1 := &models.BackendTool{
		ID:          "tool-1",
		MCPServerID: "server-1",
		ToolName:    "tool_1",
		Description: &desc1,
		InputSchema: []byte(`{}`),
		TokenCount:  10,
	}

	desc2 := "Tool 2"
	tool2 := &models.BackendTool{
		ID:          "tool-2",
		MCPServerID: "server-1",
		ToolName:    "tool_2",
		Description: &desc2,
		InputSchema: []byte(`{}`),
		TokenCount:  20,
	}

	desc3 := "Tool 3"
	tool3 := &models.BackendTool{
		ID:          "tool-3",
		MCPServerID: "server-2",
		ToolName:    "tool_3",
		Description: &desc3,
		InputSchema: []byte(`{}`),
		TokenCount:  30,
	}

	err := ops.Create(ctx, tool1, "Server 1")
	require.NoError(t, err)
	err = ops.Create(ctx, tool2, "Server 1")
	require.NoError(t, err)
	err = ops.Create(ctx, tool3, "Server 2")
	require.NoError(t, err)

	// List tools for server-1
	tools, err := ops.ListByServer(ctx, "server-1")
	require.NoError(t, err)
	assert.Len(t, tools, 2, "Should have 2 tools for server-1")

	// Verify tool names
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		toolNames[tool.ToolName] = true
	}
	assert.True(t, toolNames["tool_1"])
	assert.True(t, toolNames["tool_2"])

	// List tools for server-2
	tools, err = ops.ListByServer(ctx, "server-2")
	require.NoError(t, err)
	assert.Len(t, tools, 1, "Should have 1 tool for server-2")
	assert.Equal(t, "tool_3", tools[0].ToolName)
}

// TestBackendToolOps_ListByServer_Empty tests listing tools for server with no tools
func TestBackendToolOps_ListByServer_Empty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// List tools for non-existent server
	tools, err := ops.ListByServer(ctx, "non-existent-server")
	require.NoError(t, err)
	assert.Empty(t, tools, "Should return empty list for server with no tools")
}

// TestBackendToolOps_DeleteByServer tests deleting all tools for a server
func TestBackendToolOps_DeleteByServer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Create tools for two servers
	desc1 := "Tool 1"
	tool1 := &models.BackendTool{
		ID:          "tool-1",
		MCPServerID: "server-1",
		ToolName:    "tool_1",
		Description: &desc1,
		InputSchema: []byte(`{}`),
		TokenCount:  10,
	}

	desc2 := "Tool 2"
	tool2 := &models.BackendTool{
		ID:          "tool-2",
		MCPServerID: "server-1",
		ToolName:    "tool_2",
		Description: &desc2,
		InputSchema: []byte(`{}`),
		TokenCount:  20,
	}

	desc3 := "Tool 3"
	tool3 := &models.BackendTool{
		ID:          "tool-3",
		MCPServerID: "server-2",
		ToolName:    "tool_3",
		Description: &desc3,
		InputSchema: []byte(`{}`),
		TokenCount:  30,
	}

	err := ops.Create(ctx, tool1, "Server 1")
	require.NoError(t, err)
	err = ops.Create(ctx, tool2, "Server 1")
	require.NoError(t, err)
	err = ops.Create(ctx, tool3, "Server 2")
	require.NoError(t, err)

	// Delete all tools for server-1
	err = ops.DeleteByServer(ctx, "server-1")
	require.NoError(t, err)

	// Verify server-1 tools are deleted
	tools, err := ops.ListByServer(ctx, "server-1")
	require.NoError(t, err)
	assert.Empty(t, tools, "All server-1 tools should be deleted")

	// Verify server-2 tools are still present
	tools, err = ops.ListByServer(ctx, "server-2")
	require.NoError(t, err)
	assert.Len(t, tools, 1, "Server-2 tools should remain")
}

// TestBackendToolOps_Search tests semantic search for tools
func TestBackendToolOps_Search(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Create test tools
	desc1 := "Get current weather conditions"
	tool1 := &models.BackendTool{
		ID:          "tool-1",
		MCPServerID: "server-1",
		ToolName:    "get_weather",
		Description: &desc1,
		InputSchema: []byte(`{}`),
		TokenCount:  50,
	}

	desc2 := "Send email message"
	tool2 := &models.BackendTool{
		ID:          "tool-2",
		MCPServerID: "server-1",
		ToolName:    "send_email",
		Description: &desc2,
		InputSchema: []byte(`{}`),
		TokenCount:  40,
	}

	err := ops.Create(ctx, tool1, "Server 1")
	require.NoError(t, err)
	err = ops.Create(ctx, tool2, "Server 1")
	require.NoError(t, err)

	// Search for tools
	results, err := ops.Search(ctx, "weather information", 5, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, results, "Should find tools")

	// Weather tool should be most similar to weather query
	assert.NotEmpty(t, results, "Should find at least one tool")
	if len(results) > 0 {
		assert.Equal(t, "get_weather", results[0].ToolName,
			"Weather tool should be most similar to weather query")
	}
}

// TestBackendToolOps_Search_WithServerFilter tests search with server ID filter
func TestBackendToolOps_Search_WithServerFilter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Create tools for different servers
	desc1 := "Weather tool"
	tool1 := &models.BackendTool{
		ID:          "tool-1",
		MCPServerID: "server-1",
		ToolName:    "get_weather",
		Description: &desc1,
		InputSchema: []byte(`{}`),
		TokenCount:  50,
	}

	desc2 := "Email tool"
	tool2 := &models.BackendTool{
		ID:          "tool-2",
		MCPServerID: "server-2",
		ToolName:    "send_email",
		Description: &desc2,
		InputSchema: []byte(`{}`),
		TokenCount:  40,
	}

	err := ops.Create(ctx, tool1, "Server 1")
	require.NoError(t, err)
	err = ops.Create(ctx, tool2, "Server 2")
	require.NoError(t, err)

	// Search with server filter
	serverID := "server-1"
	results, err := ops.Search(ctx, "tool", 5, &serverID)
	require.NoError(t, err)
	assert.Len(t, results, 1, "Should only return tools from server-1")
	assert.Equal(t, "server-1", results[0].MCPServerID)
}

// TestBackendToolOps_Search_Empty tests search on empty database
func TestBackendToolOps_Search_Empty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendToolOps(db, embeddingFunc)

	// Search empty database
	results, err := ops.Search(ctx, "anything", 5, nil)
	require.NoError(t, err)
	assert.Empty(t, results, "Should return empty results for empty database")
}

// TestBackendToolOps_MetadataSerialization tests metadata serialization/deserialization
func TestBackendToolOps_MetadataSerialization(t *testing.T) {
	t.Parallel()

	description := "Test tool"
	tool := &models.BackendTool{
		ID:          "tool-1",
		MCPServerID: "server-1",
		ToolName:    "test_tool",
		Description: &description,
		InputSchema: []byte(`{"type":"object"}`),
		TokenCount:  100,
	}

	// Test serialization
	metadata, err := serializeToolMetadata(tool)
	require.NoError(t, err)
	assert.Contains(t, metadata, "data")
	assert.Equal(t, "backend_tool", metadata["type"])
	assert.Equal(t, "server-1", metadata["server_id"])

	// Test deserialization
	deserializedTool, err := deserializeToolMetadata(metadata)
	require.NoError(t, err)
	assert.Equal(t, tool.ID, deserializedTool.ID)
	assert.Equal(t, tool.ToolName, deserializedTool.ToolName)
	assert.Equal(t, tool.MCPServerID, deserializedTool.MCPServerID)
}

// TestBackendToolOps_MetadataDeserialization_MissingData tests error handling
func TestBackendToolOps_MetadataDeserialization_MissingData(t *testing.T) {
	t.Parallel()

	// Test with missing data field
	metadata := map[string]string{
		"type": "backend_tool",
	}

	_, err := deserializeToolMetadata(metadata)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing data field")
}

// TestBackendToolOps_MetadataDeserialization_InvalidJSON tests invalid JSON handling
func TestBackendToolOps_MetadataDeserialization_InvalidJSON(t *testing.T) {
	t.Parallel()

	// Test with invalid JSON
	metadata := map[string]string{
		"data": "invalid json {",
		"type": "backend_tool",
	}

	_, err := deserializeToolMetadata(metadata)
	assert.Error(t, err)
}
