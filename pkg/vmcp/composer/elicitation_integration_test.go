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

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

func TestWorkflowEngine_ExecuteElicitationStep_Accept(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)
	mockSDK := mocks.NewMockElicitationRequester(te.Ctrl)

	// Mock SDK to return accept response
	mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&vmcp.ElicitationResult{
		Action:  "accept",
		Content: map[string]any{"environment": "production"},
	}, nil)

	handler := NewDefaultElicitationHandler(mockSDK)
	stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil, nil)

	workflow := &WorkflowDefinition{
		Name: "deployment-workflow",
		Steps: []WorkflowStep{
			{
				ID:   "confirm",
				Type: StepTypeElicitation,
				Elicitation: &ElicitationConfig{
					Message: "Confirm deployment?",
					Schema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"environment": map[string]any{
								"type": "string",
								"enum": []string{"staging", "production"},
							},
						},
					},
					Timeout: 1 * time.Minute,
				},
			},
			{
				ID:        "deploy",
				Type:      StepTypeTool,
				Tool:      "deploy_tool",
				DependsOn: []string{"confirm"}, // Deploy only after user confirms to ensure user approval before deployment
				Arguments: map[string]any{
					"env": "{{.steps.confirm.output.content.environment}}",
				},
			},
		},
	}

	// Setup expectation for deploy tool call
	deployTarget := &vmcp.BackendTarget{
		WorkloadID: "deploy-backend",
		BaseURL:    "http://deploy:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), "deploy_tool").Return(deployTarget, nil)
	deployResult := &vmcp.ToolCallResult{
		StructuredContent: map[string]any{"status": "deployed"},
		Content:           []vmcp.Content{},
		IsError:           false,
		Meta:              nil,
	}
	te.Backend.EXPECT().CallTool(gomock.Any(), deployTarget, "deploy_tool", map[string]any{
		"env": "production",
	}, gomock.Any(), gomock.Any()).Return(deployResult, nil)

	result, err := engine.ExecuteWorkflow(context.Background(), workflow, nil)
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Len(t, result.Steps, 2)

	// Verify confirm step output
	confirmStep := result.Steps["confirm"]
	require.NotNil(t, confirmStep)
	assert.Equal(t, StepStatusCompleted, confirmStep.Status)
	assert.Equal(t, "accept", confirmStep.Output["action"])
	assert.Equal(t, map[string]any{"environment": "production"}, confirmStep.Output["content"])

	// Verify deploy step executed
	deployStep := result.Steps["deploy"]
	require.NotNil(t, deployStep)
	assert.Equal(t, StepStatusCompleted, deployStep.Status)
}

func TestWorkflowEngine_ExecuteElicitationStep_Decline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		onDecline      *ElicitationHandler
		wantStatus     WorkflowStatusType
		wantStepStatus StepStatusType
	}{
		{
			name:           "decline_without_handler",
			onDecline:      nil,
			wantStatus:     WorkflowStatusFailed,
			wantStepStatus: StepStatusFailed,
		},
		{
			name: "decline_with_abort",
			onDecline: &ElicitationHandler{
				Action: "abort",
			},
			wantStatus:     WorkflowStatusFailed,
			wantStepStatus: StepStatusFailed,
		},
		{
			name: "decline_with_continue",
			onDecline: &ElicitationHandler{
				Action: "continue",
			},
			wantStatus:     WorkflowStatusCompleted,
			wantStepStatus: StepStatusCompleted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			te := newTestEngine(t)
			mockSDK := mocks.NewMockElicitationRequester(te.Ctrl)

			// Mock SDK to return decline response
			mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&vmcp.ElicitationResult{
				Action: "decline",
			}, nil)

			handler := NewDefaultElicitationHandler(mockSDK)
			stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
			engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil, nil)

			workflow := &WorkflowDefinition{
				Name: "test-workflow",
				Steps: []WorkflowStep{
					{
						ID:   "confirm",
						Type: StepTypeElicitation,
						Elicitation: &ElicitationConfig{
							Message:   "Confirm?",
							Schema:    map[string]any{"type": "object"},
							Timeout:   1 * time.Minute,
							OnDecline: tt.onDecline,
						},
					},
				},
			}

			result, err := engine.ExecuteWorkflow(context.Background(), workflow, nil)

			// For failed workflows, ExecuteWorkflow returns both result and error
			if tt.wantStatus == WorkflowStatusFailed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.wantStatus, result.Status)
			confirmStep := result.Steps["confirm"]
			require.NotNil(t, confirmStep)
			assert.Equal(t, tt.wantStepStatus, confirmStep.Status)
		})
	}
}

