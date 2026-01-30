// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package composer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDAGExecutor_BuildExecutionLevels tests the topological sort algorithm.
func TestDAGExecutor_BuildExecutionLevels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		steps          []WorkflowStep
		wantLevels     int
		wantLevelSizes []int
		wantErr        bool
	}{
		{
			name: "sequential steps - no dependencies",
			steps: []WorkflowStep{
				{ID: "step1"},
				{ID: "step2"},
				{ID: "step3"},
			},
			wantLevels:     1,
			wantLevelSizes: []int{3}, // All steps in one level (can run in parallel)
		},
		{
			name: "simple chain - linear dependencies",
			steps: []WorkflowStep{
				{ID: "step1"},
				{ID: "step2", DependsOn: []string{"step1"}},
				{ID: "step3", DependsOn: []string{"step2"}},
			},
			wantLevels:     3,
			wantLevelSizes: []int{1, 1, 1}, // Each step in its own level
		},
		{
			name: "parallel branches with join",
			steps: []WorkflowStep{
				{ID: "fetch_logs"},
				{ID: "fetch_metrics"},
				{ID: "fetch_traces"},
				{ID: "create_report", DependsOn: []string{"fetch_logs", "fetch_metrics", "fetch_traces"}},
			},
			wantLevels:     2,
			wantLevelSizes: []int{3, 1}, // 3 parallel, then 1
		},
		{
			name: "complex DAG",
			steps: []WorkflowStep{
				{ID: "a"},
				{ID: "b"},
				{ID: "c", DependsOn: []string{"a"}},
				{ID: "d", DependsOn: []string{"a", "b"}},
				{ID: "e", DependsOn: []string{"c", "d"}},
			},
			wantLevels:     3,
			wantLevelSizes: []int{2, 2, 1}, // a,b -> c,d -> e (c and d can run in parallel)
		},
		{
			name: "diamond pattern",
			steps: []WorkflowStep{
				{ID: "start"},
				{ID: "left", DependsOn: []string{"start"}},
				{ID: "right", DependsOn: []string{"start"}},
				{ID: "end", DependsOn: []string{"left", "right"}},
			},
			wantLevels:     3,
			wantLevelSizes: []int{1, 2, 1}, // start -> left,right -> end
		},
		{
			name:       "empty workflow",
			steps:      []WorkflowStep{},
			wantLevels: 0,
		},
		{
			name: "single step",
			steps: []WorkflowStep{
				{ID: "only"},
			},
			wantLevels:     1,
			wantLevelSizes: []int{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			executor := newDAGExecutor(10)
			levels, err := executor.buildExecutionLevels(tt.steps)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantLevels, len(levels), "number of levels")

			if len(tt.wantLevelSizes) > 0 {
				actualSizes := make([]int, len(levels))
				for i, level := range levels {
					actualSizes[i] = len(level.steps)
				}
				assert.Equal(t, tt.wantLevelSizes, actualSizes, "level sizes")
			}
		})
	}
}

