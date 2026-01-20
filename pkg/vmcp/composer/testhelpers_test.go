// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package composer

import (
	"context"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routermocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
)

// testEngine is a test helper that sets up a workflow engine with mocks.
type testEngine struct {
	Engine  Composer
	Router  *routermocks.MockRouter
	Backend *mocks.MockBackendClient
	Ctrl    *gomock.Controller
}

// newTestEngine creates a test engine with mocks.
func newTestEngine(t *testing.T) *testEngine {
	t.Helper()
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockRouter := routermocks.NewMockRouter(ctrl)
	mockBackend := mocks.NewMockBackendClient(ctrl)
	engine := NewWorkflowEngine(mockRouter, mockBackend, nil, nil, nil) // nil elicitationHandler, stateStore, and auditor for simple tests

	return &testEngine{
		Engine:  engine,
		Router:  mockRouter,
		Backend: mockBackend,
		Ctrl:    ctrl,
	}
}

// expectToolCall is a helper to set up tool call expectations.
func (te *testEngine) expectToolCall(toolName string, args, output map[string]any) {
	target := &vmcp.BackendTarget{
		WorkloadID:   "test-backend",
		WorkloadName: "test",
		BaseURL:      "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), toolName).Return(target, nil)
	result := &vmcp.ToolCallResult{
		StructuredContent: output,
		Content:           []vmcp.Content{},
		IsError:           false,
		Meta:              nil,
	}
	te.Backend.EXPECT().CallTool(gomock.Any(), target, toolName, args).Return(result, nil)
}

// expectToolCallWithError is a helper to set up failing tool call expectations.
func (te *testEngine) expectToolCallWithError(toolName string, args map[string]any, err error) {
	target := &vmcp.BackendTarget{
		WorkloadID: "test-backend",
		BaseURL:    "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), toolName).Return(target, nil)
	te.Backend.EXPECT().CallTool(gomock.Any(), target, toolName, args).Return(nil, err)
}

// expectToolCallWithAnyArgsAndError is a helper for failing calls with any args.
func (te *testEngine) expectToolCallWithAnyArgsAndError(toolName string, err error) {
	target := &vmcp.BackendTarget{
		WorkloadID: "test-backend",
		BaseURL:    "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), toolName).Return(target, nil)
	te.Backend.EXPECT().CallTool(gomock.Any(), target, toolName, gomock.Any()).Return(nil, err)
}

// expectToolCallWithAnyArgs is a helper for calls where args are dynamically generated.
func (te *testEngine) expectToolCallWithAnyArgs(toolName string, output map[string]any) {
	target := &vmcp.BackendTarget{
		WorkloadID: "test-backend",
		BaseURL:    "http://test:8080",
	}
	te.Router.EXPECT().RouteTool(gomock.Any(), toolName).Return(target, nil)
	result := &vmcp.ToolCallResult{
		StructuredContent: output,
		Content:           []vmcp.Content{},
		IsError:           false,
		Meta:              nil,
	}
	te.Backend.EXPECT().CallTool(gomock.Any(), target, toolName, gomock.Any()).Return(result, nil)
}

// newWorkflowContext creates a test workflow context.
func newWorkflowContext(params map[string]any) *WorkflowContext {
	startTime := time.Now().UTC()
	return &WorkflowContext{
		WorkflowID: "test-workflow",
		Params:     params,
		Steps:      make(map[string]*StepResult),
		Variables:  make(map[string]any),
		Workflow: &WorkflowMetadata{
			ID:         "test-workflow",
			StartTime:  startTime,
			StepCount:  0,
			Status:     WorkflowStatusPending,
			DurationMs: 0,
		},
	}
}

// toolStep creates a simple tool step for testing.
func toolStep(id, tool string, args map[string]any) WorkflowStep {
	return WorkflowStep{
		ID:        id,
		Type:      StepTypeTool,
		Tool:      tool,
		Arguments: args,
	}
}

// toolStepWithDeps creates a tool step with dependencies.
func toolStepWithDeps(id, tool string, args map[string]any, deps []string) WorkflowStep {
	step := toolStep(id, tool, args)
	step.DependsOn = deps
	return step
}

// simpleWorkflow creates a simple workflow for testing.
func simpleWorkflow(name string, steps ...WorkflowStep) *WorkflowDefinition {
	return &WorkflowDefinition{
		Name:  name,
		Steps: steps,
	}
}

// execute is a helper to execute a workflow.
func execute(t *testing.T, engine Composer, def *WorkflowDefinition, params map[string]any) (*WorkflowResult, error) {
	t.Helper()
	return engine.ExecuteWorkflow(context.Background(), def, params)
}
