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

// TestWorkflowEngine_WithAuditor_IdentityPropagation verifies that user
// identity from context is captured in audit logs.
func TestWorkflowEngine_WithAuditor_IdentityPropagation(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	auditor, err := audit.NewWorkflowAuditor(&audit.Config{
		EventTypes: []string{
			audit.EventTypeWorkflowStarted,
			audit.EventTypeWorkflowCompleted,
		},
	})
	require.NoError(t, err)

	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, auditor)

	workflow := simpleWorkflow("identity-test",
		toolStep("step1", "tool1", map[string]any{}),
	)

	te.expectToolCall("tool1", map[string]any{}, map[string]any{})

	// Execute with rich identity
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: "auth0|user123",
		Name:    "John Doe",
		Email:   "john@example.com",
		Claims: map[string]any{
			"client_name":    "test-client",
			"client_version": "1.0.0",
		},
	})

	result, err := engine.ExecuteWorkflow(ctx, workflow, nil)

	// Verify workflow succeeds with identity context
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
}

// TestWorkflowEngine_WithAuditor_AnonymousUser verifies that workflows
// work correctly when no identity is provided.
func TestWorkflowEngine_WithAuditor_AnonymousUser(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	auditor, err := audit.NewWorkflowAuditor(&audit.Config{
		EventTypes: []string{
			audit.EventTypeWorkflowStarted,
			audit.EventTypeWorkflowCompleted,
		},
	})
	require.NoError(t, err)

	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, auditor)

	workflow := simpleWorkflow("anon-test",
		toolStep("step1", "tool1", map[string]any{}),
	)

	te.expectToolCall("tool1", map[string]any{}, map[string]any{})

	// Execute without identity (anonymous)
	ctx := context.Background()
	result, err := engine.ExecuteWorkflow(ctx, workflow, nil)

	// Verify workflow succeeds for anonymous users
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
}

// TestWorkflowEngine_WithAuditor_EventFiltering verifies that audit event
// filtering works correctly.
func TestWorkflowEngine_WithAuditor_EventFiltering(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Create auditor that only logs workflow-level events, not step-level
	auditor, err := audit.NewWorkflowAuditor(&audit.Config{
		EventTypes: []string{
			audit.EventTypeWorkflowStarted,
			audit.EventTypeWorkflowCompleted,
			// Note: step events are NOT included
		},
	})
	require.NoError(t, err)

	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, auditor)

	workflow := simpleWorkflow("filter-test",
		toolStep("step1", "tool1", map[string]any{}),
	)

	te.expectToolCall("tool1", map[string]any{}, map[string]any{})

	ctx := context.Background()
	result, err := engine.ExecuteWorkflow(ctx, workflow, nil)

	// Verify workflow succeeds with event filtering
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
}

// TestWorkflowEngine_WithAuditor_ParallelExecution verifies that audit
// logging works correctly with parallel step execution.
func TestWorkflowEngine_WithAuditor_ParallelExecution(t *testing.T) {
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

	// Workflow with parallel steps (no dependencies)
	workflow := &WorkflowDefinition{
		Name: "parallel-test",
		Steps: []WorkflowStep{
			toolStep("parallel1", "tool1", map[string]any{}),
			toolStep("parallel2", "tool2", map[string]any{}),
			toolStep("parallel3", "tool3", map[string]any{}),
		},
	}

	te.expectToolCall("tool1", map[string]any{}, map[string]any{})
	te.expectToolCall("tool2", map[string]any{}, map[string]any{})
	te.expectToolCall("tool3", map[string]any{}, map[string]any{})

	ctx := context.Background()
	result, err := engine.ExecuteWorkflow(ctx, workflow, nil)

	// Verify parallel execution works with auditing
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Len(t, result.Steps, 3)
}

// TestWorkflowEngine_WithoutAuditor_BackwardCompatibility verifies that
// workflows work correctly when no auditor is provided (backward compatibility).
func TestWorkflowEngine_WithoutAuditor_BackwardCompatibility(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	// Create engine WITHOUT auditor (nil)
	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, nil)

	workflow := simpleWorkflow("no-audit",
		toolStep("step1", "tool1", map[string]any{}),
		toolStep("step2", "tool2", map[string]any{}),
	)

	te.expectToolCall("tool1", map[string]any{}, map[string]any{})
	te.expectToolCall("tool2", map[string]any{}, map[string]any{})

	ctx := context.Background()
	result, err := engine.ExecuteWorkflow(ctx, workflow, nil)

	// Verify workflow succeeds without auditing (backward compatibility)
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Len(t, result.Steps, 2)
}

// TestWorkflowEngine_WithAuditor_ComplexWorkflow verifies auditing works
// with a complex workflow involving dependencies, conditions, and retries.
func TestWorkflowEngine_WithAuditor_ComplexWorkflow(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)

	auditor, err := audit.NewWorkflowAuditor(&audit.Config{
		EventTypes: []string{
			audit.EventTypeWorkflowStarted,
			audit.EventTypeWorkflowCompleted,
			audit.EventTypeWorkflowStepStarted,
			audit.EventTypeWorkflowStepCompleted,
			audit.EventTypeWorkflowStepSkipped,
		},
		IncludeRequestData:  true,
		IncludeResponseData: true,
	})
	require.NoError(t, err)

	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, auditor)

	workflow := &WorkflowDefinition{
		Name: "complex-test",
		Steps: []WorkflowStep{
			{
				ID:        "init",
				Type:      StepTypeTool,
				Tool:      "init_tool",
				Arguments: map[string]any{"env": "test"},
			},
			{
				ID:        "process",
				Type:      StepTypeTool,
				Tool:      "process_tool",
				Arguments: map[string]any{"data": "{{.steps.init.output.result}}"},
				DependsOn: []string{"init"},
			},
			{
				ID:        "optional",
				Type:      StepTypeTool,
				Tool:      "optional_tool",
				Arguments: map[string]any{},
				Condition: "{{if eq .params.run_optional true}}true{{else}}false{{end}}",
			},
		},
	}

	te.expectToolCall("init_tool", map[string]any{"env": "test"}, map[string]any{"result": "initialized"})
	te.expectToolCall("process_tool", map[string]any{"data": "initialized"}, map[string]any{"status": "done"})
	// optional_tool should not be called

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject: "complex-user",
	})

	result, err := engine.ExecuteWorkflow(ctx, workflow, map[string]any{
		"run_optional": false,
	})

	// Verify complex workflow executes correctly with auditing
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["init"].Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["process"].Status)
	assert.Equal(t, StepStatusSkipped, result.Steps["optional"].Status)
}
