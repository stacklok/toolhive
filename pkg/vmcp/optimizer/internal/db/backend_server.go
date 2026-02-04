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

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/models"
)

// backendServerOps provides operations for backend servers in chromem-go
// This is a private implementation detail. Use the Database interface instead.
type backendServerOps struct {
	db            *chromemDB
	embeddingFunc chromem.EmbeddingFunc
}

// newBackendServerOps creates a new backendServerOps instance
func newBackendServerOps(db *chromemDB, embeddingFunc chromem.EmbeddingFunc) *backendServerOps {
	return &backendServerOps{
		db:            db,
		embeddingFunc: embeddingFunc,
	}
}

// create adds a new backend server to the collection
func (ops *backendServerOps) create(ctx context.Context, server *models.BackendServer) error {
	collection, err := ops.db.getOrCreateCollection(ctx, BackendServerCollection, ops.embeddingFunc)
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
	if ftsDB := ops.db.getFTSDB(); ftsDB != nil {
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

// update updates an existing backend server (creates if not exists)
func (ops *backendServerOps) update(ctx context.Context, server *models.BackendServer) error {
	// chromem-go doesn't have an update operation, so we delete and re-create
	err := ops.delete(ctx, server.ID)
	if err != nil {
		// If server doesn't exist, that's fine
		logger.Debugf("Server %s not found for update, will create new", server.ID)
	}

	return ops.create(ctx, server)
}

// delete removes a backend server
func (ops *backendServerOps) delete(ctx context.Context, serverID string) error {
	collection, err := ops.db.getCollection(BackendServerCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist, nothing to delete
		return nil
	}

	err = collection.Delete(ctx, nil, nil, serverID)
	if err != nil {
		return fmt.Errorf("failed to delete server from chromem-go: %w", err)
	}

	// Also delete from FTS5 database if available
	if ftsDB := ops.db.getFTSDB(); ftsDB != nil {
		if err := ftsDB.DeleteServer(ctx, serverID); err != nil {
			// Log but don't fail
			logger.Warnf("Failed to delete server from FTS5: %v", err)
		}
	}

	logger.Debugf("Deleted backend server: %s (chromem-go + FTS5)", serverID)
	return nil
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
