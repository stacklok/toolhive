// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package builtins

import (
	"fmt"
	"sync"

	"go.starlark.net/starlark"
)

// NewParallel creates a parallel() Starlark builtin that executes a list
// of zero-arg callables concurrently and returns results in order.
//
// maxConcurrency limits the number of goroutines running simultaneously.
// A value of 0 means unlimited concurrency.
//
// Usage in Starlark:
//
//	results = parallel([
//	    lambda: tool_a(query="test"),
//	    lambda: tool_b(query="test"),
//	])
func NewParallel(maxConcurrency int) *starlark.Builtin {
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

		results := make([]starlark.Value, n)
		errs := make([]error, n)

		var wg sync.WaitGroup
		wg.Add(n)

		// Optional semaphore for concurrency limiting
		var sem chan struct{}
		if maxConcurrency > 0 {
			sem = make(chan struct{}, maxConcurrency)
		}

		for i := range n {
			go func(idx int) {
				defer wg.Done()

				if sem != nil {
					sem <- struct{}{}
					defer func() { <-sem }()
				}

				callable, ok := fns.Index(idx).(starlark.Callable)
				if !ok {
					errs[idx] = fmt.Errorf("parallel: element %d is not callable (got %s)",
						idx, fns.Index(idx).Type())
					return
				}

				childThread := &starlark.Thread{
					Name:  fmt.Sprintf("%s/parallel-%d", thread.Name, idx),
					Print: thread.Print,
				}

				result, err := starlark.Call(childThread, callable, nil, nil)
				if err != nil {
					errs[idx] = err
					return
				}
				results[idx] = result
			}(i)
		}

		wg.Wait()

		for i, err := range errs {
			if err != nil {
				return nil, fmt.Errorf("parallel: task %d failed: %w", i, err)
			}
		}

		return starlark.NewList(results), nil
	})
}