func TestWorkflowEngine_ExecuteElicitationStep_Cancel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		onCancel       *ElicitationHandler
		wantStatus     WorkflowStatusType
		wantStepStatus StepStatusType
	}{
		{
			name:           "cancel_without_handler",
			onCancel:       nil,
			wantStatus:     WorkflowStatusFailed,
			wantStepStatus: StepStatusFailed,
		},
		{
			name: "cancel_with_skip_remaining",
			onCancel: &ElicitationHandler{
				Action: "skip_remaining",
			},
			wantStatus:     WorkflowStatusCompleted,
			wantStepStatus: StepStatusCompleted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			te := newTestEngine(t)
			mockSDK := mocks.NewMockElicitationRequester(te.Ctrl)

			// Mock SDK to return cancel response
			mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&vmcp.ElicitationResult{
				Action: "cancel",
			}, nil)

			handler := NewDefaultElicitationHandler(mockSDK)
			stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
			engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil, nil)

			workflow := &WorkflowDefinition{
				Name: "test-workflow",
				Steps: []WorkflowStep{
					{
						ID:   "confirm",
						Type: StepTypeElicitation,
						Elicitation: &ElicitationConfig{
							Message:  "Confirm?",
							Schema:   map[string]any{"type": "object"},
							Timeout:  1 * time.Minute,
							OnCancel: tt.onCancel,
						},
					},
				},
			}

			result, err := engine.ExecuteWorkflow(context.Background(), workflow, nil)

			// For failed workflows, ExecuteWorkflow returns both result and error
			if tt.wantStatus == WorkflowStatusFailed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.NotNil(t, result)
			assert.Equal(t, tt.wantStatus, result.Status)
			confirmStep := result.Steps["confirm"]
			require.NotNil(t, confirmStep)
			assert.Equal(t, tt.wantStepStatus, confirmStep.Status)
		})
	}
}

func TestWorkflowEngine_ExecuteElicitationStep_Timeout(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)
	mockSDK := mocks.NewMockElicitationRequester(te.Ctrl)

	// Mock SDK to return timeout error
	mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(nil, context.DeadlineExceeded)

	handler := NewDefaultElicitationHandler(mockSDK)
	stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil, nil)

	workflow := &WorkflowDefinition{
		Name: "test-workflow",
		Steps: []WorkflowStep{
			{
				ID:   "confirm",
				Type: StepTypeElicitation,
				Elicitation: &ElicitationConfig{
					Message: "Confirm?",
					Schema:  map[string]any{"type": "object"},
					Timeout: 100 * time.Millisecond,
				},
			},
		},
	}

	result, err := engine.ExecuteWorkflow(context.Background(), workflow, nil)

	// Should fail due to timeout
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrElicitationTimeout)
	assert.Equal(t, WorkflowStatusFailed, result.Status)

	confirmStep := result.Steps["confirm"]
	require.NotNil(t, confirmStep)
	assert.Equal(t, StepStatusFailed, confirmStep.Status)
}

func TestWorkflowEngine_ExecuteElicitationStep_NoHandler(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)
	// Create engine WITHOUT elicitation handler
	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, nil, nil)

	workflow := &WorkflowDefinition{
		Name: "test-workflow",
		Steps: []WorkflowStep{
			{
				ID:   "confirm",
				Type: StepTypeElicitation,
				Elicitation: &ElicitationConfig{
					Message: "Confirm?",
					Schema:  map[string]any{"type": "object"},
				},
			},
		},
	}

	result, err := engine.ExecuteWorkflow(context.Background(), workflow, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "elicitation handler not configured")
	assert.Equal(t, WorkflowStatusFailed, result.Status)
}

