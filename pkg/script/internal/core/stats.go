// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"sync/atomic"

	"go.starlark.net/starlark"
)

// statsKey is the thread-local key under which an execution's *Stats is
// stored, so builtins running on the script thread or on parallel() child
// threads can add to it.
const statsKey = "toolhive.script.stats"

// Stats counts what one execution did. It is safe for concurrent use, because
// parallel() children add to it from their own goroutines. A nil *Stats
// ignores every update.
type Stats struct {
	steps         atomic.Uint64
	parallelCalls atomic.Int64
	parallelTasks atomic.Int64
}

// AddSteps adds n Starlark execution steps.
func (s *Stats) AddSteps(n uint64) {
	if s != nil {
		s.steps.Add(n)
	}
}

// AddParallel records one parallel() call that started tasks callables.
func (s *Stats) AddParallel(tasks int) {
	if s != nil {
		s.parallelCalls.Add(1)
		s.parallelTasks.Add(int64(tasks))
	}
}

// Steps is the number of Starlark execution steps, summed over the script
// thread and every parallel() child thread.
func (s *Stats) Steps() uint64 {
	if s == nil {
		return 0
	}
	return s.steps.Load()
}

// ParallelCalls is the number of parallel() calls.
func (s *Stats) ParallelCalls() int {
	if s == nil {
		return 0
	}
	return int(s.parallelCalls.Load())
}

// ParallelTasks is the number of callables started by parallel() calls.
func (s *Stats) ParallelTasks() int {
	if s == nil {
		return 0
	}
	return int(s.parallelTasks.Load())
}

// StatsFrom returns the *Stats stored on thread, or nil if there is none.
func StatsFrom(thread *starlark.Thread) *Stats {
	s, _ := thread.Local(statsKey).(*Stats)
	return s
}

// SetStats stores s on thread, so builtins running on it can add to s.
func SetStats(thread *starlark.Thread, s *Stats) {
	thread.SetLocal(statsKey, s)
}
