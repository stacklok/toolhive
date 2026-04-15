// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package core provides the Starlark script execution engine.
package core

import (
	"fmt"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// ExecuteResult holds the raw Starlark result of a script execution.
type ExecuteResult struct {
	// Value is the Starlark value returned by the script's top-level return.
	Value starlark.Value
	// Logs collects print() output from the script.
	Logs []string
}

// Execute runs a Starlark script with the given predeclared globals and step limit.
//
// The script is wrapped in a function body so that top-level return statements
// work naturally. A step limit of 0 disables the execution step cap (not
// recommended for untrusted scripts).
func Execute(script string, globals starlark.StringDict, stepLimit uint64) (*ExecuteResult, error) {
	wrapped := wrapScript(script)

	var logs []string
	thread := &starlark.Thread{
		Name: "script-exec",
		Print: func(_ *starlark.Thread, msg string) {
			logs = append(logs, msg)
		},
		Load: func(_ *starlark.Thread, module string) (starlark.StringDict, error) {
			return nil, fmt.Errorf("load(%q) is not permitted in scripts", module)
		},
	}

	if stepLimit > 0 {
		thread.SetMaxExecutionSteps(stepLimit)
	}

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
// The script becomes the body of __main__(), and its return value is captured
// in __result__.
//
// Known limitation: the 4-space indentation changes the content of multi-line
// string literals (triple-quoted strings). This is acceptable for tool
// orchestration scripts where triple-quoted strings are uncommon.
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
