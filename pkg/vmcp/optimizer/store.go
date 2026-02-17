// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/similarity"
	sqlitestore "github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/sqlite_store"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

// ToolStore defines the interface for storing and searching tools.
// It is defined in the internal/types package and aliased here so that
// external consumers continue to use optimizer.ToolStore.
type ToolStore = types.ToolStore

// InMemoryToolStore implements ToolStore using an in-memory map with
// case-insensitive substring matching. Thread-safe via sync.RWMutex.
type InMemoryToolStore struct {
	mu    sync.RWMutex
	tools map[string]server.ServerTool
}

// NewInMemoryToolStore creates a new InMemoryToolStore.
func NewInMemoryToolStore() *InMemoryToolStore {
	return &InMemoryToolStore{
		tools: make(map[string]server.ServerTool),
	}
}

// UpsertTools adds or updates tools in the store.
// Tools are identified by name; duplicate names are overwritten.
func (s *InMemoryToolStore) UpsertTools(_ context.Context, tools []server.ServerTool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, tool := range tools {
		s.tools[tool.Tool.Name] = tool
	}
	return nil
}

// Close is a no-op for InMemoryToolStore since there are no external resources to release.
// It is safe to call Close multiple times.
func (*InMemoryToolStore) Close() error {
	return nil
}

// Search finds tools matching the query string using case-insensitive substring
// matching on tool name and description.
// The allowedTools parameter limits results to only tools with names in the given set.
// If allowedTools is empty, no results are returned (empty = no access).
func (s *InMemoryToolStore) Search(_ context.Context, query string, allowedTools []string) ([]ToolMatch, error) {
	if len(allowedTools) == 0 {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	searchTerm := strings.ToLower(query)

	// Build allowed set for fast lookup
	allowedSet := make(map[string]struct{}, len(allowedTools))
	for _, name := range allowedTools {
		allowedSet[name] = struct{}{}
	}

	var matches []ToolMatch
	for _, tool := range s.tools {
		if _, ok := allowedSet[tool.Tool.Name]; !ok {
			continue
		}

		nameLower := strings.ToLower(tool.Tool.Name)
		descLower := strings.ToLower(tool.Tool.Description)

		if strings.Contains(nameLower, searchTerm) || strings.Contains(descLower, searchTerm) {
			matches = append(matches, ToolMatch{
				Name:        tool.Tool.Name,
				Description: tool.Tool.Description,
				Score:       1.0, // Exact match semantics for substring matching
			})
		}
	}

	return matches, nil
}

// SQLiteStoreConfig configures the SQLite-backed ToolStore.
// When nil is passed to NewSQLiteToolStore, only FTS5 search is used.
type SQLiteStoreConfig struct {
	// EmbeddingDimension enables semantic search with deterministic fake
	// embeddings of the given dimensionality. Zero disables semantic search.
	// Mutually exclusive with EmbeddingServiceURL.
	EmbeddingDimension int

	// EmbeddingServiceURL is the full URL of the TEI embedding endpoint
	// (e.g. "http://my-embedding.ml.svc.cluster.local:8080").
	// When set, a real TEI client is created for semantic search.
	// Mutually exclusive with EmbeddingDimension.
	EmbeddingServiceURL string

	// EmbeddingServiceTimeout is the HTTP request timeout for embedding calls.
	// Zero uses the TEI client default (30s).
	EmbeddingServiceTimeout time.Duration
}

// NewSQLiteToolStore creates a new ToolStore backed by SQLite for search.
// The store uses an in-memory SQLite database with shared cache for concurrent access.
//
// Embedding client selection (mutually exclusive, checked in order):
//   - EmbeddingService set: creates a real TEI HTTP client for semantic search
//   - EmbeddingDimension > 0: creates a deterministic fake embedding client
//   - Neither: only FTS5 full-text search is used (no semantic search)
func NewSQLiteToolStore(cfg *SQLiteStoreConfig) (ToolStore, error) {
	var embClient types.EmbeddingClient
	if cfg != nil {
		switch {
		case cfg.EmbeddingServiceURL != "":
			var err error
			embClient, err = similarity.NewTEIClient(similarity.TEIClientConfig{
				BaseURL: cfg.EmbeddingServiceURL,
				Timeout: cfg.EmbeddingServiceTimeout,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create TEI embedding client: %w", err)
			}
		case cfg.EmbeddingDimension > 0:
			embClient = similarity.NewFakeEmbeddingClient(cfg.EmbeddingDimension)
		}
	}
	return sqlitestore.NewSQLiteToolStore(embClient)
}
