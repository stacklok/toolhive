// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package main_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/stacklok/toolhive/pkg/memory"
	memorysqlite "github.com/stacklok/toolhive/pkg/memory/sqlite"
)

// fakeEmbedder returns a deterministic embedding for testing without a real model server.
type fakeEmbedder struct{}

func (*fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	v := []float32{0, 0, 0}
	for i, c := range text {
		if i >= 3 {
			break
		}
		v[i] = float32(c) / 128.0
	}
	return v, nil
}

func (*fakeEmbedder) Dimensions() int { return 3 }

func TestIntegration_RememberSearchForget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	db, err := memorysqlite.Open(context.Background(), filepath.Join(resolved, "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	store := memorysqlite.NewStore(db)
	vectors := memorysqlite.NewVectorStore(db)
	svc, err := memory.NewService(store, vectors, &fakeEmbedder{}, zaptest.NewLogger(t))
	require.NoError(t, err)

	ctx := context.Background()

	r, err := svc.Remember(ctx, memory.RememberInput{
		Content: "deploy to us-east-1",
		Type:    memory.TypeSemantic,
		Author:  memory.AuthorHuman,
	})
	require.NoError(t, err)
	require.NotEmpty(t, r.MemoryID)
	require.Empty(t, r.Conflicts)

	results, err := svc.Search(ctx, "deploy to us-east-1", nil, 5)
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Equal(t, "deploy to us-east-1", results[0].Entry.Content)

	entry, err := store.Get(ctx, r.MemoryID)
	require.NoError(t, err)
	require.Equal(t, 1, entry.AccessCount)

	require.NoError(t, store.Delete(ctx, r.MemoryID))
	_, err = store.Get(ctx, r.MemoryID)
	require.ErrorIs(t, err, memory.ErrNotFound)
}

func TestIntegration_ConflictDetection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	db, err := memorysqlite.Open(context.Background(), filepath.Join(resolved, "test2.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	store := memorysqlite.NewStore(db)
	vectors := memorysqlite.NewVectorStore(db)
	svc, err := memory.NewService(store, vectors, &fakeEmbedder{}, zaptest.NewLogger(t))
	require.NoError(t, err)

	ctx := context.Background()

	r1, err := svc.Remember(ctx, memory.RememberInput{
		Content: "auth port 8080",
		Type:    memory.TypeSemantic,
		Author:  memory.AuthorHuman,
	})
	require.NoError(t, err)
	require.NotEmpty(t, r1.MemoryID)

	// fakeEmbedder hashes first 3 chars — "aut" maps to same vector for both,
	// so "auth port 9090" will have cosine similarity 1.0 with "auth port 8080".
	r2, err := svc.Remember(ctx, memory.RememberInput{
		Content: "auth port 9090",
		Type:    memory.TypeSemantic,
		Author:  memory.AuthorAgent,
	})
	require.NoError(t, err)
	require.Empty(t, r2.MemoryID)
	require.NotEmpty(t, r2.Conflicts)

	r3, err := svc.Remember(ctx, memory.RememberInput{
		Content: "auth port 9090",
		Type:    memory.TypeSemantic,
		Author:  memory.AuthorHuman,
		Force:   true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, r3.MemoryID)
}
