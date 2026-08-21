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
	db *sql.DB

	// keepAlive is a dedicated connection held open for the store's whole lifetime.
	//
	// The database is in-memory with a shared cache, so it exists only while at
	// least one connection to it is open. If the database/sql pool ever drops to
	// zero connections, SQLite discards the entire database — table, FTS5 index
	// and triggers — and nothing recreates it, because the schema is executed
	// only in the constructor. Every later operation then fails with
	// "no such table: llm_capabilities" until the process restarts.
	//
	// The pool does reach zero in practice. modernc.org/sqlite implements context
	// cancellation with sqlite3_interrupt and reports an interrupted connection as
	// unusable from conn.IsValid, so database/sql discards a connection whose
	// statement was canceled instead of returning it to the pool. A single
	// canceled request while the pool holds one connection is enough to destroy
	// the database for the life of the process (#5889).
	//
	// This connection is acquired with a background context and never used to run
	// statements, so it can never be interrupted and never discarded. It exists
	// solely to keep the database alive.
	keepAlive *sql.Conn

	embeddingClient           types.EmbeddingClient // nil = FTS5-only
	maxToolsToReturn          int
	hybridSemanticRatio       float64
	semanticDistanceThreshold float64

	// configIdentity digests the configured embedding provider, endpoint and
	// model. The model id read live from the backend on every build is folded
	// in next to it (see embeddingIdentity), so vectors are not reused across
	// any provider, endpoint, or model change the identity can see — including
	// a TEI container redeployed with a different model behind an unchanged
	// URL, which the config alone cannot. When the id cannot be read the
	// identity falls back and reuse proceeds unverified, within the bounds
	// documented on embeddingIdentity. Immutable after construction.
	configIdentity string

	// lastModelID is the most recent model id successfully read from the
	// embedding backend, or nil before the first successful read. It is the
	// fallback for a failed read: keeping the last-seen id keeps cache keys
	// stable across a transient failure, where switching to a different
	// identity would force a full re-embed and a second one on recovery.
	// Held by pointer because the store is used by value.
	lastModelID *atomic.Pointer[string]

	// embeddingDim is the vector width most recently observed from the
	// embedding backend, or 0 before any embedding call has succeeded. It
	// bounds which stored vectors may be reused, as defense in depth behind
	// the model id in the cache key. Held by pointer because the store is
	// used by value.
	embeddingDim *atomic.Int64
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

	// Pin the keep-alive connection before anything else, so the database cannot
	// be discarded from this point on. See the keepAlive field for why.
	keepAlive, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		return sqliteToolStore{}, fmt.Errorf("failed to acquire keep-alive connection: %w", err)
	}

	// Execute schema
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = keepAlive.Close()
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
		keepAlive:                 keepAlive,
		embeddingClient:           embeddingClient,
		maxToolsToReturn:          maxTools,
		hybridSemanticRatio:       hybridRatio,
		semanticDistanceThreshold: semanticThreshold,
		configIdentity:            configIdentity(cfg),
		lastModelID:               &atomic.Pointer[string]{},
		embeddingDim:              &atomic.Int64{},
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

// maxResolveAttempts bounds how many times one build may restart because the
// embedding model changed under it. Two is enough for the legitimate case of
// a single redeploy; a backend that swaps models on consecutive builds is an
// operational problem no retry count fixes.
const maxResolveAttempts = 2

