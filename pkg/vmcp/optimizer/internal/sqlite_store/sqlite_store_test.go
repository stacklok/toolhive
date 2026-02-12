// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlitestore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

// testDBCounter ensures each test gets a unique in-memory database.
var testDBCounter atomic.Int64

func newTestStore(t *testing.T, embeddingClient types.EmbeddingClient) sqliteToolStore {
	t.Helper()
	id := testDBCounter.Add(1)
	store, err := newSQLiteToolStore(fmt.Sprintf("file:testdb_%d?mode=memory&cache=shared", id), embeddingClient)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func toolNames(tools []server.ServerTool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Tool.Name
	}
	return names
}

func makeTools(tools ...mcp.Tool) []server.ServerTool {
	result := make([]server.ServerTool, len(tools))
	for i, tool := range tools {
		result[i] = server.ServerTool{Tool: tool}
	}
	return result
}

func TestNewSQLiteToolStore(t *testing.T) {
	t.Parallel()

	t.Run("without embedding client", func(t *testing.T) {
		t.Parallel()
		store, err := NewSQLiteToolStore(nil)
		require.NoError(t, err)
		require.NotNil(t, store)
		concrete, ok := store.(sqliteToolStore)
		require.True(t, ok)
		require.NotNil(t, concrete.db)
		require.Nil(t, concrete.embeddingClient)
		require.NoError(t, store.Close())
	})

	t.Run("with embedding client", func(t *testing.T) {
		t.Parallel()
		client := newFakeEmbeddingClient(384)
		store, err := NewSQLiteToolStore(client)
		require.NoError(t, err)
		require.NotNil(t, store)
		concrete, ok := store.(sqliteToolStore)
		require.True(t, ok)
		require.NotNil(t, concrete.embeddingClient)
		require.NoError(t, store.Close())
	})
}

func TestSQLiteToolStore_UpsertTools(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		initial      []server.ServerTool
		upsert       []server.ServerTool
		searchQuery  string
		allowedTools []string
		wantLen      int
		wantDesc     string
	}{
		{
			name: "insert new tools",
			upsert: makeTools(
				mcp.NewTool("read_file", mcp.WithDescription("Read a file from disk")),
				mcp.NewTool("write_file", mcp.WithDescription("Write content to a file")),
			),
			searchQuery:  "file",
			allowedTools: []string{"read_file", "write_file"},
			wantLen:      2,
		},
		{
			name: "overwrite updates description",
			initial: makeTools(
				mcp.NewTool("read_file", mcp.WithDescription("Read a file")),
			),
			upsert: makeTools(
				mcp.NewTool("read_file", mcp.WithDescription("Read any file from the filesystem")),
			),
			searchQuery:  "filesystem",
			allowedTools: []string{"read_file"},
			wantLen:      1,
			wantDesc:     "Read any file from the filesystem",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t, nil)
			ctx := context.Background()

			if tc.initial != nil {
				require.NoError(t, store.UpsertTools(ctx, tc.initial))
			}
			require.NoError(t, store.UpsertTools(ctx, tc.upsert))

			results, err := store.Search(ctx, tc.searchQuery, tc.allowedTools)
			require.NoError(t, err)
			require.Len(t, results, tc.wantLen)
			if tc.wantDesc != "" && len(results) > 0 {
				require.Equal(t, tc.wantDesc, results[0].Description)
			}
		})
	}
}

func TestSQLiteToolStore_UpsertTools_WithEmbeddings(t *testing.T) {
	t.Parallel()
	client := newFakeEmbeddingClient(384)
	store := newTestStore(t, client)
	ctx := context.Background()

	tools := makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read a file from disk")),
		mcp.NewTool("send_email", mcp.WithDescription("Send an email message")),
	)
	require.NoError(t, store.UpsertTools(ctx, tools))

	// Verify embeddings were stored
	var count int
	err := store.db.QueryRow("SELECT COUNT(*) FROM llm_capabilities WHERE embedding IS NOT NULL").Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 2, count)
}

