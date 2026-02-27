// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizer

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/tokencounter"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types/mocks"
)

func TestGetAndValidateConfig(t *testing.T) {
	t.Parallel()

	ptrFloat := func(f float64) *float64 { return &f }
	ptrInt := func(i int) *int { return &i }

	tests := []struct {
		name        string
		cfg         *vmcpconfig.OptimizerConfig
		expected    *Config
		errContains string
	}{
		{
			name:     "nil config returns nil",
			cfg:      nil,
			expected: nil,
		},
		{
			name:     "empty config returns defaults",
			cfg:      &vmcpconfig.OptimizerConfig{},
			expected: &Config{},
		},
		{
			name: "embedding service is copied",
			cfg: &vmcpconfig.OptimizerConfig{
				EmbeddingService: "http://embeddings:8080",
			},
			expected: &Config{
				EmbeddingService: "http://embeddings:8080",
			},
		},
		{
			name: "all valid values are parsed",
			cfg: &vmcpconfig.OptimizerConfig{
				EmbeddingService:          "http://embeddings:8080",
				MaxToolsToReturn:          10,
				HybridSearchSemanticRatio: "0.7",
				SemanticDistanceThreshold: "1.5",
			},
			expected: &Config{
				EmbeddingService:          "http://embeddings:8080",
				MaxToolsToReturn:          ptrInt(10),
				HybridSemanticRatio:       ptrFloat(0.7),
				SemanticDistanceThreshold: ptrFloat(1.5),
			},
		},
		{
			name: "boundary: MaxToolsToReturn=1",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: 1,
			},
			expected: &Config{
				MaxToolsToReturn: ptrInt(1),
			},
		},
		{
			name: "boundary: MaxToolsToReturn=50",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: 50,
			},
			expected: &Config{
				MaxToolsToReturn: ptrInt(50),
			},
		},
		{
			name: "boundary: ratio=0.0",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "0.0",
			},
			expected: &Config{
				HybridSemanticRatio: ptrFloat(0.0),
			},
		},
		{
			name: "boundary: ratio=1.0",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "1.0",
			},
			expected: &Config{
				HybridSemanticRatio: ptrFloat(1.0),
			},
		},
		{
			name: "boundary: threshold=0.0",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "0.0",
			},
			expected: &Config{
				SemanticDistanceThreshold: ptrFloat(0.0),
			},
		},
		{
			name: "boundary: threshold=2.0",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "2.0",
			},
			expected: &Config{
				SemanticDistanceThreshold: ptrFloat(2.0),
			},
		},
		{
			name: "MaxToolsToReturn=0 treated as unset",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: 0,
			},
			expected: &Config{},
		},
		{
			name: "error: MaxToolsToReturn too high",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: 51,
			},
			errContains: "optimizer.maxToolsToReturn must be between 1 and 50",
		},
		{
			name: "error: MaxToolsToReturn negative",
			cfg: &vmcpconfig.OptimizerConfig{
				MaxToolsToReturn: -1,
			},
			errContains: "optimizer.maxToolsToReturn must be between 1 and 50",
		},
		{
			name: "error: ratio above 1.0",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "1.1",
			},
			errContains: "optimizer.hybridSearchSemanticRatio must be between 0.0 and 1.0",
		},
		{
			name: "error: ratio negative",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "-0.1",
			},
			errContains: "optimizer.hybridSearchSemanticRatio must be between 0.0 and 1.0",
		},
		{
			name: "error: ratio not a number",
			cfg: &vmcpconfig.OptimizerConfig{
				HybridSearchSemanticRatio: "abc",
			},
			errContains: "optimizer.hybridSearchSemanticRatio must be a valid number",
		},
		{
			name: "error: threshold above 2.0",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "2.1",
			},
			errContains: "optimizer.semanticDistanceThreshold must be between 0.0 and 2.0",
		},
		{
			name: "error: threshold negative",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "-0.5",
			},
			errContains: "optimizer.semanticDistanceThreshold must be between 0.0 and 2.0",
		},
		{
			name: "error: threshold not a number",
			cfg: &vmcpconfig.OptimizerConfig{
				SemanticDistanceThreshold: "not-a-float",
			},
			errContains: "optimizer.semanticDistanceThreshold must be a valid number",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := GetAndValidateConfig(tt.cfg)

			if tt.errContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			require.NoError(t, err)

			if tt.expected == nil {
				assert.Nil(t, result)
				return
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.expected.EmbeddingService, result.EmbeddingService)

			if tt.expected.MaxToolsToReturn != nil {
				require.NotNil(t, result.MaxToolsToReturn)
				assert.Equal(t, *tt.expected.MaxToolsToReturn, *result.MaxToolsToReturn)
			} else {
				assert.Nil(t, result.MaxToolsToReturn)
			}

			if tt.expected.HybridSemanticRatio != nil {
				require.NotNil(t, result.HybridSemanticRatio)
				assert.InDelta(t, *tt.expected.HybridSemanticRatio, *result.HybridSemanticRatio, 1e-9)
			} else {
				assert.Nil(t, result.HybridSemanticRatio)
			}

			if tt.expected.SemanticDistanceThreshold != nil {
				require.NotNil(t, result.SemanticDistanceThreshold)
				assert.InDelta(t, *tt.expected.SemanticDistanceThreshold, *result.SemanticDistanceThreshold, 1e-9)
			} else {
				assert.Nil(t, result.SemanticDistanceThreshold)
			}
		})
	}
}