// resolveEmbeddings returns an encoded embedding blob and a content hash for
// each tool, embedding only the tools whose hash is not already stored.
//
// The Serve path rebuilds a per-session optimizer on every session registration
// and every cross-pod rehydration, each upserting the session's whole tool set.
// Embedding all of it every time costs O(tools x sessions) on the client's
// initialize round-trip; reuse makes it O(tools whose text changed). See
// stacklok/toolhive#5847.
//
// The backend identity is read at the start of an attempt and re-read after
// the embedding batch. If the two differ, the batch spanned a model swap and
// none of its vectors is attributable to either model — each chunk may have
// hit either side of the swap — so the attempt is discarded and re-run under
// the new identity, where the stale rows simply miss by key. Without the
// re-read, vectors produced by the new model would be committed under keys
// naming the old one: wrong vectors under valid-looking keys, which no later
// build would ever re-check. Reused blobs need no such guard: a blob is only
// found under the identity it was committed with, and only identities that
// were verified when the blob was committed ever carry a hash — vectors
// embedded while the id could not be read are committed hashless, searchable
// but never reusable (see the note at the fill loop below).
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
	for i, tool := range tools {
		texts[i] = embeddedText(tool.Tool.Name, tool.Tool.Description)
	}

	keys := make([]string, len(tools))
	for attempt := 1; ; attempt++ {
		identity, verified := s.embeddingIdentity(ctx)

		for i := range tools {
			keys[i] = embeddingCacheKey(identity, texts[i])
			hashes[i] = sql.NullString{String: keys[i], Valid: true}
		}

		cached, err := s.cachedEmbeddings(ctx, keys)
		if err != nil {
			return nil, nil, err
		}

		missIndexByKey, missTexts, reused := partitionMisses(blobs, keys, texts, cached)

		slog.Debug("resolved tool embeddings",
			"tools", len(tools), "reused", reused, "embedded", len(missTexts))

		// All cache hits: nothing was embedded, so there is no batch to have
		// spanned a swap. The reused vectors are correct for the keys they
		// carry by construction.
		if len(missTexts) == 0 {
			return blobs, hashes, nil
		}

		embeddings, err := s.embedTexts(ctx, missTexts)
		if err != nil {
			return nil, nil, err
		}

		after, afterVerified := s.embeddingIdentity(ctx)
		if after != identity {
			if attempt >= maxResolveAttempts {
				return nil, nil, fmt.Errorf(
					"embedding model changed during %d consecutive build attempts; giving up", attempt)
			}
			slog.Warn("embedding model changed during the batch; discarding it and re-embedding under the new identity")
			continue
		}
		verified = verified && afterVerified

		// Published only for a batch that will commit: a discarded attempt's
		// width must not make concurrent builds reject their own valid rows.
		if n := len(embeddings[0]); n > 0 {
			s.embeddingDim.Store(int64(n))
		}

		if err := fillMisses(blobs, hashes, keys, tools, missIndexByKey, embeddings, verified); err != nil {
			return nil, nil, err
		}

		return blobs, hashes, nil
	}
}

// fillMisses encodes the freshly embedded vectors into their tools' slots.
// When the attempt's identity was not verified on both sides of the batch,
// the fresh rows are committed hashless — searchable but never reusable. A
// hashed row here would be permanent poison if the backend is later rolled
// back to the fallback model: the next verified build would derive the very
// identity these keys name, cache-hit the mislabelled vectors, and re-key
// nothing.
func fillMisses(
	blobs [][]byte, hashes []sql.NullString, keys []string,
	tools []server.ServerTool, missIndexByKey map[string]int, embeddings [][]float32, verified bool,
) error {
	for i, key := range keys {
		if blobs[i] != nil {
			continue
		}
		idx, ok := missIndexByKey[key]
		if !ok {
			// Unreachable, but a missing key would otherwise index 0 and store
			// another tool's vector under this name.
			return fmt.Errorf("no embedding resolved for tool %s", tools[i].Tool.Name)
		}
		blobs[i] = encodeEmbedding(embeddings[idx])
		if !verified {
			hashes[i] = sql.NullString{}
		}
	}
	return nil
}

// partitionMisses fills blobs with the cached vectors and returns the texts
// still to embed, deduplicated by key — the same tool may appear twice in one
// batch — along with the number of reused entries. Entries with no cached
// vector are reset to nil: a retry after a model swap must not carry blobs
// reused under the previous attempt's identity.
func partitionMisses(
	blobs [][]byte, keys, texts []string, cached map[string][]byte,
) (map[string]int, []string, int) {
	missIndexByKey := make(map[string]int, len(keys))
	var missTexts []string
	reused := 0
	for i, key := range keys {
		if blob, ok := cached[key]; ok {
			blobs[i] = blob
			reused++
			continue
		}
		blobs[i] = nil
		if _, seen := missIndexByKey[key]; !seen {
			missIndexByKey[key] = len(missTexts)
			missTexts = append(missTexts, texts[i])
		}
	}
	return missIndexByKey, missTexts, reused
}

