package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/stacklok/toolhive/pkg/optimizer/models"
)

// BackendToolOps provides database operations for backend tools
type BackendToolOps struct {
	db *DB
}

// NewBackendToolOps creates a new BackendToolOps instance
func NewBackendToolOps(db *DB) *BackendToolOps {
	return &BackendToolOps{db: db}
}

// Create creates a new backend tool
func (ops *BackendToolOps) Create(ctx context.Context, tool *models.BackendTool) error {
	// Generate ID if not provided
	if tool.ID == "" {
		tool.ID = uuid.New().String()
	}

	// Set timestamps
	now := time.Now()
	tool.CreatedAt = now
	tool.LastUpdated = now

	// Convert tool details to JSON
	detailsJSON, err := models.ToolDetailsToJSON(tool.Details)
	if err != nil {
		return fmt.Errorf("failed to marshal tool details: %w", err)
	}

	// Start transaction
	tx, err := ops.db.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Insert into tools_backend table
	query := `
		INSERT INTO tools_backend (
			id, mcpserver_id, details, details_embedding, token_count,
			last_updated, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	_, err = tx.ExecContext(ctx, query,
		tool.ID,
		tool.MCPServerID,
		detailsJSON,
		embeddingToBytes(tool.DetailsEmbedding),
		tool.TokenCount,
		tool.LastUpdated,
		tool.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to insert backend tool: %w", err)
	}

	// Insert embedding into vector table if present
	if len(tool.DetailsEmbedding) > 0 {
		vecQuery := `INSERT INTO backend_tool_vectors (tool_id, embedding) VALUES (?, ?)`
		_, err = tx.ExecContext(ctx, vecQuery, tool.ID, embeddingToBytes(tool.DetailsEmbedding))
		if err != nil {
			return fmt.Errorf("failed to insert tool embedding: %w", err)
		}
	}

	// Insert into FTS table for text search
	ftsQuery := `
		INSERT INTO backend_tool_fts (tool_id, mcp_server_name, tool_name, tool_description)
		VALUES (?, ?, ?, ?)
	`
	// Get server name from server table
	var serverName string
	err = tx.QueryRowContext(ctx, "SELECT name FROM mcpservers_backend WHERE id = ?", tool.MCPServerID).Scan(&serverName)
	if err != nil {
		return fmt.Errorf("failed to get server name: %w", err)
	}

	description := tool.Details.Description

	_, err = tx.ExecContext(ctx, ftsQuery, tool.ID, serverName, tool.Details.Name, description)
	if err != nil {
		return fmt.Errorf("failed to insert into FTS table: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetByServerID retrieves all tools for a server
func (ops *BackendToolOps) GetByServerID(ctx context.Context, serverID string) ([]*models.BackendTool, error) {
	query := `
		SELECT id, mcpserver_id, details, details_embedding, token_count,
		       last_updated, created_at
		FROM tools_backend
		WHERE mcpserver_id = ?
		ORDER BY id
	`

	rows, err := ops.db.QueryContext(ctx, query, serverID)
	if err != nil {
		return nil, fmt.Errorf("failed to query backend tools: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tools []*models.BackendTool
	for rows.Next() {
		var tool models.BackendTool
		var detailsJSON string
		var embeddingBytes []byte

		err := rows.Scan(
			&tool.ID,
			&tool.MCPServerID,
			&detailsJSON,
			&embeddingBytes,
			&tool.TokenCount,
			&tool.LastUpdated,
			&tool.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan backend tool: %w", err)
		}

		// Parse tool details from JSON
		details, err := models.ToolDetailsFromJSON(detailsJSON)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal tool details: %w", err)
		}
		tool.Details = *details

		// Convert embedding bytes to float32 slice
		if len(embeddingBytes) > 0 {
			tool.DetailsEmbedding = bytesToEmbedding(embeddingBytes)
		}

		tools = append(tools, &tool)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating backend tools: %w", err)
	}

	return tools, nil
}

// DeleteByServerID deletes all tools for a server
func (ops *BackendToolOps) DeleteByServerID(ctx context.Context, serverID string) (int64, error) {
	tx, err := ops.db.BeginTx(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Get tool IDs before deletion for cleanup
	rows, err := tx.QueryContext(ctx, "SELECT id FROM tools_backend WHERE mcpserver_id = ?", serverID)
	if err != nil {
		return 0, fmt.Errorf("failed to query tool IDs: %w", err)
	}

	var toolIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("failed to scan tool ID: %w", err)
		}
		toolIDs = append(toolIDs, id)
	}
	_ = rows.Close()

	// Delete from vector and FTS tables
	// Note: vec0 virtual tables use tool_id as primary key
	for _, id := range toolIDs {
		// Delete from vec0 table using primary key
		// Ignore errors as vec0 may not support all DELETE operations
		_, _ = tx.ExecContext(ctx, "DELETE FROM backend_tool_vectors WHERE tool_id = ?", id)
		_, err = tx.ExecContext(ctx, "DELETE FROM backend_tool_fts WHERE tool_id = ?", id)
		if err != nil {
			return 0, fmt.Errorf("failed to delete from FTS table: %w", err)
		}
	}

	// Delete from main table
	result, err := tx.ExecContext(ctx, "DELETE FROM tools_backend WHERE mcpserver_id = ?", serverID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete backend tools: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to get rows affected: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return rowsAffected, nil
}

// SearchByEmbedding searches for tools by embedding similarity
func (ops *BackendToolOps) SearchByEmbedding(
	ctx context.Context,
	embedding []float32,
	limit int,
) ([]*models.BackendToolWithMetadata, error) {
	query := `
		SELECT 
			t.id, t.mcpserver_id, t.details, t.details_embedding, t.token_count,
			t.last_updated, t.created_at,
			s.name as server_name, s.description as server_description,
			v.distance
		FROM tools_backend t
		JOIN mcpservers_backend s ON t.mcpserver_id = s.id
		JOIN (
			SELECT tool_id, distance
			FROM backend_tool_vectors
			WHERE embedding MATCH ?
			ORDER BY distance
			LIMIT ?
		) v ON t.id = v.tool_id
		ORDER BY v.distance
	`

	rows, err := ops.db.QueryContext(ctx, query, embeddingToBytes(embedding), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search backend tools: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []*models.BackendToolWithMetadata
	for rows.Next() {
		var tool models.BackendTool
		var detailsJSON string
		var embeddingBytes []byte
		var serverName string
		var serverDescription sql.NullString
		var distance float64

		err := rows.Scan(
			&tool.ID,
			&tool.MCPServerID,
			&detailsJSON,
			&embeddingBytes,
			&tool.TokenCount,
			&tool.LastUpdated,
			&tool.CreatedAt,
			&serverName,
			&serverDescription,
			&distance,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan search result: %w", err)
		}

		// Parse tool details from JSON
		details, err := models.ToolDetailsFromJSON(detailsJSON)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal tool details: %w", err)
		}
		tool.Details = *details

		// Convert embedding bytes to float32 slice
		if len(embeddingBytes) > 0 {
			tool.DetailsEmbedding = bytesToEmbedding(embeddingBytes)
		}

		result := &models.BackendToolWithMetadata{
			ServerName: serverName,
			Distance:   distance,
			Tool:       tool,
		}

		if serverDescription.Valid {
			result.ServerDescription = &serverDescription.String
		}

		results = append(results, result)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating search results: %w", err)
	}

	return results, nil
}
