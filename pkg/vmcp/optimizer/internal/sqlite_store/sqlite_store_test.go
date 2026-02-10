// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlitestore

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *SQLiteToolStore {
	t.Helper()
	store, err := NewSQLiteToolStore()
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

func toolNames(tools []server.ServerTool) []string {
	result := make([]string, len(tools))
	for i, tool := range tools {
		result[i] = tool.Tool.Name
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
	store := newTestStore(t)
	ctx := context.Background()

	tools := makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read a file from disk")),
		mcp.NewTool("write_file", mcp.WithDescription("Write content to a file")),
	)

	require.NoError(t, store.UpsertTools(ctx, tools))

	// Verify tools are stored by searching for them
	results, err := store.Search(ctx, "file", toolNames(tools))
	require.NoError(t, err)
	require.Len(t, results, 2)
}

func TestSQLiteToolStore_UpsertTools_Overwrite(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	// Insert initial tool
	tools := makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read a file")),
	)
	require.NoError(t, store.UpsertTools(ctx, tools))

	// Overwrite with updated description
	updated := makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read any file from the filesystem")),
	)
	require.NoError(t, store.UpsertTools(ctx, updated))

	// Verify updated description is returned
	results, err := store.Search(ctx, "filesystem", toolNames(updated))
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "Read any file from the filesystem", results[0].Description)
}

func TestSQLiteToolStore_Search(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tools        []server.ServerTool
		query        string
		allowedTools []string
		wantNames    []string
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newTestStore(t)
			ctx := context.Background()

			require.NoError(t, store.UpsertTools(ctx, tc.tools))

			results, err := store.Search(ctx, tc.query, tc.allowedTools)
			require.NoError(t, err)

			var gotNames []string
			for _, r := range results {
				gotNames = append(gotNames, r.Name)
			}
			require.ElementsMatch(t, tc.wantNames, gotNames)
		})
	}
}

func TestSQLiteToolStore_Search_BM25Ranking(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	tools := makeTools(
		mcp.NewTool("generic_tool", mcp.WithDescription("A tool that does many things including search")),
		mcp.NewTool("search_tool", mcp.WithDescription("Search for files, search documents, search everything")),
	)
	require.NoError(t, store.UpsertTools(ctx, tools))

	results, err := store.Search(ctx, "search", toolNames(tools))
	require.NoError(t, err)
	require.NotEmpty(t, results)

	for _, r := range results {
		require.Greater(t, r.Score, 0.0, "score should be positive for tool %s", r.Name)
		require.LessOrEqual(t, r.Score, 1.0, "score should be <= 1 for tool %s", r.Name)
	}
}

func TestSQLiteToolStore_Search_SpecialChars(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	tools := makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read a file from disk")),
	)
	require.NoError(t, store.UpsertTools(ctx, tools))
	allowed := toolNames(tools)

	// Queries containing the word "read" should still match (special chars
	// are passed through to FTS5 as part of the quoted phrase/term).
	matchQueries := []string{
		"read",
		"read file",
		"read disk",
	}
	for _, q := range matchQueries {
		results, err := store.Search(ctx, q, allowed)
		require.NoError(t, err, "search failed for query %q", q)
		require.NotEmpty(t, results, "expected at least 1 result for query %q", q)
	}

	// Queries with only special chars that produce no real words should
	// return empty (no LIKE fallback).
	emptyQueries := []string{
		"",
		"   ",
	}
	for _, q := range emptyQueries {
		results, err := store.Search(ctx, q, allowed)
		require.NoError(t, err, "search should not error for query %q", q)
		require.Empty(t, results, "expected empty results for query %q", q)
	}
}

func TestSQLiteToolStore_Search_EmptyQuery(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	tools := makeTools(
		mcp.NewTool("read_file", mcp.WithDescription("Read a file")),
	)
	require.NoError(t, store.UpsertTools(ctx, tools))

	// Empty query returns no results (no LIKE fallback)
	results, err := store.Search(ctx, "", toolNames(tools))
	require.NoError(t, err)
	require.Empty(t, results)
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
		expected string
	}{
		{input: "simple", expected: `"simple"`},
		{input: "two words", expected: `"two" OR "words"`},
		{input: "hello world foo", expected: `"hello" OR "world" OR "foo"`},
		{input: "", expected: ""},
		{input: "   ", expected: ""},

		// Special chars are NOT stripped (unlike previous behavior)
		{input: "key:value", expected: `"key:value"`},
		{input: `"quoted"`, expected: `"""quoted"""`},
		{input: "read*", expected: `"read*"`},
		{input: "***", expected: `"***"`},
		{input: "read + file", expected: `"read" OR "+" OR "file"`},

		// Problematic words trigger phrase search
		{input: "name value", expected: `"name value"`},
		{input: "search description fast", expected: `"search description fast"`},
		{input: "read tool write", expected: `"read tool write"`},
		{input: "schema definition", expected: `"schema definition"`},

		// Non-problematic multi-word queries use OR
		{input: "read write", expected: `"read" OR "write"`},
		{input: "github slack", expected: `"github" OR "slack"`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := sanitizeFTS5Query(tt.input)
			require.Equal(t, tt.expected, got)
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
