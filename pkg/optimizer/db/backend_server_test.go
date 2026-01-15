package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/optimizer/models"
)

// TestBackendServerOps_Create tests creating a backend server
func TestBackendServerOps_Create(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	description := "A test MCP server"
	server := &models.BackendServer{
		ID:          "server-1",
		Name:        "Test Server",
		Description: &description,
		Group:       "default",
	}

	err := ops.Create(ctx, server)
	require.NoError(t, err)

	// Verify server was created by retrieving it
	retrieved, err := ops.Get(ctx, "server-1")
	require.NoError(t, err)
	assert.Equal(t, "Test Server", retrieved.Name)
	assert.Equal(t, "server-1", retrieved.ID)
	assert.Equal(t, description, *retrieved.Description)
}

// TestBackendServerOps_CreateWithEmbedding tests creating server with precomputed embedding
func TestBackendServerOps_CreateWithEmbedding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	description := "Server with embedding"
	embedding := make([]float32, 384)
	for i := range embedding {
		embedding[i] = 0.5
	}

	server := &models.BackendServer{
		ID:              "server-2",
		Name:            "Embedded Server",
		Description:     &description,
		Group:           "default",
		ServerEmbedding: embedding,
	}

	err := ops.Create(ctx, server)
	require.NoError(t, err)

	// Verify server was created
	retrieved, err := ops.Get(ctx, "server-2")
	require.NoError(t, err)
	assert.Equal(t, "Embedded Server", retrieved.Name)
}

// TestBackendServerOps_Get tests retrieving a backend server
func TestBackendServerOps_Get(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// Create a server first
	description := "GitHub MCP server"
	server := &models.BackendServer{
		ID:          "github-server",
		Name:        "GitHub",
		Description: &description,
		Group:       "development",
	}

	err := ops.Create(ctx, server)
	require.NoError(t, err)

	// Test Get
	retrieved, err := ops.Get(ctx, "github-server")
	require.NoError(t, err)
	assert.Equal(t, "github-server", retrieved.ID)
	assert.Equal(t, "GitHub", retrieved.Name)
	assert.Equal(t, "development", retrieved.Group)
}

// TestBackendServerOps_Get_NotFound tests retrieving non-existent server
func TestBackendServerOps_Get_NotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// Try to get a non-existent server
	_, err := ops.Get(ctx, "non-existent")
	assert.Error(t, err)
	// Error message could be "server not found" or "collection not found" depending on state
	assert.True(t, err != nil, "Should return an error for non-existent server")
}

// TestBackendServerOps_Update tests updating a backend server
func TestBackendServerOps_Update(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// Create initial server
	description := "Original description"
	server := &models.BackendServer{
		ID:          "server-1",
		Name:        "Original Name",
		Description: &description,
		Group:       "default",
	}

	err := ops.Create(ctx, server)
	require.NoError(t, err)

	// Update the server
	updatedDescription := "Updated description"
	server.Name = "Updated Name"
	server.Description = &updatedDescription

	err = ops.Update(ctx, server)
	require.NoError(t, err)

	// Verify update
	retrieved, err := ops.Get(ctx, "server-1")
	require.NoError(t, err)
	assert.Equal(t, "Updated Name", retrieved.Name)
	assert.Equal(t, "Updated description", *retrieved.Description)
}

// TestBackendServerOps_Update_NonExistent tests updating non-existent server
func TestBackendServerOps_Update_NonExistent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// Try to update non-existent server (should create it)
	description := "New server"
	server := &models.BackendServer{
		ID:          "new-server",
		Name:        "New Server",
		Description: &description,
		Group:       "default",
	}

	err := ops.Update(ctx, server)
	require.NoError(t, err)

	// Verify server was created
	retrieved, err := ops.Get(ctx, "new-server")
	require.NoError(t, err)
	assert.Equal(t, "New Server", retrieved.Name)
}

// TestBackendServerOps_Delete tests deleting a backend server
func TestBackendServerOps_Delete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// Create a server
	description := "Server to delete"
	server := &models.BackendServer{
		ID:          "delete-me",
		Name:        "Delete Me",
		Description: &description,
		Group:       "default",
	}

	err := ops.Create(ctx, server)
	require.NoError(t, err)

	// Delete the server
	err = ops.Delete(ctx, "delete-me")
	require.NoError(t, err)

	// Verify deletion
	_, err = ops.Get(ctx, "delete-me")
	assert.Error(t, err, "Should not find deleted server")
}

// TestBackendServerOps_Delete_NonExistent tests deleting non-existent server
func TestBackendServerOps_Delete_NonExistent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// Try to delete a non-existent server - should not error
	err := ops.Delete(ctx, "non-existent")
	assert.NoError(t, err)
}

