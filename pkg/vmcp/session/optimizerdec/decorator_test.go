// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package optimizerdec_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
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

func TestOptimizerDecorator_Tools(t *testing.T) {
	t.Parallel()

	t.Run("returns only find_tool and call_tool", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		base := sessionmocks.NewMockMultiSession(ctrl)
		base.EXPECT().Tools().Return([]vmcp.Tool{{Name: "backend_search"}}).AnyTimes()

		dec := optimizerdec.NewDecorator(base, &stubOptimizer{})

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
		dec := optimizerdec.NewDecorator(base, opt)

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
		dec := optimizerdec.NewDecorator(base, opt)

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
		dec := optimizerdec.NewDecorator(base, opt)

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
		dec := optimizerdec.NewDecorator(base, opt)

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
		dec := optimizerdec.NewDecorator(base, opt)

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

		dec := optimizerdec.NewDecorator(base, &stubOptimizer{})

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

		dec := optimizerdec.NewDecorator(base, &stubOptimizer{})

		_, err := dec.CallTool(context.Background(), nil, "nonexistent_tool", nil, nil)

		require.Error(t, err)
	})
}

// TestCallToolArgConstantsMatchStructTags verifies that CallToolArgToolName and
// CallToolArgParameters match the json tags on optimizer.CallToolInput. The middleware
// uses these constants to look up fields from parsed arguments; a mismatch causes an
// authz bypass or parameters being silently dropped.
func TestCallToolArgConstantsMatchStructTags(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeOf(optimizer.CallToolInput{})

	cases := []struct {
		field    string
		constant string
	}{
		{"ToolName", optimizerdec.CallToolArgToolName},
		{"Parameters", optimizerdec.CallToolArgParameters},
	}

	for _, tc := range cases {
		f, ok := typ.FieldByName(tc.field)
		require.True(t, ok, "optimizer.CallToolInput must have a %s field", tc.field)
		assert.Equal(t, tc.constant, f.Tag.Get("json"),
			"constant for %s must match its json struct tag", tc.field)
	}
}
