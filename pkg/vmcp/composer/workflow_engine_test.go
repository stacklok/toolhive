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
			Return(map[string]any{"ok": true}, nil),
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
	engine := NewWorkflowEngine(mockRouter, mockBackend, nil, stateStore)

	// Track execution timing to verify parallel execution
	var executionMu sync.Mutex
	startTimes := make(map[string]time.Time)
	endTimes := make(map[string]time.Time)
	var concurrentCount int32
	var maxConcurrent int32

	// Helper to track execution timing
	trackStart := func(stepID string) {
		executionMu.Lock()
		startTimes[stepID] = time.Now()
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
		executionMu.Lock()
		endTimes[stepID] = time.Now()
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
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (map[string]any, error) {
			trackStart("fetch_logs")
			time.Sleep(50 * time.Millisecond)
			trackEnd("fetch_logs")
			return map[string]any{"data": "log_data"}, nil
		})

	// fetch_metrics
	mockRouter.EXPECT().RouteTool(gomock.Any(), "test.fetch").Return(target, nil)
	mockBackend.EXPECT().CallTool(gomock.Any(), target, "test.fetch", map[string]any{"type": "metrics"}).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (map[string]any, error) {
			trackStart("fetch_metrics")
			time.Sleep(50 * time.Millisecond)
			trackEnd("fetch_metrics")
			return map[string]any{"data": "metrics_data"}, nil
		})

	// create_report
	mockRouter.EXPECT().RouteTool(gomock.Any(), "test.report").Return(target, nil)
	mockBackend.EXPECT().CallTool(gomock.Any(), target, "test.report", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (map[string]any, error) {
			trackStart("create_report")
			time.Sleep(30 * time.Millisecond)
			trackEnd("create_report")
			return map[string]any{"issue": "created"}, nil
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
	// Parallel should be: max(50,50)+30 = 80ms
	// With overhead, should be < 120ms
	assert.Less(t, totalDuration, 120*time.Millisecond,
		"parallel execution should be faster than sequential")

	// Verify concurrency - at least 2 steps should run concurrently
	assert.GreaterOrEqual(t, int(maxConcurrent), 2,
		"at least 2 steps should run concurrently")

	// Verify both fetch steps completed before report
	if len(startTimes) >= 3 && len(endTimes) >= 2 {
		assert.True(t, endTimes["fetch_logs"].Before(startTimes["create_report"]),
			"fetch_logs should complete before create_report starts")
		assert.True(t, endTimes["fetch_metrics"].Before(startTimes["create_report"]),
			"fetch_metrics should complete before create_report starts")
	}
}

func TestWorkflowEngine_ExecuteWorkflow_WithOutputFormat(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// Workflow with output_format for aggregation
	def := &WorkflowDefinition{
		Name: "aggregate-workflow",
		Steps: []WorkflowStep{
			toolStep("fetch_logs", "logs.fetch", nil),
			toolStep("fetch_metrics", "metrics.fetch", nil),
		},
		OutputFormat: `{
			"logs": {{json .steps.fetch_logs.output}},
			"metrics": {{json .steps.fetch_metrics.output}},
			"workflow_id": "{{.workflow.id}}"
		}`,
	}

	// Set up expectations
	te.expectToolCall("logs.fetch", nil, map[string]any{"count": 100})
	te.expectToolCall("metrics.fetch", nil, map[string]any{"cpu": "50%"})

	result, err := execute(t, te.Engine, def, nil)

	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Verify aggregated output
	require.NotNil(t, result.Output)

	// Check aggregated fields
	logs, exists := result.Output["logs"]
	require.True(t, exists, "logs field should exist")
	assert.Equal(t, map[string]any{"count": float64(100)}, logs)

	metrics, exists := result.Output["metrics"]
	require.True(t, exists, "metrics field should exist")
	assert.Equal(t, map[string]any{"cpu": "50%"}, metrics)

	workflowID, exists := result.Output["workflow_id"]
	require.True(t, exists, "workflow_id field should exist")
	assert.NotEmpty(t, workflowID)
}

func TestWorkflowEngine_ExecuteWorkflow_WithoutOutputFormat(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// Workflow WITHOUT output_format (backward compatibility)
	// Using sequential steps (step2 depends on step1) to ensure order
	def := &WorkflowDefinition{
		Name: "backward-compat",
		Steps: []WorkflowStep{
			toolStep("step1", "tool.one", nil),
			toolStepWithDeps("step2", "tool.two", nil, []string{"step1"}),
		},
	}

	te.expectToolCall("tool.one", nil, map[string]any{"result": "first"})
	te.expectToolCall("tool.two", nil, map[string]any{"result": "last"})

	result, err := execute(t, te.Engine, def, nil)

	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Should return last step output (backward compatible)
	// Since step2 depends on step1, step2 will always complete last
	assert.Equal(t, map[string]any{"result": "last"}, result.Output)
}

func TestWorkflowEngine_ExecuteWorkflow_OutputFormatWithMetadata(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	def := &WorkflowDefinition{
		Name: "metadata-workflow",
		Steps: []WorkflowStep{
			toolStep("fetch_data", "data.fetch", nil),
		},
		OutputFormat: `{
			"data": {{json .steps.fetch_data.output}},
			"metadata": {
				"workflow_id": "{{.workflow.id}}",
				"step_count": {{.workflow.step_count}},
				"duration_ms": {{.workflow.duration_ms}}
			}
		}`,
	}

	te.expectToolCall("data.fetch", nil, map[string]any{"value": "test"})

	result, err := execute(t, te.Engine, def, nil)

	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	// Check data field
	data := result.Output["data"]
	assert.Equal(t, map[string]any{"value": "test"}, data)

	// Check metadata
	metadata, exists := result.Output["metadata"]
	require.True(t, exists)
	metadataMap, ok := metadata.(map[string]any)
	require.True(t, ok)

	assert.NotEmpty(t, metadataMap["workflow_id"])
	assert.Equal(t, float64(1), metadataMap["step_count"])
	assert.GreaterOrEqual(t, metadataMap["duration_ms"], float64(0))
}

func TestWorkflowEngine_ExecuteWorkflow_OutputFormatInvalid(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	// Workflow with invalid output_format template
	def := &WorkflowDefinition{
		Name: "invalid-format",
		Steps: []WorkflowStep{
			toolStep("step1", "tool.one", nil),
		},
		OutputFormat: `{"invalid json syntax`,
	}

	te.expectToolCall("tool.one", nil, map[string]any{"result": "ok"})

	result, err := execute(t, te.Engine, def, nil)

	// Should fail due to invalid output format
	require.Error(t, err)
	assert.Contains(t, err.Error(), "output format")
	assert.Equal(t, WorkflowStatusFailed, result.Status)
}

func TestWorkflowEngine_ExecuteWorkflow_OutputFormatWithParameters(t *testing.T) {
	t.Parallel()
	te := newTestEngine(t)

	def := &WorkflowDefinition{
		Name: "params-workflow",
		Steps: []WorkflowStep{
			toolStep("fetch", "data.fetch", nil),
		},
		OutputFormat: `{
			"incident_id": "{{.params.incident_id}}",
			"data": {{json .steps.fetch.output}}
		}`,
	}

	te.expectToolCall("data.fetch", nil, map[string]any{"status": "resolved"})

	result, err := execute(t, te.Engine, def, map[string]any{"incident_id": "INC-123"})

	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)

	assert.Equal(t, "INC-123", result.Output["incident_id"])
	assert.Equal(t, map[string]any{"status": "resolved"}, result.Output["data"])
}
