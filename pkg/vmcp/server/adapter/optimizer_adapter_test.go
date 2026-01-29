// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// mockOptimizerHandlerProvider implements OptimizerHandlerProvider for testing.
type mockOptimizerHandlerProvider struct {
	findToolHandler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	callToolHandler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

func (m *mockOptimizerHandlerProvider) CreateFindToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if m.findToolHandler != nil {
		return m.findToolHandler
	}
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
}

func (m *mockOptimizerHandlerProvider) CreateCallToolHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if m.callToolHandler != nil {
		return m.callToolHandler
	}
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}
}

func TestCreateOptimizerTools(t *testing.T) {
	t.Parallel()

	provider := &mockOptimizerHandlerProvider{}
	tools, err := CreateOptimizerTools(provider)

	require.NoError(t, err)
	require.Len(t, tools, 2)
	require.Equal(t, FindToolName, tools[0].Tool.Name)
	require.Equal(t, CallToolName, tools[1].Tool.Name)
}

func TestCreateOptimizerTools_NilProvider(t *testing.T) {
	t.Parallel()

	tools, err := CreateOptimizerTools(nil)

	require.Error(t, err)
	require.Nil(t, tools)
	require.Contains(t, err.Error(), "cannot be nil")
}

func TestFindToolHandler(t *testing.T) {
	t.Parallel()

	provider := &mockOptimizerHandlerProvider{
		findToolHandler: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, ok := req.Params.Arguments.(map[string]any)
			require.True(t, ok)
			require.Equal(t, "read files", args["tool_description"])
			return mcp.NewToolResultText("found tools"), nil
		},
	}

	tools, err := CreateOptimizerTools(provider)
	require.NoError(t, err)
	handler := tools[0].Handler

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"tool_description": "read files",
			},
		},
	}

	result, err := handler(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
}

func TestCallToolHandler(t *testing.T) {
	t.Parallel()

	provider := &mockOptimizerHandlerProvider{
		callToolHandler: func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args, ok := req.Params.Arguments.(map[string]any)
			require.True(t, ok)
			require.Equal(t, "read_file", args["tool_name"])
			params := args["parameters"].(map[string]any)
			require.Equal(t, "/etc/hosts", params["path"])
			return mcp.NewToolResultText("file contents here"), nil
		},
	}

	tools, err := CreateOptimizerTools(provider)
	require.NoError(t, err)
	handler := tools[1].Handler

	request := mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Arguments: map[string]any{
				"tool_name": "read_file",
				"parameters": map[string]any{
					"path": "/etc/hosts",
				},
			},
		},
	}

	result, err := handler(context.Background(), request)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
}
