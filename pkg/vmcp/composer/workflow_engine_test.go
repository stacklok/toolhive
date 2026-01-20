// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package composer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routermocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
)

func TestWorkflowEngine_ExecuteWorkflow_Success(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// Two-step workflow: create issue -> add label
	def := simpleWorkflow("test-workflow",
		toolStep("create_issue", "github.create_issue", map[string]any{
			"title": "{{.params.title}}",
			"body":  "Test body",
		}),
		toolStepWithDeps("add_label", "github.add_label", map[string]any{
			"issue": "{{.steps.create_issue.output.number}}",
			"label": "bug",
		}, []string{"create_issue"}),
	)

	// Expectations
	te.expectToolCall("github.create_issue",
		map[string]any{"title": "Test Issue", "body": "Test body"},
		map[string]any{"number": 123, "url": "https://github.com/org/repo/issues/123"})

	te.expectToolCallWithAnyArgs("github.add_label", map[string]any{"success": true})

	// Execute
	result, err := execute(t, te.Engine, def, map[string]any{"title": "Test Issue"})

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Len(t, result.Steps, 2)
	assert.Equal(t, StepStatusCompleted, result.Steps["create_issue"].Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["add_label"].Status)
}

func TestWorkflowEngine_ExecuteWorkflow_StepFailure(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	def := simpleWorkflow("test", toolStep("fail", "test.tool", map[string]any{"p": "v"}))

	te.expectToolCallWithError("test.tool", map[string]any{"p": "v"}, errors.New("tool failed"))

	result, err := execute(t, te.Engine, def, nil)

	require.Error(t, err)
	assert.Equal(t, WorkflowStatusFailed, result.Status)
	assert.Equal(t, StepStatusFailed, result.Steps["fail"].Status)
}

func TestWorkflowEngine_ExecuteWorkflow_WithRetry(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	def := &WorkflowDefinition{
		Name: "retry-test",
		Steps: []WorkflowStep{{
			ID:   "flaky",
			Type: StepTypeTool,
			Tool: "test.tool",
			OnError: &ErrorHandler{
				Action:     "retry",
				RetryCount: 2,
				RetryDelay: 10 * time.Millisecond,
			},
		}},
	}

	target := &vmcp.BackendTarget{WorkloadID: "test", BaseURL: "http://test:8080"}
	te.Router.EXPECT().RouteTool(gomock.Any(), "test.tool").Return(target, nil)

	// Fail once, then succeed
	gomock.InOrder(
		te.Backend.EXPECT().CallTool(gomock.Any(), target, "test.tool", gomock.Any()).
			Return(nil, errors.New("temp fail")),
		te.Backend.EXPECT().CallTool(gomock.Any(), target, "test.tool", gomock.Any()).
			Return(&vmcp.ToolCallResult{
				StructuredContent: map[string]any{"ok": true},
				Content:           []vmcp.Content{},
			}, nil),
	)

	result, err := execute(t, te.Engine, def, nil)

	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Equal(t, 1, result.Steps["flaky"].RetryCount)
}

func TestWorkflowEngine_ExecuteWorkflow_ConditionalSkip(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	def := &WorkflowDefinition{
		Name: "conditional",
		Steps: []WorkflowStep{
			toolStep("always", "test.tool1", nil),
			{
				ID:        "conditional",
				Type:      StepTypeTool,
				Tool:      "test.tool2",
				Condition: "{{if eq .params.enabled true}}true{{else}}false{{end}}",
			},
		},
	}

	te.expectToolCall("test.tool1", nil, map[string]any{"ok": true})
	// tool2 should NOT be called (condition is false)

	result, err := execute(t, te.Engine, def, map[string]any{"enabled": false})

	require.NoError(t, err)
	assert.Equal(t, StepStatusCompleted, result.Steps["always"].Status)
	assert.Equal(t, StepStatusSkipped, result.Steps["conditional"].Status)
}

