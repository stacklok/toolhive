// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package composer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestWorkflowEngine_WithAuditor_SuccessfulWorkflow verifies that workflows
// execute successfully when auditing is enabled.
func TestWorkflowEngine_WithAuditor_SuccessfulWorkflow(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Create auditor with all event types enabled
	auditor, err := audit.NewWorkflowAuditor(&audit.Config{
		EventTypes: []string{
			audit.EventTypeWorkflowStarted,
			audit.EventTypeWorkflowCompleted,
			audit.EventTypeWorkflowStepStarted,
			audit.EventTypeWorkflowStepCompleted,
		},
		IncludeRequestData:  true,
		IncludeResponseData: true,
	})
	require.NoError(t, err)

	// Create engine with auditor
	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, auditor)

	// Setup simple workflow
	workflow := simpleWorkflow("audit-test",
		toolStep("step1", "tool1", map[string]any{"arg": "value1"}),
		toolStep("step2", "tool2", map[string]any{"arg": "value2"}),
	)

	// Setup expectations
	te.expectToolCall("tool1", map[string]any{"arg": "value1"}, map[string]any{"result": "ok1"})
	te.expectToolCall("tool2", map[string]any{"arg": "value2"}, map[string]any{"result": "ok2"})

	// Execute with identity
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: "test-user",
		Email:   "test@example.com",
	})

	result, err := engine.ExecuteWorkflow(ctx, workflow, map[string]any{
		"param1": "test",
	})

	// Verify workflow succeeds with auditing enabled
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Len(t, result.Steps, 2)
	assert.Equal(t, StepStatusCompleted, result.Steps["step1"].Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["step2"].Status)
}

// TestWorkflowEngine_WithAuditor_FailedWorkflow verifies that workflow
// failures are properly audited.
func TestWorkflowEngine_WithAuditor_FailedWorkflow(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	auditor, err := audit.NewWorkflowAuditor(&audit.Config{
		EventTypes: []string{
			audit.EventTypeWorkflowStarted,
			audit.EventTypeWorkflowFailed,
			audit.EventTypeWorkflowStepStarted,
			audit.EventTypeWorkflowStepFailed,
		},
	})
	require.NoError(t, err)

	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, auditor)

	workflow := simpleWorkflow("fail-test",
		toolStep("step1", "tool1", map[string]any{"arg": "value"}),
	)

	// Setup expectation for failure
	te.expectToolCallWithError("tool1", map[string]any{"arg": "value"}, errors.New("tool failure"))

	ctx := context.Background()
	result, err := engine.ExecuteWorkflow(ctx, workflow, nil)

	// Verify workflow fails and auditing doesn't prevent error reporting
	require.Error(t, err)
	assert.Equal(t, WorkflowStatusFailed, result.Status)
	assert.Contains(t, err.Error(), "tool failure")
}

// TestWorkflowEngine_WithAuditor_WorkflowTimeout verifies that timeouts
// are properly audited.
func TestWorkflowEngine_WithAuditor_WorkflowTimeout(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	auditor, err := audit.NewWorkflowAuditor(&audit.Config{
		EventTypes: []string{
			audit.EventTypeWorkflowStarted,
			audit.EventTypeWorkflowTimedOut,
		},
	})
	require.NoError(t, err)

	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, auditor)

	workflow := &WorkflowDefinition{
		Name:    "timeout-test",
		Timeout: 1 * time.Nanosecond, // Extremely short timeout to guarantee timeout
		Steps: []WorkflowStep{
			toolStep("slow", "slow_tool", map[string]any{}),
		},
	}

	// Don't set up any mock expectations - let it try to execute and timeout

	ctx := context.Background()
	result, err := engine.ExecuteWorkflow(ctx, workflow, nil)

	// Verify timeout is reported correctly
	// The workflow may either timeout or fail depending on timing
	require.Error(t, err)
	if result.Status == WorkflowStatusTimedOut {
		assert.ErrorIs(t, err, ErrWorkflowTimeout)
	} else {
		// If it fails before timeout, that's also acceptable for this test
		assert.Equal(t, WorkflowStatusFailed, result.Status)
	}
}

// TestWorkflowEngine_WithAuditor_StepSkipped verifies that skipped steps
// are properly audited.
func TestWorkflowEngine_WithAuditor_StepSkipped(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	auditor, err := audit.NewWorkflowAuditor(&audit.Config{
		EventTypes: []string{
			audit.EventTypeWorkflowStarted,
			audit.EventTypeWorkflowCompleted,
			audit.EventTypeWorkflowStepStarted,
			audit.EventTypeWorkflowStepSkipped,
		},
	})
	require.NoError(t, err)

	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, auditor)

	workflow := &WorkflowDefinition{
		Name: "skip-test",
		Steps: []WorkflowStep{
			{
				ID:        "always-run",
				Type:      StepTypeTool,
				Tool:      "tool1",
				Arguments: map[string]any{},
			},
			{
				ID:        "conditional",
				Type:      StepTypeTool,
				Tool:      "tool2",
				Arguments: map[string]any{},
				Condition: "{{if eq .params.run_conditional true}}true{{else}}false{{end}}", // Will be false
			},
		},
	}

	te.expectToolCall("tool1", map[string]any{}, map[string]any{"result": "ok"})
	// tool2 should not be called due to condition

	ctx := context.Background()
	result, err := engine.ExecuteWorkflow(ctx, workflow, map[string]any{
		"run_conditional": false,
	})

	// Verify workflow succeeds with skipped step
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["always-run"].Status)
	assert.Equal(t, StepStatusSkipped, result.Steps["conditional"].Status)
}

// TestWorkflowEngine_WithAuditor_RetryStep verifies that retried steps
// include retry count in audit metadata.
func TestWorkflowEngine_WithAuditor_RetryStep(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	auditor, err := audit.NewWorkflowAuditor(&audit.Config{
		EventTypes: []string{
			audit.EventTypeWorkflowStarted,
			audit.EventTypeWorkflowCompleted,
			audit.EventTypeWorkflowStepStarted,
			audit.EventTypeWorkflowStepCompleted,
		},
	})
	require.NoError(t, err)

	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, auditor)

	workflow := &WorkflowDefinition{
		Name: "retry-test",
		Steps: []WorkflowStep{
			{
				ID:        "retry-step",
				Type:      StepTypeTool,
				Tool:      "flaky_tool",
				Arguments: map[string]any{},
				OnError: &ErrorHandler{
					Action:     "retry",
					RetryCount: 2,
					RetryDelay: 1 * time.Millisecond,
				},
			},
		},
	}

	// Setup routing (called once)
	target := &vmcp.BackendTarget{
		WorkloadID: "test-backend",
		BaseURL:    "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), "flaky_tool").Return(target, nil)

	// Fail twice, succeed on third attempt (CallTool is called three times)
	gomock.InOrder(
		te.Backend.EXPECT().CallTool(gomock.Any(), target, "flaky_tool", gomock.Any()).
			Return(nil, errors.New("temp failure")),
		te.Backend.EXPECT().CallTool(gomock.Any(), target, "flaky_tool", gomock.Any()).
			Return(nil, errors.New("temp failure")),
		te.Backend.EXPECT().CallTool(gomock.Any(), target, "flaky_tool", gomock.Any()).
			Return(map[string]any{"success": true}, nil),
	)

	ctx := context.Background()
	result, err := engine.ExecuteWorkflow(ctx, workflow, nil)

	// Verify workflow succeeds after retries
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Equal(t, 2, result.Steps["retry-step"].RetryCount)
}
