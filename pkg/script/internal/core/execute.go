// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package core provides the Starlark script execution engine.
package core

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// ErrStepLimitExceeded marks an execution that stopped because a thread ran
// out of its step budget. The returned error matches it with errors.Is; its
// message is unchanged from the interpreter's own.
var ErrStepLimitExceeded = errors.New("step limit exceeded")

// stepLimitError keeps the interpreter's message while matching
// ErrStepLimitExceeded.
type stepLimitError struct{ err error }

func (e *stepLimitError) Error() string   { return e.err.Error() }
func (e *stepLimitError) Unwrap() []error { return []error{e.err, ErrStepLimitExceeded} }

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
	return ExecuteWithStats(script, globals, stepLimit, nil)
}

// ExecuteWithStats is [Execute] that also counts, on stats, the execution steps
// and parallel() fan-out of the run. stats may be nil. It is filled in whether
// or not the script succeeds.
func ExecuteWithStats(
	script string, globals starlark.StringDict, stepLimit uint64, stats *Stats,
) (*ExecuteResult, error) {
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

	SetStats(thread, stats)

	// OnMaxSteps does what the interpreter does by default (cancel with "too
	// many steps") and also records that the step limit was the cause. Child
	// threads started by parallel() inherit this callback, so a child that
	// runs out of steps is recorded too.
	var stepLimitHit atomic.Bool
	thread.OnMaxSteps = func(t *starlark.Thread) {
		stepLimitHit.Store(true)
		t.Cancel("too many steps")
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
	// Child threads add their own steps in parallel(); this thread's count
	// covers only the script body.
	stats.AddSteps(thread.ExecutionSteps())
	if err != nil {
		err = fmt.Errorf("script execution failed: %w", err)
		if stepLimitHit.Load() {
			err = &stepLimitError{err: err}
		}
		return nil, err
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
