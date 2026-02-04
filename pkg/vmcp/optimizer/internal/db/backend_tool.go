// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

// backendToolOps provides operations for backend tools in chromem-go
// This is a private implementation detail. Use the Database interface instead.
type backendToolOps struct {
	db            *chromemDB
	embeddingFunc chromem.EmbeddingFunc
}

// newBackendToolOps creates a new backendToolOps instance
func newBackendToolOps(db *chromemDB, embeddingFunc chromem.EmbeddingFunc) *backendToolOps {
	return &backendToolOps{
		db:            db,
		embeddingFunc: embeddingFunc,
	}
}

// create adds a new backend tool to the collection
func (ops *backendToolOps) create(ctx context.Context, tool *models.BackendTool, serverName string) error {
	collection, err := ops.db.getOrCreateCollection(ctx, BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		return fmt.Errorf("failed to get backend tool collection: %w", err)
	}

	// Prepare content for embedding (name + description + input schema summary)
	content := tool.ToolName
	if tool.Description != nil && *tool.Description != "" {
		content += ". " + *tool.Description
	}

	// Serialize metadata
	metadata, err := serializeToolMetadata(tool)
	if err != nil {
		return fmt.Errorf("failed to serialize tool metadata: %w", err)
	}

	// Create document
	doc := chromem.Document{
		ID:       tool.ID,
		Content:  content,
		Metadata: metadata,
	}

	// If embedding is provided, use it
	if len(tool.ToolEmbedding) > 0 {
		doc.Embedding = tool.ToolEmbedding
	}

	// Add document to chromem-go collection
	err = collection.AddDocument(ctx, doc)
	if err != nil {
		return fmt.Errorf("failed to add tool document to chromem-go: %w", err)
	}

	// Also add to FTS5 database if available (for BM25 search)
	// Use background context to avoid cancellation issues - FTS5 is supplementary
	if ops.db.fts != nil {
		// Use background context with timeout for FTS operations
		// This ensures FTS operations complete even if the original context is canceled
		ftsCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := ops.db.fts.UpsertToolMeta(ftsCtx, tool, serverName); err != nil {
			// Log but don't fail - FTS5 is supplementary
			logger.Warnf("Failed to upsert tool to FTS5: %v", err)
		}
	}

	logger.Debugf("Created backend tool: %s (chromem-go + FTS5)", tool.ID)
	return nil
}

// deleteByServer removes all tools for a given server from both chromem-go and FTS5
func (ops *backendToolOps) deleteByServer(ctx context.Context, serverID string) error {
	collection, err := ops.db.getCollection(BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist, nothing to delete in chromem-go
		logger.Debug("Backend tool collection not found, skipping chromem-go deletion")
	} else {
		// Query all tools for this server
		tools, err := ops.listByServer(ctx, serverID)
		if err != nil {
			return fmt.Errorf("failed to list tools for server: %w", err)
		}

		// Delete each tool from chromem-go
		for _, tool := range tools {
			if err := collection.Delete(ctx, nil, nil, tool.ID); err != nil {
				logger.Warnf("Failed to delete tool %s from chromem-go: %v", tool.ID, err)
			}
		}

		logger.Debugf("Deleted %d tools from chromem-go for server: %s", len(tools), serverID)
	}

	// Also delete from FTS5 database if available
	if ops.db.fts != nil {
		if err := ops.db.fts.DeleteToolsByServer(ctx, serverID); err != nil {
			logger.Warnf("Failed to delete tools from FTS5 for server %s: %v", serverID, err)
		} else {
			logger.Debugf("Deleted tools from FTS5 for server: %s", serverID)
		}
	}

	return nil
}

// listByServer returns all tools for a given server
func (ops *backendToolOps) listByServer(ctx context.Context, serverID string) ([]*models.BackendTool, error) {
	collection, err := ops.db.getCollection(BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist yet, return empty list
		return []*models.BackendTool{}, nil
	}

	// Get count to determine nResults
	count := collection.Count()
	if count == 0 {
		return []*models.BackendTool{}, nil
	}

	// Query with a generic term and metadata filter
	// Using "tool" as a generic query that should match all tools
	results, err := collection.Query(ctx, "tool", count, map[string]string{"server_id": serverID}, nil)
	if err != nil {
		// If no tools match, return empty list
		return []*models.BackendTool{}, nil
	}

	tools := make([]*models.BackendTool, 0, len(results))
	for _, result := range results {
		tool, err := deserializeToolMetadata(result.Metadata)
		if err != nil {
			logger.Warnf("Failed to deserialize tool: %v", err)
			continue
		}
		tools = append(tools, tool)
	}

	return tools, nil
}

// search performs semantic search for backend tools
// This is used internally by searchHybrid.
func (ops *backendToolOps) search(
	ctx context.Context,
	query string,
	limit int,
	serverID *string,
) ([]*models.BackendToolWithMetadata, error) {
	collection, err := ops.db.getCollection(BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		return []*models.BackendToolWithMetadata{}, nil
	}

	// Get collection count and adjust limit if necessary
	count := collection.Count()
	if count == 0 {
		return []*models.BackendToolWithMetadata{}, nil
	}
	if limit > count {
		limit = count
	}

	// Build metadata filter if server ID is provided
	var metadataFilter map[string]string
	if serverID != nil {
		metadataFilter = map[string]string{"server_id": *serverID}
	}

	results, err := collection.Query(ctx, query, limit, metadataFilter, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to search tools: %w", err)
	}

	tools := make([]*models.BackendToolWithMetadata, 0, len(results))
	for _, result := range results {
		tool, err := deserializeToolMetadata(result.Metadata)
		if err != nil {
			logger.Warnf("Failed to deserialize tool: %v", err)
			continue
		}

		// Add similarity score
		toolWithMeta := &models.BackendToolWithMetadata{
			BackendTool: *tool,
			Similarity:  result.Similarity,
		}
		tools = append(tools, toolWithMeta)
	}

	return tools, nil
}

// Helper functions for metadata serialization

func serializeToolMetadata(tool *models.BackendTool) (map[string]string, error) {
	data, err := json.Marshal(tool)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"data":      string(data),
		"type":      "backend_tool",
		"server_id": tool.MCPServerID,
	}, nil
}

func deserializeToolMetadata(metadata map[string]string) (*models.BackendTool, error) {
	data, ok := metadata["data"]
	if !ok {
		return nil, fmt.Errorf("missing data field in metadata")
	}

	var tool models.BackendTool
	if err := json.Unmarshal([]byte(data), &tool); err != nil {
		return nil, err
	}

	return &tool, nil
}
