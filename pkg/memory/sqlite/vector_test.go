// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/memory"
	memorysqlite "github.com/stacklok/toolhive/pkg/memory/sqlite"
)

func TestVectorStore_UpsertAndSearch(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := memorysqlite.NewStore(db)
	vectors := memorysqlite.NewVectorStore(db)

	ctx := context.Background()

	entries := []struct {
		id        string
		embedding []float32
	}{
		{"vec_001", []float32{1, 0, 0}},
		{"vec_002", []float32{0.9, 0.1, 0}},
		{"vec_003", []float32{0, 0, 1}},
	}
	for _, e := range entries {
		require.NoError(t, store.Create(ctx, memory.Entry{
			ID: e.id, Type: memory.TypeSemantic, Content: "c",
			Author: memory.AuthorAgent, Source: memory.SourceMemory,
			Status: memory.EntryStatusActive,
		}))
		require.NoError(t, vectors.Upsert(ctx, e.id, e.embedding))
	}

	query := []float32{0.95, 0.05, 0}
	results, err := vectors.Search(ctx, query, 2, memory.VectorFilter{})
	require.NoError(t, err)
	require.Len(t, results, 2)

	ids := []string{results[0].ID, results[1].ID}
	require.Contains(t, ids, "vec_001")
	require.Contains(t, ids, "vec_002")
	require.NotContains(t, ids, "vec_003")

	require.GreaterOrEqual(t, results[0].Similarity, results[1].Similarity)
}

func TestVectorStore_Delete(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := memorysqlite.NewStore(db)
	vectors := memorysqlite.NewVectorStore(db)

	ctx := context.Background()
	require.NoError(t, store.Create(ctx, memory.Entry{
		ID: "vec_del", Type: memory.TypeSemantic, Content: "c",
		Author: memory.AuthorAgent, Source: memory.SourceMemory,
		Status: memory.EntryStatusActive,
	}))
	require.NoError(t, vectors.Upsert(ctx, "vec_del", []float32{1, 0, 0}))
	require.NoError(t, vectors.Delete(ctx, "vec_del"))

	results, err := vectors.Search(ctx, []float32{1, 0, 0}, 5, memory.VectorFilter{})
	require.NoError(t, err)
	for _, r := range results {
		require.NotEqual(t, "vec_del", r.ID)
	}
}
