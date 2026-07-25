// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package toolstore implements a SQLite-based ToolStore for search over
// MCP tool metadata. It uses FTS5 for full-text search and optional
// embedding-based semantic search for hybrid retrieval.
package toolstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/errgroup"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
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

	// embeddingIdentity describes the backend that produces embeddings. It is
	// mixed into every content hash so vectors are never reused across a
	// provider, endpoint, or model change. Immutable after construction.
	embeddingIdentity string

	// embeddingDim is the vector width most recently observed from the
	// embedding backend, or 0 before any embedding call has succeeded. It
	// bounds which stored vectors may be reused, catching a model swap that
	// embeddingIdentity cannot see (the TEI model is fixed by the running
	// container, not by config). Held by pointer because the store is used by
	// value.
	embeddingDim *atomic.Int64

	// canary serializes the backend probe so concurrent builds cannot race on
	// the stored probe row. Held by pointer because the store is used by value.
	canary *canaryState
}

// canaryState serializes the backend probe and counts completed probes, so a
// build that waited through one can tell that its result already applies.
type canaryState struct {
	mu         sync.Mutex
	generation atomic.Uint64
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
		embeddingIdentity:         embeddingIdentity(cfg),
		embeddingDim:              &atomic.Int64{},
		canary:                    &canaryState{},
	}

	slog.Debug("optimizer tool store created",
		"max_tools_to_return", maxTools,
		"hybrid_semantic_ratio", hybridRatio,
		"semantic_distance_threshold", semanticThreshold,
		"semantic_search_enabled", embeddingClient != nil,
	)

	return store, nil
}

// UpsertTools adds or updates tools in the store.
func (s sqliteToolStore) UpsertTools(ctx context.Context, tools []server.ServerTool) (retErr error) {
	// Resolve embeddings before opening the write transaction, so no lock is
	// held across the multi-second embedding call. SQLite in shared-cache mode
	// takes table-level locks: a read inside the transaction would pin a read
	// lock on llm_capabilities, and concurrent builds would then fail to take
	// the write lock with SQLITE_LOCKED, which the busy handler does not retry.
	// See TestSQLiteToolStore_UpsertTools_ConcurrentBuilds.
	embBlobs, hashes, err := s.resolveEmbeddings(ctx, tools)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = tx.Rollback()
		}
	}()

	stmt, err := tx.PrepareContext(ctx,
		"INSERT OR REPLACE INTO llm_capabilities (name, description, embedding, content_hash) VALUES (?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i, tool := range tools {
		if _, err := stmt.ExecContext(ctx, tool.Tool.Name, tool.Tool.Description, embBlobs[i], hashes[i]); err != nil {
			return fmt.Errorf("failed to upsert tool %s: %w", tool.Tool.Name, err)
		}
	}

	slog.Debug("upserted tools into store", "count", len(tools))

	return tx.Commit()
}

