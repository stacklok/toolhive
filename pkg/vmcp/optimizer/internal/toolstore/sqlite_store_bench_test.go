// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// TODO: These benchmarks are a quality/performance practice rather than
// functional tests of sqlite_store. Consider moving them to a dedicated
// benchmarking repo or similar in the future.

package toolstore

import (
	"context"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

const benchToolCount = 1000

func newBenchStore(b *testing.B, embeddingClient types.EmbeddingClient) sqliteToolStore {
	b.Helper()
	id := testDBCounter.Add(1)
	store, err := newSQLiteToolStore(fmt.Sprintf("file:benchdb_%d?mode=memory&cache=shared", id), embeddingClient, nil)
	require.NoError(b, err)
	b.Cleanup(func() { _ = store.Close() })
	return store
}

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
	store := newBenchStore(b, nil)

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
	store := newBenchStore(b, client)

	ctx := context.Background()
	tools, names := generateTools()
	require.NoError(b, store.UpsertTools(ctx, tools))

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = store.searchSemantic(ctx, "find a task handler", names, DefaultMaxToolsToReturn)
	}
}

func BenchmarkSearch_Hybrid_1000Tools(b *testing.B) {
	client := newFakeEmbeddingClient(384)
	store := newBenchStore(b, client)

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
	store := newBenchStore(b, client)

	ctx := context.Background()
	tools, names := generateTools()
	require.NoError(b, store.UpsertTools(ctx, tools))

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = store.searchSemantic(ctx, "find a task handler", names, DefaultMaxToolsToReturn)
	}
}