// embedTexts runs one embedding batch, returning exactly one vector per text.
func (s sqliteToolStore) embedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	embeddings, err := s.embeddingClient.EmbedBatch(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("failed to generate embeddings: %w", err)
	}
	if len(embeddings) != len(texts) {
		return nil, fmt.Errorf("embedding client returned %d embeddings for %d inputs",
			len(embeddings), len(texts))
	}
	return embeddings, nil
}

// embeddingIdentity derives the backend identity mixed into every cache key
// for one build, folding the live model id into the configured identity.
//
// The id is read from the backend on every build rather than once at
// construction: the store lives for the whole process and the embedding
// service is addressed by a stable URL, so a TEI container can be redeployed
// with a different model behind an unchanged URL — invisible to the config,
// and to the dimension check when the widths match. A live id turns that
// swap into different cache keys, so stale rows simply stop being found;
// there is nothing to detect and nothing to discard.
//
// Every failure path keeps the previous identity and reports it unverified.
// A read that fails says the backend is unreachable, not that it changed —
// and an unreachable backend cannot re-embed the catalogue either, so
// flipping the identity would turn a working build into a full re-embed now
// and a second one on recovery. Before any successful read it degrades to
// the configured identity alone. Previously verified rows stay reusable
// under an unverified identity; what an unverified identity must never do is
// attribute NEW vectors (see resolveEmbeddings).
func (s sqliteToolStore) embeddingIdentity(ctx context.Context) (identity string, verified bool) {
	modelID, err := s.embeddingClient.ModelID(ctx)
	verified = err == nil && modelID != ""
	if !verified {
		modelID = ""
		if last := s.lastModelID.Load(); last != nil {
			modelID = *last
		}
		slog.Warn("could not read the embedding model id; proceeding with the last known identity unverified",
			"error", err, "assumed_model_id", modelID)
	} else {
		s.lastModelID.Store(&modelID)
	}
	// Digested rather than joined so the identity is fixed-length and cannot
	// shift the field boundaries of the cache key it is folded into.
	return hashParts(s.configIdentity, modelID), verified
}

// cachedEmbeddings returns the reusable stored embeddings among the given
// content hashes, keyed by hash. Hashes with no usable vector are absent.
//
// Matching on content_hash rather than tool name confirms in one lookup that
// the embedded text and the producing backend are both unchanged; a changed
// description or a repointed backend cannot quietly reuse a vector. A rename
// changes the text, so it re-embeds. Runs outside any transaction — see the
// lock note in UpsertTools.
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

// configIdentity digests the configured half of the backend identity; the
// model id read live per build completes it (see embeddingIdentity).
func configIdentity(cfg *types.OptimizerConfig) string {
	if cfg == nil {
		return ""
	}
	// Digested rather than joined so the identity is fixed-length and cannot
	// shift the field boundaries of the cache key it is folded into.
	return hashParts(cfg.EmbeddingProvider, cfg.EmbeddingService, cfg.EmbeddingModel)
}

