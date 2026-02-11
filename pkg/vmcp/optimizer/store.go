// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ToolStore defines the interface for storing and searching tools.
// Implementations may use in-memory maps, SQLite FTS5, or other backends.
//
// A ToolStore is shared across multiple optimizer instances (one per session)
// and is accessed concurrently. Implementations must be thread-safe.
type ToolStore interface {
	// UpsertTools adds or updates tools in the store.
	// Tools are identified by name; duplicate names are overwritten.
	UpsertTools(ctx context.Context, tools []server.ServerTool) error

	// Search finds tools matching the query string.
	// The scope parameter limits results to only tools with names in the given set.
	// If scope is empty, all tools are searched.
	// Returns matches ranked by relevance.
	Search(ctx context.Context, query string, scope []string) ([]ToolMatch, error)
}

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

// Search finds tools matching the query string using case-insensitive substring
// matching on tool name and description.
// The scope parameter limits results to only tools with names in the given set.
// If scope is empty, all tools are searched.
func (s *InMemoryToolStore) Search(_ context.Context, query string, scope []string) ([]ToolMatch, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	searchTerm := strings.ToLower(query)

	// Build scope set for fast lookup
	scopeSet := make(map[string]struct{}, len(scope))
	for _, name := range scope {
		scopeSet[name] = struct{}{}
	}

	var matches []ToolMatch
	for _, tool := range s.tools {
		// If scope is specified, skip tools not in scope
		if len(scopeSet) > 0 {
			if _, ok := scopeSet[tool.Tool.Name]; !ok {
				continue
			}
		}

		nameLower := strings.ToLower(tool.Tool.Name)
		descLower := strings.ToLower(tool.Tool.Description)

		if strings.Contains(nameLower, searchTerm) || strings.Contains(descLower, searchTerm) {
			schema, err := getToolSchema(tool.Tool)
			if err != nil {
				return nil, err
			}
			matches = append(matches, ToolMatch{
				Name:        tool.Tool.Name,
				Description: tool.Tool.Description,
				InputSchema: schema,
				Score:       1.0, // Exact match semantics for substring matching
			})
		}
	}

	return matches, nil
}

// getToolSchema returns the input schema for a tool.
// Prefers RawInputSchema if set, otherwise marshals InputSchema.
func getToolSchema(tool mcp.Tool) (json.RawMessage, error) {
	if len(tool.RawInputSchema) > 0 {
		return tool.RawInputSchema, nil
	}

	// Fall back to InputSchema
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal input schema for tool %s: %w", tool.Name, err)
	}
	return data, nil
}
