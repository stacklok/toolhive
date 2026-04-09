// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package syncutil

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAtMost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T, a *AtMost, setTime func(time.Time))
		want int32 // expected invocation count
	}{
		{
			name: "first_call_executes",
			run: func(t *testing.T, _ *AtMost, _ func(time.Time)) {
				t.Helper()
				// Do nothing extra; the default test body calls Do once.
			},
			want: 1,
		},
		{
			name: "skips_within_interval",
			run: func(t *testing.T, a *AtMost, _ func(time.Time)) {
				t.Helper()
				// Second call at same time should be skipped.
				a.Do(func() { t.Error("should not have been called") })
			},
			want: 1,
		},
		{
			name: "executes_again_after_interval",
			run: func(t *testing.T, _ *AtMost, setTime func(time.Time)) {
				t.Helper()
				setTime(time.Unix(1000000, 0).Add(2 * time.Minute))
				// Should execute again after advancing past the interval.
			},
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var count atomic.Int32
			fakeTime := time.Unix(1000000, 0)
			var mu sync.Mutex

			a := NewAtMost(time.Minute)
			a.now = func() time.Time {
				mu.Lock()
				defer mu.Unlock()
				return fakeTime
			}

			setTime := func(newTime time.Time) {
				mu.Lock()
				defer mu.Unlock()
				fakeTime = newTime
			}

			// First call always happens.
			a.Do(func() { count.Add(1) })

			tt.run(t, a, setTime)

			// Final call to capture any post-advancement execution.
			a.Do(func() { count.Add(1) })

			assert.Equal(t, tt.want, count.Load())
		})
	}
}

func TestAtMost_ConcurrentSafety(t *testing.T) {
	t.Parallel()

	var count atomic.Int32
	a := NewAtMost(time.Minute)
	// Freeze time so all goroutines see the same instant.
	a.now = func() time.Time { return time.Unix(1000000, 0) }

	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.Do(func() { count.Add(1) })
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), count.Load(),
		"fn should execute exactly once when all goroutines call Do at the same instant")
}
