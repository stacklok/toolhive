// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package script provides a Starlark-based script execution engine for orchestrating
// MCP tool calls. It allows agents to send scripts that call multiple tools and
// return aggregated results.
package script

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// DefaultStepLimit is the default maximum number of Starlark execution steps.
const DefaultStepLimit uint64 = 100_000

// ExecuteResult holds the result of a Starlark script execution.
type ExecuteResult struct {
	Value starlark.Value
	Logs  []string
}

// Execute runs a Starlark script with the given globals and step limit.
// The script is wrapped in a function so that top-level `return` statements work.
// A stepLimit of 0 uses DefaultStepLimit.
func Execute(script string, globals starlark.StringDict, stepLimit uint64) (*ExecuteResult, error) {
	if stepLimit == 0 {
		stepLimit = DefaultStepLimit
	}

	wrapped := wrapScript(script)

	var logs []string
	thread := &starlark.Thread{
		Name: "script-exec",
		Print: func(_ *starlark.Thread, msg string) {
			logs = append(logs, msg)
		},
	}
	thread.SetMaxExecutionSteps(stepLimit)

	// Merge globals into the predeclared set so they're available at top level
	predeclared := make(starlark.StringDict, len(globals))
	for k, v := range globals {
		predeclared[k] = v
	}

	resultGlobals, err := starlark.ExecFileOptions(
		&syntax.FileOptions{},
		thread,
		"script.star",
		wrapped,
		predeclared,
	)
	if err != nil {
		return nil, fmt.Errorf("script execution failed: %w", err)
	}

	result, ok := resultGlobals["__result__"]
	if !ok {
		result = starlark.None
	}

	return &ExecuteResult{
		Value: result,
		Logs:  logs,
	}, nil
}

// wrapScript wraps a user script in a function body so top-level return works.
func wrapScript(script string) string {
	var b strings.Builder
	b.WriteString("def __main__():\n")
	for _, line := range strings.Split(script, "\n") {
		b.WriteString("    ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("__result__ = __main__()\n")
	return b.String()
}
