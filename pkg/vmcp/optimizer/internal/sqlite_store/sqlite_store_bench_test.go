// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// TODO: These benchmarks are a quality/performance practice rather than
// functional tests of sqlite_store. Consider moving them to a dedicated
// benchmarking repo or similar in the future.

package sqlitestore

import (
	"context"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

const benchToolCount = 1000

func generateTools() ([]server.ServerTool, []string) {
	tools := make([]server.ServerTool, benchToolCount)
	names := make([]string, benchToolCount)
	for i := range benchToolCount {
		name := fmt.Sprintf("tool_%04d", i)
		names[i] = name
		tools[i] = server.ServerTool{
			Tool: mcp.Tool{
				Name:        name,
				Description: fmt.Sprintf("This is tool number %d which does task %d and handles operation %d", i, i%50, i%20),
			},
		}
	}
	return tools, names
}

func BenchmarkSearch_FTS5Only_1000Tools(b *testing.B) {
	store, err := NewSQLiteToolStore(nil)
	require.NoError(b, err)
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	tools, names := generateTools()
	require.NoError(b, store.UpsertTools(ctx, tools))

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = store.Search(ctx, "task operation", names)
	}
}

func BenchmarkSearch_Semantic_1000Tools_384Dim(b *testing.B) {
	client := newFakeEmbeddingClient(384)
	store, err := newSQLiteToolStore("file:bench_sem384?mode=memory&cache=shared", client)
	require.NoError(b, err)
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	tools, names := generateTools()
	require.NoError(b, store.UpsertTools(ctx, tools))

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = store.searchSemantic(ctx, "find a task handler", names, maxToolsToReturn)
	}
}

func BenchmarkSearch_Hybrid_1000Tools(b *testing.B) {
	client := newFakeEmbeddingClient(384)
	store, err := NewSQLiteToolStore(client)
	require.NoError(b, err)
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	tools, names := generateTools()
	require.NoError(b, store.UpsertTools(ctx, tools))

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = store.Search(ctx, "task operation", names)
	}
}

func BenchmarkSearch_Semantic_1000Tools_768Dim(b *testing.B) {
	client := newFakeEmbeddingClient(768)
	store, err := newSQLiteToolStore("file:bench_sem768?mode=memory&cache=shared", client)
	require.NoError(b, err)
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	tools, names := generateTools()
	require.NoError(b, store.UpsertTools(ctx, tools))

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = store.searchSemantic(ctx, "find a task handler", names, maxToolsToReturn)
	}
}
