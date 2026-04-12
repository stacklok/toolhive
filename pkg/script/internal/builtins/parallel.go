// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package builtins

import (
	"context"
	"fmt"
	"sync"

	"go.starlark.net/starlark"
)

// parallelState holds the shared state for a single parallel() invocation.
type parallelState struct {
	fns       *starlark.List
	results   []starlark.Value
	errs      []error
	childLogs [][]string
	sem       chan struct{}
	stepLimit uint64
	ctx       context.Context
}

// newParallel creates a parallel() Starlark builtin that executes a list
// of zero-arg callables concurrently and returns results in order.
//
// ctx is checked for cancellation before launching each goroutine and
// while acquiring semaphore slots. stepLimit is propagated to child
// threads. maxConcurrency limits simultaneous goroutines (0 = unlimited).
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

		state := &parallelState{
			fns:       fns,
			results:   make([]starlark.Value, n),
			errs:      make([]error, n),
			childLogs: make([][]string, n),
			stepLimit: stepLimit,
			ctx:       ctx,
		}
		if maxConcurrency > 0 {
			state.sem = make(chan struct{}, maxConcurrency)
		}

		var wg sync.WaitGroup
		wg.Add(n)
		for i := range n {
			go state.runTask(&wg, i, thread)
		}
		waitWithContext(ctx, &wg)

		for i, err := range state.errs {
			if err != nil {
				return nil, fmt.Errorf("parallel: task %d failed: %w", i, err)
			}
		}

		// Merge child logs into parent thread
		for _, logs := range state.childLogs {
			for _, msg := range logs {
				thread.Print(thread, msg)
			}
		}

		return starlark.NewList(state.results), nil
	})
}

// runTask executes a single callable within the parallel fan-out.
func (s *parallelState) runTask(wg *sync.WaitGroup, idx int, parent *starlark.Thread) {
	defer wg.Done()

	if s.ctx.Err() != nil {
		s.errs[idx] = s.ctx.Err()
		return
	}

	if s.sem != nil {
		select {
		case s.sem <- struct{}{}:
			defer func() { <-s.sem }()
		case <-s.ctx.Done():
			s.errs[idx] = s.ctx.Err()
			return
		}
	}

	callable, ok := s.fns.Index(idx).(starlark.Callable)
	if !ok {
		s.errs[idx] = fmt.Errorf("parallel: element %d is not callable (got %s)",
			idx, s.fns.Index(idx).Type())
		return
	}

	var logs []string
	childThread := &starlark.Thread{
		Name: fmt.Sprintf("%s/parallel-%d", parent.Name, idx),
		Print: func(_ *starlark.Thread, msg string) {
			logs = append(logs, msg)
		},
	}
	if s.stepLimit > 0 {
		childThread.SetMaxExecutionSteps(s.stepLimit)
	}

	result, err := starlark.Call(childThread, callable, nil, nil)
	if err != nil {
		s.errs[idx] = err
		return
	}
	s.results[idx] = result
	s.childLogs[idx] = logs
}

// waitWithContext waits for wg to complete, remaining responsive to context
// cancellation. If the context is cancelled, it still waits for goroutines
// to finish (they will observe ctx.Err() on their next semaphore acquire
// or tool call).
func waitWithContext(ctx context.Context, wg *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		<-done
	}
}