func TestSQLiteToolStore_Search(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tools        []server.ServerTool
		query        string
		allowedTools []string
		wantNames    []string
		wantNonEmpty bool // just assert results are non-empty (when exact names vary)
		checkScores  bool // assert all scores are in (0, 2)
	}{
		{
			name: "search by name",
			tools: makeTools(
				mcp.NewTool("github_create_issue", mcp.WithDescription("Create a GitHub issue")),
				mcp.NewTool("github_list_repos", mcp.WithDescription("List GitHub repositories")),
				mcp.NewTool("slack_send_message", mcp.WithDescription("Send a Slack message")),
			),
			query:        "github",
			allowedTools: []string{"github_create_issue", "github_list_repos", "slack_send_message"},
			wantNames:    []string{"github_create_issue", "github_list_repos"},
		},
		{
			name: "search by description",
			tools: makeTools(
				mcp.NewTool("tool_a", mcp.WithDescription("Manage Kubernetes deployments")),
				mcp.NewTool("tool_b", mcp.WithDescription("Send email notifications")),
			),
			query:        "Kubernetes",
			allowedTools: []string{"tool_a", "tool_b"},
			wantNames:    []string{"tool_a"},
		},
		{
			name: "scoped to allowedTools",
			tools: makeTools(
				mcp.NewTool("file_read", mcp.WithDescription("Read files")),
				mcp.NewTool("file_write", mcp.WithDescription("Write files")),
				mcp.NewTool("file_delete", mcp.WithDescription("Delete files")),
			),
			query:        "file",
			allowedTools: []string{"file_read", "file_write"},
			wantNames:    []string{"file_read", "file_write"},
		},
		{
			name: "empty allowedTools returns no results",
			tools: makeTools(
				mcp.NewTool("tool_a", mcp.WithDescription("Tool A")),
				mcp.NewTool("tool_b", mcp.WithDescription("Tool B")),
			),
			query:        "tool",
			allowedTools: nil,
			wantNames:    nil,
		},
		{
			name: "no matches",
			tools: makeTools(
				mcp.NewTool("read_file", mcp.WithDescription("Read a file")),
			),
			query:        "nonexistent_xyz_query",
			allowedTools: []string{"read_file"},
			wantNames:    nil,
		},
		{
			name: "empty query returns no results",
			tools: makeTools(
				mcp.NewTool("read_file", mcp.WithDescription("Read a file")),
			),
			query:        "",
			allowedTools: []string{"read_file"},
			wantNames:    nil,
		},
		{
			name: "whitespace-only query returns no results",
			tools: makeTools(
				mcp.NewTool("read_file", mcp.WithDescription("Read a file")),
			),
			query:        "   ",
			allowedTools: []string{"read_file"},
			wantNames:    nil,
		},
		{
			name: "special chars - multi-word query matches",
			tools: makeTools(
				mcp.NewTool("read_file", mcp.WithDescription("Read a file from disk")),
			),
			query:        "read disk",
			allowedTools: []string{"read_file"},
			wantNonEmpty: true,
		},
		{
			name: "BM25 scores are normalized to (0, 2)",
			tools: makeTools(
				mcp.NewTool("generic_tool", mcp.WithDescription("A tool that does many things including search")),
				mcp.NewTool("search_tool", mcp.WithDescription("Search for files, search documents, search everything")),
			),
			query:        "search",
			allowedTools: []string{"generic_tool", "search_tool"},
			wantNonEmpty: true,
			checkScores:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t, nil)
			ctx := context.Background()

			require.NoError(t, store.UpsertTools(ctx, tc.tools))

			results, err := store.Search(ctx, tc.query, tc.allowedTools)
			require.NoError(t, err)

			if tc.wantNonEmpty {
				require.NotEmpty(t, results)
			} else {
				var gotNames []string
				for _, r := range results {
					gotNames = append(gotNames, r.Name)
				}
				require.ElementsMatch(t, tc.wantNames, gotNames)
			}

			if tc.checkScores {
				for _, r := range results {
					require.Greater(t, r.Score, 0.0, "score should be positive for tool %s", r.Name)
					require.Less(t, r.Score, 2.0, "score should be < 2 for tool %s", r.Name)
				}
			}
		})
	}
}

func TestSQLiteToolStore_Search_ResultsCapped(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, nil)
	ctx := context.Background()

	// Create more tools than defaultSearchLimit that all match "file"
	tools := makeTools(
		mcp.NewTool("file_read", mcp.WithDescription("Read files")),
		mcp.NewTool("file_write", mcp.WithDescription("Write files")),
		mcp.NewTool("file_delete", mcp.WithDescription("Delete files")),
		mcp.NewTool("file_copy", mcp.WithDescription("Copy files")),
		mcp.NewTool("file_move", mcp.WithDescription("Move files")),
		mcp.NewTool("file_list", mcp.WithDescription("List files")),
	)
	require.NoError(t, store.UpsertTools(ctx, tools))

	results, err := store.Search(ctx, "file", toolNames(tools))
	require.NoError(t, err)
	require.LessOrEqual(t, len(results), maxToolsToReturn,
		"results should be capped at %d", maxToolsToReturn)
}

