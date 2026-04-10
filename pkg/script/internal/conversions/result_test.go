// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package conversions

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestParseToolResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  *mcp.CallToolResult
		expect  interface{}
		wantErr string
	}{
		{
			name: "structured content with SDK wrapper",
			result: &mcp.CallToolResult{
				StructuredContent: map[string]interface{}{
					"result": map[string]interface{}{"key": "value"},
				},
			},
			expect: map[string]interface{}{"key": "value"},
		},
		{
			name: "structured content without wrapper",
			result: &mcp.CallToolResult{
				StructuredContent: map[string]interface{}{
					"key1": "value1",
					"key2": "value2",
				},
			},
			expect: map[string]interface{}{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name: "text content with JSON",
			result: mcp.NewToolResultText(`{"items": [1, 2, 3]}`),
			expect: map[string]interface{}{
				"items": []interface{}{float64(1), float64(2), float64(3)},
			},
		},
		{
			name:   "text content plain string",
			result: mcp.NewToolResultText("hello world"),
			expect: "hello world",
		},
		{
			name:   "empty content",
			result: &mcp.CallToolResult{},
			expect: nil,
		},
		{
			name:    "error result",
			result:  mcp.NewToolResultError("something went wrong"),
			wantErr: "something went wrong",
		},
		{
			name: "error result with empty content",
			result: &mcp.CallToolResult{
				IsError: true,
			},
			wantErr: "tool execution error",
		},
		{
			name: "structured content non-map type",
			result: &mcp.CallToolResult{
				StructuredContent: []interface{}{"a", "b"},
			},
			expect: []interface{}{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseToolResult(tt.result)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expect, got)
		})
	}
}
