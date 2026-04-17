// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package syncutil provides concurrency utilities.
package syncutil

import (
	"sync/atomic"
	"time"
)

// AtMost executes a function at most once per the configured interval.
// Subsequent calls within the interval are silently skipped.
// Safe for concurrent use from multiple goroutines.
type AtMost struct {
	interval time.Duration
	last     atomic.Int64
	now      func() time.Time
}

// NewAtMost creates an AtMost that allows execution at most once per interval.
// The first call to Do always executes.
func NewAtMost(interval time.Duration) *AtMost {
	return &AtMost{
		interval: interval,
		now:      time.Now,
	}
}

// Do calls fn if at least interval has elapsed since the last successful
// invocation. Otherwise the call is silently skipped.
// The first call always executes because last is initialized to zero,
// which is treated as "never called".
func (a *AtMost) Do(fn func()) {
	now := a.now().UnixNano()
	last := a.last.Load()
	if last != 0 && now-last < a.interval.Nanoseconds() {
		return
	}
	if a.last.CompareAndSwap(last, now) {
		fn()
	}
}
