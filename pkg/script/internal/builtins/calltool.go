// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package builtins

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/stacklok/toolhive/pkg/script/internal/conversions"
)

// NewCallTool creates a generic call_tool("name", ...) Starlark builtin
// that dispatches to tools by name. It uses the same positional/keyword
// argument handling as direct tool callables.
//
// Usage in Starlark:
//
//	call_tool("github-fetch-prs", owner="stacklok", repo="toolhive")
//	call_tool("my-tool", "positional_value")
func NewCallTool(ctx context.Context, toolMap map[string]CallFunc) *starlark.Builtin {
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

		callFn, exists := toolMap[toolName]
		if !exists {
			return nil, fmt.Errorf("call_tool: unknown tool %q", toolName)
		}

		// Remaining positional args (after name) + kwargs
		arguments := argsToGoMap(args[1:], kwargs)
		return callToolAndConvert(ctx, callFn, arguments)
	})
}

// BuildToolMap creates a name → CallFunc map and a sanitized-name → CallFunc
// map from a set of tool names and call functions. It also returns collision
// warnings for tools whose sanitized names conflict.
func BuildToolMap(tools []ToolEntry) (byName map[string]CallFunc, bySanitized map[string]CallFunc, collisions []string) {
	byName = make(map[string]CallFunc, len(tools))
	bySanitized = make(map[string]CallFunc, len(tools))
	seen := make(map[string]string, len(tools)) // sanitized → original

	for _, t := range tools {
		byName[t.Name] = t.Call
		sanitized := conversions.SanitizeName(t.Name)
		if existing, ok := seen[sanitized]; ok {
			collisions = append(collisions, fmt.Sprintf(
				"tool name collision: %q and %q both sanitize to %q", existing, t.Name, sanitized))
			continue
		}
		seen[sanitized] = t.Name
		bySanitized[sanitized] = t.Call
	}
	return byName, bySanitized, collisions
}

// ToolEntry pairs a tool name with its call function for BuildToolMap.
type ToolEntry struct {
	Name string
	Call CallFunc
}
