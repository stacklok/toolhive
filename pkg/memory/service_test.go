// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap/zaptest"

	"github.com/stacklok/toolhive/pkg/memory"
	"github.com/stacklok/toolhive/pkg/memory/mocks"
)

func TestService_Remember_NoConflict(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	vectors := mocks.NewMockVectorStore(ctrl)
	embedder := mocks.NewMockEmbedder(ctrl)

	emb := []float32{1, 0, 0}
	embedder.EXPECT().Embed(gomock.Any(), "test fact").Return(emb, nil)
	active := memory.EntryStatusActive
	vectors.EXPECT().Search(gomock.Any(), emb, 5, memory.VectorFilter{
		Type:   ptrOf(memory.TypeSemantic),
		Status: &active,
	}).Return(nil, nil)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	vectors.EXPECT().Upsert(gomock.Any(), gomock.Any(), emb).Return(nil)

	svc, err := memory.NewService(store, vectors, embedder, zaptest.NewLogger(t))
	require.NoError(t, err)

	result, err := svc.Remember(context.Background(), memory.RememberInput{
		Content: "test fact",
		Type:    memory.TypeSemantic,
		Author:  memory.AuthorHuman,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.MemoryID)
	require.Empty(t, result.Conflicts)
}

func TestService_Remember_ConflictDetected(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	vectors := mocks.NewMockVectorStore(ctrl)
	embedder := mocks.NewMockEmbedder(ctrl)

	emb := []float32{1, 0, 0}
	embedder.EXPECT().Embed(gomock.Any(), "conflicting fact").Return(emb, nil)
	active := memory.EntryStatusActive
	vectors.EXPECT().Search(gomock.Any(), emb, 5, memory.VectorFilter{
		Type:   ptrOf(memory.TypeSemantic),
		Status: &active,
	}).Return([]memory.ScoredID{{ID: "mem_existing", Similarity: 0.92}}, nil)
	store.EXPECT().Get(gomock.Any(), "mem_existing").Return(memory.Entry{
		ID:      "mem_existing",
		Content: "existing fact",
	}, nil)

	svc, err := memory.NewService(store, vectors, embedder, zaptest.NewLogger(t))
	require.NoError(t, err)

	result, err := svc.Remember(context.Background(), memory.RememberInput{
		Content: "conflicting fact",
		Type:    memory.TypeSemantic,
		Author:  memory.AuthorAgent,
	})
	require.NoError(t, err)
	require.Empty(t, result.MemoryID)
	require.Len(t, result.Conflicts, 1)
	require.Equal(t, "mem_existing", result.Conflicts[0].ID)
}

func TestService_Remember_Force(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	vectors := mocks.NewMockVectorStore(ctrl)
	embedder := mocks.NewMockEmbedder(ctrl)

	emb := []float32{1, 0, 0}
	embedder.EXPECT().Embed(gomock.Any(), "forced fact").Return(emb, nil)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	vectors.EXPECT().Upsert(gomock.Any(), gomock.Any(), emb).Return(nil)

	svc, err := memory.NewService(store, vectors, embedder, zaptest.NewLogger(t))
	require.NoError(t, err)

	result, err := svc.Remember(context.Background(), memory.RememberInput{
		Content: "forced fact",
		Type:    memory.TypeSemantic,
		Author:  memory.AuthorHuman,
		Force:   true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.MemoryID)
}

func TestService_Search_CompositeScoring(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := mocks.NewMockStore(ctrl)
	vectors := mocks.NewMockVectorStore(ctrl)
	embedder := mocks.NewMockEmbedder(ctrl)

	emb := []float32{1, 0, 0}
	embedder.EXPECT().Embed(gomock.Any(), "auth endpoint").Return(emb, nil)

	active := memory.EntryStatusActive
	// Two results: high raw similarity but stale/flagged vs lower similarity but fresh+trusted.
	vectors.EXPECT().Search(gomock.Any(), emb, 10, memory.VectorFilter{Status: &active}).
		Return([]memory.ScoredID{
			{ID: "stale_high", Similarity: 0.95},
			{ID: "fresh_low", Similarity: 0.80},
		}, nil)

	now := time.Now()
	flagTime := now.Add(-24 * time.Hour)

	store.EXPECT().Get(gomock.Any(), "stale_high").Return(memory.Entry{
		ID: "stale_high", Author: memory.AuthorAgent,
		TrustScore: 0.5, StalenessScore: 0.8, CreatedAt: now, FlaggedAt: &flagTime,
	}, nil)
	store.EXPECT().IncrementAccess(gomock.Any(), "stale_high").Return(nil)

	store.EXPECT().Get(gomock.Any(), "fresh_low").Return(memory.Entry{
		ID: "fresh_low", Author: memory.AuthorHuman,
		TrustScore: 1.0, StalenessScore: 0.0, CreatedAt: now,
	}, nil)
	store.EXPECT().IncrementAccess(gomock.Any(), "fresh_low").Return(nil)

	svc, err := memory.NewService(store, vectors, embedder, zaptest.NewLogger(t))
	require.NoError(t, err)

	results, err := svc.Search(context.Background(), "auth endpoint", nil, 0)
	require.NoError(t, err)
	require.Len(t, results, 2)

	// fresh_low (composite ≈ 0.80) should rank above stale_high (0.95 × 0.5 × (1-0.3×0.8) ≈ 0.361)
	require.Equal(t, "fresh_low", results[0].Entry.ID)
	require.Equal(t, "stale_high", results[1].Entry.ID)
	require.Greater(t, results[0].Similarity, results[1].Similarity)
}

func ptrOf[T any](v T) *T { return &v }
