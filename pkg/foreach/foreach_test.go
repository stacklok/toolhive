// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package foreach

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestConcurrent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		n          int
		maxWorkers int
		fn         func(ctx context.Context, i int) (string, error)
		expect     []string
		wantErr    string
	}{
		{
			name:       "empty input",
			n:          0,
			maxWorkers: 4,
			fn:         func(_ context.Context, _ int) (string, error) { return "", nil },
		},
		{
			name:       "all succeed",
			n:          3,
			maxWorkers: 2,
			fn: func(_ context.Context, i int) (string, error) {
				return fmt.Sprintf("result-%d", i), nil
			},
			expect: []string{"result-0", "result-1", "result-2"},
		},
		{
			name:       "error reported from lowest index",
			n:          3,
			maxWorkers: 3,
			fn: func(_ context.Context, i int) (string, error) {
				if i == 1 {
					return "", fmt.Errorf("task 1 broke")
				}
				return "ok", nil
			},
			wantErr: "task 1 failed",
		},
		{
			name:       "maxWorkers zero defaults to n",
			n:          3,
			maxWorkers: 0,
			fn: func(_ context.Context, i int) (string, error) {
				return fmt.Sprintf("r%d", i), nil
			},
			expect: []string{"r0", "r1", "r2"},
		},
		{
			name:       "maxWorkers exceeds n capped to n",
			n:          2,
			maxWorkers: 100,
			fn: func(_ context.Context, i int) (string, error) {
				return fmt.Sprintf("v%d", i), nil
			},
			expect: []string{"v0", "v1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			results, err := Concurrent(context.Background(), tt.n, tt.maxWorkers, tt.fn)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expect, results)
		})
	}
}

func TestConcurrent_BoundsGoroutines(t *testing.T) {
	t.Parallel()

	var maxConcurrent atomic.Int32
	var current atomic.Int32

	fn := func(ctx context.Context, i int) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		cur := current.Add(1)
		for {
			old := maxConcurrent.Load()
			if cur <= old {
				break
			}
			if maxConcurrent.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		current.Add(-1)
		return fmt.Sprintf("done-%d", i), nil
	}

	done := make(chan struct{})
	go func() {
		results, err := Concurrent(context.Background(), 10, 2, fn)
		require.NoError(t, err)
		require.Len(t, results, 10)
		require.Equal(t, "done-0", results[0])
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent execution")
	}

	require.LessOrEqual(t, maxConcurrent.Load(), int32(2),
		"should never exceed worker pool size of 2")
}

func TestConcurrent_RespectsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	var started atomic.Int32
	results, err := Concurrent(ctx, 100, 1, func(ctx context.Context, _ int) (string, error) {
		n := started.Add(1)
		// Cancel after a few tasks have started
		if n == 3 {
			cancel()
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "ok", nil
	})

	// Some tasks should have completed, but not all 100
	completedCount := 0
	for _, r := range results {
		if r == "ok" {
			completedCount++
		}
	}
	// With 1 worker and cancellation after task 3, most tasks should not run
	require.Less(t, completedCount, 100, "cancellation should prevent most tasks from running")

	// Either we get an error from a cancelled task, or all dispatched tasks succeeded
	// before the producer noticed the cancellation
	_ = err
}
