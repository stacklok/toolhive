// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/stacklok/toolhive/pkg/script"
)

// ScriptOutcome is how one execute_tool_script call ended. It is a small, fixed
// set, so it is safe to use as a metric attribute.
type ScriptOutcome string

const (
	// OutcomeSuccess means the script ran and returned a result.
	OutcomeSuccess ScriptOutcome = "success"
	// OutcomeError means the call failed for any reason not covered below:
	// invalid arguments, a runtime error in the script, or a failed tool call.
	OutcomeError ScriptOutcome = "error"
	// OutcomeTimeout means the script or one of its tool calls hit a deadline.
	OutcomeTimeout ScriptOutcome = "timeout"
	// OutcomeStepLimit means the script used up its execution step budget.
	OutcomeStepLimit ScriptOutcome = "step_limit"
)

// ScriptStats describes one execute_tool_script call. It carries no script
// body, arguments or tool names, so an observer cannot leak them by accident.
type ScriptStats struct {
	// ToolCalls is the number of tool calls the script started, including
	// calls that failed or were denied. Calls made under parallel() count
	// individually. It is zero when the call failed before the script ran.
	ToolCalls int
	// Outcome is how the call ended.
	Outcome ScriptOutcome
	// Duration is the wall-clock time of the whole call.
	Duration time.Duration
	// Steps is the number of Starlark execution steps the script used,
	// including those of callables run under parallel().
	Steps uint64
	// ParallelCalls is the number of parallel() calls the script made.
	ParallelCalls int
	// ParallelTasks is the number of callables those parallel() calls started.
	ParallelTasks int
}

// ScriptObserver receives the stats of every execute_tool_script call. It runs
// synchronously on the request path after the script finishes, so it must be
// cheap and must not block; hand slow work to another goroutine. A panic in the
// observer is recovered and logged, and never changes the tool result.
type ScriptObserver func(ctx context.Context, stats ScriptStats)

// notify calls observer, recovering any panic so an observer can never break
// the script path.
func notify(ctx context.Context, observer ScriptObserver, stats ScriptStats) {
	if observer == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.WarnContext(ctx, "codemode: script observer panicked", "panic", r)
		}
	}()
	observer(ctx, stats)
}

// classifyOutcome maps the result of a script execution to an outcome.
// timedOut reports that an inner tool call hit its deadline, which catches
// backend clients that do not wrap context.DeadlineExceeded in their errors.
// execCtx is the script's own deadline-bound context.
func classifyOutcome(execCtx context.Context, err error, timedOut bool) ScriptOutcome {
	switch {
	case err == nil:
		return OutcomeSuccess
	case errors.Is(err, script.ErrStepLimitExceeded):
		return OutcomeStepLimit
	case timedOut,
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(execCtx.Err(), context.DeadlineExceeded):
		return OutcomeTimeout
	default:
		return OutcomeError
	}
}
