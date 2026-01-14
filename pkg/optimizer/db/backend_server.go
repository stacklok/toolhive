package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/philippgille/chromem-go"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/optimizer/models"
)

// BackendServerOps provides operations for backend servers in chromem-go
type BackendServerOps struct {
	db            *DB
	embeddingFunc chromem.EmbeddingFunc
}

// NewBackendServerOps creates a new BackendServerOps instance
func NewBackendServerOps(db *DB, embeddingFunc chromem.EmbeddingFunc) *BackendServerOps {
	return &BackendServerOps{
		db:            db,
		embeddingFunc: embeddingFunc,
	}
}

// Create adds a new backend server to the collection
func (ops *BackendServerOps) Create(ctx context.Context, server *models.BackendServer) error {
	collection, err := ops.db.GetOrCreateCollection(ctx, BackendServerCollection, ops.embeddingFunc)
	if err != nil {
		return fmt.Errorf("failed to get backend server collection: %w", err)
	}

	// Prepare content for embedding (name + description)
	content := server.Name
	if server.Description != nil && *server.Description != "" {
		content += ". " + *server.Description
	}

	// Serialize metadata
	metadata, err := serializeServerMetadata(server)
	if err != nil {
		return fmt.Errorf("failed to serialize server metadata: %w", err)
	}

	// Create document
	doc := chromem.Document{
		ID:       server.ID,
		Content:  content,
		Metadata: metadata,
	}

	// If embedding is provided, use it
	if len(server.ServerEmbedding) > 0 {
		doc.Embedding = server.ServerEmbedding
	}

	// Add document to collection
	err = collection.AddDocument(ctx, doc)
	if err != nil {
		return fmt.Errorf("failed to add server document: %w", err)
	}

	logger.Debugf("Created backend server: %s", server.ID)
	return nil
}

// Get retrieves a backend server by ID
func (ops *BackendServerOps) Get(ctx context.Context, serverID string) (*models.BackendServer, error) {
	collection, err := ops.db.GetCollection(BackendServerCollection, ops.embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("backend server collection not found: %w", err)
	}

	// Query by ID with exact match
	results, err := collection.Query(ctx, serverID, 1, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query server: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("server not found: %s", serverID)
	}

	// Deserialize from metadata
	server, err := deserializeServerMetadata(results[0].Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize server: %w", err)
	}

	return server, nil
}

// Update updates an existing backend server
func (ops *BackendServerOps) Update(ctx context.Context, server *models.BackendServer) error {
	// chromem-go doesn't have an update operation, so we delete and re-create
	err := ops.Delete(ctx, server.ID)
	if err != nil {
		// If server doesn't exist, that's fine
		logger.Debugf("Server %s not found for update, will create new", server.ID)
	}

	return ops.Create(ctx, server)
}

// Delete removes a backend server
func (ops *BackendServerOps) Delete(ctx context.Context, serverID string) error {
	collection, err := ops.db.GetCollection(BackendServerCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist, nothing to delete
		return nil
	}

	err = collection.Delete(ctx, nil, nil, serverID)
	if err != nil {
		return fmt.Errorf("failed to delete server: %w", err)
	}

	logger.Debugf("Deleted backend server: %s", serverID)
	return nil
}

// List returns all backend servers
func (ops *BackendServerOps) List(ctx context.Context) ([]*models.BackendServer, error) {
	collection, err := ops.db.GetCollection(BackendServerCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist yet, return empty list
		return []*models.BackendServer{}, nil
	}

	// chromem-go doesn't have a "list all" method, so we query with a broad search
	// Using empty query to get all documents
	results, err := collection.Query(ctx, "", 10000, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}

	servers := make([]*models.BackendServer, 0, len(results))
	for _, result := range results {
		server, err := deserializeServerMetadata(result.Metadata)
		if err != nil {
			logger.Warnf("Failed to deserialize server: %v", err)
			continue
		}
		servers = append(servers, server)
	}

	return servers, nil
}

// Search performs semantic search for backend servers
func (ops *BackendServerOps) Search(ctx context.Context, query string, limit int) ([]*models.BackendServer, error) {
	collection, err := ops.db.GetCollection(BackendServerCollection, ops.embeddingFunc)
	if err != nil {
		return []*models.BackendServer{}, nil
	}

	results, err := collection.Query(ctx, query, limit, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to search servers: %w", err)
	}

	servers := make([]*models.BackendServer, 0, len(results))
	for _, result := range results {
		server, err := deserializeServerMetadata(result.Metadata)
		if err != nil {
			logger.Warnf("Failed to deserialize server: %v", err)
			continue
		}
		servers = append(servers, server)
	}

	return servers, nil
}

// Helper functions for metadata serialization

func serializeServerMetadata(server *models.BackendServer) (map[string]string, error) {
	data, err := json.Marshal(server)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"data": string(data),
		"type": "backend_server",
	}, nil
}

func deserializeServerMetadata(metadata map[string]string) (*models.BackendServer, error) {
	data, ok := metadata["data"]
	if !ok {
		return nil, fmt.Errorf("missing data field in metadata")
	}

	var server models.BackendServer
	if err := json.Unmarshal([]byte(data), &server); err != nil {
		return nil, err
	}

	return &server, nil
}
