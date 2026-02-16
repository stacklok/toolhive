// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package types defines shared types used across optimizer sub-packages.
package types

import (
	"context"

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
	// The allowedTools parameter limits results to only tools with names in the given set.
	// If allowedTools is empty, no results are returned (empty = no access).
	// Returns matches ranked by relevance.
	Search(ctx context.Context, query string, allowedTools []string) ([]ToolMatch, error)

	// Close releases any resources held by the store (e.g., database connections).
	// For in-memory stores this is a no-op.
	// It is safe to call Close multiple times.
	Close() error
}

// ToolMatch represents a tool that matched the search criteria.
type ToolMatch struct {
	// Name is the unique identifier of the tool.
	Name string `json:"name"`

	// Description is the human-readable description of the tool.
	Description string `json:"description"`

	// Score is a distance metric indicating how well this tool matches.
	// Lower values indicate better matches (0 = identical, 2 = opposite).
	Score float64 `json:"score"`
}

// EmbeddingClient generates vector embeddings from text.
// Implementations may use local models, remote APIs, or deterministic fakes.
// The dimensionality of embeddings can be inferred from the returned vectors.
type EmbeddingClient interface {
	// Embed returns a vector embedding for the given text.
	Embed(ctx context.Context, text string) ([]float32, error)

	// EmbedBatch returns vector embeddings for multiple texts.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)

	// Close releases any resources held by the client.
	Close() error
}
