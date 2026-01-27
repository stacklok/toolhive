// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/models"
)

//go:embed schema_fts.sql
var schemaFTS string

// FTSConfig holds FTS5 database configuration
type FTSConfig struct {
	// DBPath is the path to the SQLite database file
	// If empty, uses ":memory:" for in-memory database
	DBPath string
}

// FTSDatabase handles FTS5 (BM25) search operations
type FTSDatabase struct {
	config *FTSConfig
	db     *sql.DB
	mu     sync.RWMutex
}

// NewFTSDatabase creates a new FTS5 database for BM25 search
func NewFTSDatabase(config *FTSConfig) (*FTSDatabase, error) {
	dbPath := config.DBPath
	if dbPath == "" {
		dbPath = ":memory:"
	}

	// Open with modernc.org/sqlite (pure Go)
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open FTS database: %w", err)
	}

	// Set pragmas for performance
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	}

	for _, pragma := range pragmas {
		if _, err := sqlDB.Exec(pragma); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("failed to set pragma: %w", err)
		}
	}

	ftsDB := &FTSDatabase{
		config: config,
		db:     sqlDB,
	}

	// Initialize schema
	if err := ftsDB.initializeSchema(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to initialize FTS schema: %w", err)
	}

	logger.Infof("FTS5 database initialized successfully at: %s", dbPath)
	return ftsDB, nil
}

// initializeSchema creates the FTS5 tables and triggers
//
// Note: We execute the schema directly rather than using a migration framework
// because the FTS database is ephemeral (destroyed on shutdown, recreated on startup).
// Migrations are only needed when you need to preserve data across schema changes.
func (fts *FTSDatabase) initializeSchema() error {
	fts.mu.Lock()
	defer fts.mu.Unlock()

	_, err := fts.db.Exec(schemaFTS)
	if err != nil {
		return fmt.Errorf("failed to execute schema: %w", err)
	}

	logger.Debug("FTS5 schema initialized")
	return nil
}

// UpsertServer inserts or updates a server in the FTS database
func (fts *FTSDatabase) UpsertServer(
	ctx context.Context,
	server *models.BackendServer,
) error {
	fts.mu.Lock()
	defer fts.mu.Unlock()

	query := `
		INSERT INTO backend_servers_fts (id, name, description, server_group, last_updated, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			server_group = excluded.server_group,
			last_updated = excluded.last_updated
	`

	_, err := fts.db.ExecContext(
		ctx,
		query,
		server.ID,
		server.Name,
		server.Description,
		server.Group,
		server.LastUpdated,
		server.CreatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to upsert server in FTS: %w", err)
	}

	logger.Debugf("Upserted server in FTS: %s", server.ID)
	return nil
}

// UpsertToolMeta inserts or updates a tool in the FTS database
func (fts *FTSDatabase) UpsertToolMeta(
	ctx context.Context,
	tool *models.BackendTool,
	_ string, // serverName - unused, keeping for interface compatibility
) error {
	fts.mu.Lock()
	defer fts.mu.Unlock()

	// Convert input schema to JSON string
	var schemaStr *string
	if len(tool.InputSchema) > 0 {
		str := string(tool.InputSchema)
		schemaStr = &str
	}

	query := `
		INSERT INTO backend_tools_fts (
			id, mcpserver_id, tool_name, tool_description, 
			input_schema, token_count, last_updated, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			mcpserver_id = excluded.mcpserver_id,
			tool_name = excluded.tool_name,
			tool_description = excluded.tool_description,
			input_schema = excluded.input_schema,
			token_count = excluded.token_count,
			last_updated = excluded.last_updated
	`

	_, err := fts.db.ExecContext(
		ctx,
		query,
		tool.ID,
		tool.MCPServerID,
		tool.ToolName,
		tool.Description,
		schemaStr,
		tool.TokenCount,
		tool.LastUpdated,
		tool.CreatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to upsert tool in FTS: %w", err)
	}

	logger.Debugf("Upserted tool in FTS: %s", tool.ToolName)
	return nil
}

// DeleteServer removes a server and its tools from FTS database
func (fts *FTSDatabase) DeleteServer(ctx context.Context, serverID string) error {
	fts.mu.Lock()
	defer fts.mu.Unlock()

	// Foreign key cascade will delete related tools
	_, err := fts.db.ExecContext(ctx, "DELETE FROM backend_servers_fts WHERE id = ?", serverID)
	if err != nil {
		return fmt.Errorf("failed to delete server from FTS: %w", err)
	}

	logger.Debugf("Deleted server from FTS: %s", serverID)
	return nil
}