func TestWorkflowEngine_MultiStepWithElicitation(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)
	mockSDK := mocks.NewMockElicitationRequester(te.Ctrl)

	// Mock SDK to return accept with proceed=true
	mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&vmcp.ElicitationResult{
		Action:  "accept",
		Content: map[string]any{"proceed": true},
	}, nil)

	handler := NewDefaultElicitationHandler(mockSDK)
	stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil, nil)

	workflow := &WorkflowDefinition{
		Name: "multi-step-workflow",
		Steps: []WorkflowStep{
			{
				ID:        "fetch_data",
				Type:      StepTypeTool,
				Tool:      "fetch_tool",
				Arguments: map[string]any{"source": "api"},
			},
			{
				ID:   "confirm_process",
				Type: StepTypeElicitation,
				Elicitation: &ElicitationConfig{
					Message: "Data fetched. Proceed with processing?",
					Schema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"proceed": map[string]any{"type": "boolean"},
						},
					},
					Timeout: 1 * time.Minute,
				},
			},
			{
				ID:        "process_data",
				Type:      StepTypeTool,
				Tool:      "process_tool",
				DependsOn: []string{"fetch_data", "confirm_process"}, // Process only after data is fetched and user confirms to ensure data availability and approval
				Arguments: map[string]any{
					"data": "{{.steps.fetch_data.output.text}}",
				},
			},
		},
	}

	// Setup expectations
	te.expectToolCall("fetch_tool", map[string]any{"source": "api"}, map[string]any{"text": "fetched_data"})
	te.expectToolCall("process_tool", map[string]any{"data": "fetched_data"}, map[string]any{"result": "processed"})

	result, err := engine.ExecuteWorkflow(context.Background(), workflow, nil)
	require.NoError(t, err)
	assert.Equal(t, WorkflowStatusCompleted, result.Status)
	assert.Len(t, result.Steps, 3)

	// All steps should be completed
	assert.Equal(t, StepStatusCompleted, result.Steps["fetch_data"].Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["confirm_process"].Status)
	assert.Equal(t, StepStatusCompleted, result.Steps["process_data"].Status)
}

// TestWorkflowEngine_SkipRemainingStopsGatedSteps verifies that the
// "skip_remaining" elicitation action actually stops the workflow: a later tool
// step that depends on a declined, cancelled or timed-out gate must never run and
// must be reported as skipped, while the workflow still completes successfully
// (unlike "abort", which fails the workflow).
//
// Regression test for GHSA-hjq9-7gq7-wg83: previously skip_remaining recorded the
// gate as a success and returned nil, so the DAG executor ran every later step —
// including backend tool calls the user had refused.
func TestWorkflowEngine_SkipRemainingStopsGatedSteps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		elicitResult *vmcp.ElicitationResult
		elicitErr    error
		useOnCancel  bool   // route skip_remaining via OnCancel (cancel/timeout) vs OnDecline
		failureMode  string // workflow-level failure mode
	}{
		{
			name:         "decline",
			elicitResult: &vmcp.ElicitationResult{Action: "decline"},
		},
		{
			name:         "cancel",
			elicitResult: &vmcp.ElicitationResult{Action: "cancel"},
			useOnCancel:  true,
		},
		{
			name:        "timeout_uses_oncancel",
			elicitErr:   context.DeadlineExceeded,
			useOnCancel: true,
		},
		{
			// skip_remaining must override failureMode "continue": a graceful
			// stop is not an error that continue-on-error should swallow.
			name:         "decline_overrides_failure_mode_continue",
			elicitResult: &vmcp.ElicitationResult{Action: "decline"},
			failureMode:  failureModeContinue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			te := newTestEngine(t)
			mockSDK := mocks.NewMockElicitationRequester(te.Ctrl)
			mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).
				Return(tt.elicitResult, tt.elicitErr)

			handler := NewDefaultElicitationHandler(mockSDK)
			engine := NewWorkflowEngine(te.Router, te.Backend, handler, nil, nil, nil)

			skip := &ElicitationHandler{Action: "skip_remaining"}
			elicitation := &ElicitationConfig{
				Message: "Proceed with deployment?",
				Schema:  map[string]any{"type": "object"},
				Timeout: 100 * time.Millisecond,
			}
			if tt.useOnCancel {
				elicitation.OnCancel = skip
			} else {
				elicitation.OnDecline = skip
			}

			workflow := &WorkflowDefinition{
				Name:        "deploy-workflow",
				FailureMode: tt.failureMode,
				Steps: []WorkflowStep{
					{
						ID:          "confirm",
						Type:        StepTypeElicitation,
						Elicitation: elicitation,
					},
					{
						ID:        "deploy",
						Type:      StepTypeTool,
						Tool:      "deploy_tool",
						DependsOn: []string{"confirm"},
						Arguments: map[string]any{"env": "prod"},
					},
					{
						ID:        "notify",
						Type:      StepTypeTool,
						Tool:      "notify_tool",
						DependsOn: []string{"deploy"},
						Arguments: map[string]any{},
					},
				},
			}

			// No RouteTool/CallTool expectations are registered on purpose: if
			// either gated step ran, gomock would fail the test on the unexpected
			// backend call.
			result, err := engine.ExecuteWorkflow(context.Background(), workflow, nil)
			require.NoError(t, err)
			require.NotNil(t, result)

			// The workflow completes gracefully (skip_remaining, not abort).
			assert.Equal(t, WorkflowStatusCompleted, result.Status)

			// The gate step itself completed (the user answered it).
			confirmStep := result.Steps["confirm"]
			require.NotNil(t, confirmStep)
			assert.Equal(t, StepStatusCompleted, confirmStep.Status)

			// Every remaining step, not only the direct dependent, is reported
			// as skipped and was never executed.
			for _, id := range []string{"deploy", "notify"} {
				gated := result.Steps[id]
				require.NotNil(t, gated, "step %s missing from result", id)
				assert.Equal(t, StepStatusSkipped, gated.Status, "step %s", id)
			}
		})
	}
}

