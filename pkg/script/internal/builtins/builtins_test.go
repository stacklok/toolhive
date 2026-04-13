// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package builtins

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/stacklok/toolhive/pkg/script/internal/core"
)

func TestBuild_ToolCallable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		script     string
		expectArgs map[string]interface{}
	}{
		{
			name:       "kwargs",
			script:     `return my_tool(name="test", count=42)`,
			expectArgs: map[string]interface{}{"name": "test", "count": int64(42)},
		},
		{
			name:       "positional",
			script:     `return my_tool("hello", "world")`,
			expectArgs: map[string]interface{}{"arg0": "hello", "arg1": "world"},
		},
		{
			name:       "mixed",
			script:     `return my_tool("positional", key="named")`,
			expectArgs: map[string]interface{}{"arg0": "positional", "key": "named"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var captured map[string]interface{}
			tools := []ToolDef{{
				Name: "my-tool",
				Call: func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
					captured = args
					return mcp.NewToolResultText("ok"), nil
				},
			}}

			globals := Build(context.Background(), tools, 100_000, 0)
			_, err := core.Execute(tt.script, globals, 100_000)
			require.NoError(t, err)
			require.Equal(t, tt.expectArgs, captured)
		})
	}
}

func TestBuild_CallTool(t *testing.T) {
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
			name:    "no arguments",
			script:  `return call_tool()`,
			wantErr: "requires at least 1 argument",
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
			tools := []ToolDef{{
				Name: "my-tool",
				Call: func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
					captured = args
					return mcp.NewToolResultText("ok"), nil
				},
			}}

			globals := Build(context.Background(), tools, 100_000, 0)
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

func TestBuild_Parallel_OrderedResults(t *testing.T) {
	t.Parallel()

	globals := Build(context.Background(), nil, 100_000, 0)

	result, err := core.Execute(`
results = parallel([
    lambda: "first",
    lambda: "second",
    lambda: "third",
])
return results
`, globals, 100_000)
	require.NoError(t, err)

	list, ok := result.Value.(*starlark.List)
	require.True(t, ok)
	require.Equal(t, 3, list.Len())
	require.Equal(t, starlark.String("first"), list.Index(0))
	require.Equal(t, starlark.String("second"), list.Index(1))
	require.Equal(t, starlark.String("third"), list.Index(2))
}

func TestBuild_Parallel_ErrorPropagation(t *testing.T) {
	t.Parallel()

	failing := starlark.NewBuiltin("failing", func(
		_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple,
	) (starlark.Value, error) {
		return nil, fmt.Errorf("intentional failure")
	})

	globals := Build(context.Background(), nil, 100_000, 0)
	globals["failing"] = failing

	_, err := core.Execute(`return parallel([lambda: failing()])`, globals, 100_000)
	require.Error(t, err)
	require.Contains(t, err.Error(), "intentional failure")
}

func TestBuild_Parallel_ConcurrencyLimit(t *testing.T) {
	t.Parallel()

	var maxConcurrent atomic.Int32
	var current atomic.Int32

	slow := starlark.NewBuiltin("slow", func(
		_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple,
	) (starlark.Value, error) {
		cur := current.Add(1)
		for {
			old := maxConcurrent.Load()
			if cur <= old {
				break
			}
			if maxConcurrent.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		current.Add(-1)
		return starlark.String("done"), nil
	})

	globals := Build(context.Background(), nil, 1_000_000, 2) // limit to 2
	globals["slow"] = slow

	done := make(chan struct{})
	go func() {
		result, err := core.Execute(`
return parallel([
    lambda: slow(),
    lambda: slow(),
    lambda: slow(),
    lambda: slow(),
])
`, globals, 1_000_000)
		require.NoError(t, err)

		list, ok := result.Value.(*starlark.List)
		require.True(t, ok)
		require.Equal(t, 4, list.Len())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for parallel execution")
	}

	require.LessOrEqual(t, maxConcurrent.Load(), int32(2),
		"should never exceed concurrency limit of 2")
}

func TestBuild_GlobalsContainBuiltins(t *testing.T) {
	t.Parallel()

	tools := []ToolDef{
		{Name: "my-tool", Call: func(_ context.Context, _ map[string]interface{}) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("ok"), nil
		}},
	}

	globals := Build(context.Background(), tools, 100_000, 0)

	require.Contains(t, globals, "my_tool", "sanitized tool name should be in globals")
	require.Contains(t, globals, "call_tool", "call_tool should be in globals")
	require.Contains(t, globals, "parallel", "parallel should be in globals")
}

func TestBuild_NameCollision(t *testing.T) {
	t.Parallel()

	tools := []ToolDef{
		{Name: "my-tool", Call: func(_ context.Context, _ map[string]interface{}) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("first"), nil
		}},
		{Name: "my.tool", Call: func(_ context.Context, _ map[string]interface{}) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("second"), nil
		}},
	}

	globals := Build(context.Background(), tools, 100_000, 0)

	// Only the first tool should survive sanitization collision
	_, hasMyTool := globals["my_tool"]
	require.True(t, hasMyTool)

	// But both should be callable via call_tool by original name
	result, err := core.Execute(`return call_tool("my.tool")`, globals, 100_000)
	require.NoError(t, err)
	require.Equal(t, starlark.String("second"), result.Value, "should dispatch to my.tool, not my-tool")
}