func TestSQLiteToolStore_Close(t *testing.T) {
	t.Parallel()

	t.Run("close without embedding client", func(t *testing.T) {
		t.Parallel()
		store, err := NewSQLiteToolStore(nil)
		require.NoError(t, err)
		require.NoError(t, store.Close())
	})

	t.Run("close with embedding client", func(t *testing.T) {
		t.Parallel()
		client := newFakeEmbeddingClient(384)
		store, err := NewSQLiteToolStore(client)
		require.NoError(t, err)
		require.NoError(t, store.Close())
	})

	t.Run("double close is safe", func(t *testing.T) {
		t.Parallel()
		store, err := NewSQLiteToolStore(nil)
		require.NoError(t, err)
		require.NoError(t, store.Close())
		// sql.DB.Close() returns nil on repeated calls
		require.NoError(t, store.Close())
	})
}

func TestSQLiteToolStore_Concurrent(t *testing.T) {
	t.Parallel()
	store := newTestStore(t, nil)
	ctx := context.Background()

	initial := makeTools(
		mcp.NewTool("tool_0", mcp.WithDescription("Initial tool")),
	)
	require.NoError(t, store.UpsertTools(ctx, initial))

	const numGoroutines = 10
	var wg sync.WaitGroup

	for i := range numGoroutines {
		wg.Add(2)

		go func(idx int) {
			defer wg.Done()
			tools := makeTools(
				mcp.NewTool(
					fmt.Sprintf("concurrent_tool_%d", idx),
					mcp.WithDescription(fmt.Sprintf("Concurrent tool number %d", idx)),
				),
			)
			if err := store.UpsertTools(ctx, tools); err != nil {
				t.Errorf("concurrent upsert failed for goroutine %d: %v", idx, err)
			}
		}(i)

		go func(idx int) {
			defer wg.Done()
			// Pass a known tool name so we don't hit the empty-allowedTools shortcut
			_, err := store.Search(ctx, "tool", []string{"tool_0"})
			if err != nil {
				t.Errorf("concurrent search failed for goroutine %d: %v", idx, err)
			}
		}(i)
	}

	wg.Wait()
}

func TestSQLiteToolStore_SemanticSearch(t *testing.T) {
	t.Parallel()
	client := newFakeEmbeddingClient(384)
	store := newTestStore(t, client)
	ctx := context.Background()

	tools := makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read a file from disk")),
		mcp.NewTool("write_file", mcp.WithDescription("Write content to a file")),
		mcp.NewTool("send_email", mcp.WithDescription("Send an email message")),
		mcp.NewTool("list_repos", mcp.WithDescription("List GitHub repositories")),
	)
	require.NoError(t, store.UpsertTools(ctx, tools))

	results, err := store.searchSemantic(ctx, "read a file from disk", toolNames(tools), maxToolsToReturn)
	require.NoError(t, err)
	require.NotEmpty(t, results)

	// Results should be sorted by score ascending (lower = better)
	for i := 1; i < len(results); i++ {
		require.LessOrEqual(t, results[i-1].Score, results[i].Score,
			"results should be sorted by score ascending")
	}
}

func TestSQLiteToolStore_HybridSearch(t *testing.T) {
	t.Parallel()
	client := newFakeEmbeddingClient(384)
	store := newTestStore(t, client)
	ctx := context.Background()

	tools := makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read a file from disk")),
		mcp.NewTool("write_file", mcp.WithDescription("Write content to a file")),
		mcp.NewTool("send_email", mcp.WithDescription("Send an email message")),
	)
	require.NoError(t, store.UpsertTools(ctx, tools))

	// Hybrid search should return results from both FTS5 and semantic
	results, err := store.Search(ctx, "file", toolNames(tools))
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.LessOrEqual(t, len(results), maxToolsToReturn)
}

func TestSQLiteToolStore_ConcurrentSemantic(t *testing.T) {
	t.Parallel()
	client := newFakeEmbeddingClient(384)
	store := newTestStore(t, client)
	ctx := context.Background()

	tools := makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read a file from disk")),
		mcp.NewTool("write_file", mcp.WithDescription("Write content to a file")),
	)
	require.NoError(t, store.UpsertTools(ctx, tools))

	const numGoroutines = 10
	var wg sync.WaitGroup

	for i := range numGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := store.Search(ctx, "file", toolNames(tools))
			if err != nil {
				t.Errorf("concurrent semantic search failed for goroutine %d: %v", idx, err)
			}
		}(i)
	}

	wg.Wait()
}