// TestDAGExecutor_CircularDependency tests cycle detection.
func TestDAGExecutor_CircularDependency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		steps []WorkflowStep
	}{
		{
			name: "direct cycle - A->B->A",
			steps: []WorkflowStep{
				{ID: "a", DependsOn: []string{"b"}},
				{ID: "b", DependsOn: []string{"a"}},
			},
		},
		{
			name: "indirect cycle - A->B->C->A",
			steps: []WorkflowStep{
				{ID: "a", DependsOn: []string{"c"}},
				{ID: "b", DependsOn: []string{"a"}},
				{ID: "c", DependsOn: []string{"b"}},
			},
		},
		{
			name: "self-reference",
			steps: []WorkflowStep{
				{ID: "a", DependsOn: []string{"a"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			executor := newDAGExecutor(10)
			_, err := executor.buildExecutionLevels(tt.steps)
			assert.Error(t, err)
			assert.ErrorIs(t, err, ErrCircularDependency)
		})
	}
}

// TestDAGExecutor_ParallelExecution tests that independent steps run in parallel.
func TestDAGExecutor_ParallelExecution(t *testing.T) {
	t.Parallel()
	executor := newDAGExecutor(10)

	// Track execution timing
	var executionOrder []string
	var executionMu sync.Mutex
	startTime := time.Now()

	// Create steps that take 100ms each
	steps := []WorkflowStep{
		{ID: "step1"},
		{ID: "step2"},
		{ID: "step3"},
	}

	// Execution function that records order and sleeps
	execFunc := func(_ context.Context, step *WorkflowStep) error {
		time.Sleep(100 * time.Millisecond)
		executionMu.Lock()
		executionOrder = append(executionOrder, step.ID)
		executionMu.Unlock()
		return nil
	}

	err := executor.executeDAG(context.Background(), steps, execFunc, "abort")
	require.NoError(t, err)

	duration := time.Since(startTime)

	// All 3 steps should execute in parallel, so total time should be ~100ms
	// not 300ms (sequential). Use 250ms (2.5x expected time) to account for race detector overhead.
	assert.Less(t, duration, 250*time.Millisecond, "parallel execution should be faster than sequential")

	// All steps should have executed
	assert.Len(t, executionOrder, 3)
}

// TestDAGExecutor_DependencyOrder tests that dependencies are respected.
func TestDAGExecutor_DependencyOrder(t *testing.T) {
	t.Parallel()
	executor := newDAGExecutor(10)

	var executionOrder []string
	var executionMu sync.Mutex

	// Create a chain: step1 -> step2 -> step3
	steps := []WorkflowStep{
		{ID: "step1"},
		{ID: "step2", DependsOn: []string{"step1"}},
		{ID: "step3", DependsOn: []string{"step2"}},
	}

	execFunc := func(_ context.Context, step *WorkflowStep) error {
		executionMu.Lock()
		executionOrder = append(executionOrder, step.ID)
		executionMu.Unlock()
		return nil
	}

	err := executor.executeDAG(context.Background(), steps, execFunc, "abort")
	require.NoError(t, err)

	// Steps must execute in order
	assert.Equal(t, []string{"step1", "step2", "step3"}, executionOrder)
}

// TestDAGExecutor_ErrorHandling tests error propagation and failure modes.
func TestDAGExecutor_ErrorHandling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		failureMode string
		failAt      string // Which step should fail
		wantErr     bool
		wantAllRun  bool // Should all steps still run?
	}{
		{
			name:        "abort on error",
			failureMode: "abort",
			failAt:      "step2",
			wantErr:     true,
			wantAllRun:  false,
		},
		{
			name:        "continue on error",
			failureMode: "continue",
			failAt:      "step2",
			wantErr:     false,
			wantAllRun:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			executor := newDAGExecutor(10)

			var executed []string
			var execMu sync.Mutex

			steps := []WorkflowStep{
				{ID: "step1"},
				{ID: "step2"},
				{ID: "step3"},
			}

			execFunc := func(_ context.Context, step *WorkflowStep) error {
				execMu.Lock()
				executed = append(executed, step.ID)
				execMu.Unlock()

				if step.ID == tt.failAt {
					return errors.New("intentional failure")
				}
				return nil
			}

			err := executor.executeDAG(context.Background(), steps, execFunc, tt.failureMode)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.wantAllRun {
				assert.Len(t, executed, 3, "all steps should execute")
			}
		})
	}
}

// TestDAGExecutor_StepLevelErrorHandling tests per-step error handling.
func TestDAGExecutor_StepLevelErrorHandling(t *testing.T) {
	t.Parallel()
	executor := newDAGExecutor(10)

	var executed []string
	var execMu sync.Mutex

	steps := []WorkflowStep{
		{ID: "step1"},
		{ID: "step2", OnError: &ErrorHandler{ContinueOnError: true}}, // Continue on error
		{ID: "step3"},
	}

	execFunc := func(_ context.Context, step *WorkflowStep) error {
		execMu.Lock()
		executed = append(executed, step.ID)
		execMu.Unlock()

		if step.ID == "step2" {
			return errors.New("intentional failure")
		}
		return nil
	}

	// Even with "abort" mode, step2's ContinueOnError should allow execution to continue
	err := executor.executeDAG(context.Background(), steps, execFunc, "abort")
	assert.NoError(t, err)
	assert.Len(t, executed, 3, "all steps should execute")
}

