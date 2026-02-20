// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokencounter

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestJSONByteDivisionCounter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		divisor int
		tool    mcp.Tool
	}{
		{
			name:    "minimal tool",
			divisor: 4,
			tool:    mcp.Tool{Name: "t"},
		},
		{
			name:    "tool with description",
			divisor: 4,
			tool:    mcp.Tool{Name: "read_file", Description: "Read a file from the filesystem"},
		},
		{
			name:    "tool with schema",
			divisor: 4,
			tool: mcp.NewTool("search",
				mcp.WithDescription("Search for items"),
				mcp.WithString("query", mcp.Description("The search query"), mcp.Required()),
			),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			counter := JSONByteDivisionCounter{Divisor: tc.divisor}
			got := counter.CountTokens(tc.tool)

			data, err := json.Marshal(tc.tool)
			require.NoError(t, err)
			expected := len(data) / tc.divisor
			require.Equal(t, expected, got)
			require.Greater(t, got, 0)
		})
	}
}

func TestJSONByteDivisionCounter_ZeroDivisor(t *testing.T) {
	t.Parallel()

	counter := JSONByteDivisionCounter{Divisor: 0}
	got := counter.CountTokens(mcp.Tool{Name: "test"})
	require.Equal(t, 0, got)
}

func TestNewJSONByteCounter(t *testing.T) {
	t.Parallel()

	counter := NewJSONByteCounter()
	require.NotNil(t, counter)

	cdc, ok := counter.(JSONByteDivisionCounter)
	require.True(t, ok)
	require.Equal(t, 4, cdc.Divisor)
}

func TestComputeTokenMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		baselineTokens int
		tokenCounts    map[string]int
		matchedNames   []string
		expected       TokenMetrics
	}{
		{
			name:           "zero baseline returns empty metrics",
			baselineTokens: 0,
			tokenCounts:    map[string]int{},
			matchedNames:   nil,
			expected:       TokenMetrics{},
		},
		{
			name:           "all tools matched returns zero savings",
			baselineTokens: 100,
			tokenCounts:    map[string]int{"a": 50, "b": 50},
			matchedNames:   []string{"a", "b"},
			expected: TokenMetrics{
				BaselineTokens: 100,
				ReturnedTokens: 100,
				SavingsPercent: 0,
			},
		},
		{
			name:           "subset matched returns positive savings",
			baselineTokens: 100,
			tokenCounts:    map[string]int{"a": 30, "b": 70},
			matchedNames:   []string{"a"},
			expected: TokenMetrics{
				BaselineTokens: 100,
				ReturnedTokens: 30,
				SavingsPercent: 70,
			},
		},
		{
			name:           "no matches returns full savings",
			baselineTokens: 100,
			tokenCounts:    map[string]int{"a": 50, "b": 50},
			matchedNames:   nil,
			expected: TokenMetrics{
				BaselineTokens: 100,
				ReturnedTokens: 0,
				SavingsPercent: 100,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ComputeTokenMetrics(tc.baselineTokens, tc.tokenCounts, tc.matchedNames)
			require.Equal(t, tc.expected, got)
		})
	}
}
