// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"
)

// mockToolStore implements ToolStore for testing optimizer logic against a
// controllable store without any database dependency.
type mockToolStore struct {
	upsertFunc func(ctx context.Context, tools []server.ServerTool) error
	searchFunc func(ctx context.Context, query string, allowedTools []string) ([]ToolMatch, error)
}

func (m *mockToolStore) UpsertTools(ctx context.Context, tools []server.ServerTool) error {
	if m.upsertFunc != nil {
		return m.upsertFunc(ctx, tools)
	}
	panic("mockToolStore.UpsertTools called but not configured")
}

func (m *mockToolStore) Search(ctx context.Context, query string, allowedTools []string) ([]ToolMatch, error) {
	if m.searchFunc != nil {
		return m.searchFunc(ctx, query, allowedTools)
	}
	panic("mockToolStore.Search called but not configured")
}

func (*mockToolStore) Close() error {
	return nil
}

// TestDummyOptimizer_MockStore tests the optimizer against a mock ToolStore,
// verifying search delegation, scoping, and error handling without any database.
func TestDummyOptimizer_MockStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		tools          []server.ServerTool
		searchFunc     func(ctx context.Context, query string, allowedTools []string) ([]ToolMatch, error)
		upsertFunc     func(ctx context.Context, tools []server.ServerTool) error
		input          FindToolInput
		expectedNames  []string
		expectErr      bool
		errContains    string
		expectCreate   bool // if false, expect NewDummyOptimizer to fail
		createErr      string
		checkMetrics   bool
		expectBaseline int
		expectReturned int
	}{
		{
			name: "delegates search to store with allowedTools and computes metrics",
			tools: []server.ServerTool{
				{Tool: mcp.Tool{Name: "tool_a", Description: "Tool A"}},
				{Tool: mcp.Tool{Name: "tool_b", Description: "Tool B"}},
			},
			upsertFunc: func(_ context.Context, _ []server.ServerTool) error { return nil },
			searchFunc: func(_ context.Context, query string, allowedTools []string) ([]ToolMatch, error) {
				require.Equal(t, "query", query)
				require.ElementsMatch(t, []string{"tool_a", "tool_b"}, allowedTools)
				return []ToolMatch{
					{Name: "tool_a", Description: "Tool A", Score: 0.9},
				}, nil
			},
			input:         FindToolInput{ToolDescription: "query"},
			expectedNames: []string{"tool_a"},
			expectCreate:  true,
			checkMetrics:  true,
		},
		{
			name: "propagates store search errors",
			tools: []server.ServerTool{
				{Tool: mcp.Tool{Name: "tool_a", Description: "Tool A"}},
			},
			upsertFunc: func(_ context.Context, _ []server.ServerTool) error { return nil },
			searchFunc: func(context.Context, string, []string) ([]ToolMatch, error) {
				return nil, fmt.Errorf("store unavailable")
			},
			input:        FindToolInput{ToolDescription: "query"},
			expectErr:    true,
			errContains:  "tool search failed",
			expectCreate: true,
		},
		{
			name: "propagates store upsert errors at creation",
			tools: []server.ServerTool{
				{Tool: mcp.Tool{Name: "tool_a", Description: "Tool A"}},
			},
			upsertFunc: func(context.Context, []server.ServerTool) error {
				return fmt.Errorf("upsert failed")
			},
			input:        FindToolInput{ToolDescription: "query"},
			expectCreate: false,
			createErr:    "failed to upsert tools into store",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := &mockToolStore{
				upsertFunc: tc.upsertFunc,
				searchFunc: tc.searchFunc,
			}

			opt, err := NewDummyOptimizer(context.Background(), store, DefaultTokenCounter(), tc.tools)
			if !tc.expectCreate {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.createErr)
				return
			}
			require.NoError(t, err)

			result, err := opt.FindTool(context.Background(), tc.input)
			if tc.expectErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.errContains)
				return
			}

			require.NoError(t, err)
			var names []string
			for _, m := range result.Tools {
				names = append(names, m.Name)
			}
			require.ElementsMatch(t, tc.expectedNames, names)

			if tc.checkMetrics {
				require.Greater(t, result.TokenMetrics.BaselineTokens, 0)
				require.Greater(t, result.TokenMetrics.ReturnedTokens, 0)
				require.Greater(t, result.TokenMetrics.SavingsPercent, 0.0)
			}
		})
	}
}

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
	opt, err := NewDummyOptimizer(context.Background(), store, DefaultTokenCounter(), tools)
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

			// TokenMetrics baseline should always be positive (3 tools in store)
			require.Greater(t, result.TokenMetrics.BaselineTokens, 0)
			if len(tc.expectedNames) > 0 {
				require.Greater(t, result.TokenMetrics.ReturnedTokens, 0)
			}
		})
	}
}

