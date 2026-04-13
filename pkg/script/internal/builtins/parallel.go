// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package builtins

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/stacklok/toolhive/pkg/foreach"
)

// newParallel creates a parallel() Starlark builtin that executes a list
// of zero-arg callables concurrently and returns results in order.
//
// Uses a bounded worker pool (via foreach.Concurrent) so the goroutine
// count matches maxConcurrency, not the task count. The step budget is
// divided evenly across children to prevent amplification.
func newParallel(ctx context.Context, stepLimit uint64, maxConcurrency int) *starlark.Builtin {
	return starlark.NewBuiltin("parallel", func(
		thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var fns *starlark.List
		if err := starlark.UnpackPositionalArgs("parallel", args, kwargs, 1, &fns); err != nil {
			return nil, err
		}

		n := fns.Len()
		if n == 0 {
			return starlark.NewList(nil), nil
		}

		// Divide step budget evenly across children to prevent amplification.
		// With N children, each gets stepLimit/N steps.
		childStepLimit := stepLimit / uint64(n) //nolint:gosec // n is from starlark.List.Len(), always non-negative

		childLogs := make([][]string, n)

		results, err := foreach.Concurrent(ctx, n, maxConcurrency,
			func(_ context.Context, idx int) (starlark.Value, error) {
				callable, ok := fns.Index(idx).(starlark.Callable)
				if !ok {
					return nil, fmt.Errorf("parallel: element %d is not callable (got %s)",
						idx, fns.Index(idx).Type())
				}

				// Each child gets its own log buffer to avoid data races
				var logs []string
				childThread := &starlark.Thread{
					Name: fmt.Sprintf("%s/parallel-%d", thread.Name, idx),
					Print: func(_ *starlark.Thread, msg string) {
						logs = append(logs, msg)
					},
				}
				if childStepLimit > 0 {
					childThread.SetMaxExecutionSteps(childStepLimit)
				}

				result, callErr := starlark.Call(childThread, callable, nil, nil)
				childLogs[idx] = logs
				return result, callErr
			},
		)
		if err != nil {
			return nil, fmt.Errorf("parallel: %w", err)
		}

		// Merge child logs into parent thread in order
		for _, logs := range childLogs {
			for _, msg := range logs {
				thread.Print(thread, msg)
			}
		}

		return starlark.NewList(results), nil
	})
}