// resolveEmbeddings returns an encoded embedding blob and a content hash for
// each tool, embedding only the tools whose hash is not already stored.
//
// The Serve path rebuilds a per-session optimizer on every session registration
// and every cross-pod rehydration, each upserting the session's whole tool set.
// Embedding all of it every time costs O(tools x sessions) on the client's
// initialize round-trip; reuse makes it O(tools whose text changed). See
// stacklok/toolhive#5847.
//
// With no embedding client it returns nil blobs and hashes (FTS5-only mode).
func (s sqliteToolStore) resolveEmbeddings(
	ctx context.Context, tools []server.ServerTool,
) ([][]byte, []sql.NullString, error) {
	blobs := make([][]byte, len(tools))
	hashes := make([]sql.NullString, len(tools))

	if s.embeddingClient == nil {
		return blobs, hashes, nil
	}

	texts := make([]string, len(tools))
	keys := make([]string, len(tools))
	for i, tool := range tools {
		texts[i] = embeddedText(tool.Tool.Name, tool.Tool.Description)
		keys[i] = embeddingCacheKey(s.embeddingIdentity, texts[i])
		hashes[i] = sql.NullString{String: keys[i], Valid: true}
	}

	// Discards stored vectors first if the backend has changed under us, so the
	// lookup below simply finds nothing to reuse for them.
	s.syncBackendProbe(ctx)

	cached, err := s.cachedEmbeddings(ctx, keys)
	if err != nil {
		return nil, nil, err
	}

	// Deduplicated by key: the same tool may appear twice in one batch.
	missIndexByKey := make(map[string]int, len(keys))
	var missTexts []string
	reused := 0
	for i, key := range keys {
		if blob, ok := cached[key]; ok {
			blobs[i] = blob
			reused++
			continue
		}
		if _, seen := missIndexByKey[key]; !seen {
			missIndexByKey[key] = len(missTexts)
			missTexts = append(missTexts, texts[i])
		}
	}

	slog.Debug("resolved tool embeddings",
		"tools", len(tools), "reused", reused, "embedded", len(missTexts))

	if len(missTexts) == 0 {
		return blobs, hashes, nil
	}

	embeddings, err := s.embeddingClient.EmbedBatch(ctx, missTexts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate embeddings: %w", err)
	}
	if len(embeddings) != len(missTexts) {
		return nil, nil, fmt.Errorf("embedding client returned %d embeddings for %d inputs",
			len(embeddings), len(missTexts))
	}
	if n := len(embeddings[0]); n > 0 {
		s.embeddingDim.Store(int64(n))
	}

	for i, key := range keys {
		if blobs[i] != nil {
			continue
		}
		idx, ok := missIndexByKey[key]
		if !ok {
			// Unreachable, but a missing key would otherwise index 0 and store
			// another tool's vector under this name.
			return nil, nil, fmt.Errorf("no embedding resolved for tool %s", tools[i].Tool.Name)
		}
		blobs[i] = encodeEmbedding(embeddings[idx])
	}

	return blobs, hashes, nil
}

// canaryText is the fixed probe embedded to detect a change of embedding
// backend. Its content is arbitrary but must never change: an edit would make
// every stored canary incomparable and force one needless full re-embed.
const canaryText = "toolhive optimizer embedding canary v1"

// canaryMaxDistance is the cosine distance below which two probe embeddings are
// considered to come from the same backend.
//
// The threshold is not zero because a backend is not required to be
// deterministic: reduction order can differ across hardware and runtimes, so the
// same model may return slightly different vectors on different deployments.
// It is small because the signal it must not miss is large — two different
// models of equal width place the same text roughly a full unit apart, i.e.
// effectively orthogonal, so this leaves two orders of magnitude of margin.
const canaryMaxDistance = 0.01

// syncBackendProbe discards stored embeddings when the embedding backend
// has started returning different vectors.
//
// It runs on every build, not once per store. The store lives for the whole
// process and the embedding service is addressed by a stable URL, so the backend
// can be replaced underneath a running server — redeploying the embedding
// service with a different model of the same width changes neither the URL nor
// the vector length, leaving it invisible to both the content hash and the
// dimension check. Probing once at startup would never see it, and reuse would
// then serve vectors from the old model for the rest of the process's life.
// Before embedding reuse existed the next session simply re-embedded everything
// and the condition healed on its own.
//
// Cost is one embedding per build, against the ~140 it saves.
//
// Every failure path leaves the stored embeddings reusable. A probe that cannot
// be taken says the backend is unreachable, not that it changed — and an
// unreachable backend cannot re-embed the catalogue either, so refusing reuse
// would turn a working build into a failed one. Serving possibly-stale vectors
// is bounded: the next build with a reachable backend re-probes and discards
// them. A build that still works beats a client with no tools.
func (s sqliteToolStore) syncBackendProbe(ctx context.Context) {
	// Every build must be ordered after any in-flight probe, because a probe may
	// be about to discard the very vectors this build is about to read and write
	// back. Skipping the lock instead of waiting for it lets a build read
	// pre-discard rows and re-insert them — restoring both the stale vector and
	// its content hash — after which the freshly written probe certifies the
	// store as current and nothing ever re-checks. That is permanent, not
	// bounded. See TestSQLiteToolStore_ConcurrentBuilds_OrderedAfterProbe.
	//
	// Waiting is still cheap: a build that waited through someone else's probe
	// sees the generation move and skips the network call, so a burst of
	// concurrent builds costs one embedding between them, not one each.
	gen := s.canary.generation.Load()
	s.canary.mu.Lock()
	defer s.canary.mu.Unlock()
	if s.canary.generation.Load() != gen {
		return
	}

	// Embedded outside any transaction so no database lock is held across the
	// network call (see the note in UpsertTools).
	probe, err := s.embeddingClient.Embed(ctx, canaryText)
	if err != nil {
		slog.Warn("could not probe the embedding backend; reusing stored embeddings unverified", "error", err)
		return
	}
	if len(probe) == 0 {
		slog.Warn("embedding backend returned an empty probe; reusing stored embeddings unverified")
		return
	}
	s.embeddingDim.Store(int64(len(probe)))

	changed, err := s.reconcileCanary(ctx, probe)
	if err != nil {
		slog.Warn("could not reconcile the embedding probe; reusing stored embeddings unverified", "error", err)
		return
	}

	s.canary.generation.Add(1)
	if changed {
		slog.Warn("embedding backend changed; stored embeddings were discarded and will be recomputed")
	}
}

