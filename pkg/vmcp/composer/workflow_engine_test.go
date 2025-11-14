package composer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
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
		Timeout: 50 * time.Millisecond,
		Steps: []WorkflowStep{
			toolStep("s1", "test.tool", nil),
			toolStep("s2", "test.tool", nil),
		},
	}

	target := &vmcp.BackendTarget{WorkloadID: "test", BaseURL: "http://test:8080"}
	te.Router.EXPECT().RouteTool(gomock.Any(), "test.tool").Return(target, nil)
	te.Backend.EXPECT().CallTool(gomock.Any(), target, "test.tool", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (map[string]any, error) {
			time.Sleep(60 * time.Millisecond) // Exceed workflow timeout
			return map[string]any{"ok": true}, nil
		})

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
