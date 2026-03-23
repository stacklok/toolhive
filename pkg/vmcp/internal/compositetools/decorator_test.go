// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package compositetools_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/internal/compositetools"
	sessionmocks "github.com/stacklok/toolhive/pkg/vmcp/session/types/mocks"
)

// stubExecutor is a simple WorkflowExecutor for tests.
type stubExecutor struct {
	output map[string]any
	err    error
}

func (s *stubExecutor) ExecuteWorkflow(_ context.Context, _ map[string]any) (*compositetools.WorkflowResult, error) {
	return &compositetools.WorkflowResult{Output: s.output}, s.err
}

func TestCompositeToolsDecorator_Tools(t *testing.T) {
	t.Parallel()

	t.Run("appends composite tools to backend tools", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		backendTools := []vmcp.Tool{{Name: "backend_search", Description: "search"}}
		base.EXPECT().Tools().Return(backendTools).AnyTimes()

		compositeToolList := []vmcp.Tool{{Name: "my_workflow", Description: "a workflow"}}
		dec := compositetools.NewDecorator(base, compositeToolList, nil)

		got := dec.Tools()
		require.Len(t, got, 2)
		assert.Equal(t, "backend_search", got[0].Name)
		assert.Equal(t, "my_workflow", got[1].Name)
	})

	t.Run("returns only backend tools when no composite tools", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		backendTools := []vmcp.Tool{{Name: "backend_search", Description: "search"}}
		base.EXPECT().Tools().Return(backendTools).AnyTimes()

		dec := compositetools.NewDecorator(base, nil, nil)

		got := dec.Tools()
		require.Len(t, got, 1)
		assert.Equal(t, "backend_search", got[0].Name)
	})
}

func TestCompositeToolsDecorator_CallTool(t *testing.T) {
	t.Parallel()

	t.Run("routes composite tool name to executor", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		expectedOutput := map[string]any{"result": "done"}
		exec := &stubExecutor{output: expectedOutput}
		executors := map[string]compositetools.WorkflowExecutor{"my_workflow": exec}

		dec := compositetools.NewDecorator(base, []vmcp.Tool{{Name: "my_workflow"}}, executors)
		result, err := dec.CallTool(context.Background(), nil, "my_workflow", map[string]any{"x": 1}, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, expectedOutput, result.StructuredContent)
	})

	t.Run("delegates unknown tool name to embedded session", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		expectedResult := &vmcp.ToolCallResult{IsError: false}
		base.EXPECT().
			CallTool(gomock.Any(), gomock.Any(), "backend_tool", gomock.Any(), gomock.Any()).
			Return(expectedResult, nil)

		dec := compositetools.NewDecorator(base, nil, nil)
		result, err := dec.CallTool(context.Background(), &auth.Identity{}, "backend_tool", nil, nil) //nolint:exhaustruct // empty identity is intentional for test

		require.NoError(t, err)
		assert.Equal(t, expectedResult, result)
	})

	t.Run("propagates executor error as tool error result", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		execErr := errors.New("workflow failed")
		exec := &stubExecutor{err: execErr}
		executors := map[string]compositetools.WorkflowExecutor{"failing_wf": exec}

		dec := compositetools.NewDecorator(base, []vmcp.Tool{{Name: "failing_wf"}}, executors)
		result, err := dec.CallTool(context.Background(), nil, "failing_wf", nil, nil)

		require.NoError(t, err) // errors surface as IsError results per MCP convention
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})
}
