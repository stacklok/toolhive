package composer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer/mocks"
)

func TestWorkflowEngine_ExecuteElicitationStep_Accept(t *testing.T) {
	t.Parallel()

	te := newTestEngine(t)
	mockSDK := mocks.NewMockSDKElicitationRequester(te.Ctrl)

	// Mock SDK to return accept response
	mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action:  mcp.ElicitationResponseActionAccept,
			Content: map[string]any{"environment": "production"},
		},
	}, nil)

	handler := NewDefaultElicitationHandler(mockSDK)
	stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil)

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
	te.Router.EXPECT().RouteTool(gomock.Any(), "deploy_tool").Return(&vmcp.BackendTarget{
		WorkloadID: "deploy-backend",
		BaseURL:    "http://deploy:8080",
	}, nil)
	te.Backend.EXPECT().CallTool(gomock.Any(), gomock.Any(), "deploy_tool", map[string]any{
		"env": "production",
	}).Return(map[string]any{"status": "deployed"}, nil)

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
			mockSDK := mocks.NewMockSDKElicitationRequester(te.Ctrl)

			// Mock SDK to return decline response
			mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&mcp.ElicitationResult{
				ElicitationResponse: mcp.ElicitationResponse{
					Action: mcp.ElicitationResponseActionDecline,
				},
			}, nil)

			handler := NewDefaultElicitationHandler(mockSDK)
			stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
			engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil)

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
			mockSDK := mocks.NewMockSDKElicitationRequester(te.Ctrl)

			// Mock SDK to return cancel response
			mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&mcp.ElicitationResult{
				ElicitationResponse: mcp.ElicitationResponse{
					Action: mcp.ElicitationResponseActionCancel,
				},
			}, nil)

			handler := NewDefaultElicitationHandler(mockSDK)
			stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
			engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil)

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
	mockSDK := mocks.NewMockSDKElicitationRequester(te.Ctrl)

	// Mock SDK to return timeout error
	mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(nil, context.DeadlineExceeded)

	handler := NewDefaultElicitationHandler(mockSDK)
	stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil)

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
	engine := NewWorkflowEngine(te.Router, te.Backend, nil, nil, nil)

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
	mockSDK := mocks.NewMockSDKElicitationRequester(te.Ctrl)

	// Mock SDK to return accept with proceed=true
	mockSDK.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action:  mcp.ElicitationResponseActionAccept,
			Content: map[string]any{"proceed": true},
		},
	}, nil)

	handler := NewDefaultElicitationHandler(mockSDK)
	stateStore := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	engine := NewWorkflowEngine(te.Router, te.Backend, handler, stateStore, nil)

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
			mockSDK := mocks.NewMockSDKElicitationRequester(ctrl)

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
