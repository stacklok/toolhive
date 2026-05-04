// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/memory"
	memorysqlite "github.com/stacklok/toolhive/pkg/memory/sqlite"
)

func openTestDB(t *testing.T) *memorysqlite.DB {
	t.Helper()
	dir := t.TempDir()
	resolved, _ := filepath.EvalSymlinks(dir)
	db, err := memorysqlite.Open(context.Background(), filepath.Join(resolved, "memory.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMemoryStore_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := memorysqlite.NewStore(db)

	entry := memory.Entry{
		ID:        "mem_test_001",
		Type:      memory.TypeSemantic,
		Content:   "we deploy to us-east-1",
		Tags:      []string{"deployment", "infra"},
		Author:    memory.AuthorHuman,
		Source:    memory.SourceMemory,
		Status:    memory.EntryStatusActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err := store.Create(context.Background(), entry)
	require.NoError(t, err)

	got, err := store.Get(context.Background(), "mem_test_001")
	require.NoError(t, err)
	require.Equal(t, entry.ID, got.ID)
	require.Equal(t, entry.Content, got.Content)
	require.Equal(t, entry.Tags, got.Tags)
	require.Equal(t, entry.Author, got.Author)
	require.Equal(t, entry.Status, got.Status)
}

func TestMemoryStore_Update(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := memorysqlite.NewStore(db)

	entry := memory.Entry{
		ID:        "mem_test_002",
		Type:      memory.TypeSemantic,
		Content:   "old content",
		Author:    memory.AuthorHuman,
		Source:    memory.SourceMemory,
		Status:    memory.EntryStatusActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.Create(context.Background(), entry))

	err := store.Update(context.Background(), "mem_test_002", "new content", memory.AuthorHuman, "corrected")
	require.NoError(t, err)

	got, err := store.Get(context.Background(), "mem_test_002")
	require.NoError(t, err)
	require.Equal(t, "new content", got.Content)
	require.Len(t, got.History, 1)
	require.Equal(t, "old content", got.History[0].Content)
	require.Equal(t, "corrected", got.History[0].CorrectionNote)
}

func TestMemoryStore_Archive(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := memorysqlite.NewStore(db)

	entry := memory.Entry{
		ID:        "mem_test_003",
		Type:      memory.TypeProcedural,
		Content:   "check Docker health before E2E tests",
		Author:    memory.AuthorAgent,
		Source:    memory.SourceMemory,
		Status:    memory.EntryStatusActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	require.NoError(t, store.Create(context.Background(), entry))

	err := store.Archive(context.Background(), "mem_test_003", memory.ArchiveReasonConsolidated, "mem_test_consolidated")
	require.NoError(t, err)

	got, err := store.Get(context.Background(), "mem_test_003")
	require.NoError(t, err)
	require.Equal(t, memory.EntryStatusArchived, got.Status)
	require.Equal(t, "mem_test_consolidated", got.ConsolidatedInto)
	require.NotNil(t, got.ArchivedAt)
}

func TestMemoryStore_List(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := memorysqlite.NewStore(db)

	ctx := context.Background()
	for i, content := range []string{"fact A", "fact B", "procedure X"} {
		mtype := memory.TypeSemantic
		if i == 2 {
			mtype = memory.TypeProcedural
		}
		require.NoError(t, store.Create(ctx, memory.Entry{
			ID:        fmt.Sprintf("mem_list_%d", i),
			Type:      mtype,
			Content:   content,
			Author:    memory.AuthorHuman,
			Source:    memory.SourceMemory,
			Status:    memory.EntryStatusActive,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		}))
	}

	sem := memory.TypeSemantic
	results, err := store.List(ctx, memory.ListFilter{Type: &sem, Limit: 10})
	require.NoError(t, err)
	require.Len(t, results, 2)
}

func TestMemoryStore_ListTimeRange(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	store := memorysqlite.NewStore(db)
	ctx := context.Background()

	past := time.Now().Add(-2 * time.Hour)
	recent := time.Now().Add(-30 * time.Minute)

	require.NoError(t, store.Create(ctx, memory.Entry{
		ID: "mem_old", Type: memory.TypeEpisodic, Content: "old event",
		Author: memory.AuthorAgent, Source: memory.SourceMemory, Status: memory.EntryStatusActive,
		CreatedAt: past, UpdatedAt: past,
	}))
	require.NoError(t, store.Create(ctx, memory.Entry{
		ID: "mem_new", Type: memory.TypeEpisodic, Content: "recent event",
		Author: memory.AuthorAgent, Source: memory.SourceMemory, Status: memory.EntryStatusActive,
		CreatedAt: recent, UpdatedAt: recent,
	}))

	cutoff := time.Now().Add(-1 * time.Hour)
	results, err := store.List(ctx, memory.ListFilter{CreatedAfter: &cutoff})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "mem_new", results[0].ID)
}
