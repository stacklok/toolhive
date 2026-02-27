// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package toolstore implements a SQLite-based ToolStore for search over
// MCP tool metadata. It uses FTS5 for full-text search and optional
// embedding-based semantic search for hybrid retrieval.
package toolstore

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/server"
	"golang.org/x/sync/errgroup"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/similarity"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

// Default values for configurable search parameters.
const (
	// DefaultMaxToolsToReturn is the maximum number of results returned to the caller.
	DefaultMaxToolsToReturn = 8

	// DefaultHybridSemanticToolsRatio controls the proportion of semantic vs FTS5
	// results in hybrid mode: 0 = all FTS5, 1 = all semantic.
	DefaultHybridSemanticToolsRatio = 0.5

	// DefaultSemanticDistanceThreshold is the maximum cosine distance for semantic search results.
	// Results with distance > threshold are filtered out in searchSemantic only.
	// Cosine distance: 0 = identical, 2 = opposite.
	DefaultSemanticDistanceThreshold = 1.0
)

//go:embed schema.sql
var schemaSQL string

// sqliteToolStore implements a tool store using SQLite with FTS5 for full-text search
// and optional vector embedding-based semantic search.
// It satisfies the types.ToolStore interface.
type sqliteToolStore struct {
	db                        *sql.DB
	embeddingClient           types.EmbeddingClient // nil = FTS5-only
	maxToolsToReturn          int
	hybridSemanticRatio       float64
	semanticDistanceThreshold float64
}

// NewSQLiteToolStore creates a new ToolStore backed by a shared in-memory
// SQLite database. All callers of this constructor share the same database,
// which is the intended production behavior (one shared store per server).
// If embeddingClient is non-nil, semantic search is enabled alongside FTS5.
// If cfg is non-nil, its search parameters override the defaults; nil values use defaults.
func NewSQLiteToolStore(embeddingClient types.EmbeddingClient, cfg *types.OptimizerConfig) (types.ToolStore, error) {
	return newSQLiteToolStore("file:memdb?mode=memory&cache=shared", embeddingClient, cfg)
}

// newSQLiteToolStore creates a tool store backed by a database described
// in the connectionString. It is useful for tests, where we want multiple
// isolated (non-shared) databases.
func newSQLiteToolStore(
	connectionString string, embeddingClient types.EmbeddingClient, cfg *types.OptimizerConfig,
) (sqliteToolStore, error) {
	db, err := sql.Open("sqlite", connectionString)
	if err != nil {
		return sqliteToolStore{}, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Execute schema
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return sqliteToolStore{}, fmt.Errorf("failed to initialize schema: %w", err)
	}

	maxTools := DefaultMaxToolsToReturn
	hybridRatio := DefaultHybridSemanticToolsRatio
	semanticThreshold := DefaultSemanticDistanceThreshold
	if cfg != nil {
		if cfg.MaxToolsToReturn != nil {
			maxTools = *cfg.MaxToolsToReturn
		}
		if cfg.HybridSemanticRatio != nil {
			hybridRatio = *cfg.HybridSemanticRatio
		}
		if cfg.SemanticDistanceThreshold != nil {
			semanticThreshold = *cfg.SemanticDistanceThreshold
		}
	}

	store := sqliteToolStore{
		db:                        db,
		embeddingClient:           embeddingClient,
		maxToolsToReturn:          maxTools,
		hybridSemanticRatio:       hybridRatio,
		semanticDistanceThreshold: semanticThreshold,
	}

	return store, nil
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

	embBlobs, err := s.generateEmbeddings(ctx, tools)
	if err != nil {
		return err
	}

	stmt, err := tx.PrepareContext(ctx, "INSERT OR REPLACE INTO llm_capabilities (name, description, embedding) VALUES (?, ?, ?)")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i, tool := range tools {
		if _, err := stmt.ExecContext(ctx, tool.Tool.Name, tool.Tool.Description, embBlobs[i]); err != nil {
			return fmt.Errorf("failed to upsert tool %s: %w", tool.Tool.Name, err)
		}
	}

	return tx.Commit()
}

