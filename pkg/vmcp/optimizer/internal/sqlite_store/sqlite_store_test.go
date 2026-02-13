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
)

// testDBCounter ensures each test gets a unique in-memory database.
var testDBCounter atomic.Int64

func newTestStore(t *testing.T) sqliteToolStore {
	t.Helper()
	id := testDBCounter.Add(1)
	store, err := newSQLiteToolStore(fmt.Sprintf("file:testdb_%d?mode=memory&cache=shared", id))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
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

	store, err := NewSQLiteToolStore()
	require.NoError(t, err)
	require.NotNil(t, store)
	require.NotNil(t, store.db)
	require.NoError(t, store.Close())
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
			store := newTestStore(t)
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

func TestSQLiteToolStore_Search(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tools        []server.ServerTool
		query        string
		allowedTools []string
		wantNames    []string
		wantNonEmpty bool // just assert results are non-empty (when exact names vary)
		checkScores  bool // assert all scores are in (0, 1]
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
			name: "BM25 scores are normalized to (0, 1]",
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
			store := newTestStore(t)
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
					require.LessOrEqual(t, r.Score, 1.0, "score should be <= 1 for tool %s", r.Name)
				}
			}
		})
	}
}

func TestSQLiteToolStore_Close(t *testing.T) {
	t.Parallel()

	t.Run("close is safe", func(t *testing.T) {
		t.Parallel()
		store, err := NewSQLiteToolStore()
		require.NoError(t, err)
		require.NoError(t, store.Close())
	})

	t.Run("double close is safe", func(t *testing.T) {
		t.Parallel()
		store, err := NewSQLiteToolStore()
		require.NoError(t, err)
		require.NoError(t, store.Close())
		// sql.DB.Close() returns nil on repeated calls
		require.NoError(t, store.Close())
	})
}

func TestSQLiteToolStore_Concurrent(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
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

func TestNormalizeBM25(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rank    float64
		wantMin float64
		wantMax float64
	}{
		{rank: 0, wantMin: 0.9, wantMax: 1.1},
		{rank: -1, wantMin: 0.4, wantMax: 0.6},
		{rank: -9, wantMin: 0.09, wantMax: 0.11},
		{rank: -0.5, wantMin: 0.6, wantMax: 0.7},
	}

	for _, tt := range tests {
		score := normalizeBM25(tt.rank)
		require.GreaterOrEqual(t, score, tt.wantMin, "normalizeBM25(%f) = %f", tt.rank, score)
		require.LessOrEqual(t, score, tt.wantMax, "normalizeBM25(%f) = %f", tt.rank, score)
	}
}