func TestWorkflowEngine_ValidateWorkflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		def    *WorkflowDefinition
		errMsg string
	}{
		{"valid", simpleWorkflow("test", toolStep("s1", "t1", nil)), ""},
		{"nil workflow", nil, "workflow definition is nil"},
		{"missing name", &WorkflowDefinition{Steps: []WorkflowStep{toolStep("s1", "t1", nil)}}, "name is required"},
		{"no steps", &WorkflowDefinition{Name: "test"}, "at least one step"},
		{"duplicate IDs", simpleWorkflow("test", toolStep("s1", "t1", nil), toolStep("s1", "t2", nil)), "duplicate step ID"},
		{"circular deps", simpleWorkflow("test",
			toolStepWithDeps("s1", "t1", nil, []string{"s2"}),
			toolStepWithDeps("s2", "t2", nil, []string{"s1"})), "circular dependency"},
		{"invalid dep", simpleWorkflow("test", toolStepWithDeps("s1", "t1", nil, []string{"unknown"})), "non-existent"},
		{"too many steps", &WorkflowDefinition{Name: "test", Steps: make([]WorkflowStep, 101)}, "too many steps"},
	}

	te := newTestEngine(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := te.Engine.ValidateWorkflow(context.Background(), tt.def)
			if tt.errMsg == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			}
		})
	}
}

func TestWorkflowEngine_ExecuteWorkflow_Timeout(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	def := &WorkflowDefinition{
		Name:    "timeout-test",
		Timeout: 30 * time.Millisecond, // Shorter timeout for reliable test
		Steps: []WorkflowStep{
			toolStep("s1", "test.tool", nil),
			toolStep("s2", "test.tool", nil),
		},
	}

	target := &vmcp.BackendTarget{WorkloadID: "test", BaseURL: "http://test:8080"}
	// Both steps can run in parallel, so expect multiple calls
	te.Router.EXPECT().RouteTool(gomock.Any(), "test.tool").Return(target, nil).AnyTimes()
	te.Backend.EXPECT().CallTool(gomock.Any(), target, "test.tool", gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (map[string]any, error) {
			// Sleep longer than workflow timeout, but respect context cancellation
			select {
			case <-time.After(100 * time.Millisecond):
				return map[string]any{"ok": true}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}).AnyTimes()

	result, err := execute(t, te.Engine, def, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrWorkflowTimeout)
	assert.Equal(t, WorkflowStatusTimedOut, result.Status)
}

func TestWorkflowEngine_ExecuteWorkflow_ParameterDefaults(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// Workflow with parameter that has a default value
	def := &WorkflowDefinition{
		Name: "with-defaults",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":    "string",
					"default": "https://default.example.com",
				},
				"count": map[string]any{
					"type":    "integer",
					"default": 42,
				},
			},
		},
		Steps: []WorkflowStep{
			toolStep("fetch", "fetch.tool", map[string]any{
				"url":   "{{.params.url}}",
				"count": "{{.params.count}}",
			}),
		},
	}

	// Expect tool call with default values applied
	te.expectToolCall("fetch.tool",
		map[string]any{"url": "https://default.example.com", "count": "42"},
		map[string]any{"status": "ok"})

	// Execute with empty params - defaults should be applied
	result, err := execute(t, te.Engine, def, map[string]any{})

	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
}

func TestWorkflowEngine_ExecuteWorkflow_ParameterDefaultsOverride(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// Workflow with parameter defaults
	def := &WorkflowDefinition{
		Name: "with-defaults",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":    "string",
					"default": "https://default.example.com",
				},
			},
		},
		Steps: []WorkflowStep{
			toolStep("fetch", "fetch.tool", map[string]any{
				"url": "{{.params.url}}",
			}),
		},
	}

	// Expect tool call with client-provided value (not default)
	te.expectToolCall("fetch.tool",
		map[string]any{"url": "https://custom.example.com"},
		map[string]any{"status": "ok"})

	// Execute with explicit param - should override default
	result, err := execute(t, te.Engine, def, map[string]any{
		"url": "https://custom.example.com",
	})

	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
}

