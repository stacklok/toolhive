// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizerdec_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
	sessionmocks "github.com/stacklok/toolhive/pkg/vmcp/session/types/mocks"
)

// stubOptimizer implements optimizer.Optimizer for tests.
type stubOptimizer struct {
	findOutput *optimizer.FindToolOutput
	findErr    error
	callOutput *mcp.CallToolResult
	callErr    error
}

func (s *stubOptimizer) FindTool(_ context.Context, _ optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
	return s.findOutput, s.findErr
}

func (s *stubOptimizer) CallTool(_ context.Context, _ optimizer.CallToolInput) (*mcp.CallToolResult, error) {
	return s.callOutput, s.callErr
}

// stubAuthorizer implements authorizers.Authorizer for tests.
// Hand-rolled because no generated mock exists for the single-method Authorizer
// interface, and a simple map-based stub is more readable than gomock here.
type stubAuthorizer struct {
	// results maps resourceID -> authorized. Missing keys return false.
	results map[string]bool
}

func (s *stubAuthorizer) AuthorizeWithJWTClaims(
	_ context.Context,
	_ authorizers.MCPFeature,
	_ authorizers.MCPOperation,
	resourceID string,
	_ map[string]interface{},
) (bool, error) {
	return s.results[resourceID], nil
}

// argsCapturingAuthorizer captures the arguments passed to AuthorizeWithJWTClaims
// and delegates the authorization decision to a callback. Hand-rolled for the
// same reason as stubAuthorizer (single-method interface, no generated mock).
type argsCapturingAuthorizer struct {
	// decideFn receives the resourceID and arguments and returns the decision.
	decideFn func(resourceID string, arguments map[string]interface{}) (bool, error)
	// capturedArgs stores the arguments from the last call.
	capturedArgs map[string]interface{}
}

func (a *argsCapturingAuthorizer) AuthorizeWithJWTClaims(
	_ context.Context,
	_ authorizers.MCPFeature,
	_ authorizers.MCPOperation,
	resourceID string,
	arguments map[string]interface{},
) (bool, error) {
	a.capturedArgs = arguments
	return a.decideFn(resourceID, arguments)
}

func TestOptimizerDecorator_Tools(t *testing.T) {
	t.Parallel()

	t.Run("returns only find_tool and call_tool", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return([]vmcp.Tool{{Name: "backend_search"}}).AnyTimes()

		dec := optimizerdec.NewDecorator(base, &stubOptimizer{}, nil)

		got := dec.Tools()
		require.Len(t, got, 2)
		assert.Equal(t, "find_tool", got[0].Name)
		assert.Equal(t, "call_tool", got[1].Name)
		// Both tools must have non-empty input schemas.
		assert.NotEmpty(t, got[0].InputSchema)
		assert.NotEmpty(t, got[1].InputSchema)
	})
}