// TestWorkflowEngine_SkipRemainingDeadlines verifies that a workflow timeout or
// caller deadline expiring while a skip_remaining gate waits is reported as a
// timeout, not as a gracefully completed workflow, while the gate's own timeout
// is still a graceful skip.
func TestWorkflowEngine_SkipRemainingDeadlines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		workflowTimeout time.Duration
		callerTimeout   time.Duration
		gateTimeout     time.Duration // zero means one minute
		wantCompleted   bool
	}{
		{name: "workflow_timeout", workflowTimeout: 100 * time.Millisecond},
		{name: "caller_deadline", callerTimeout: 100 * time.Millisecond},
		{name: "gate_own_timeout", gateTimeout: 100 * time.Millisecond, wantCompleted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			te := newTestEngine(t)
			mockSDK := mocks.NewMockElicitationRequester(te.Ctrl)
			mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).
				DoAndReturn(func(ctx context.Context, _ vmcp.ElicitationRequest) (*vmcp.ElicitationResult, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				})

			handler := NewDefaultElicitationHandler(mockSDK)
			engine := NewWorkflowEngine(te.Router, te.Backend, handler, nil, nil, nil)

			gateTimeout := tt.gateTimeout
			if gateTimeout == 0 {
				gateTimeout = time.Minute
			}

			workflow := &WorkflowDefinition{
				Name:    "deploy-workflow",
				Timeout: tt.workflowTimeout,
				Steps: []WorkflowStep{
					{
						ID:   "confirm",
						Type: StepTypeElicitation,
						Elicitation: &ElicitationConfig{
							Message:  "Proceed with deployment?",
							Schema:   map[string]any{"type": "object"},
							Timeout:  gateTimeout,
							OnCancel: &ElicitationHandler{Action: "skip_remaining"},
						},
					},
					{
						ID:        "deploy",
						Type:      StepTypeTool,
						Tool:      "deploy_tool",
						DependsOn: []string{"confirm"},
						Arguments: map[string]any{"env": "prod"},
					},
				},
			}

			ctx := context.Background()
			if tt.callerTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.callerTimeout)
				defer cancel()
			}

			// No backend expectations are registered: if "deploy" ran, gomock
			// would fail the test.
			result, err := engine.ExecuteWorkflow(ctx, workflow, nil)
			require.NotNil(t, result)
			if tt.wantCompleted {
				require.NoError(t, err)
				assert.Equal(t, WorkflowStatusCompleted, result.Status)
			} else {
				require.ErrorIs(t, err, ErrWorkflowTimeout)
				assert.Equal(t, WorkflowStatusTimedOut, result.Status)
			}

			deploy := result.Steps["deploy"]
			require.NotNil(t, deploy)
			assert.Equal(t, StepStatusSkipped, deploy.Status)
		})
	}
}

