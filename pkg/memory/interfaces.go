// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package memory

import "context"

//go:generate mockgen -destination mocks/mock_store.go -package mocks github.com/stacklok/toolhive/pkg/memory Store
//go:generate mockgen -destination mocks/mock_vector.go -package mocks github.com/stacklok/toolhive/pkg/memory VectorStore
//go:generate mockgen -destination mocks/mock_embedder.go -package mocks github.com/stacklok/toolhive/pkg/memory Embedder

// Store is the structured persistence layer for memory entries.
// It handles CRUD, lifecycle transitions, and score updates.
// Implementations must be safe for concurrent use.
type Store interface {
	Create(ctx context.Context, entry Entry) error
	Get(ctx context.Context, id string) (Entry, error)
	// Update replaces the content of an existing entry and appends the
	// previous content to History. The embedding must be recomputed by
	// the caller (Service) after this call succeeds.
	Update(ctx context.Context, id string, content string, author AuthorType, correctionNote string) error
	Flag(ctx context.Context, id string, reason string) error
	Unflag(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, filter ListFilter) ([]Entry, error)
	Archive(ctx context.Context, id string, reason ArchiveReason, ref string) error
	IncrementAccess(ctx context.Context, id string) error
	UpdateScores(ctx context.Context, id string, trustScore, stalenessScore float32) error
	// ListExpired returns all active entries whose ExpiresAt is in the past.
	ListExpired(ctx context.Context) ([]Entry, error)
	// ListActive returns all non-archived entries for score recomputation.
	ListActive(ctx context.Context) ([]Entry, error)
}

// VectorStore stores and queries embedding vectors for memory entries.
// Implementations must be safe for concurrent use.
type VectorStore interface {
	// Upsert stores or replaces the embedding for the given entry ID.
	Upsert(ctx context.Context, id string, embedding []float32) error
	// Search returns the topK entries most similar to query, restricted by filter.
	Search(ctx context.Context, query []float32, topK int, filter VectorFilter) ([]ScoredID, error)
	Delete(ctx context.Context, id string) error
}

// Embedder converts text to a fixed-dimension float32 vector.
// Implementations must be safe for concurrent use.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	// Dimensions returns the fixed vector length produced by this embedder.
	Dimensions() int
}
