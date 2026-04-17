// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package builtins

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"
)

// newCallTool creates a generic call_tool("name", ...) Starlark builtin
// that dispatches to tools by name.
func newCallTool(ctx context.Context, toolMap map[string]callFunc) *starlark.Builtin {
	return starlark.NewBuiltin("call_tool", func(
		_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		if len(args) < 1 {
			return nil, fmt.Errorf("call_tool: requires at least 1 argument (tool name)")
		}

		nameVal, ok := args[0].(starlark.String)
		if !ok {
			return nil, fmt.Errorf("call_tool: first argument must be a string, got %s", args[0].Type())
		}
		toolName := string(nameVal)

		fn, exists := toolMap[toolName]
		if !exists {
			return nil, fmt.Errorf("call_tool: unknown tool %q", toolName)
		}

		// Remaining positional args (after name) + kwargs
		arguments := argsToGoMap(args[1:], kwargs)
		return callToolAndConvert(ctx, fn, arguments)
	})
}