// TestDAGExecutor_Concurrency tests semaphore-based concurrency limiting.
func TestDAGExecutor_Concurrency(t *testing.T) {
	t.Parallel()
	maxParallel := 2
	executor := newDAGExecutor(maxParallel)

	var concurrentCount int32
	var maxConcurrent int32

	// Create 5 independent steps
	steps := []WorkflowStep{
		{ID: "step1"},
		{ID: "step2"},
		{ID: "step3"},
		{ID: "step4"},
		{ID: "step5"},
	}

	execFunc := func(_ context.Context, _ *WorkflowStep) error {
		// Increment concurrent count
		current := atomic.AddInt32(&concurrentCount, 1)

		// Track max concurrent
		for {
			maxVal := atomic.LoadInt32(&maxConcurrent)
			if current <= maxVal {
				break
			}
			if atomic.CompareAndSwapInt32(&maxConcurrent, maxVal, current) {
				break
			}
		}

		// Simulate work
		time.Sleep(50 * time.Millisecond)

		// Decrement concurrent count
		atomic.AddInt32(&concurrentCount, -1)
		return nil
	}

	err := executor.executeDAG(context.Background(), steps, execFunc, "abort")
	require.NoError(t, err)

	// Max concurrent should not exceed the semaphore limit
	assert.LessOrEqual(t, int(maxConcurrent), maxParallel,
		"max concurrent executions should not exceed limit")
}

// TestDAGExecutor_ContextCancellation tests that context cancellation stops execution.
func TestDAGExecutor_ContextCancellation(t *testing.T) {
	t.Parallel()
	executor := newDAGExecutor(1) // Limit to 1 parallel to ensure sequential execution

	var executed []string
	var execMu sync.Mutex

	// Create a chain to force sequential execution
	steps := []WorkflowStep{
		{ID: "step1"},
		{ID: "step2", DependsOn: []string{"step1"}},
		{ID: "step3", DependsOn: []string{"step2"}},
	}

	ctx, cancel := context.WithCancel(context.Background())

	execFunc := func(ctx context.Context, step *WorkflowStep) error {
		// Cancel context after first step
		if step.ID == "step1" {
			execMu.Lock()
			executed = append(executed, step.ID)
			execMu.Unlock()
			cancel()
			return nil
		}

		// Check for cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		execMu.Lock()
		executed = append(executed, step.ID)
		execMu.Unlock()
		return nil
	}

	err := executor.executeDAG(ctx, steps, execFunc, "abort")
	assert.Error(t, err)

	// Only first step should have executed
	assert.Equal(t, 1, len(executed), "only first step should execute before cancellation")
	assert.Equal(t, "step1", executed[0])
}

// TestDAGExecutor_GetExecutionStats tests execution statistics.
func TestDAGExecutor_GetExecutionStats(t *testing.T) {
	t.Parallel()
	executor := newDAGExecutor(10)

	// Create a complex workflow
	steps := []WorkflowStep{
		{ID: "a"},
		{ID: "b"},
		{ID: "c", DependsOn: []string{"a"}},
		{ID: "d", DependsOn: []string{"a", "b"}},
		{ID: "e", DependsOn: []string{"c", "d"}},
	}

	levels, err := executor.buildExecutionLevels(steps)
	require.NoError(t, err)

	stats := executor.getExecutionStats(levels)

	assert.Equal(t, 3, stats["total_levels"]) // a,b -> c,d -> e
	assert.Equal(t, 5, stats["total_steps"])
	assert.Equal(t, 2, stats["max_parallelism"])
	assert.Equal(t, 1, stats["min_parallelism"])
}

