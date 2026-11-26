// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	aggmocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routermocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
)

// compositeCoreFailingWith builds a real core advertising one composite tool,
// "workflow", whose only step calls a backend tool that fails with stepErr.
func compositeCoreFailingWith(t *testing.T, stepErr error) (core.VMCP, vmcp.BackendRegistry) {
	t.Helper()
	ctrl := gomock.NewController(t)
	target := &vmcp.BackendTarget{WorkloadID: "legacy-x", BaseURL: "http://backend.example:8401/mcp"}
	agg := aggmocks.NewMockAggregator(ctrl)
	agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{{Name: "fetch", BackendID: target.WorkloadID}},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{"fetch": target}},
	}, nil).AnyTimes()
	backend := vmcpmocks.NewMockBackendClient(ctrl)
	backend.EXPECT().CallTool(gomock.Any(), target, "fetch", gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, stepErr).AnyTimes()

	registry := vmcp.NewImmutableRegistry([]vmcp.Backend{{ID: target.WorkloadID}})
	v, err := core.New(&core.Config{
		Aggregator: agg, Router: routermocks.NewMockRouter(ctrl), BackendClient: backend, BackendRegistry: registry,
		WorkflowDefs: map[string]*composer.WorkflowDefinition{
			"workflow": {Name: "workflow", Steps: []composer.WorkflowStep{
				{ID: "fetch-step", Type: composer.StepTypeTool, Tool: "fetch"},
			}},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, v.Close()) })
	return v, registry
}

// TestCompositeToolDelegatedCredentialErrors pins that a composite tool whose
// step fails on a delegated credential is answered exactly as a direct call
// is, on both revisions: authorization required as an isError result naming
// only the provider, an unavailable credential as -32603 with a fixed message,
// and neither carrying the workflow, step, or backend.
func TestCompositeToolDelegatedCredentialErrors(t *testing.T) {
	t.Parallel()

	const (
		authReqText     = `provider "github" requires upstream authorization`
		unavailableText = "upstream credential unavailable"
	)
	tests := []struct {
		name     string
		stepErr  error
		wantText string // isError result text; empty when the call fails with -32603
	}{
		{
			name:     "authorization required",
			stepErr:  authtypes.NewDelegatedAuthRequiredError("github", errors.New("private cause")),
			wantText: authReqText,
		},
		{name: "unavailable", stepErr: authtypes.ErrDelegatedCredentialUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/Legacy", func(t *testing.T) {
			t.Parallel()
			v, registry := compositeCoreFailingWith(t, tt.stepErr)
			srv, sessionID, _ := registerServeSessionWithCore(t, v, nil, registry)

			req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "workflow", Arguments: map[string]any{}}}
			res, err := srv.coreToolHandler(sessionID, "workflow", "")(t.Context(), req)
			if tt.wantText == "" {
				require.Nil(t, res)
				wireErr, ok := err.(*jsonrpc.Error)
				require.True(t, ok, "expected a bare *jsonrpc.Error, got %T: %v", err, err)
				assert.EqualValues(t, jsonrpc.CodeInternalError, wireErr.Code)
				assert.Equal(t, unavailableText, wireErr.Message)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, res)
			assert.True(t, res.IsError)
			require.Len(t, res.Content, 1)
			text, ok := res.Content[0].(mcp.TextContent)
			require.True(t, ok)
			assert.Equal(t, tt.wantText, text.Text)
		})
		t.Run(tt.name+"/Modern", func(t *testing.T) {
			t.Parallel()
			v, _ := compositeCoreFailingWith(t, tt.stepErr)
			s := &Server{config: &Config{Name: testServerName, Version: testServerVersion}, core: v}
			parsed := &mcpparser.ParsedMCPRequest{
				Method: "tools/call", ResourceID: "workflow", IsRequest: true, ID: "1", Arguments: map[string]any{},
			}
			rec := httptest.NewRecorder()
			s.dispatchModern(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(t.Context()), parsed)

			require.Equal(t, http.StatusOK, rec.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			if tt.wantText == "" {
				assert.NotContains(t, body, "result")
				assert.Equal(t, map[string]any{"code": float64(-32603), "message": unavailableText}, body["error"])
				return
			}
			assert.NotContains(t, body, "error")
			result, ok := body["result"].(map[string]any)
			require.True(t, ok, "got %v", body)
			assert.Equal(t, true, result["isError"])
			assert.Equal(t, []any{map[string]any{"type": "text", "text": tt.wantText}}, result["content"])
		})
	}
}

