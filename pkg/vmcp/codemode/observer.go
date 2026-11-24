// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"context"
	"log/slog"
)

// ScriptStats describes one execute_tool_script call. It carries no script
// body, arguments or tool names, so an observer cannot leak them by accident.
type ScriptStats struct {
	// ToolCalls is the number of tool calls the script started, including
	// calls that failed or were denied. Calls made under parallel() count
	// individually. It is zero when the call failed before the script ran.
	ToolCalls int
}

// ScriptObserver receives the stats of every execute_tool_script call. It runs
// synchronously on the request path after the script finishes, so it must be
// cheap and must not block; hand slow work to another goroutine. A panic in the
// observer is recovered and logged, and never changes the tool result.
type ScriptObserver func(ctx context.Context, stats ScriptStats)

// notify calls observer, recovering any panic so an observer can never break
// the script path.
func notify(ctx context.Context, observer ScriptObserver, stats ScriptStats) {
	defer func() {
		if r := recover(); r != nil {
			slog.WarnContext(ctx, "codemode: script observer panicked", "panic", r)
		}
	}()
	observer(ctx, stats)
}