// TestBackendServerOps_List tests listing all servers
func TestBackendServerOps_List(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// Create multiple servers
	desc1 := "Server 1"
	server1 := &models.BackendServer{
		ID:          "server-1",
		Name:        "Server 1",
		Description: &desc1,
		Group:       "group-a",
	}

	desc2 := "Server 2"
	server2 := &models.BackendServer{
		ID:          "server-2",
		Name:        "Server 2",
		Description: &desc2,
		Group:       "group-b",
	}

	desc3 := "Server 3"
	server3 := &models.BackendServer{
		ID:          "server-3",
		Name:        "Server 3",
		Description: &desc3,
		Group:       "group-a",
	}

	err := ops.Create(ctx, server1)
	require.NoError(t, err)
	err = ops.Create(ctx, server2)
	require.NoError(t, err)
	err = ops.Create(ctx, server3)
	require.NoError(t, err)

	// List all servers
	servers, err := ops.List(ctx)
	require.NoError(t, err)
	assert.Len(t, servers, 3, "Should have 3 servers")

	// Verify server names
	serverNames := make(map[string]bool)
	for _, server := range servers {
		serverNames[server.Name] = true
	}
	assert.True(t, serverNames["Server 1"])
	assert.True(t, serverNames["Server 2"])
	assert.True(t, serverNames["Server 3"])
}

// TestBackendServerOps_List_Empty tests listing servers on empty database
func TestBackendServerOps_List_Empty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// List empty database
	servers, err := ops.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, servers, "Should return empty list for empty database")
}

// TestBackendServerOps_Search tests semantic search for servers
func TestBackendServerOps_Search(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// Create test servers
	desc1 := "GitHub integration server"
	server1 := &models.BackendServer{
		ID:          "github",
		Name:        "GitHub Server",
		Description: &desc1,
		Group:       "vcs",
	}

	desc2 := "Slack messaging server"
	server2 := &models.BackendServer{
		ID:          "slack",
		Name:        "Slack Server",
		Description: &desc2,
		Group:       "messaging",
	}

	err := ops.Create(ctx, server1)
	require.NoError(t, err)
	err = ops.Create(ctx, server2)
	require.NoError(t, err)

	// Search for servers
	results, err := ops.Search(ctx, "integration", 5)
	require.NoError(t, err)
	assert.NotEmpty(t, results, "Should find servers")
}

// TestBackendServerOps_Search_Empty tests search on empty database
func TestBackendServerOps_Search_Empty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db := createTestDB(t)
	defer func() { _ = db.Close() }()

	embeddingFunc := createTestEmbeddingFunc(t)
	ops := NewBackendServerOps(db, embeddingFunc)

	// Search empty database
	results, err := ops.Search(ctx, "anything", 5)
	require.NoError(t, err)
	assert.Empty(t, results, "Should return empty results for empty database")
}

// TestBackendServerOps_MetadataSerialization tests metadata serialization/deserialization
func TestBackendServerOps_MetadataSerialization(t *testing.T) {
	t.Parallel()

	description := "Test server"
	server := &models.BackendServer{
		ID:          "server-1",
		Name:        "Test Server",
		Description: &description,
		Group:       "default",
	}

	// Test serialization
	metadata, err := serializeServerMetadata(server)
	require.NoError(t, err)
	assert.Contains(t, metadata, "data")
	assert.Equal(t, "backend_server", metadata["type"])

	// Test deserialization
	deserializedServer, err := deserializeServerMetadata(metadata)
	require.NoError(t, err)
	assert.Equal(t, server.ID, deserializedServer.ID)
	assert.Equal(t, server.Name, deserializedServer.Name)
	assert.Equal(t, server.Group, deserializedServer.Group)
}

// TestBackendServerOps_MetadataDeserialization_MissingData tests error handling
func TestBackendServerOps_MetadataDeserialization_MissingData(t *testing.T) {
	t.Parallel()

	// Test with missing data field
	metadata := map[string]string{
		"type": "backend_server",
	}

	_, err := deserializeServerMetadata(metadata)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing data field")
}

// TestBackendServerOps_MetadataDeserialization_InvalidJSON tests invalid JSON handling
func TestBackendServerOps_MetadataDeserialization_InvalidJSON(t *testing.T) {
	t.Parallel()

	// Test with invalid JSON
	metadata := map[string]string{
		"data": "invalid json {",
		"type": "backend_server",
	}

	_, err := deserializeServerMetadata(metadata)
	assert.Error(t, err)
}
