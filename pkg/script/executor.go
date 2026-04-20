// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package script

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Apply per-script execution timeout to bound total wall-clock time
	// including all tool calls.
	ctx, cancel := context.WithTimeout(ctx, e.config.ScriptTimeout)
	defer cancel()

	globals := e.buildGlobals(ctx)

	// Inject data arguments, rejecting any that shadow builtins or tools
	for k, v := range data {
		if _, exists := globals[k]; exists {
			return nil, fmt.Errorf("data argument %q conflicts with a builtin or tool name", k)
		}
		sv, err := conversions.GoToStarlark(v)
		if err != nil {
			return nil, fmt.Errorf("data argument %q: %w", k, err)
		}
		globals[k] = sv
	}

	result, err := core.Execute(script, globals, e.config.StepLimit)
	if err != nil {
		return nil, err
	}

	return buildResult(result)
}

// ToolDescription returns the dynamic description for the virtual tool.
func (e *executor) ToolDescription() string {
	return GenerateToolDescription(e.tools)
}

// buildGlobals creates the Starlark global environment from the bound tools.
func (e *executor) buildGlobals(ctx context.Context) starlark.StringDict {
	defs := make([]builtins.ToolDef, len(e.tools))
	for i, t := range e.tools {
		defs[i] = builtins.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			Call:        t.Call,
		}
	}

	return builtins.Build(ctx, defs, e.config.StepLimit, e.config.ParallelMax)
}

// buildResult converts a core.ExecuteResult into an mcp.CallToolResult.
func buildResult(execResult *core.ExecuteResult) (*mcp.CallToolResult, error) {
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