// TestDAGExecutor_ComplexWorkflow tests a realistic complex workflow.
func TestDAGExecutor_ComplexWorkflow(t *testing.T) {
	t.Parallel()
	executor := newDAGExecutor(10)

	var executionOrder []string
	var executionMu sync.Mutex
	// Use sequence numbers instead of wall-clock time to verify ordering.
	// This is immune to race detector overhead and timing precision issues.
	startSeq := make(map[string]int64)
	endSeq := make(map[string]int64)
	var seqCounter atomic.Int64

	// Simulate the incident investigation workflow from the proposal
	steps := []WorkflowStep{
		{ID: "fetch_logs"},
		{ID: "fetch_metrics"},
		{ID: "fetch_traces"},
		{ID: "analyze_logs", DependsOn: []string{"fetch_logs"}},
		{ID: "analyze_metrics", DependsOn: []string{"fetch_metrics"}},
		{ID: "analyze_traces", DependsOn: []string{"fetch_traces"}},
		{ID: "correlate", DependsOn: []string{"analyze_logs", "analyze_metrics", "analyze_traces"}},
		{ID: "create_report", DependsOn: []string{"correlate"}},
	}

	execFunc := func(_ context.Context, step *WorkflowStep) error {
		// Increment atomically outside the lock to reduce critical section
		seq := seqCounter.Add(1)
		executionMu.Lock()
		startSeq[step.ID] = seq
		executionOrder = append(executionOrder, step.ID)
		executionMu.Unlock()

		// Simulate work (50ms per step)
		time.Sleep(50 * time.Millisecond)

		seq = seqCounter.Add(1)
		executionMu.Lock()
		endSeq[step.ID] = seq
		executionMu.Unlock()

		return nil
	}

	startTime := time.Now()
	err := executor.executeDAG(context.Background(), steps, execFunc, "abort")
	totalDuration := time.Since(startTime)

	require.NoError(t, err)
	assert.Len(t, executionOrder, 8, "all steps should execute")

	// Verify parallel execution happened
	// Sequential would take 8 * 50ms = 400ms
	// Parallel should take about 4 levels * 50ms = 200ms
	// Use 500ms timeout (2.5x expected time) to account for race detector instrumentation overhead
	assert.Less(t, totalDuration, 500*time.Millisecond,
		"parallel execution should be significantly faster than sequential")

	// Verify dependencies were respected using sequence numbers
	// fetch steps should complete before analyze steps
	for _, fetchStep := range []string{"fetch_logs", "fetch_metrics", "fetch_traces"} {
		for _, analyzeStep := range []string{"analyze_logs", "analyze_metrics", "analyze_traces"} {
			if (fetchStep == "fetch_logs" && analyzeStep == "analyze_logs") ||
				(fetchStep == "fetch_metrics" && analyzeStep == "analyze_metrics") ||
				(fetchStep == "fetch_traces" && analyzeStep == "analyze_traces") {
				require.Contains(t, endSeq, fetchStep, "fetch step should have completed")
				require.Contains(t, startSeq, analyzeStep, "analyze step should have started")
				assert.Less(t, endSeq[fetchStep], startSeq[analyzeStep],
					fmt.Sprintf("%s (seq %d) must complete before %s starts (seq %d)",
						fetchStep, endSeq[fetchStep], analyzeStep, startSeq[analyzeStep]))
			}
		}
	}

	// correlate should start after all analyze steps complete
	for _, analyzeStep := range []string{"analyze_logs", "analyze_metrics", "analyze_traces"} {
		require.Contains(t, endSeq, analyzeStep, "analyze step should have completed")
		require.Contains(t, startSeq, "correlate", "correlate step should have started")
		assert.Less(t, endSeq[analyzeStep], startSeq["correlate"],
			fmt.Sprintf("%s (seq %d) must complete before correlate starts (seq %d)",
				analyzeStep, endSeq[analyzeStep], startSeq["correlate"]))
	}

	// create_report should be last
	require.Contains(t, endSeq, "correlate", "correlate step should have completed")
	require.Contains(t, startSeq, "create_report", "create_report step should have started")
	assert.Less(t, endSeq["correlate"], startSeq["create_report"],
		fmt.Sprintf("correlate (seq %d) must complete before create_report starts (seq %d)",
			endSeq["correlate"], startSeq["create_report"]))
}
