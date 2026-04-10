// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package builtins provides Starlark builtin functions for the script engine.
package builtins

import (
	"context"
	"log/slog"

	"github.com/mark3labs/mcp-go/mcp"
	"go.starlark.net/starlark"

	"github.com/stacklok/toolhive/pkg/script/internal/conversions"
)

// ToolDef defines a tool for the script environment.
type ToolDef struct {
	Name        string
	Description string
	Call        func(ctx context.Context, arguments map[string]interface{}) (*mcp.CallToolResult, error)
}

// Build creates all Starlark globals for the script environment.
//
// The returned globals include:
//   - Tool callables by sanitized name (e.g., github_fetch_prs)
//   - call_tool("name", ...) for name-based dispatch
//   - parallel([fn1, fn2, ...]) for concurrent fan-out
//
// The caller can check for key existence in the returned globals to
// prevent data arguments from shadowing builtins or tools.
func Build(
	ctx context.Context, tools []ToolDef, stepLimit uint64, parallelMax int,
) starlark.StringDict {
	byName := make(map[string]callFunc, len(tools))
	seen := make(map[string]string, len(tools)) // sanitized → original

	globals := make(starlark.StringDict, len(tools)+2)

	// Register each tool as a callable by its sanitized name
	for _, t := range tools {
		byName[t.Name] = t.Call

		sanitized := conversions.SanitizeName(t.Name)
		if existing, ok := seen[sanitized]; ok {
			slog.Warn("tool name collision after sanitization",
				"tool1", existing, "tool2", t.Name, "sanitized", sanitized)
			continue
		}
		seen[sanitized] = t.Name

		globals[sanitized] = makeToolCallable(ctx, sanitized, t.Call)
	}

	// Register call_tool() for name-based dispatch
	globals["call_tool"] = newCallTool(ctx, byName)

	// Register parallel() for concurrent fan-out
	globals["parallel"] = newParallel(ctx, stepLimit, parallelMax)

	return globals
}
