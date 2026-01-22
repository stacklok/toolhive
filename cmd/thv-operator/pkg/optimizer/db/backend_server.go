// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package db provides chromem-go based database operations for the optimizer.
package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/philippgille/chromem-go"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/optimizer/models"
	"github.com/stacklok/toolhive/pkg/logger"
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

	// Add document to chromem-go collection
	err = collection.AddDocument(ctx, doc)
	if err != nil {
		return fmt.Errorf("failed to add server document to chromem-go: %w", err)
	}

	// Also add to FTS5 database if available (for keyword filtering)
	// Use background context to avoid cancellation issues - FTS5 is supplementary
	if ftsDB := ops.db.GetFTSDB(); ftsDB != nil {
		// Use background context with timeout for FTS operations
		// This ensures FTS operations complete even if the original context is canceled
		ftsCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ftsDB.UpsertServer(ftsCtx, server); err != nil {
			// Log but don't fail - FTS5 is supplementary
			logger.Warnf("Failed to upsert server to FTS5: %v", err)
		}
	}

	logger.Debugf("Created backend server: %s (chromem-go + FTS5)", server.ID)
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
		return fmt.Errorf("failed to delete server from chromem-go: %w", err)
	}

	// Also delete from FTS5 database if available
	if ftsDB := ops.db.GetFTSDB(); ftsDB != nil {
		if err := ftsDB.DeleteServer(ctx, serverID); err != nil {
			// Log but don't fail
			logger.Warnf("Failed to delete server from FTS5: %v", err)
		}
	}

	logger.Debugf("Deleted backend server: %s (chromem-go + FTS5)", serverID)
	return nil
}

// List returns all backend servers
func (ops *BackendServerOps) List(ctx context.Context) ([]*models.BackendServer, error) {
	collection, err := ops.db.GetCollection(BackendServerCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist yet, return empty list
		return []*models.BackendServer{}, nil
	}

	// Get count to determine nResults
	count := collection.Count()
	if count == 0 {
		return []*models.BackendServer{}, nil
	}

	// Query with a generic term to get all servers
	// Using "server" as a generic query that should match all servers
	results, err := collection.Query(ctx, "server", count, nil, nil)
	if err != nil {
		return []*models.BackendServer{}, nil
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

	// Get collection count and adjust limit if necessary
	count := collection.Count()
	if count == 0 {
		return []*models.BackendServer{}, nil
	}
	if limit > count {
		limit = count
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