// DeleteToolsByServer removes all tools for a server from FTS database
func (fts *FTSDatabase) DeleteToolsByServer(ctx context.Context, serverID string) error {
	fts.mu.Lock()
	defer fts.mu.Unlock()

	result, err := fts.db.ExecContext(ctx, "DELETE FROM backend_tools_fts WHERE mcpserver_id = ?", serverID)
	if err != nil {
		return fmt.Errorf("failed to delete tools from FTS: %w", err)
	}

	count, _ := result.RowsAffected()
	logger.Debugf("Deleted %d tools from FTS for server: %s", count, serverID)
	return nil
}

// DeleteTool removes a tool from FTS database
func (fts *FTSDatabase) DeleteTool(ctx context.Context, toolID string) error {
	fts.mu.Lock()
	defer fts.mu.Unlock()

	_, err := fts.db.ExecContext(ctx, "DELETE FROM backend_tools_fts WHERE id = ?", toolID)
	if err != nil {
		return fmt.Errorf("failed to delete tool from FTS: %w", err)
	}

	logger.Debugf("Deleted tool from FTS: %s", toolID)
	return nil
}

// SearchBM25 performs BM25 full-text search on tools
func (fts *FTSDatabase) SearchBM25(
	ctx context.Context,
	query string,
	limit int,
	serverID *string,
) ([]*models.BackendToolWithMetadata, error) {
	fts.mu.RLock()
	defer fts.mu.RUnlock()

	// Sanitize FTS5 query
	sanitizedQuery := sanitizeFTS5Query(query)
	if sanitizedQuery == "" {
		return []*models.BackendToolWithMetadata{}, nil
	}

	// Build query with optional server filter
	sqlQuery := `
		SELECT 
			t.id,
			t.mcpserver_id,
			t.tool_name,
			t.tool_description,
			t.input_schema,
			t.token_count,
			t.last_updated,
			t.created_at,
			fts.rank
		FROM backend_tool_fts_index fts
		JOIN backend_tools_fts t ON fts.tool_id = t.id
		WHERE backend_tool_fts_index MATCH ?
	`

	args := []interface{}{sanitizedQuery}

	if serverID != nil {
		sqlQuery += " AND t.mcpserver_id = ?"
		args = append(args, *serverID)
	}

	sqlQuery += " ORDER BY rank LIMIT ?"
	args = append(args, limit)

	rows, err := fts.db.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search tools: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []*models.BackendToolWithMetadata
	for rows.Next() {
		var tool models.BackendTool
		var schemaStr sql.NullString
		var rank float32

		err := rows.Scan(
			&tool.ID,
			&tool.MCPServerID,
			&tool.ToolName,
			&tool.Description,
			&schemaStr,
			&tool.TokenCount,
			&tool.LastUpdated,
			&tool.CreatedAt,
			&rank,
		)
		if err != nil {
			logger.Warnf("Failed to scan tool row: %v", err)
			continue
		}

		if schemaStr.Valid {
			tool.InputSchema = []byte(schemaStr.String)
		}

		// Convert BM25 rank to similarity score (higher is better)
		// FTS5 rank is negative, so we negate and normalize
		similarity := float32(1.0 / (1.0 - float64(rank)))

		results = append(results, &models.BackendToolWithMetadata{
			BackendTool: tool,
			Similarity:  similarity,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tool rows: %w", err)
	}

	logger.Debugf("BM25 search found %d tools for query: %s", len(results), query)
	return results, nil
}

// GetTotalToolTokens returns the sum of token_count across all tools
func (fts *FTSDatabase) GetTotalToolTokens(ctx context.Context) (int, error) {
	fts.mu.RLock()
	defer fts.mu.RUnlock()

	var totalTokens int
	query := "SELECT COALESCE(SUM(token_count), 0) FROM backend_tools_fts"

	err := fts.db.QueryRowContext(ctx, query).Scan(&totalTokens)
	if err != nil {
		return 0, fmt.Errorf("failed to get total tool tokens: %w", err)
	}

	return totalTokens, nil
}

// Close closes the FTS database connection
func (fts *FTSDatabase) Close() error {
	return fts.db.Close()
}

// sanitizeFTS5Query escapes special characters in FTS5 queries
// FTS5 uses: " * ( ) AND OR NOT
func sanitizeFTS5Query(query string) string {
	// Remove or escape special FTS5 characters
	replacer := strings.NewReplacer(
		`"`, `""`, // Escape quotes
		`*`, ` `, // Remove wildcards
		`(`, ` `, // Remove parentheses
		`)`, ` `,
	)

	sanitized := replacer.Replace(query)

	// Remove multiple spaces
	sanitized = strings.Join(strings.Fields(sanitized), " ")

	return strings.TrimSpace(sanitized)
}
