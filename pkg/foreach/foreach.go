// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package foreach provides bounded concurrent iteration. A fixed pool of
// worker goroutines processes tasks, so the goroutine count matches the
// concurrency limit rather than the task count.
package foreach

import (
	"context"
	"fmt"
	"sync"
)

// Concurrent executes fn for each index in [0, n) using at most maxWorkers
// concurrent goroutines. If maxWorkers is <= 0, it defaults to n (all tasks
// run concurrently). The context is checked before dispatching each task;
// cancelled contexts cause remaining tasks to return ctx.Err().
//
// Results and errors are collected by index. The first error encountered
// does not cancel other tasks — all tasks run to completion or until the
// context is cancelled. The returned error is from the lowest-indexed
// failing task.
func Concurrent[T any](ctx context.Context, n int, maxWorkers int, fn func(ctx context.Context, i int) (T, error)) ([]T, error) {
	if n == 0 {
		return nil, nil
	}

	if maxWorkers <= 0 || maxWorkers > n {
		maxWorkers = n
	}

	results := make([]T, n)
	errs := make([]error, n)

	tasks := make(chan int, n)
	go func() {
		defer close(tasks)
		for i := range n {
			select {
			case tasks <- i:
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(maxWorkers)
	for range maxWorkers {
		go func() {
			defer wg.Done()
			for idx := range tasks {
				results[idx], errs[idx] = fn(ctx, idx)
			}
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return results, fmt.Errorf("task %d failed: %w", i, err)
		}
	}

	return results, nil
}