// generateEmbeddings produces encoded embedding blobs for each tool.
// If no embedding client is configured, it returns a slice of nil byte slices.
func (s sqliteToolStore) generateEmbeddings(ctx context.Context, tools []server.ServerTool) ([][]byte, error) {
	blobs := make([][]byte, len(tools))

	if s.embeddingClient == nil {
		return blobs, nil
	}

	texts := make([]string, len(tools))
	for i, tool := range tools {
		texts[i] = fmt.Sprintf("name: %s description: %s", tool.Tool.Name, tool.Tool.Description)
	}

	embeddings, err := s.embeddingClient.EmbedBatch(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate embeddings: %w", err)
	}

	for i, emb := range embeddings {
		blobs[i] = encodeEmbedding(emb)
	}

	return blobs, nil
}

// Search finds tools matching the query string using FTS5 full-text search
// and optional semantic search when an embedding client is configured.
// The allowedTools parameter limits results to only tools with names in the given set.
// If allowedTools is empty, no results are returned (empty = no access).
// Returns matches ranked by relevance.
func (s sqliteToolStore) Search(ctx context.Context, query string, allowedTools []string) ([]types.ToolMatch, error) {
	if len(allowedTools) == 0 {
		return nil, nil
	}

	ftsExpr := sanitizeFTS5Query(query)

	// FTS5-only path (no embedding client)
	if s.embeddingClient == nil {
		if ftsExpr == "" {
			return nil, nil
		}
		results, err := s.searchFTS5(ctx, ftsExpr, allowedTools, s.maxToolsToReturn)
		if err != nil {
			return nil, err
		}
		return results, nil
	}

	// Hybrid search: derive per-method limits from the ratio.
	ftsLimit, semanticLimit := hybridSearchLimits(s.maxToolsToReturn, s.hybridSemanticRatio)

	g, gCtx := errgroup.WithContext(ctx)

	var ftsResults []types.ToolMatch
	if ftsExpr != "" && ftsLimit > 0 {
		g.Go(func() error {
			var err error
			ftsResults, err = s.searchFTS5(gCtx, ftsExpr, allowedTools, ftsLimit)
			return err
		})
	}

	var semanticResults []types.ToolMatch
	if semanticLimit > 0 {
		g.Go(func() error {
			var err error
			semanticResults, err = s.searchSemantic(gCtx, query, allowedTools, semanticLimit)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	merged := mergeResults(ftsResults, semanticResults, s.maxToolsToReturn)

	return merged, nil
}

// Close releases the underlying database connection.
func (s sqliteToolStore) Close() error {
	var embErr error
	if s.embeddingClient != nil {
		embErr = s.embeddingClient.Close()
	}
	dbErr := s.db.Close()
	return errors.Join(embErr, dbErr)
}

// searchFTS5 performs a full-text search using FTS5 MATCH with BM25 ranking.
// It uses json_each() to pass the allowed tool names as a single JSON array
// parameter, avoiding manual placeholder construction.
//
// The limit parameter caps results per this method. In hybrid mode, FTS5 and
// semantic search each independently return their top-k results (split by
// hybridSemanticToolsRatio). A tool with a low BM25 rank won't be missed if
// it has high cosine similarity, because the semantic query runs separately
// and will surface it.
//
// The ftsExpr is produced by sanitizeFTS5Query and is always passed as a
// parameterized ? value, never interpolated into SQL.
func (s sqliteToolStore) searchFTS5(
	ctx context.Context, ftsExpr string, allowedTools []string, limit int,
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
		ORDER BY rank
		LIMIT ?`

	rows, err := s.db.QueryContext(ctx, queryStr, ftsExpr, string(allowedJSON), limit)
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
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return matches, nil
}

// searchSemantic performs embedding-based semantic search.
// It embeds the query, loads all candidate embeddings from the database,
// computes cosine distance, and returns the closest matches.
//
// This runs as a separate query from searchFTS5 because BM25 rank and cosine
// similarity are fundamentally different metrics that cannot be meaningfully
// combined in a single SQL query. BM25 rank is a hidden FTS5 column computed
// on-the-fly from term frequency, while cosine similarity requires loading
// embedding blobs and computing distances in Go. Merging happens afterward
// in mergeResults, which deduplicates and keeps the best score per tool.
//
//nolint:unparam // limit kept for API consistency with searchFTS5
func (s sqliteToolStore) searchSemantic(
	ctx context.Context, query string, allowedTools []string, limit int,
) ([]types.ToolMatch, error) {
	queryVec, err := s.embeddingClient.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}

	allowedJSON, err := json.Marshal(allowedTools)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal allowed tools: %w", err)
	}

	queryStr := `SELECT name, description, embedding
		FROM llm_capabilities
		WHERE embedding IS NOT NULL
		  AND name IN (SELECT value FROM json_each(?))`

	rows, err := s.db.QueryContext(ctx, queryStr, string(allowedJSON))
	if err != nil {
		return nil, fmt.Errorf("semantic query failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type rankedMatch struct {
		name        string
		description string
		dist        float64
	}

	var ranked []rankedMatch
	var candidatesEvaluated int
	for rows.Next() {
		var name, description string
		var embBlob []byte
		if err := rows.Scan(&name, &description, &embBlob); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		candidatesEvaluated++
		emb := decodeEmbedding(embBlob)
		dist := similarity.CosineDistance(queryVec, emb)

		// Filter by semantic distance threshold.
		// This is meaningful only for cosine distance (semantic search).
		// FTS5 ranks are normalized BM25 scores, not true distance measures.
		if dist > s.semanticDistanceThreshold {
			continue
		}

		ranked = append(ranked, rankedMatch{
			name:        name,
			description: description,
			dist:        dist,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort by distance ascending (lower = better match)
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].dist < ranked[j].dist
	})

	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	matches := make([]types.ToolMatch, len(ranked))
	for i, r := range ranked {
		matches[i] = types.ToolMatch{
			Name:        r.name,
			Description: r.description,
		}
	}

	return matches, nil
}

// mergeResults combines semantic and FTS5 results, deduplicating by name.
// Semantic results are listed first (preserving their distance-based order),
// followed by FTS5 results not already present, and truncated to maxResults.
func mergeResults(fts, semantic []types.ToolMatch, maxResults int) []types.ToolMatch {
	seen := make(map[string]struct{}, len(fts)+len(semantic))
	merged := make([]types.ToolMatch, 0, len(fts)+len(semantic))

	// Semantic results first.
	for _, m := range semantic {
		if _, ok := seen[m.Name]; ok {
			continue
		}
		seen[m.Name] = struct{}{}
		merged = append(merged, m)
	}

	// Then FTS5 results not already seen.
	for _, m := range fts {
		if _, ok := seen[m.Name]; ok {
			continue
		}
		seen[m.Name] = struct{}{}
		merged = append(merged, m)
	}

	if len(merged) > maxResults {
		merged = merged[:maxResults]
	}

	return merged
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

// hybridSearchLimits computes the per-method result limits for hybrid search
// from the total limit and the semantic ratio (0 = all FTS5, 1 = all semantic).
func hybridSearchLimits(total int, semanticRatio float64) (ftsLimit, semanticLimit int) {
	semanticLimit = int(math.Round(float64(total) * semanticRatio))
	ftsLimit = total - semanticLimit
	return ftsLimit, semanticLimit
}

// encodeEmbedding serializes a float32 slice to a little-endian byte slice.
func encodeEmbedding(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// decodeEmbedding deserializes a little-endian byte slice to a float32 slice.
func decodeEmbedding(buf []byte) []float32 {
	vec := make([]float32, len(buf)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return vec
}
