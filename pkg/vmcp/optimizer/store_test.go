// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

func TestInMemoryToolStore_UpsertTools(t *testing.T) {
	t.Parallel()

	t.Run("adds tools", func(t *testing.T) {
		t.Parallel()

		store := NewInMemoryToolStore()
		tools := []server.ServerTool{
			{Tool: mcp.Tool{Name: "tool_a", Description: "Tool A"}},
			{Tool: mcp.Tool{Name: "tool_b", Description: "Tool B"}},
		}

		err := store.UpsertTools(context.Background(), tools)
		require.NoError(t, err)

		// Verify tools are searchable
		matches, err := store.Search(context.Background(), "tool", nil)
		require.NoError(t, err)
		require.Len(t, matches, 2)
	})

	t.Run("overwrites existing tools", func(t *testing.T) {
		t.Parallel()

		store := NewInMemoryToolStore()

		// Insert initial tool
		err := store.UpsertTools(context.Background(), []server.ServerTool{
			{Tool: mcp.Tool{Name: "tool_a", Description: "Original description"}},
		})
		require.NoError(t, err)

		// Overwrite with new description
		err = store.UpsertTools(context.Background(), []server.ServerTool{
			{Tool: mcp.Tool{Name: "tool_a", Description: "Updated description"}},
		})
		require.NoError(t, err)

		// Search by new description
		matches, err := store.Search(context.Background(), "Updated", nil)
		require.NoError(t, err)
		require.Len(t, matches, 1)
		require.Equal(t, "Updated description", matches[0].Description)

		// Old description should not match
		matches, err = store.Search(context.Background(), "Original", nil)
		require.NoError(t, err)
		require.Empty(t, matches)
	})
}

func TestInMemoryToolStore_Search(t *testing.T) {
	t.Parallel()

	store := NewInMemoryToolStore()
	tools := []server.ServerTool{
		{Tool: mcp.Tool{Name: "fetch_url", Description: "Fetch content from a URL"}},
		{Tool: mcp.Tool{Name: "read_file", Description: "Read a file from the filesystem"}},
		{Tool: mcp.Tool{Name: "write_file", Description: "Write content to a file"}},
		{Tool: mcp.Tool{Name: "list_dir", Description: "List directory contents"}},
	}
	err := store.UpsertTools(context.Background(), tools)
	require.NoError(t, err)

	t.Run("finds by name substring", func(t *testing.T) {
		t.Parallel()

		matches, err := store.Search(context.Background(), "fetch", nil)
		require.NoError(t, err)
		require.Len(t, matches, 1)
		require.Equal(t, "fetch_url", matches[0].Name)
	})

	t.Run("finds by description substring", func(t *testing.T) {
		t.Parallel()

		matches, err := store.Search(context.Background(), "filesystem", nil)
		require.NoError(t, err)
		require.Len(t, matches, 1)
		require.Equal(t, "read_file", matches[0].Name)
	})

	t.Run("case insensitive", func(t *testing.T) {
		t.Parallel()

		matches, err := store.Search(context.Background(), "FETCH", nil)
		require.NoError(t, err)
		require.Len(t, matches, 1)
		require.Equal(t, "fetch_url", matches[0].Name)
	})

	t.Run("respects scope parameter", func(t *testing.T) {
		t.Parallel()

		// "file" matches both read_file and write_file by name/description,
		// but scope limits to only read_file
		matches, err := store.Search(context.Background(), "file", []string{"read_file"})
		require.NoError(t, err)
		require.Len(t, matches, 1)
		require.Equal(t, "read_file", matches[0].Name)
	})

	t.Run("empty scope returns all matches", func(t *testing.T) {
		t.Parallel()

		matches, err := store.Search(context.Background(), "file", nil)
		require.NoError(t, err)
		require.Len(t, matches, 2)

		var names []string
		for _, m := range matches {
			names = append(names, m.Name)
		}
		require.ElementsMatch(t, []string{"read_file", "write_file"}, names)
	})

	t.Run("no matches returns empty slice", func(t *testing.T) {
		t.Parallel()

		matches, err := store.Search(context.Background(), "nonexistent", nil)
		require.NoError(t, err)
		require.Empty(t, matches)
	})

	t.Run("score is 1.0 for all matches", func(t *testing.T) {
		t.Parallel()

		matches, err := store.Search(context.Background(), "file", nil)
		require.NoError(t, err)
		for _, m := range matches {
			require.Equal(t, 1.0, m.Score)
		}
	})
}

func TestInMemoryToolStore_Close(t *testing.T) {
	t.Parallel()

	t.Run("close is safe", func(t *testing.T) {
		t.Parallel()

		store := NewInMemoryToolStore()
		err := store.Close()
		require.NoError(t, err)
	})

	t.Run("close is safe to call multiple times", func(t *testing.T) {
		t.Parallel()

		store := NewInMemoryToolStore()
		require.NoError(t, store.Close())
		require.NoError(t, store.Close())
		require.NoError(t, store.Close())
	})
}

func TestInMemoryToolStore_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	store := NewInMemoryToolStore()
	ctx := context.Background()

	// Seed with initial tools
	err := store.UpsertTools(ctx, []server.ServerTool{
		{Tool: mcp.Tool{Name: "initial_tool", Description: "An initial tool"}},
	})
	require.NoError(t, err)

	const goroutines = 10
	var wg sync.WaitGroup
	wg.Add(goroutines * 2) // goroutines for upsert + goroutines for search

	// Concurrent upserts
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			tools := []server.ServerTool{
				{Tool: mcp.Tool{
					Name:        "concurrent_tool",
					Description: "Updated by goroutine",
				}},
			}
			_ = idx
			upsertErr := store.UpsertTools(ctx, tools)
			require.NoError(t, upsertErr)
		}(i)
	}

	// Concurrent searches
	for range goroutines {
		go func() {
			defer wg.Done()
			_, searchErr := store.Search(ctx, "tool", nil)
			require.NoError(t, searchErr)
		}()
	}

	wg.Wait()
}
