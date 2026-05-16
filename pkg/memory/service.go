// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	conflictSimilarityThreshold = float32(0.85)
	defaultConflictTopK         = 5
	// stalenessSearchPenaltyWeight controls how much staleness reduces ranking score.
	stalenessSearchPenaltyWeight = float32(0.3)
)

// Service orchestrates Store, VectorStore, and Embedder to provide
// the full memory lifecycle including conflict detection and scoring.
type Service struct {
	store    Store
	vectors  VectorStore
	embedder Embedder
	log      *zap.Logger
}

// NewService constructs a Service. All dependencies are required.
//
// The Store provides durable persistence for memory entries.
// The VectorStore enables semantic similarity search over entry embeddings.
// The Embedder converts text to vectors; the caller is responsible for
// ensuring the same Embedder is used consistently — switching embedders
// will invalidate stored vectors.
func NewService(store Store, vectors VectorStore, embedder Embedder, log *zap.Logger) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if vectors == nil {
		return nil, fmt.Errorf("vector store is required")
	}
	if embedder == nil {
		return nil, fmt.Errorf("embedder is required")
	}
	if log == nil {
		return nil, fmt.Errorf("logger is required")
	}
	return &Service{store: store, vectors: vectors, embedder: embedder, log: log}, nil
}

// RememberInput is the input to Service.Remember.
type RememberInput struct {
	Content   string
	Type      Type
	Tags      []string
	Author    AuthorType
	AgentID   string
	SessionID string
	Source    SourceType
	SkillRef  string
	TTLDays   *int
	// Force bypasses conflict detection and writes unconditionally.
	Force bool
}

// RememberResult is returned by Service.Remember.
// If Conflicts is non-empty, MemoryID is empty and the write was not performed.
type RememberResult struct {
	MemoryID  string
	Conflicts []ConflictResult
}

// Remember embeds content, checks for conflicts, and writes the entry if none found.
// When Force is true the conflict check is skipped entirely.
func (s *Service) Remember(ctx context.Context, in RememberInput) (*RememberResult, error) {
	embedding, err := s.embedder.Embed(ctx, in.Content)
	if err != nil {
		return nil, fmt.Errorf("embedding content: %w", err)
	}

	if !in.Force {
		conflicts, err := s.detectConflicts(ctx, embedding, in.Type)
		if err != nil {
			return nil, fmt.Errorf("detecting conflicts: %w", err)
		}
		if len(conflicts) > 0 {
			return &RememberResult{Conflicts: conflicts}, nil
		}
	}

	id := "mem_" + uuid.New().String()
	now := time.Now().UTC()
	entry := Entry{
		ID:        id,
		Type:      in.Type,
		Content:   in.Content,
		Tags:      in.Tags,
		Author:    in.Author,
		AgentID:   in.AgentID,
		SessionID: in.SessionID,
		Source:    sourceOrDefault(in.Source),
		SkillRef:  in.SkillRef,
		Status:    EntryStatusActive,
		TTLDays:   in.TTLDays,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if in.TTLDays != nil {
		t := now.AddDate(0, 0, *in.TTLDays)
		entry.ExpiresAt = &t
	}
	entry.TrustScore = ComputeTrustScore(entry)
	entry.StalenessScore = ComputeStalenessScore(entry)

	if err := s.store.Create(ctx, entry); err != nil {
		return nil, fmt.Errorf("creating entry: %w", err)
	}
	if err := s.vectors.Upsert(ctx, id, embedding); err != nil {
		// Best-effort rollback: remove the orphaned store entry.
		_ = s.store.Delete(ctx, id)
		return nil, fmt.Errorf("upserting vector: %w", err)
	}

	return &RememberResult{MemoryID: id}, nil
}

// Search embeds the query, searches the vector store, fetches entries, and
// increments access counts.
func (s *Service) Search(ctx context.Context, query string, memType *Type, topK int) ([]ScoredEntry, error) {
	if topK <= 0 {
		topK = 10
	}
	embedding, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}

	active := EntryStatusActive
	ids, err := s.vectors.Search(ctx, embedding, topK, VectorFilter{Type: memType, Status: &active})
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	var results []ScoredEntry
	for _, scored := range ids {
		entry, err := s.store.Get(ctx, scored.ID)
		if err != nil {
			s.log.Warn("skipping missing entry", zap.String("id", scored.ID), zap.Error(err))
			continue
		}
		// Increment access count; failure is non-fatal.
		_ = s.store.IncrementAccess(ctx, scored.ID)
		// Composite score: boost by trust, penalise by staleness.
		composite := scored.Similarity * entry.TrustScore * (1 - stalenessSearchPenaltyWeight*entry.StalenessScore)
		results = append(results, ScoredEntry{Entry: entry, Similarity: composite})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Similarity > results[j].Similarity
	})
	return results, nil
}

// detectConflicts returns any existing entries whose embedding similarity to
// the candidate exceeds conflictSimilarityThreshold.
func (s *Service) detectConflicts(ctx context.Context, embedding []float32, memType Type) ([]ConflictResult, error) {
	active := EntryStatusActive
	candidates, err := s.vectors.Search(ctx, embedding, defaultConflictTopK, VectorFilter{
		Type:   &memType,
		Status: &active,
	})
	if err != nil {
		return nil, err
	}

	var conflicts []ConflictResult
	for _, c := range candidates {
		if c.Similarity < conflictSimilarityThreshold {
			continue
		}
		entry, err := s.store.Get(ctx, c.ID)
		if err != nil {
			// Skip entries that can't be fetched; they may have been deleted concurrently.
			s.log.Warn("skipping conflict candidate", zap.String("id", c.ID), zap.Error(err))
			continue
		}
		conflicts = append(conflicts, ConflictResult{
			ID:         entry.ID,
			Content:    entry.Content,
			Similarity: c.Similarity,
			TrustScore: entry.TrustScore,
		})
	}
	return conflicts, nil
}

func sourceOrDefault(s SourceType) SourceType {
	if s == "" {
		return SourceMemory
	}
	return s
}
