// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package builtins

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/stacklok/toolhive/pkg/script/internal/core"
)

func TestNewCallTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		script     string
		expectArgs map[string]interface{}
		wantErr    string
	}{
		{
			name:       "kwargs dispatch",
			script:     `return call_tool("my-tool", query="test")`,
			expectArgs: map[string]interface{}{"query": "test"},
		},
		{
			name:       "positional dispatch",
			script:     `return call_tool("my-tool", "value")`,
			expectArgs: map[string]interface{}{"arg0": "value"},
		},
		{
			name:       "mixed dispatch",
			script:     `return call_tool("my-tool", "pos", key="named")`,
			expectArgs: map[string]interface{}{"arg0": "pos", "key": "named"},
		},
		{
			name:    "no arguments",
			script:  `return call_tool()`,
			wantErr: "requires at least 1 argument",
		},
		{
			name:    "non-string name",
			script:  `return call_tool(42)`,
			wantErr: "first argument must be a string",
		},
		{
			name:    "unknown tool",
			script:  `return call_tool("nonexistent")`,
			wantErr: `unknown tool "nonexistent"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var captured map[string]interface{}
			toolMap := map[string]CallFunc{
				"my-tool": func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
					captured = args
					return mcp.NewToolResultText("ok"), nil
				},
			}

			globals := starlark.StringDict{
				"call_tool": NewCallTool(context.Background(), toolMap),
			}

			_, err := core.Execute(tt.script, globals, 100_000)

			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expectArgs, captured)
		})
	}
}

func TestBuildToolMap(t *testing.T) {
	t.Parallel()

	dummyCall := func(_ context.Context, _ map[string]interface{}) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("ok"), nil
	}

	tests := []struct {
		name            string
		tools           []ToolEntry
		expectByName    []string
		expectSanitized []string
		expectCollision bool
	}{
		{
			name: "no collisions",
			tools: []ToolEntry{
				{Name: "github-fetch-prs", Call: dummyCall},
				{Name: "slack-send", Call: dummyCall},
			},
			expectByName:    []string{"github-fetch-prs", "slack-send"},
			expectSanitized: []string{"github_fetch_prs", "slack_send"},
		},
		{
			name: "collision detected",
			tools: []ToolEntry{
				{Name: "my-tool", Call: dummyCall},
				{Name: "my.tool", Call: dummyCall},
			},
			expectByName:    []string{"my-tool", "my.tool"},
			expectSanitized: []string{"my_tool"}, // only first survives
			expectCollision: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			byName, bySanitized, collisions := BuildToolMap(tt.tools)

			for _, name := range tt.expectByName {
				require.Contains(t, byName, name)
			}
			for _, name := range tt.expectSanitized {
				require.Contains(t, bySanitized, name)
			}
			if tt.expectCollision {
				require.NotEmpty(t, collisions)
			} else {
				require.Empty(t, collisions)
			}
		})
	}
}