// TestWorkflowEngine_ParallelExecution tests parallel workflow execution
// with dependencies, demonstrating the DAG-based execution model.
func TestWorkflowEngine_ParallelExecution(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRouter := routermocks.NewMockRouter(ctrl)
	mockBackend := mocks.NewMockBackendClient(ctrl)
	stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	engine := NewWorkflowEngine(mockRouter, mockBackend, nil, stateStore, nil)

	// Track execution timing to verify parallel execution
	var executionMu sync.Mutex
	// Use sequence numbers instead of wall-clock time to verify ordering.
	// This is immune to race detector overhead and timing precision issues.
	startSeq := make(map[string]int64)
	endSeq := make(map[string]int64)
	var seqCounter atomic.Int64
	var concurrentCount int32
	var maxConcurrent int32

	// Helper to track execution timing
	trackStart := func(stepID string) {
		// Increment atomically outside the lock to reduce critical section
		seq := seqCounter.Add(1)
		executionMu.Lock()
		startSeq[stepID] = seq
		executionMu.Unlock()

		// Track concurrency
		current := atomic.AddInt32(&concurrentCount, 1)
		for {
			maxVal := atomic.LoadInt32(&maxConcurrent)
			if current <= maxVal || atomic.CompareAndSwapInt32(&maxConcurrent, maxVal, current) {
				break
			}
		}
	}

	trackEnd := func(stepID string) {
		atomic.AddInt32(&concurrentCount, -1)
		seq := seqCounter.Add(1)
		executionMu.Lock()
		endSeq[stepID] = seq
		executionMu.Unlock()
	}

	// Create a simple workflow that demonstrates parallel execution:
	// Level 1 (parallel): fetch_logs, fetch_metrics
	// Level 2 (sequential): create_report
	workflow := &WorkflowDefinition{
		Name: "incident-investigation-e2e",
		Steps: []WorkflowStep{
			{
				ID:        "fetch_logs",
				Type:      StepTypeTool,
				Tool:      "test.fetch",
				Arguments: map[string]any{"type": "logs"},
			},
			{
				ID:        "fetch_metrics",
				Type:      StepTypeTool,
				Tool:      "test.fetch",
				Arguments: map[string]any{"type": "metrics"},
			},
			{
				ID:        "create_report",
				Type:      StepTypeTool,
				Tool:      "test.report",
				DependsOn: []string{"fetch_logs", "fetch_metrics"},
				Arguments: map[string]any{
					"logs":    "{{.steps.fetch_logs.output.data}}",
					"metrics": "{{.steps.fetch_metrics.output.data}}",
				},
			},
		},
	}

	// Setup mock expectations with timing tracking
	target := &vmcp.BackendTarget{WorkloadID: "test-backend", BaseURL: "http://test:8080"}

	// fetch_logs
	mockRouter.EXPECT().RouteTool(gomock.Any(), "test.fetch").Return(target, nil)
	mockBackend.EXPECT().CallTool(gomock.Any(), target, "test.fetch", map[string]any{"type": "logs"}).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (*vmcp.ToolCallResult, error) {
			trackStart("fetch_logs")
			time.Sleep(50 * time.Millisecond)
			trackEnd("fetch_logs")
			return &vmcp.ToolCallResult{
				StructuredContent: map[string]any{"data": "log_data"},
				Content:           []vmcp.Content{},
			}, nil
		})

	// fetch_metrics
	mockRouter.EXPECT().RouteTool(gomock.Any(), "test.fetch").Return(target, nil)
	mockBackend.EXPECT().CallTool(gomock.Any(), target, "test.fetch", map[string]any{"type": "metrics"}).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (*vmcp.ToolCallResult, error) {
			trackStart("fetch_metrics")
			time.Sleep(50 * time.Millisecond)
			trackEnd("fetch_metrics")
			return &vmcp.ToolCallResult{
				StructuredContent: map[string]any{"data": "metrics_data"},
				Content:           []vmcp.Content{},
			}, nil
		})

	// create_report
	mockRouter.EXPECT().RouteTool(gomock.Any(), "test.report").Return(target, nil)
	mockBackend.EXPECT().CallTool(gomock.Any(), target, "test.report", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (*vmcp.ToolCallResult, error) {
			trackStart("create_report")
			time.Sleep(30 * time.Millisecond)
			trackEnd("create_report")
			return &vmcp.ToolCallResult{
				StructuredContent: map[string]any{"issue": "created"},
				Content:           []vmcp.Content{},
			}, nil
		})

	// Execute workflow
	startTime := time.Now()
	result, err := engine.ExecuteWorkflow(context.Background(), workflow, nil)
	totalDuration := time.Since(startTime)

	// Verify execution succeeded
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify state store captured workflow state
	status, err := engine.GetWorkflowStatus(context.Background(), result.WorkflowID)
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, status.Status)
	assert.Equal(t, 3, len(status.CompletedSteps))

	// Verify all steps executed
	assert.Len(t, result.Steps, 3, "all 3 steps should have results")

	// Verify parallel execution performance
	// Sequential would be: 50+50+30 = 130ms
	// Parallel should be: max(50,50)+30 = 80ms expected
	// Use 200ms timeout (2.5x expected time) to account for race detector instrumentation overhead
	assert.Less(t, totalDuration, 200*time.Millisecond,
		"parallel execution should be faster than sequential")

	// Verify concurrency - at least 2 steps should run concurrently
	assert.GreaterOrEqual(t, int(maxConcurrent), 2,
		"at least 2 steps should run concurrently")

	// Verify both fetch steps completed before report using sequence numbers
	require.Len(t, startSeq, 3, "all steps should have start sequences")
	require.Len(t, endSeq, 3, "all steps should have end sequences")
	assert.Less(t, endSeq["fetch_logs"], startSeq["create_report"],
		"fetch_logs (seq %d) should complete before create_report starts (seq %d)",
		endSeq["fetch_logs"], startSeq["create_report"])
	assert.Less(t, endSeq["fetch_metrics"], startSeq["create_report"],
		"fetch_metrics (seq %d) should complete before create_report starts (seq %d)",
		endSeq["fetch_metrics"], startSeq["create_report"])
}

