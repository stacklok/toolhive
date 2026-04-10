// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package builtins provides Starlark builtin functions for the script engine,
// including tool callables, call_tool(), and parallel().
package builtins

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"go.starlark.net/starlark"

	"github.com/stacklok/toolhive/pkg/script/internal/conversions"
)

// CallFunc is the signature for invoking an MCP tool.
type CallFunc func(ctx context.Context, arguments map[string]interface{}) (*mcp.CallToolResult, error)

// MakeToolCallable creates a Starlark builtin that invokes an MCP tool.
// It supports both positional and keyword arguments:
//   - my_tool(key=val) → {"key": val}
//   - my_tool(val1, val2) → {"arg0": val1, "arg1": val2}
//   - my_tool(val1, key=val2) → {"arg0": val1, "key": val2}
func MakeToolCallable(ctx context.Context, displayName string, callFn CallFunc) *starlark.Builtin {
	return starlark.NewBuiltin(displayName, func(
		_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		arguments := argsToGoMap(args, kwargs)
		return callToolAndConvert(ctx, callFn, arguments)
	})
}

// callToolAndConvert invokes a tool and converts the result to a Starlark value.
func callToolAndConvert(ctx context.Context, callFn CallFunc, arguments map[string]interface{}) (starlark.Value, error) {
	result, err := callFn(ctx, arguments)
	if err != nil {
		return nil, err
	}

	goVal, err := conversions.ParseToolResult(result)
	if err != nil {
		return nil, err
	}

	sv, err := conversions.GoToStarlark(goVal)
	if err != nil {
		return nil, fmt.Errorf("result conversion failed: %w", err)
	}
	return sv, nil
}

// argsToGoMap converts positional and keyword Starlark arguments into a
// Go map. Positional args are keyed as "arg0", "arg1", etc. Keyword args
// use their name as the key.
func argsToGoMap(args starlark.Tuple, kwargs []starlark.Tuple) map[string]interface{} {
	m := make(map[string]interface{}, len(args)+len(kwargs))
	for i, arg := range args {
		m[fmt.Sprintf("arg%d", i)] = conversions.StarlarkToGo(arg)
	}
	for _, kv := range kwargs {
		key := string(kv[0].(starlark.String))
		m[key] = conversions.StarlarkToGo(kv[1])
	}
	return m
}
