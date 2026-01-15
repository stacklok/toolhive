package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/philippgille/chromem-go"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/optimizer/models"
)

// BackendToolOps provides operations for backend tools in chromem-go
type BackendToolOps struct {
	db            *DB
	embeddingFunc chromem.EmbeddingFunc
}

// NewBackendToolOps creates a new BackendToolOps instance
func NewBackendToolOps(db *DB, embeddingFunc chromem.EmbeddingFunc) *BackendToolOps {
	return &BackendToolOps{
		db:            db,
		embeddingFunc: embeddingFunc,
	}
}

// Create adds a new backend tool to the collection
func (ops *BackendToolOps) Create(ctx context.Context, tool *models.BackendTool, serverName string) error {
	collection, err := ops.db.GetOrCreateCollection(ctx, BackendToolCollection, ops.embeddingFunc)
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
	if ops.db.fts != nil {
		if err := ops.db.fts.UpsertToolMeta(ctx, tool, serverName); err != nil {
			// Log but don't fail - FTS5 is supplementary
			logger.Warnf("Failed to upsert tool to FTS5: %v", err)
		}
	}

	logger.Debugf("Created backend tool: %s (chromem-go + FTS5)", tool.ID)
	return nil
}

// Get retrieves a backend tool by ID
func (ops *BackendToolOps) Get(ctx context.Context, toolID string) (*models.BackendTool, error) {
	collection, err := ops.db.GetCollection(BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("backend tool collection not found: %w", err)
	}

	// Query by ID with exact match
	results, err := collection.Query(ctx, toolID, 1, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query tool: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("tool not found: %s", toolID)
	}

	// Deserialize from metadata
	tool, err := deserializeToolMetadata(results[0].Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize tool: %w", err)
	}

	return tool, nil
}

// Update updates an existing backend tool in chromem-go
// Note: This only updates chromem-go, not FTS5. Use Create to update both.
func (ops *BackendToolOps) Update(ctx context.Context, tool *models.BackendTool) error {
	collection, err := ops.db.GetOrCreateCollection(ctx, BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		return fmt.Errorf("failed to get backend tool collection: %w", err)
	}

	// Prepare content for embedding
	content := tool.ToolName
	if tool.Description != nil && *tool.Description != "" {
		content += ". " + *tool.Description
	}

	// Serialize metadata
	metadata, err := serializeToolMetadata(tool)
	if err != nil {
		return fmt.Errorf("failed to serialize tool metadata: %w", err)
	}

	// Delete existing document
	_ = collection.Delete(ctx, nil, nil, tool.ID) // Ignore error if doesn't exist

	// Create updated document
	doc := chromem.Document{
		ID:       tool.ID,
		Content:  content,
		Metadata: metadata,
	}

	if len(tool.ToolEmbedding) > 0 {
		doc.Embedding = tool.ToolEmbedding
	}

	err = collection.AddDocument(ctx, doc)
	if err != nil {
		return fmt.Errorf("failed to update tool document: %w", err)
	}

	logger.Debugf("Updated backend tool: %s", tool.ID)
	return nil
}

// Delete removes a backend tool
func (ops *BackendToolOps) Delete(ctx context.Context, toolID string) error {
	collection, err := ops.db.GetCollection(BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist, nothing to delete
		return nil
	}

	err = collection.Delete(ctx, nil, nil, toolID)
	if err != nil {
		return fmt.Errorf("failed to delete tool: %w", err)
	}

	logger.Debugf("Deleted backend tool: %s", toolID)
	return nil
}

// DeleteByServer removes all tools for a given server from both chromem-go and FTS5
func (ops *BackendToolOps) DeleteByServer(ctx context.Context, serverID string) error {
	collection, err := ops.db.GetCollection(BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist, nothing to delete in chromem-go
		logger.Debug("Backend tool collection not found, skipping chromem-go deletion")
	} else {
		// Query all tools for this server
		tools, err := ops.ListByServer(ctx, serverID)
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

// ListByServer returns all tools for a given server
func (ops *BackendToolOps) ListByServer(ctx context.Context, serverID string) ([]*models.BackendTool, error) {
	collection, err := ops.db.GetCollection(BackendToolCollection, ops.embeddingFunc)
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

// Search performs semantic search for backend tools
func (ops *BackendToolOps) Search(
	ctx context.Context,
	query string,
	limit int,
	serverID *string,
) ([]*models.BackendToolWithMetadata, error) {
	collection, err := ops.db.GetCollection(BackendToolCollection, ops.embeddingFunc)
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