func TestWorkflowEngine_ExecuteWorkflow_WithWorkflowMetadata(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow that uses workflow metadata in output
	workflow := &WorkflowDefinition{
		Name:        "metadata_test",
		Description: "Test workflow metadata in output templates",
		Steps: []WorkflowStep{
			toolStep("fetch_data", "data.fetch", map[string]any{
				"source": "{{.params.source}}",
			}),
			toolStepWithDeps("process", "data.process", map[string]any{
				"data": "{{.steps.fetch_data.output.result}}",
			}, []string{"fetch_data"}),
		},
		Output: &config.OutputConfig{
			Properties: map[string]config.OutputProperty{
				"summary": {
					Type:        "object",
					Description: "Workflow execution summary",
					Properties: map[string]config.OutputProperty{
						"workflow_id": {
							Type:        "string",
							Description: "Workflow execution ID",
							Value:       "{{.workflow.id}}",
						},
						"duration_ms": {
							Type:        "integer",
							Description: "Workflow duration in milliseconds",
							Value:       "{{.workflow.duration_ms}}",
						},
						"step_count": {
							Type:        "integer",
							Description: "Number of completed steps",
							Value:       "{{.workflow.step_count}}",
						},
						"status": {
							Type:        "string",
							Description: "Workflow status",
							Value:       "{{.workflow.status}}",
						},
						"start_time": {
							Type:        "string",
							Description: "Workflow start time",
							Value:       "{{.workflow.start_time}}",
						},
					},
				},
				"data_result": {
					Type:        "string",
					Description: "Processed data result",
					Value:       "{{.steps.process.output.value}}",
				},
			},
		},
	}

	// Setup expectations with delays to ensure duration > 0
	target := &vmcp.BackendTarget{
		WorkloadID:   "test-backend",
		WorkloadName: "test",
		BaseURL:      "http://test:8080",
	}

	te.Router.EXPECT().RouteTool(gomock.Any(), "data.fetch").Return(target, nil)
	te.Backend.EXPECT().CallTool(gomock.Any(), target, "data.fetch", map[string]any{"source": "test-source"}).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (*vmcp.ToolCallResult, error) {
			time.Sleep(10 * time.Millisecond)
			return &vmcp.ToolCallResult{
				StructuredContent: map[string]any{"result": "raw-data"},
				Content:           []vmcp.Content{},
			}, nil
		})

	te.Router.EXPECT().RouteTool(gomock.Any(), "data.process").Return(target, nil)
	te.Backend.EXPECT().CallTool(gomock.Any(), target, "data.process", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (*vmcp.ToolCallResult, error) {
			time.Sleep(10 * time.Millisecond)
			return &vmcp.ToolCallResult{
				StructuredContent: map[string]any{"value": "processed-data"},
				Content:           []vmcp.Content{},
			}, nil
		})

	// Execute workflow
	startTime := time.Now()
	result, err := execute(t, te.Engine, workflow, map[string]any{"source": "test-source"})
	executionTime := time.Since(startTime)

	// Verify execution success
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Len(t, result.Steps, 2)

	// Verify output structure
	require.NotNil(t, result.Output)
	require.Contains(t, result.Output, "summary")
	require.Contains(t, result.Output, "data_result")

	// Verify data result
	assert.Equal(t, "processed-data", result.Output["data_result"])

	// Verify workflow metadata in output
	summary, ok := result.Output["summary"].(map[string]any)
	require.True(t, ok, "summary should be a map")

	// Check workflow_id
	workflowID, ok := summary["workflow_id"].(string)
	require.True(t, ok, "workflow_id should be a string")
	assert.NotEmpty(t, workflowID)
	assert.Equal(t, result.WorkflowID, workflowID)

	// Check duration_ms
	durationMs, ok := summary["duration_ms"].(int64)
	require.True(t, ok, "duration_ms should be an int64")
	// With 20ms of artificial delays (10ms per step), duration should be at least a few ms
	assert.Greater(t, durationMs, int64(0), "duration should be positive")
	// Duration should be reasonable (less than total execution time in ms + buffer)
	assert.Less(t, durationMs, executionTime.Milliseconds()+100, "duration should be less than total execution time")

	// Check step_count
	stepCount, ok := summary["step_count"].(int64)
	require.True(t, ok, "step_count should be an int64")
	assert.Equal(t, int64(2), stepCount, "should have 2 completed steps")

	// Check status
	status, ok := summary["status"].(string)
	require.True(t, ok, "status should be a string")
	assert.Equal(t, "completed", status)

	// Check start_time (RFC3339 format)
	startTimeStr, ok := summary["start_time"].(string)
	require.True(t, ok, "start_time should be a string")
	assert.NotEmpty(t, startTimeStr)
	// Verify it's valid RFC3339 format
	parsedTime, err := time.Parse(time.RFC3339, startTimeStr)
	require.NoError(t, err, "start_time should be valid RFC3339 format")
	assert.WithinDuration(t, startTime, parsedTime, 5*time.Second, "start_time should be close to actual start")
}

