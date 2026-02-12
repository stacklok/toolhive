// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package sqlitestore implements a SQLite-based ToolStore for search over
// MCP tool metadata. It currently uses FTS5 for full-text search and may
// be extended with embedding-based semantic search in the future.
package sqlitestore

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/server"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

//go:embed schema.sql
var schemaSQL string

// sqliteToolStore implements a tool store using SQLite with FTS5 for full-text search.
// It satisfies the optimizer.ToolStore interface.
type sqliteToolStore struct {
	db *sql.DB
}

// NewSQLiteToolStore creates a new sqliteToolStore backed by a shared in-memory
// SQLite database. All callers of this constructor share the same database,
// which is the intended production behavior (one shared store per server).
func NewSQLiteToolStore() (sqliteToolStore, error) {
	return newSQLiteToolStore("file:memdb?mode=memory&cache=shared")
}

// newSQLiteToolStore creates a tool store backed by a database described
// in the connectionString. It is useful for tests, where we want multiple
// isolated (non-shared) databases.
func newSQLiteToolStore(connectionString string) (sqliteToolStore, error) {
	db, err := sql.Open("sqlite", connectionString)
	if err != nil {
		return sqliteToolStore{}, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Execute schema
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return sqliteToolStore{}, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return sqliteToolStore{db: db}, nil
}

// UpsertTools adds or updates tools in the store.
func (s sqliteToolStore) UpsertTools(ctx context.Context, tools []server.ServerTool) (retErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO llm_capabilities (name, description) VALUES (?, ?)")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for _, tool := range tools {
		if _, err := stmt.ExecContext(ctx, tool.Tool.Name, tool.Tool.Description); err != nil {
			return fmt.Errorf("failed to upsert tool %s: %w", tool.Tool.Name, err)
		}
	}

	return tx.Commit()
}

// Search finds tools matching the query string using FTS5 full-text search.
// The allowedTools parameter limits results to only tools with names in the given set.
// If allowedTools is empty, no results are returned (empty = no access).
// Returns matches ranked by relevance.
func (s sqliteToolStore) Search(ctx context.Context, query string, allowedTools []string) ([]types.ToolMatch, error) {
	if len(allowedTools) == 0 {
		return nil, nil
	}

	ftsExpr := sanitizeFTS5Query(query)
	if ftsExpr == "" {
		return nil, nil
	}

	return s.searchFTS5(ctx, ftsExpr, allowedTools)
}

// Close releases the underlying database connection.
func (s sqliteToolStore) Close() error {
	return s.db.Close()
}

// searchFTS5 performs a full-text search using FTS5 MATCH with BM25 ranking.
// It uses json_each() to pass the allowed tool names as a single JSON array
// parameter, avoiding manual placeholder construction.
//
// The ftsExpr is produced by sanitizeFTS5Query and is always passed as a
// parameterized ? value, never interpolated into SQL.
func (s sqliteToolStore) searchFTS5(
	ctx context.Context, ftsExpr string, allowedTools []string,
) ([]types.ToolMatch, error) {
	allowedJSON, err := json.Marshal(allowedTools)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal allowed tools: %w", err)
	}

	queryStr := `SELECT t.name, t.description, rank
		FROM llm_capabilities_fts fts
		JOIN llm_capabilities t ON t.rowid = fts.rowid
		WHERE llm_capabilities_fts MATCH ?
		  AND t.name IN (SELECT value FROM json_each(?))
		ORDER BY rank`

	rows, err := s.db.QueryContext(ctx, queryStr, ftsExpr, string(allowedJSON))
	if err != nil {
		return nil, fmt.Errorf("FTS5 query failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var matches []types.ToolMatch
	for rows.Next() {
		var name, description string
		var rank float64
		if err := rows.Scan(&name, &description, &rank); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		matches = append(matches, types.ToolMatch{
			Name:        name,
			Description: description,
			Score:       normalizeBM25(rank),
		})
	}

	return matches, rows.Err()
}

// problematicWords contains words that FTS5 interprets as operators or that
// are too common in tool metadata to be useful search terms. This set aligns
// with Python mcp_optimizer's DEFAULT_FTS_PROBLEMATIC_WORDS.
var problematicWords = map[string]struct{}{
	"name": {}, "description": {}, "schema": {}, "input": {},
	"output": {}, "type": {}, "properties": {}, "required": {},
	"title": {}, "id": {}, "tool": {}, "server": {},
	"meta": {}, "data": {}, "content": {}, "text": {},
	"value": {}, "field": {}, "column": {}, "table": {},
	"index": {}, "key": {}, "primary": {},
}

// sanitizeFTS5Query prepares a user query string for use with FTS5 MATCH.
//
// The returned string is designed to be passed as a single ? parameter to
// QueryContext. It cannot cause SQL injection because it is always bound via ?.
//
// FTS5 MATCH requires a single string operand containing the full query
// expression (e.g., "read" OR "write"). Individual terms cannot be separate
// ? SQL parameters because the OR/AND operators are part of the FTS5 query
// language, not SQL.
// See: https://sqlite.org/fts5.html#full_text_query_syntax
//
// Safety:
//   - SQL injection is prevented because the expression is always bound via ?.
//   - FTS5 operator injection is prevented by double-quoting each term and
//     escaping embedded double-quotes (standard FTS5 escaping).
func sanitizeFTS5Query(query string) string {
	words := strings.Fields(strings.TrimSpace(query))
	if len(words) == 0 {
		return ""
	}

	hasProblematic := false
	for _, word := range words {
		if _, ok := problematicWords[strings.ToLower(word)]; ok {
			hasProblematic = true
			break
		}
	}

	// Single word or any problematic word present: use phrase search
	if len(words) == 1 || hasProblematic {
		escaped := strings.ReplaceAll(strings.Join(words, " "), `"`, `""`)
		return `"` + escaped + `"`
	}

	// Multi-word with no problematic words: join with OR
	quoted := make([]string, len(words))
	for i, word := range words {
		escaped := strings.ReplaceAll(word, `"`, `""`)
		quoted[i] = `"` + escaped + `"`
	}
	return strings.Join(quoted, " OR ")
}

// normalizeBM25 converts an FTS5 bm25() rank to a 0-1 score.
// FTS5 bm25() returns negative values where more negative = better match.
func normalizeBM25(rank float64) float64 {
	return 1.0 / (1.0 - rank)
}
