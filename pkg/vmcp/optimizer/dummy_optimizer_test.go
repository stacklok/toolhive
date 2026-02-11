// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

func TestDummyOptimizer_FindTool(t *testing.T) {
	t.Parallel()

	tools := []server.ServerTool{
		{
			Tool: mcp.Tool{
				Name:        "fetch_url",
				Description: "Fetch content from a URL",
			},
		},
		{
			Tool: mcp.Tool{
				Name:        "read_file",
				Description: "Read a file from the filesystem",
			},
		},
		{
			Tool: mcp.Tool{
				Name:        "write_file",
				Description: "Write content to a file",
			},
		},
	}

	store := NewInMemoryToolStore()
	opt, err := NewDummyOptimizer(store, tools)
	require.NoError(t, err)

	tests := []struct {
		name          string
		input         FindToolInput
		expectedNames []string
		expectedError bool
		errorContains string
	}{
		{
			name: "find by exact name",
			input: FindToolInput{
				ToolDescription: "fetch_url",
			},
			expectedNames: []string{"fetch_url"},
		},
		{
			name: "find by description substring",
			input: FindToolInput{
				ToolDescription: "file",
			},
			expectedNames: []string{"read_file", "write_file"},
		},
		{
			name: "case insensitive search",
			input: FindToolInput{
				ToolDescription: "FETCH",
			},
			expectedNames: []string{"fetch_url"},
		},
		{
			name: "no matches",
			input: FindToolInput{
				ToolDescription: "nonexistent",
			},
			expectedNames: []string{},
		},
		{
			name:          "empty description",
			input:         FindToolInput{},
			expectedError: true,
			errorContains: "tool_description is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := opt.FindTool(context.Background(), tc.input)

			if tc.expectedError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			// Extract names from results
			var names []string
			for _, match := range result.Tools {
				names = append(names, match.Name)
			}

			require.ElementsMatch(t, tc.expectedNames, names)
		})
	}
}

func TestDummyOptimizerFactory_SharedStorage(t *testing.T) {
	t.Parallel()

	factory := NewDummyOptimizerFactory()

	// First optimizer with tool_a
	opt1, err := factory([]server.ServerTool{
		{Tool: mcp.Tool{Name: "tool_a", Description: "Alpha tool"}},
	})
	require.NoError(t, err)

	// Second optimizer with tool_b
	opt2, err := factory([]server.ServerTool{
		{Tool: mcp.Tool{Name: "tool_b", Description: "Beta tool"}},
	})
	require.NoError(t, err)

	// opt1 can only find tool_a (scoped to its allowedTools)
	result1, err := opt1.FindTool(context.Background(), FindToolInput{ToolDescription: "tool"})
	require.NoError(t, err)
	require.Len(t, result1.Tools, 1)
	require.Equal(t, "tool_a", result1.Tools[0].Name)

	// opt2 can only find tool_b (scoped to its allowedTools)
	result2, err := opt2.FindTool(context.Background(), FindToolInput{ToolDescription: "tool"})
	require.NoError(t, err)
	require.Len(t, result2.Tools, 1)
	require.Equal(t, "tool_b", result2.Tools[0].Name)

	// Both tools exist in the shared store — verify by creating an optimizer with both in scope
	opt3, err := factory([]server.ServerTool{
		{Tool: mcp.Tool{Name: "tool_a", Description: "Alpha tool"}},
		{Tool: mcp.Tool{Name: "tool_b", Description: "Beta tool"}},
	})
	require.NoError(t, err)

	result3, err := opt3.FindTool(context.Background(), FindToolInput{ToolDescription: "tool"})
	require.NoError(t, err)
	require.Len(t, result3.Tools, 2)

	names := []string{result3.Tools[0].Name, result3.Tools[1].Name}
	require.ElementsMatch(t, []string{"tool_a", "tool_b"}, names)
}

func TestDummyOptimizer_CallTool(t *testing.T) {
	t.Parallel()

	tools := []server.ServerTool{
		{
			Tool: mcp.Tool{
				Name:        "test_tool",
				Description: "A test tool",
			},
			Handler: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				args, _ := req.Params.Arguments.(map[string]any)
				input := args["input"].(string)
				return mcp.NewToolResultText("Hello, " + input + "!"), nil
			},
		},
	}

	store := NewInMemoryToolStore()
	opt, err := NewDummyOptimizer(store, tools)
	require.NoError(t, err)

	tests := []struct {
		name          string
		input         CallToolInput
		expectedText  string
		expectedError bool
		isToolError   bool
		errorContains string
	}{
		{
			name: "successful tool call",
			input: CallToolInput{
				ToolName:   "test_tool",
				Parameters: map[string]any{"input": "World"},
			},
			expectedText: "Hello, World!",
		},
		{
			name: "tool not found",
			input: CallToolInput{
				ToolName:   "nonexistent",
				Parameters: map[string]any{},
			},
			isToolError:  true,
			expectedText: "tool not found: nonexistent",
		},
		{
			name: "empty tool name",
			input: CallToolInput{
				Parameters: map[string]any{},
			},
			expectedError: true,
			errorContains: "tool_name is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result, err := opt.CallTool(context.Background(), tc.input)

			if tc.expectedError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errorContains)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)

			if tc.isToolError {
				require.True(t, result.IsError)
			}

			if tc.expectedText != "" {
				require.Len(t, result.Content, 1)
				textContent, ok := result.Content[0].(mcp.TextContent)
				require.True(t, ok)
				require.Equal(t, tc.expectedText, textContent.Text)
			}
		})
	}
}