// Search finds tools matching q using FTS5 full-text search and optional
// semantic search when an embedding client is configured. The lexical arm
// prefers q.Keywords, falling back to q.Description when Keywords is empty;
// the semantic arm always embeds q.Description.
// The allowedTools parameter limits results to only tools with names in the given set.
// If allowedTools is empty, no results are returned (empty = no access).
// Returns matches ranked by relevance.
func (s sqliteToolStore) Search(ctx context.Context, q types.SearchQuery, allowedTools []string) ([]mcp.Tool, error) {
	if len(allowedTools) == 0 {
		slog.Debug("search skipped, no allowed tools")
		return nil, nil
	}

	// The lexical arm prefers explicit keywords and falls back to the words of
	// the description, either when no keywords were supplied or when every
	// keyword was dropped as too common to discriminate. The semantic arm
	// always embeds the description.
	ftsExpr := sanitizeFTS5Terms(q.Keywords)
	if ftsExpr == "" {
		ftsExpr = sanitizeFTS5Terms(strings.Fields(q.Description))
	}

	// FTS5-only path (no embedding client)
	if s.embeddingClient == nil {
		if ftsExpr == "" {
			slog.Debug("search skipped, empty FTS5 expression", "query", q.Description, "keywords", q.Keywords)
			return nil, nil
		}
		results, err := s.searchFTS5(ctx, ftsExpr, allowedTools, s.maxToolsToReturn)
		if err != nil {
			return nil, err
		}
		slog.Debug("search completed (FTS5-only)",
			"query", q.Description, "keywords", q.Keywords, "results", len(results), "matched_tools", matchNames(results))
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
			semanticResults, err = s.searchSemantic(gCtx, q.Description, allowedTools, semanticLimit)
			return err
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	merged := mergeResults(ftsResults, semanticResults, s.maxToolsToReturn)

	slog.Debug("search completed (hybrid)",
		"query", q.Description,
		"keywords", q.Keywords,
		"fts5_results", len(ftsResults),
		"semantic_results", len(semanticResults),
		"merged_results", len(merged),
		"matched_tools", matchNames(merged),
	)

	return merged, nil
}

// Close releases the underlying database connections. Releasing the keep-alive
// connection drops the in-memory database, so it happens only here, on the way
// to closing the pool itself.
//
// Close may be called more than once: a keep-alive connection that was already
// released reports sql.ErrConnDone, which is not a failure.
func (s sqliteToolStore) Close() error {
	var embErr error
	if s.embeddingClient != nil {
		embErr = s.embeddingClient.Close()
	}

	var keepAliveErr error
	if s.keepAlive != nil {
		if err := s.keepAlive.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
			keepAliveErr = err
		}
	}

	dbErr := s.db.Close()
	return errors.Join(embErr, keepAliveErr, dbErr)
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
// The ftsExpr is produced by sanitizeFTS5Terms and is always passed as a
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

		// A width mismatch means the vector survived a model change (see
		// embeddingIdentity); skip it rather than fail the whole search.
		dist, err := similarity.CosineDistance(queryVec, emb)
		if err != nil {
			dimensionMismatches++
			continue
		}

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
// are too common in tool metadata to be useful search terms.
//
// sanitizeFTS5Terms drops them. The alternative, falling back to a phrase
// search over the whole query when one of these appears, demands exact token
// adjacency and so reliably matches nothing; dropping the noise word and
// OR-joining the rest avoids the result flood without going empty-handed.
var problematicWords = map[string]struct{}{
	"name": {}, "description": {}, "schema": {}, "input": {},
	"output": {}, "type": {}, "properties": {}, "required": {},
	"title": {}, "id": {}, "tool": {}, "server": {},
	"meta": {}, "data": {}, "content": {}, "text": {},
	"value": {}, "field": {}, "column": {}, "table": {},
	"index": {}, "key": {}, "primary": {},
}

// sanitizeFTS5Terms builds an FTS5 MATCH expression from a list of search terms.
//
// Each entry is split on whitespace (a single term may arrive multi-word), words
// in problematicWords are dropped, the remainder is deduplicated case-insensitively,
// and what is left is OR-joined. Returns "" when no usable term remains, letting
// the caller fall back to another source of terms.
//
// The returned string is designed to be passed as a single ? parameter to
// QueryContext. FTS5 MATCH requires a single string operand containing the full
// query expression (e.g., "read" OR "write"). Individual terms cannot be separate
// ? SQL parameters because the OR/AND operators are part of the FTS5 query
// language, not SQL.
// See: https://sqlite.org/fts5.html#full_text_query_syntax
//
// Safety:
//   - SQL injection is prevented because the expression is always bound via ?.
//   - FTS5 operator injection is prevented by double-quoting each term and
//     escaping embedded double-quotes (standard FTS5 escaping).
func sanitizeFTS5Terms(terms []string) string {
	seen := make(map[string]struct{}, len(terms))
	var quoted []string
	for _, term := range terms {
		for _, word := range strings.Fields(term) {
			lower := strings.ToLower(word)
			if _, ok := problematicWords[lower]; ok {
				continue
			}
			if _, ok := seen[lower]; ok {
				continue
			}
			seen[lower] = struct{}{}
			escaped := strings.ReplaceAll(word, `"`, `""`)
			quoted = append(quoted, `"`+escaped+`"`)
		}
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
