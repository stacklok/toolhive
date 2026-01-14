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
func (ops *BackendToolOps) Create(ctx context.Context, tool *models.BackendTool) error {
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

	// Add document to collection
	err = collection.AddDocument(ctx, doc)
	if err != nil {
		return fmt.Errorf("failed to add tool document: %w", err)
	}

	logger.Debugf("Created backend tool: %s", tool.ID)
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

// Update updates an existing backend tool
func (ops *BackendToolOps) Update(ctx context.Context, tool *models.BackendTool) error {
	// chromem-go doesn't have an update operation, so we delete and re-create
	err := ops.Delete(ctx, tool.ID)
	if err != nil {
		// If tool doesn't exist, that's fine
		logger.Debugf("Tool %s not found for update, will create new", tool.ID)
	}

	return ops.Create(ctx, tool)
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

// DeleteByServer removes all tools for a given server
func (ops *BackendToolOps) DeleteByServer(ctx context.Context, serverID string) error {
	collection, err := ops.db.GetCollection(BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist, nothing to delete
		return nil
	}

	// Query all tools for this server
	tools, err := ops.ListByServer(ctx, serverID)
	if err != nil {
		return fmt.Errorf("failed to list tools for server: %w", err)
	}

	// Delete each tool
	for _, tool := range tools {
		if err := collection.Delete(ctx, nil, nil, tool.ID); err != nil {
			logger.Warnf("Failed to delete tool %s: %v", tool.ID, err)
		}
	}

	logger.Debugf("Deleted %d tools for server: %s", len(tools), serverID)
	return nil
}

// ListByServer returns all tools for a given server
func (ops *BackendToolOps) ListByServer(ctx context.Context, serverID string) ([]*models.BackendTool, error) {
	collection, err := ops.db.GetCollection(BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		// Collection doesn't exist yet, return empty list
		return []*models.BackendTool{}, nil
	}

	// Query with server_id metadata filter
	results, err := collection.Query(ctx, "", 10000, map[string]string{"server_id": serverID}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list tools for server: %w", err)
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
func (ops *BackendToolOps) Search(ctx context.Context, query string, limit int, serverID *string) ([]*models.BackendToolWithMetadata, error) {
	collection, err := ops.db.GetCollection(BackendToolCollection, ops.embeddingFunc)
	if err != nil {
		return []*models.BackendToolWithMetadata{}, nil
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