func TestDummyOptimizerFactory_SharedStorage(t *testing.T) {
	t.Parallel()

	factory := NewDummyOptimizerFactory()
	ctx := context.Background()

	// First optimizer with tool_a
	opt1, err := factory(ctx, []server.ServerTool{
		{Tool: mcp.Tool{Name: "tool_a", Description: "Alpha tool"}},
	})
	require.NoError(t, err)

	// Second optimizer with tool_b
	opt2, err := factory(ctx, []server.ServerTool{
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

	// Both tools exist in the shared store — verify by creating an optimizer with both in allowedTools
	opt3, err := factory(ctx, []server.ServerTool{
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

func TestNewDummyOptimizerFactoryWithStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		sessionATools  []server.ServerTool
		sessionBTools  []server.ServerTool
		searchQuery    string
		sessionAExpect []string
		sessionBExpect []string
	}{
		{
			name: "separate sessions see only their own tools",
			sessionATools: []server.ServerTool{
				{Tool: mcp.Tool{Name: "tool_alpha", Description: "Alpha tool"}},
			},
			sessionBTools: []server.ServerTool{
				{Tool: mcp.Tool{Name: "tool_beta", Description: "Beta tool"}},
			},
			searchQuery:    "tool",
			sessionAExpect: []string{"tool_alpha"},
			sessionBExpect: []string{"tool_beta"},
		},
		{
			name: "overlapping tools are shared",
			sessionATools: []server.ServerTool{
				{Tool: mcp.Tool{Name: "shared_tool", Description: "Shared tool"}},
				{Tool: mcp.Tool{Name: "tool_a_only", Description: "A only"}},
			},
			sessionBTools: []server.ServerTool{
				{Tool: mcp.Tool{Name: "shared_tool", Description: "Shared tool"}},
				{Tool: mcp.Tool{Name: "tool_b_only", Description: "B only"}},
			},
			searchQuery:    "tool",
			sessionAExpect: []string{"shared_tool", "tool_a_only"},
			sessionBExpect: []string{"shared_tool", "tool_b_only"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := NewInMemoryToolStore()
			factory := NewDummyOptimizerFactoryWithStore(store, DefaultTokenCounter())
			ctx := context.Background()

			optA, err := factory(ctx, tc.sessionATools)
			require.NoError(t, err)

			optB, err := factory(ctx, tc.sessionBTools)
			require.NoError(t, err)

			resultA, err := optA.FindTool(ctx, FindToolInput{ToolDescription: tc.searchQuery})
			require.NoError(t, err)

			var namesA []string
			for _, m := range resultA.Tools {
				namesA = append(namesA, m.Name)
			}
			require.ElementsMatch(t, tc.sessionAExpect, namesA)

			resultB, err := optB.FindTool(ctx, FindToolInput{ToolDescription: tc.searchQuery})
			require.NoError(t, err)

			var namesB []string
			for _, m := range resultB.Tools {
				namesB = append(namesB, m.Name)
			}
			require.ElementsMatch(t, tc.sessionBExpect, namesB)
		})
	}
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
	opt, err := NewDummyOptimizer(context.Background(), store, DefaultTokenCounter(), tools)
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