// TestLegacyResourceAndPromptDelegatedCredentialErrors pins that Legacy
// resources/read and prompts/get keep authorization required as a plain error,
// and a denial keeps its deny message, when either outranks an unavailable
// credential.
func TestLegacyResourceAndPromptDelegatedCredentialErrors(t *testing.T) {
	t.Parallel()

	const uri = "file:///doc"
	authReq := authtypes.NewDelegatedAuthRequiredError("github", errors.New("private cause"))
	unavailable := fmt.Errorf("calling legacy-x at http://backend.example: %w", authtypes.ErrDelegatedCredentialUnavailable)

	tests := []struct {
		name        string
		err         error
		wantMessage string
	}{
		{name: "authorization required stays plain", err: authReq, wantMessage: authReq.Error()},
		{
			name: "authorization required wins over unavailable", err: errors.Join(unavailable, authReq),
			wantMessage: authReq.Error(),
		},
		{name: "denial wins over unavailable", err: errors.Join(vmcp.ErrAuthorizationFailed, unavailable)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fc := &fakeCore{
				resources: []vmcp.Resource{{Name: "doc", URI: uri}},
				prompts:   []vmcp.Prompt{{Name: "review"}},
				readErr:   tt.err,
				promptErr: tt.err,
			}
			srv, sessionID, _ := registerServeSession(t, fc)

			_, readErr := srv.coreResourceHandler(sessionID, uri, "")(t.Context(), mcp.ReadResourceRequest{})
			_, promptErr := srv.corePromptHandler(sessionID, "review", "")(t.Context(), mcp.GetPromptRequest{})
			for verb, err := range map[string]error{"resources/read": readErr, "prompts/get": promptErr} {
				require.Error(t, err, verb)
				assert.False(t, errors.As(err, new(*jsonrpc.Error)), verb)
				if tt.wantMessage != "" {
					assert.Contains(t, err.Error(), tt.wantMessage, verb)
				} else {
					assert.Contains(t, err.Error(), "denied by authorization policy", verb)
				}
			}
		})
	}
}

// TestLegacyResourceAndPromptUnavailableOnTheWire pins the wire answer for an
// unavailable credential: JSON-RPC -32603 with the fixed message, where the
// SDK would otherwise send the plain error with code 0.
func TestLegacyResourceAndPromptUnavailableOnTheWire(t *testing.T) {
	t.Parallel()

	const uri = "file:///doc"
	unavailable := fmt.Errorf("calling legacy-x at http://backend.example: %w", authtypes.ErrDelegatedCredentialUnavailable)
	fc := &fakeCore{
		resources: []vmcp.Resource{{Name: "doc", URI: uri}},
		prompts:   []vmcp.Prompt{{Name: "review"}},
		readErr:   unavailable,
		promptErr: unavailable,
	}
	_, sessionID, baseURL := registerServeSession(t, fc)

	for method, params := range map[string]map[string]any{
		"resources/read": {"uri": uri},
		"prompts/get":    {"name": "review"},
	} {
		resp := postServeMCP(t, baseURL, map[string]any{"jsonrpc": "2.0", "id": 2, "method": method, "params": params}, sessionID)
		t.Cleanup(func() { _ = resp.Body.Close() })
		require.Equal(t, http.StatusOK, resp.StatusCode, method)
		var body map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body), method)
		assert.NotContains(t, body, "result", method)
		assert.JSONEq(t, `{"code":-32603,"message":"upstream credential unavailable"}`, string(body["error"]), method)
	}
}