// reconcileCanary compares probe against the stored canary, discarding every
// stored embedding when they differ, and records probe as the new canary.
// Reports whether stored embeddings were discarded.
//
// The discard and the new canary are written in one transaction so a failure
// cannot leave a canary that claims vectors are current when they are not.
func (s sqliteToolStore) reconcileCanary(ctx context.Context, probe []float32) (discarded bool, retErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("failed to begin canary transaction: %w", err)
	}
	defer func() {
		if retErr != nil {
			_ = tx.Rollback()
		}
	}()

	var storedBlob []byte
	err = tx.QueryRowContext(ctx, "SELECT embedding FROM embedding_canary WHERE id = 1").Scan(&storedBlob)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No probe recorded. Any embeddings already present came from a backend
		// this store cannot vouch for, so they are not reusable.
		var existing int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM llm_capabilities WHERE embedding IS NOT NULL").Scan(&existing); err != nil {
			return false, fmt.Errorf("failed to count stored embeddings: %w", err)
		}
		discarded = existing > 0
	case err != nil:
		return false, fmt.Errorf("failed to read the stored canary: %w", err)
	default:
		stored := decodeEmbedding(storedBlob)
		// Compare widths first: cosine distance indexes both slices positionally
		// and would panic on a shorter stored vector.
		discarded = len(stored) != len(probe) ||
			similarity.CosineDistance(stored, probe) > canaryMaxDistance
	}

	if discarded {
		// Clear the vectors but keep the rows: they also back the external-content
		// FTS5 index, so deleting them would break keyword search for tools
		// outside the current session's set.
		if _, err := tx.ExecContext(ctx,
			"UPDATE llm_capabilities SET embedding = NULL, content_hash = NULL"); err != nil {
			return false, fmt.Errorf("failed to discard stale embeddings: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		"INSERT OR REPLACE INTO embedding_canary (id, embedding) VALUES (1, ?)",
		encodeEmbedding(probe)); err != nil {
		return false, fmt.Errorf("failed to record the canary: %w", err)
	}

	return discarded, tx.Commit()
}

// cachedEmbeddings returns the reusable stored embeddings among the given
// content hashes, keyed by hash. Hashes with no usable vector are absent.
//
// Matching on content_hash rather than tool name lets a renamed tool keep its
// vector. Runs outside any transaction — see the lock note in UpsertTools.
//
// A stale-width vector is treated as a miss rather than reused: reuse would be
// permanent, since it is handed back and re-stored on every rebuild while
// search discards it, silently dropping the tool from semantic results.
func (s sqliteToolStore) cachedEmbeddings(ctx context.Context, keys []string) (map[string][]byte, error) {
	keysJSON, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal content hashes: %w", err)
	}

	queryStr := `SELECT content_hash, embedding
		FROM llm_capabilities
		WHERE embedding IS NOT NULL
		  AND content_hash IN (SELECT value FROM json_each(?))`

	rows, err := s.db.QueryContext(ctx, queryStr, string(keysJSON))
	if err != nil {
		return nil, fmt.Errorf("embedding cache lookup failed: %w", err)
	}
	defer func() { _ = rows.Close() }()

	wantBytes := int(s.embeddingDim.Load()) * 4
	cached := make(map[string][]byte, len(keys))
	var unusable int
	for rows.Next() {
		var hash string
		var blob []byte
		if err := rows.Scan(&hash, &blob); err != nil {
			return nil, fmt.Errorf("failed to scan cached embedding: %w", err)
		}
		if len(blob) == 0 || (wantBytes > 0 && len(blob) != wantBytes) {
			unusable++
			continue
		}
		cached[hash] = blob
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if unusable > 0 {
		slog.Warn("ignoring unusable stored embeddings, they will be recomputed",
			"count", unusable, "expected_bytes", wantBytes)
	}

	return cached, nil
}

// embeddedText builds the exact string sent to the embedding backend. The
// content hash is only meaningful if it covers precisely what was embedded, so
// callers must not construct this string themselves.
func embeddedText(name, description string) string {
	return fmt.Sprintf("name: %s description: %s", name, description)
}

// cacheKeyVersion invalidates every stored key if the embedded-text format changes.
const cacheKeyVersion = "v1"

// embeddingCacheKey hashes the embedded text together with the identity of the
// backend that produced it.
//
// The backend identity is what makes reuse safe: an embedding is interchangeable
// only with one from the same provider, endpoint, and model. Without it,
// repointing the service would silently serve vectors from a different semantic
// space.
func embeddingCacheKey(identity, text string) string {
	return hashParts(cacheKeyVersion, identity, text)
}

// hashParts hashes an ordered list of strings so that distinct lists can never
// produce the same digest.
//
// Each part is length-prefixed rather than delimiter-joined. A delimiter alone
// is ambiguous — moving the separator between two adjacent parts yields an
// identical byte stream, so ("a\x00b", "c") and ("a", "b\x00c") would collide.
// That matters because tool names and descriptions reach this function from
// aggregated backend servers, so a backend could otherwise craft a description
// that collides with another tool's key and take over its stored embedding.
func hashParts(parts ...string) string {
	h := sha256.New()
	var length [8]byte
	for _, part := range parts {
		binary.LittleEndian.PutUint64(length[:], uint64(len(part)))
		h.Write(length[:])
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// embeddingIdentity derives the backend identity mixed into every content hash.
//
// Known limitation: for the TEI provider the model is fixed by the running
// container rather than by config, so swapping the model behind an unchanged
// service URL is not detected here. Search tolerates the resulting stale
// vectors (see searchSemantic) but they remain semantically stale until the
// process restarts. Reading the model id from the TEI /info endpoint would
// close this gap.
func embeddingIdentity(cfg *types.OptimizerConfig) string {
	if cfg == nil {
		return ""
	}
	// Digested rather than joined so the identity is fixed-length and cannot
	// shift the field boundaries of the cache key it is folded into.
	return hashParts(cfg.EmbeddingProvider, cfg.EmbeddingService, cfg.EmbeddingModel)
}

// Search finds tools matching the query string using FTS5 full-text search
// and optional semantic search when an embedding client is configured.
// The allowedTools parameter limits results to only tools with names in the given set.
// If allowedTools is empty, no results are returned (empty = no access).
// Returns matches ranked by relevance.
func (s sqliteToolStore) Search(ctx context.Context, query string, allowedTools []string) ([]mcp.Tool, error) {
	if len(allowedTools) == 0 {
		slog.Debug("search skipped, no allowed tools")
		return nil, nil
	}

	ftsExpr := sanitizeFTS5Query(query)

	// FTS5-only path (no embedding client)
	if s.embeddingClient == nil {
		if ftsExpr == "" {
			slog.Debug("search skipped, empty FTS5 expression", "query", query)
			return nil, nil
		}
		results, err := s.searchFTS5(ctx, ftsExpr, allowedTools, s.maxToolsToReturn)
		if err != nil {
			return nil, err
		}
		slog.Debug("search completed (FTS5-only)", "query", query, "results", len(results), "matched_tools", matchNames(results))
		return results, nil
	}

	// Hybrid search: derive per-method limits from the ratio.
	ftsLimit, semanticLimit := hybridSearchLimits(s.maxToolsToReturn, s.hybridSemanticRatio)

	g, gCtx := errgroup.WithContext(ctx)

	var ftsResults []mcp.Tool
	if ftsExpr != "" && ftsLimit > 0 {
		g.Go(func() error {
			var err error
			ftsResults, err = s.searchFTS5(gCtx, ftsExpr, allowedTools, ftsLimit)
			return err
		})
	}

	var semanticResults []mcp.Tool
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

	slog.Debug("search completed (hybrid)",
		"query", query,
		"fts5_results", len(ftsResults),
		"semantic_results", len(semanticResults),
		"merged_results", len(merged),
		"matched_tools", matchNames(merged),
	)

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
) ([]mcp.Tool, error) {
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

	var matches []mcp.Tool
	for rows.Next() {
		var name, description string
		var rank float64
		if err := rows.Scan(&name, &description, &rank); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		matches = append(matches, mcp.Tool{
			Name:        name,
			Description: description,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	slog.Debug("FTS5 search completed",
		"fts_expression", ftsExpr,
		"allowed_tools", len(allowedTools),
		"limit", limit,
		"results", len(matches),
		"matched_tools", matchNames(matches),
	)

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
) ([]mcp.Tool, error) {
	queryVec, err := s.embeddingClient.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to embed query: %w", err)
	}
	// A build whose tools are all cache hits never embeds anything, so the query
	// round-trip is the only place a model swap is still observable.
	if len(queryVec) > 0 {
		s.embeddingDim.Store(int64(len(queryVec)))
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
	var candidatesEvaluated, dimensionMismatches int
	for rows.Next() {
		var name, description string
		var embBlob []byte
		if err := rows.Scan(&name, &description, &embBlob); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		candidatesEvaluated++
		emb := decodeEmbedding(embBlob)

		// Cosine distance indexes both slices positionally: a shorter stored
		// vector panics, a longer one silently ignores its tail. A mismatch
		// means the vector survived a model change (see embeddingIdentity).
		if len(emb) != len(queryVec) {
			dimensionMismatches++
			continue
		}

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

	matches := make([]mcp.Tool, len(ranked))
	for i, r := range ranked {
		matches[i] = mcp.Tool{
			Name:        r.name,
			Description: r.description,
		}
	}

	if dimensionMismatches > 0 {
		slog.Warn("skipped stored embeddings with a mismatched dimension",
			"count", dimensionMismatches, "query_dimensions", len(queryVec))
	}

	slog.Debug("semantic search completed",
		"allowed_tools", len(allowedTools),
		"limit", limit,
		"candidates_evaluated", candidatesEvaluated,
		"dimension_mismatches", dimensionMismatches,
		"results", len(matches),
		"matched_tools", matchNames(matches),
	)

	return matches, nil
}

// mergeResults combines semantic and FTS5 results, deduplicating by name.
// Semantic results are listed first (preserving their distance-based order),
// followed by FTS5 results not already present, and truncated to maxResults.
func mergeResults(fts, semantic []mcp.Tool, maxResults int) []mcp.Tool {
	seen := make(map[string]struct{}, len(fts)+len(semantic))
	merged := make([]mcp.Tool, 0, len(fts)+len(semantic))

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

// matchNames extracts tool names from a slice of ToolMatch results for logging.
func matchNames(matches []mcp.Tool) []string {
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name
	}
	return names
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
