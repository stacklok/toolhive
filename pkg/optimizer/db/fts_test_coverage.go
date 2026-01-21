// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/optimizer/models"
)

// stringPtr returns a pointer to the given string
func stringPtr(s string) *string {
	return &s
}

// TestFTSDatabase_GetTotalToolTokens tests token counting
func TestFTSDatabase_GetTotalToolTokens(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	config := &FTSConfig{
		DBPath: ":memory:",
	}

	ftsDB, err := NewFTSDatabase(config)
	require.NoError(t, err)
	defer func() { _ = ftsDB.Close() }()

	// Initially should be 0
	totalTokens, err := ftsDB.GetTotalToolTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, totalTokens)

	// Add a tool
	tool := &models.BackendTool{
		ID:          "tool-1",
		MCPServerID: "server-1",
		ToolName:    "test_tool",
		Description: stringPtr("Test tool"),
		TokenCount:  100,
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}

	err = ftsDB.UpsertToolMeta(ctx, tool, "TestServer")
	require.NoError(t, err)

	// Should now have tokens
	totalTokens, err = ftsDB.GetTotalToolTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, 100, totalTokens)

	// Add another tool
	tool2 := &models.BackendTool{
		ID:          "tool-2",
		MCPServerID: "server-1",
		ToolName:    "test_tool2",
		Description: stringPtr("Test tool 2"),
		TokenCount:  50,
		CreatedAt:   time.Now(),
		LastUpdated: time.Now(),
	}

	err = ftsDB.UpsertToolMeta(ctx, tool2, "TestServer")
	require.NoError(t, err)

	// Should sum tokens
	totalTokens, err = ftsDB.GetTotalToolTokens(ctx)
	require.NoError(t, err)
	assert.Equal(t, 150, totalTokens)
}

// TestSanitizeFTS5Query tests query sanitization
func TestSanitizeFTS5Query(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "remove quotes",
			input:    `"test query"`,
			expected: "test query",
		},
		{
			name:     "remove wildcards",
			input:    "test*query",
			expected: "test query",
		},
		{
			name:     "remove parentheses",
			input:    "test(query)",
			expected: "test query",
		},
		{
			name:     "remove multiple spaces",
			input:    "test    query",
			expected: "test query",
		},
		{
			name:     "trim whitespace",
			input:    "  test query  ",
			expected: "test query",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "only special characters",
			input:    `"*()`,
			expected: "",
		},
		{
			name:     "mixed special characters",
			input:    `test"query*with(special)chars`,
			expected: "test query with special chars",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := sanitizeFTS5Query(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestFTSDatabase_SearchBM25_EmptyQuery tests empty query handling
func TestFTSDatabase_SearchBM25_EmptyQuery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	config := &FTSConfig{
		DBPath: ":memory:",
	}

	ftsDB, err := NewFTSDatabase(config)
	require.NoError(t, err)
	defer func() { _ = ftsDB.Close() }()

	// Empty query should return empty results
	results, err := ftsDB.SearchBM25(ctx, "", 10, nil)
	require.NoError(t, err)
	assert.Empty(t, results)

	// Query with only special characters should return empty results
	results, err = ftsDB.SearchBM25(ctx, `"*()`, 10, nil)
	require.NoError(t, err)
	assert.Empty(t, results)
}
