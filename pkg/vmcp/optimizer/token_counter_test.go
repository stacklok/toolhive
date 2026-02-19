// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestCharDivTokenCounter(t *testing.T) {
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

			counter := CharDivTokenCounter{Divisor: tc.divisor}
			got := counter.CountTokens(tc.tool)

			data, err := json.Marshal(tc.tool)
			require.NoError(t, err)
			expected := len(data) / tc.divisor
			require.Equal(t, expected, got)
			require.Greater(t, got, 0)
		})
	}
}

func TestCharDivTokenCounter_ZeroDivisor(t *testing.T) {
	t.Parallel()

	counter := CharDivTokenCounter{Divisor: 0}
	got := counter.CountTokens(mcp.Tool{Name: "test"})
	require.Equal(t, 0, got)
}

func TestDefaultTokenCounter(t *testing.T) {
	t.Parallel()

	counter := DefaultTokenCounter()
	require.NotNil(t, counter)

	cdc, ok := counter.(CharDivTokenCounter)
	require.True(t, ok)
	require.Equal(t, 4, cdc.Divisor)
}