func TestOptimizerDecorator_CallTool_FindTool(t *testing.T) {
	t.Parallel()

	t.Run("find_tool calls optimizer and returns JSON result", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		findOutput := &optimizer.FindToolOutput{
			Tools: []mcp.Tool{{Name: "search"}},
		}
		opt := &stubOptimizer{findOutput: findOutput}
		dec := optimizerdec.NewDecorator(base, opt, nil)

		args := map[string]any{"tool_description": "web search"}
		result, err := dec.CallTool(context.Background(), nil, "find_tool", args, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		// Result should be non-error and contain the marshaled output.
		assert.False(t, result.IsError)
		// The structured content should be present or content should have JSON text.
		require.NotEmpty(t, result.Content)
	})

	t.Run("find_tool propagates optimizer error as tool error result", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		opt := &stubOptimizer{findErr: errors.New("index unavailable")}
		dec := optimizerdec.NewDecorator(base, opt, nil)

		result, err := dec.CallTool(context.Background(), nil, "find_tool", map[string]any{"tool_description": "x"}, nil)

		require.NoError(t, err) // errors are surfaced as IsError results per MCP convention
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("find_tool returns error result when optimizer returns nil output", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		opt := &stubOptimizer{findOutput: nil, findErr: nil}
		dec := optimizerdec.NewDecorator(base, opt, nil)

		result, err := dec.CallTool(context.Background(), nil, "find_tool", map[string]any{"tool_description": "x"}, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})
}

func TestOptimizerDecorator_CallTool_CallTool(t *testing.T) {
	t.Parallel()

	t.Run("call_tool routes through optimizer and converts result", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()
		// The underlying session must NOT be called — call_tool routes through optimizer.CallTool.

		opt := &stubOptimizer{
			callOutput: mcp.NewToolResultText("fetched content"),
		}
		dec := optimizerdec.NewDecorator(base, opt, nil)

		args := map[string]any{
			"tool_name":  "backend_fetch",
			"parameters": map[string]any{"url": "https://example.com"},
		}
		result, err := dec.CallTool(context.Background(), &auth.Identity{}, "call_tool", args, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		require.Len(t, result.Content, 1)
		assert.Equal(t, "fetched content", result.Content[0].Text)
	})

	t.Run("call_tool propagates optimizer error as tool error result", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		opt := &stubOptimizer{callErr: errors.New("backend unreachable")}
		dec := optimizerdec.NewDecorator(base, opt, nil)

		args := map[string]any{"tool_name": "backend_fetch"}
		result, err := dec.CallTool(context.Background(), nil, "call_tool", args, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("call_tool returns error result when tool_name missing", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		dec := optimizerdec.NewDecorator(base, &stubOptimizer{}, nil)

		result, err := dec.CallTool(context.Background(), nil, "call_tool", map[string]any{}, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("unknown tool returns error", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		dec := optimizerdec.NewDecorator(base, &stubOptimizer{}, nil)

		_, err := dec.CallTool(context.Background(), nil, "nonexistent_tool", nil, nil)

		require.Error(t, err)
	})
}

func TestOptimizerDecorator_FindTool_AuthzFiltering(t *testing.T) {
	t.Parallel()

	t.Run("find_tool filters unauthorized tools from results", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		findOutput := &optimizer.FindToolOutput{
			Tools: []mcp.Tool{
				{Name: "allowed_tool"},
				{Name: "denied_tool"},
				{Name: "another_allowed"},
			},
		}
		opt := &stubOptimizer{findOutput: findOutput}
		authz := &stubAuthorizer{results: map[string]bool{
			"allowed_tool":    true,
			"denied_tool":     false,
			"another_allowed": true,
		}}
		dec := optimizerdec.NewDecorator(base, opt, authz)

		args := map[string]any{"tool_description": "search"}
		result, err := dec.CallTool(context.Background(), nil, "find_tool", args, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		// The JSON result should contain the two allowed tools but not the denied one.
		assert.Contains(t, result.Content[0].Text, "allowed_tool")
		assert.Contains(t, result.Content[0].Text, "another_allowed")
		assert.NotContains(t, result.Content[0].Text, "denied_tool")
	})
}

func TestOptimizerDecorator_CallTool_AuthzGating(t *testing.T) {
	t.Parallel()

	t.Run("call_tool blocks unauthorized backend tool", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		opt := &stubOptimizer{
			callOutput: mcp.NewToolResultText("should not reach here"),
		}
		authz := &stubAuthorizer{results: map[string]bool{
			"admin_tool": false,
		}}
		dec := optimizerdec.NewDecorator(base, opt, authz)

		args := map[string]any{
			"tool_name":  "admin_tool",
			"parameters": map[string]any{},
		}
		result, err := dec.CallTool(context.Background(), nil, "call_tool", args, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Content[0].Text, "not permitted to call tool")
	})

	t.Run("call_tool returns error result when authorizer fails", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		opt := &stubOptimizer{
			callOutput: mcp.NewToolResultText("should not reach here"),
		}
		authz := &argsCapturingAuthorizer{
			decideFn: func(_ string, _ map[string]interface{}) (bool, error) {
				return false, errors.New("cedar engine unavailable")
			},
		}
		dec := optimizerdec.NewDecorator(base, opt, authz)

		args := map[string]any{
			"tool_name":  "some_tool",
			"parameters": map[string]any{},
		}
		result, err := dec.CallTool(context.Background(), nil, "call_tool", args, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Content[0].Text, "authorization check failed for tool")
	})

	t.Run("call_tool allows authorized backend tool", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		opt := &stubOptimizer{
			callOutput: mcp.NewToolResultText("success"),
		}
		authz := &stubAuthorizer{results: map[string]bool{
			"safe_tool": true,
		}}
		dec := optimizerdec.NewDecorator(base, opt, authz)

		args := map[string]any{
			"tool_name":  "safe_tool",
			"parameters": map[string]any{},
		}
		result, err := dec.CallTool(context.Background(), nil, "call_tool", args, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		assert.Equal(t, "success", result.Content[0].Text)
	})

	t.Run("call_tool with nil authorizer allows all tools without authorization", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		opt := &stubOptimizer{
			callOutput: mcp.NewToolResultText("no-authz success"),
		}
		// Explicitly pass nil authorizer to verify the default-open behavior.
		dec := optimizerdec.NewDecorator(base, opt, nil)

		args := map[string]any{
			"tool_name":  "any_tool",
			"parameters": map[string]any{},
		}
		result, err := dec.CallTool(context.Background(), nil, "call_tool", args, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		assert.Equal(t, "no-authz success", result.Content[0].Text)
	})

	t.Run("call_tool passes parameters to authorizer", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return(nil).AnyTimes()

		opt := &stubOptimizer{
			callOutput: mcp.NewToolResultText("done"),
		}
		authzMock := &argsCapturingAuthorizer{
			decideFn: func(_ string, _ map[string]interface{}) (bool, error) {
				return true, nil
			},
		}
		dec := optimizerdec.NewDecorator(base, opt, authzMock)

		toolParams := map[string]any{"city": "London", "units": "metric"}
		args := map[string]any{
			"tool_name":  "weather",
			"parameters": toolParams,
		}
		result, err := dec.CallTool(context.Background(), nil, "call_tool", args, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		// Verify the authorizer received the tool parameters.
		require.NotNil(t, authzMock.capturedArgs)
		assert.Equal(t, "London", authzMock.capturedArgs["city"])
		assert.Equal(t, "metric", authzMock.capturedArgs["units"])
	})
}