func TestWorkflowEngine_ValidateElicitationStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		step        WorkflowStep
		wantErr     bool
		errContains string
	}{
		{
			name: "valid_elicitation_step",
			step: WorkflowStep{
				ID:   "elicit-1",
				Type: StepTypeElicitation,
				Elicitation: &ElicitationConfig{
					Message: "Confirm?",
					Schema:  map[string]any{"type": "object"},
				},
			},
			wantErr: false,
		},
		{
			name: "missing_elicitation_config",
			step: WorkflowStep{
				ID:          "elicit-1",
				Type:        StepTypeElicitation,
				Elicitation: nil,
			},
			wantErr:     true,
			errContains: "elicitation config is required",
		},
		{
			name: "missing_message",
			step: WorkflowStep{
				ID:   "elicit-1",
				Type: StepTypeElicitation,
				Elicitation: &ElicitationConfig{
					Schema: map[string]any{"type": "object"},
				},
			},
			wantErr:     true,
			errContains: "elicitation message is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			te := newTestEngine(t)
			workflow := &WorkflowDefinition{
				Name:  "test",
				Steps: []WorkflowStep{tt.step},
			}

			err := te.Engine.ValidateWorkflow(context.Background(), workflow)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDefaultElicitationHandler_SDKErrorHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		sdkError    error
		wantErr     bool
		errType     error
		errContains string
	}{
		{
			name:        "context_deadline_exceeded",
			sdkError:    context.DeadlineExceeded,
			wantErr:     true,
			errType:     ErrElicitationTimeout,
			errContains: "elicitation request timed out",
		},
		{
			name:        "context_canceled",
			sdkError:    context.Canceled,
			wantErr:     true,
			errContains: "elicitation request failed",
		},
		{
			name:        "generic_sdk_error",
			sdkError:    errors.New("connection refused"),
			wantErr:     true,
			errContains: "elicitation request failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockSDK := mocks.NewMockElicitationRequester(ctrl)

			mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(nil, tt.sdkError)

			handler := NewDefaultElicitationHandler(mockSDK)

			config := &ElicitationConfig{
				Message: "Test?",
				Schema:  map[string]any{"type": "object"},
			}

			response, err := handler.RequestElicitation(context.Background(), "wf-1", "step-1", config)

			require.Error(t, err)
			assert.Nil(t, response)
			if tt.errType != nil {
				assert.ErrorIs(t, err, tt.errType)
			}
			if tt.errContains != "" {
				assert.Contains(t, err.Error(), tt.errContains)
			}
		})
	}
}

func TestWorkflowEngine_ElicitationMessageTemplateExpansion(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		message         string
		params          map[string]any
		expectedMessage string
	}{
		{
			name:            "expands template message",
			message:         "Deploy {{.params.repo}} to {{.params.env}}?",
			params:          map[string]any{"repo": "acme/widget", "env": "production"},
			expectedMessage: "Deploy acme/widget to production?",
		},
		{
			name:            "passes through plain message",
			message:         "Deploy now?",
			params:          map[string]any{},
			expectedMessage: "Deploy now?",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			te := newTestEngine(t)
			mockSDK := mocks.NewMockElicitationRequester(te.Ctrl)

			var capturedReq vmcp.ElicitationRequest
			mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, req vmcp.ElicitationRequest) (*vmcp.ElicitationResult, error) {
					capturedReq = req
					return &vmcp.ElicitationResult{
						Action:  "accept",
						Content: map[string]any{"confirmed": true},
					}, nil
				},
			)

			handler := NewDefaultElicitationHandler(mockSDK)
			stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
			engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil, nil)

			workflow := &WorkflowDefinition{
				Name: "template-elicit",
				Steps: []WorkflowStep{
					{
						ID:   "ask",
						Type: StepTypeElicitation,
						Elicitation: &ElicitationConfig{
							Message: tt.message,
							Schema: map[string]any{
								"type": "object",
								"properties": map[string]any{
									"confirmed": map[string]any{"type": "boolean"},
								},
							},
							Timeout: 1 * time.Minute,
						},
					},
				},
			}

			result, err := engine.ExecuteWorkflow(context.Background(), workflow, tt.params)
			require.NoError(t, err)
			assert.Equal(t, WorkflowStatusCompleted, result.Status)
			assert.Equal(t, tt.expectedMessage, capturedReq.Message)
			assert.Equal(t, tt.message, workflow.Steps[0].Elicitation.Message)
		})
	}
}