func TestWorkflowEngine_WorkflowMetadataAvailableInTemplates(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Workflow that uses workflow metadata in step arguments
	// Note: workflow.id and workflow.start_time are available, but workflow.step_count and
	// workflow.duration_ms are only updated before output construction, not during step execution.
	workflow := &WorkflowDefinition{
		Name: "metadata_in_args",
		Steps: []WorkflowStep{
			toolStep("first", "tool.first", nil),
			toolStepWithDeps("second", "tool.second", map[string]any{
				"workflow_id": "{{.workflow.id}}",
				"status":      "{{.workflow.status}}",
			}, []string{"first"}),
		},
	}

	// Setup expectations
	te.expectToolCall("tool.first", nil, map[string]any{"ok": true})

	// For the second tool, verify it receives basic workflow metadata
	target := &vmcp.BackendTarget{
		WorkloadID:   "test-backend",
		WorkloadName: "test",
		BaseURL:      "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), "tool.second").Return(target, nil)
	te.Backend.EXPECT().CallTool(gomock.Any(), target, "tool.second", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, args map[string]any) (*vmcp.ToolCallResult, error) {
			// Verify workflow metadata was expanded in arguments
			workflowID, ok := args["workflow_id"].(string)
			assert.True(t, ok, "workflow_id should be a string")
			assert.NotEmpty(t, workflowID, "workflow_id should not be empty")

			// Status should be available (though not yet "completed" since workflow is still running)
			status, ok := args["status"].(string)
			assert.True(t, ok, "status should be a string")
			assert.Contains(t, []string{"pending", "running"}, status, "status should be pending or running during execution")

			return &vmcp.ToolCallResult{
				StructuredContent: map[string]any{"done": true},
				Content:           []vmcp.Content{},
			}, nil
		})

	// Execute workflow
	result, err := execute(t, te.Engine, workflow, nil)

	// Verify
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Len(t, result.Steps, 2)
}
