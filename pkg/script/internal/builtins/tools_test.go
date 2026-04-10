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

func TestMakeToolCallable_Kwargs(t *testing.T) {
	t.Parallel()

	var captured map[string]interface{}
	callFn := func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		captured = args
		return mcp.NewToolResultText("ok"), nil
	}

	globals := starlark.StringDict{
		"my_tool": MakeToolCallable(context.Background(), "my_tool", callFn),
	}

	_, err := core.Execute(`return my_tool(name="test", count=42)`, globals, 100_000)
	require.NoError(t, err)
	require.Equal(t, "test", captured["name"])
	require.Equal(t, int64(42), captured["count"])
}

func TestMakeToolCallable_Positional(t *testing.T) {
	t.Parallel()

	var captured map[string]interface{}
	callFn := func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		captured = args
		return mcp.NewToolResultText("ok"), nil
	}

	globals := starlark.StringDict{
		"my_tool": MakeToolCallable(context.Background(), "my_tool", callFn),
	}

	_, err := core.Execute(`return my_tool("hello", "world")`, globals, 100_000)
	require.NoError(t, err)
	require.Equal(t, "hello", captured["arg0"])
	require.Equal(t, "world", captured["arg1"])
}

func TestMakeToolCallable_Mixed(t *testing.T) {
	t.Parallel()

	var captured map[string]interface{}
	callFn := func(_ context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		captured = args
		return mcp.NewToolResultText("ok"), nil
	}

	globals := starlark.StringDict{
		"my_tool": MakeToolCallable(context.Background(), "my_tool", callFn),
	}

	_, err := core.Execute(`return my_tool("positional", key="named")`, globals, 100_000)
	require.NoError(t, err)
	require.Equal(t, "positional", captured["arg0"])
	require.Equal(t, "named", captured["key"])
}