func TestSQLiteToolStore_EmbeddingRoundTrip(t *testing.T) {
	t.Parallel()

	// Verify that embeddings survive encode/decode round-trip
	original := []float32{0.1, -0.2, 0.3, 0.0, -1.0, 1.0}
	encoded := encodeEmbedding(original)
	decoded := decodeEmbedding(encoded)
	require.Equal(t, original, decoded)
}

func TestSanitizeFTS5Query(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantExpr string
	}{
		{input: "simple", wantExpr: `"simple"`},
		{input: "two words", wantExpr: `"two" OR "words"`},
		{input: "hello world foo", wantExpr: `"hello" OR "world" OR "foo"`},
		{input: "", wantExpr: ""},
		{input: "   ", wantExpr: ""},

		// Special chars are NOT stripped (unlike previous behavior)
		{input: "key:value", wantExpr: `"key:value"`},
		{input: `"quoted"`, wantExpr: `"""quoted"""`},
		{input: "read*", wantExpr: `"read*"`},
		{input: "***", wantExpr: `"***"`},
		{input: "read + file", wantExpr: `"read" OR "+" OR "file"`},

		// Problematic words trigger phrase search
		{input: "name value", wantExpr: `"name value"`},
		{input: "search description fast", wantExpr: `"search description fast"`},
		{input: "read tool write", wantExpr: `"read tool write"`},
		{input: "schema definition", wantExpr: `"schema definition"`},

		// Non-problematic multi-word queries use OR
		{input: "read write", wantExpr: `"read" OR "write"`},
		{input: "github slack", wantExpr: `"github" OR "slack"`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			gotExpr := sanitizeFTS5Query(tt.input)
			require.Equal(t, tt.wantExpr, gotExpr)
		})
	}
}

func TestHybridSearchLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		total        int
		ratio        float64
		wantFTS      int
		wantSemantic int
	}{
		{name: "all FTS5", total: 8, ratio: 0.0, wantFTS: 8, wantSemantic: 0},
		{name: "all semantic", total: 8, ratio: 1.0, wantFTS: 0, wantSemantic: 8},
		{name: "even split", total: 8, ratio: 0.5, wantFTS: 4, wantSemantic: 4},
		{name: "mostly semantic", total: 10, ratio: 0.7, wantFTS: 3, wantSemantic: 7},
		{name: "mostly FTS5", total: 10, ratio: 0.3, wantFTS: 7, wantSemantic: 3},
		{name: "rounding up", total: 7, ratio: 0.5, wantFTS: 3, wantSemantic: 4},
		{name: "zero total", total: 0, ratio: 0.5, wantFTS: 0, wantSemantic: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fts, semantic := hybridSearchLimits(tc.total, tc.ratio)
			require.Equal(t, tc.wantFTS, fts, "FTS limit")
			require.Equal(t, tc.wantSemantic, semantic, "semantic limit")
			require.Equal(t, tc.total, fts+semantic, "limits must sum to total")
		})
	}
}

func TestNormalizeBM25(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rank    float64
		wantMin float64
		wantMax float64
	}{
		{name: "zero rank", rank: 0, wantMin: 1.9, wantMax: 2.1},
		{name: "rank -1", rank: -1, wantMin: 0.9, wantMax: 1.1},
		{name: "rank -9", rank: -9, wantMin: 0.19, wantMax: 0.21},
		{name: "rank -0.5", rank: -0.5, wantMin: 1.3, wantMax: 1.4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			score := normalizeBM25(tt.rank)
			require.GreaterOrEqual(t, score, tt.wantMin, "normalizeBM25(%f) = %f", tt.rank, score)
			require.LessOrEqual(t, score, tt.wantMax, "normalizeBM25(%f) = %f", tt.rank, score)
		})
	}
}

func TestMergeResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fts        []types.ToolMatch
		semantic   []types.ToolMatch
		maxResults int
		wantNames  []string // expected names in order (sorted by score ascending)
		wantScores []float64
	}{
		{
			name: "deduplicates keeping lower score",
			fts: []types.ToolMatch{
				{Name: "tool_a", Description: "A", Score: 1.0},
			},
			semantic: []types.ToolMatch{
				{Name: "tool_a", Description: "A", Score: 0.5},
			},
			maxResults: 10,
			wantNames:  []string{"tool_a"},
			wantScores: []float64{0.5},
		},
		{
			name: "deduplicates keeping fts score when lower",
			fts: []types.ToolMatch{
				{Name: "tool_a", Description: "A", Score: 0.3},
			},
			semantic: []types.ToolMatch{
				{Name: "tool_a", Description: "A", Score: 0.8},
			},
			maxResults: 10,
			wantNames:  []string{"tool_a"},
			wantScores: []float64{0.3},
		},
		{
			name: "combines unique results sorted by score",
			fts: []types.ToolMatch{
				{Name: "tool_a", Description: "A", Score: 0.5},
			},
			semantic: []types.ToolMatch{
				{Name: "tool_b", Description: "B", Score: 0.3},
			},
			maxResults: 10,
			wantNames:  []string{"tool_b", "tool_a"},
			wantScores: []float64{0.3, 0.5},
		},
		{
			name: "sorts by score ascending",
			fts: []types.ToolMatch{
				{Name: "tool_c", Description: "C", Score: 0.9},
				{Name: "tool_a", Description: "A", Score: 0.1},
			},
			semantic: []types.ToolMatch{
				{Name: "tool_b", Description: "B", Score: 0.5},
			},
			maxResults: 10,
			wantNames:  []string{"tool_a", "tool_b", "tool_c"},
			wantScores: []float64{0.1, 0.5, 0.9},
		},
		{
			name: "truncates to maxResults",
			fts: []types.ToolMatch{
				{Name: "tool_a", Description: "A", Score: 0.1},
				{Name: "tool_b", Description: "B", Score: 0.2},
				{Name: "tool_c", Description: "C", Score: 0.3},
			},
			semantic: []types.ToolMatch{
				{Name: "tool_d", Description: "D", Score: 0.4},
				{Name: "tool_e", Description: "E", Score: 0.5},
			},
			maxResults: 3,
			wantNames:  []string{"tool_a", "tool_b", "tool_c"},
			wantScores: []float64{0.1, 0.2, 0.3},
		},
		{
			name: "truncation removes worst scores",
			fts: []types.ToolMatch{
				{Name: "tool_z", Description: "Z", Score: 1.5},
				{Name: "tool_a", Description: "A", Score: 0.1},
			},
			semantic: []types.ToolMatch{
				{Name: "tool_m", Description: "M", Score: 0.7},
			},
			maxResults: 2,
			wantNames:  []string{"tool_a", "tool_m"},
			wantScores: []float64{0.1, 0.7},
		},
		{
			name:       "both empty",
			fts:        nil,
			semantic:   nil,
			maxResults: 10,
			wantNames:  nil,
			wantScores: nil,
		},
		{
			name: "dedup with sort and truncate combined",
			fts: []types.ToolMatch{
				{Name: "dup", Description: "D", Score: 0.8},
				{Name: "best", Description: "B", Score: 0.1},
				{Name: "worst", Description: "W", Score: 1.9},
			},
			semantic: []types.ToolMatch{
				{Name: "dup", Description: "D", Score: 0.3},
				{Name: "mid", Description: "M", Score: 0.5},
			},
			maxResults: 3,
			wantNames:  []string{"best", "dup", "mid"},
			wantScores: []float64{0.1, 0.3, 0.5},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			merged := mergeResults(tc.fts, tc.semantic, tc.maxResults)

			var gotNames []string
			var gotScores []float64
			for _, m := range merged {
				gotNames = append(gotNames, m.Name)
				gotScores = append(gotScores, m.Score)
			}
			require.Equal(t, tc.wantNames, gotNames)
			require.Equal(t, tc.wantScores, gotScores)
		})
	}
}

// newFakeEmbeddingClient is a test helper that creates a deterministic embedding client.
// It mirrors the FakeEmbeddingClient from the optimizer package but is local to avoid
// import cycles.
type fakeEmbeddingClient struct {
	dim int
}

func newFakeEmbeddingClient(dim int) *fakeEmbeddingClient {
	return &fakeEmbeddingClient{dim: dim}
}

func (f *fakeEmbeddingClient) Embed(_ context.Context, text string) ([]float32, error) {
	// Simple deterministic hash: use string bytes as seed
	vec := make([]float32, f.dim)
	for i := range vec {
		// Use text bytes to generate deterministic values
		b := byte(0)
		if len(text) > 0 {
			b = text[i%len(text)]
		}
		vec[i] = float32(b)/128.0 - 1.0 + float32(i)*0.001
	}
	return vec, nil
}

func (f *fakeEmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i, text := range texts {
		vec, err := f.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		result[i] = vec
	}
	return result, nil
}

func (f *fakeEmbeddingClient) Dimension() int { return f.dim }
func (*fakeEmbeddingClient) Close() error     { return nil }