// newMockStoreWithSubstringSearch returns a gomock MockToolStore configured with
// DoAndReturn handlers that accumulate tools via UpsertTools and perform
// case-insensitive substring matching on Search. Suitable for tests that need
// basic search behavior without a real database.
func newMockStoreWithSubstringSearch(ctrl *gomock.Controller) *mocks.MockToolStore {
	store := mocks.NewMockToolStore(ctrl)
	tools := make(map[string]server.ServerTool)

	store.EXPECT().UpsertTools(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, ts []server.ServerTool) error {
			for _, t := range ts {
				tools[t.Tool.Name] = t
			}
			return nil
		},
	).AnyTimes()

	store.EXPECT().Search(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, query string, allowedTools []string) ([]ToolMatch, error) {
			if len(allowedTools) == 0 {
				return nil, nil
			}
			searchTerm := strings.ToLower(query)
			allowedSet := make(map[string]struct{}, len(allowedTools))
			for _, name := range allowedTools {
				allowedSet[name] = struct{}{}
			}
			var matches []ToolMatch
			for _, tool := range tools {
				if _, ok := allowedSet[tool.Tool.Name]; !ok {
					continue
				}
				nameLower := strings.ToLower(tool.Tool.Name)
				descLower := strings.ToLower(tool.Tool.Description)
				if strings.Contains(nameLower, searchTerm) || strings.Contains(descLower, searchTerm) {
					matches = append(matches, ToolMatch{
						Name:        tool.Tool.Name,
						Description: tool.Tool.Description,
					})
				}
			}
			return matches, nil
		},
	).AnyTimes()

	store.EXPECT().Close().Return(nil).AnyTimes()

	return store
}

// TestOptimizer_SearchDelegation verifies that FindTool delegates to the
// store with the correct query and allowedTools, and computes token metrics.
func TestOptimizer_SearchDelegation(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockToolStore(ctrl)

	tools := []server.ServerTool{
		{Tool: mcp.Tool{Name: "tool_a", Description: "Tool A"}},
		{Tool: mcp.Tool{Name: "tool_b", Description: "Tool B"}},
	}

	store.EXPECT().UpsertTools(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().Search(gomock.Any(), "query", gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, allowedTools []string) ([]ToolMatch, error) {
			require.ElementsMatch(t, []string{"tool_a", "tool_b"}, allowedTools)
			return []ToolMatch{
				{Name: "tool_a", Description: "Tool A"},
			}, nil
		},
	)

	opt, err := newToolOptimizer(context.Background(), store, tokencounter.NewJSONByteCounter(), tools)
	require.NoError(t, err)

	result, err := opt.FindTool(context.Background(), FindToolInput{ToolDescription: "query"})
	require.NoError(t, err)

	var names []string
	for _, m := range result.Tools {
		names = append(names, m.Name)
	}
	require.ElementsMatch(t, []string{"tool_a"}, names)

	require.Greater(t, result.TokenMetrics.BaselineTokens, 0)
	require.Greater(t, result.TokenMetrics.ReturnedTokens, 0)
	require.Greater(t, result.TokenMetrics.SavingsPercent, 0.0)
}

// TestOptimizer_SearchError verifies that store search errors are propagated.
func TestOptimizer_SearchError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockToolStore(ctrl)

	store.EXPECT().UpsertTools(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().Search(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("store unavailable"))

	opt, err := newToolOptimizer(context.Background(), store, tokencounter.NewJSONByteCounter(), []server.ServerTool{
		{Tool: mcp.Tool{Name: "tool_a", Description: "Tool A"}},
	})
	require.NoError(t, err)

	_, err = opt.FindTool(context.Background(), FindToolInput{ToolDescription: "query"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool search failed")
}

// TestOptimizer_UpsertError verifies that store upsert errors during creation are propagated.
func TestOptimizer_UpsertError(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	store := mocks.NewMockToolStore(ctrl)

	store.EXPECT().UpsertTools(gomock.Any(), gomock.Any()).Return(fmt.Errorf("upsert failed"))

	_, err := newToolOptimizer(context.Background(), store, tokencounter.NewJSONByteCounter(), []server.ServerTool{
		{Tool: mcp.Tool{Name: "tool_a", Description: "Tool A"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to upsert tools into store")
}

func TestOptimizer_FindTool(t *testing.T) {
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

	ctrl := gomock.NewController(t)
	store := newMockStoreWithSubstringSearch(ctrl)
	opt, err := newToolOptimizer(context.Background(), store, tokencounter.NewJSONByteCounter(), tools)
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

func TestOptimizerFactoryWithStore(t *testing.T) {
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

			ctrl := gomock.NewController(t)
			store := newMockStoreWithSubstringSearch(ctrl)
			factory := newOptimizerFactoryWithStore(store, tokencounter.NewJSONByteCounter())
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

func TestOptimizer_CallTool(t *testing.T) {
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

	ctrl := gomock.NewController(t)
	store := newMockStoreWithSubstringSearch(ctrl)
	opt, err := newToolOptimizer(context.Background(), store, tokencounter.NewJSONByteCounter(), tools)
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
