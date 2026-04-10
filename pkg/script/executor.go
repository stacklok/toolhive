// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"go.starlark.net/starlark"

	"github.com/stacklok/toolhive/pkg/script/internal/builtins"
	"github.com/stacklok/toolhive/pkg/script/internal/conversions"
	"github.com/stacklok/toolhive/pkg/script/internal/core"
)

// executor is the unexported implementation of Executor.
type executor struct {
	tools  []Tool
	config Config
}

// Execute runs a Starlark script against the bound tools.
func (e *executor) Execute(ctx context.Context, script string, data map[string]interface{}) (*mcp.CallToolResult, error) {
	globals := e.buildGlobals(ctx, data)

	result, err := core.Execute(script, globals, e.config.StepLimit)
	if err != nil {
		return nil, err
	}

	return e.buildResult(result)
}

// ToolDescription returns the dynamic description for the virtual tool.
func (e *executor) ToolDescription() string {
	return GenerateToolDescription(e.tools)
}

// buildGlobals creates the Starlark global environment from the bound tools
// and data arguments.
func (e *executor) buildGlobals(ctx context.Context, data map[string]interface{}) starlark.StringDict {
	// Build tool maps
	entries := make([]builtins.ToolEntry, len(e.tools))
	for i, t := range e.tools {
		entries[i] = builtins.ToolEntry{Name: t.Name, Call: t.Call}
	}

	byName, bySanitized, collisions := builtins.BuildToolMap(entries)
	for _, warning := range collisions {
		slog.Warn(warning)
	}

	// Build globals
	globals := make(starlark.StringDict, len(bySanitized)+len(data)+2)

	// Register each tool as a callable by its sanitized name
	for sanitized, callFn := range bySanitized {
		globals[sanitized] = builtins.MakeToolCallable(ctx, sanitized, callFn)
	}

	// Register call_tool() for name-based dispatch
	globals["call_tool"] = builtins.NewCallTool(ctx, byName)

	// Register parallel()
	globals["parallel"] = builtins.NewParallel(e.config.ParallelMax)

	// Inject data arguments as top-level variables
	for k, v := range data {
		sv, err := conversions.GoToStarlark(v)
		if err != nil {
			slog.Warn("failed to convert data argument to Starlark", "key", k, "error", err)
			continue
		}
		globals[k] = sv
	}

	return globals
}

// buildResult converts a core.ExecuteResult into an mcp.CallToolResult.
func (*executor) buildResult(execResult *core.ExecuteResult) (*mcp.CallToolResult, error) {
	goVal := conversions.StarlarkToGo(execResult.Value)

	resultJSON, err := json.Marshal(goVal)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize script result: %w", err)
	}

	result := mcp.NewToolResultText(string(resultJSON))

	if len(execResult.Logs) > 0 {
		result.Content = append(result.Content, mcp.NewTextContent(
			"Script logs:\n"+strings.Join(execResult.Logs, "\n"),
		))
	}

	return result, nil
}
