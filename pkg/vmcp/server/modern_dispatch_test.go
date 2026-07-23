// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

// modernFakeCore is a core.VMCP whose only exercised methods are the ones
// dispatchModern can reach. The embedded nil interface satisfies the rest and
// panics if dispatch ever calls a method it should not (e.g. a list call for a
// notification/batch/unknown-method request that must short-circuit before
// reaching the core at all).
type modernFakeCore struct {
	core.VMCP

	tools     []vmcp.Tool
	resources []vmcp.Resource
	templates []vmcp.ResourceTemplate
	prompts   []vmcp.Prompt

	discoverCaps core.DiscoverCapabilities
	discoverErr  error

	checkToolErr, checkResourceErr, checkPromptErr error
	callToolErr, readResourceErr, getPromptErr     error
	completeErr                                    error
	listErr                                        error // returned by every List* method

	checkCalled, callCalled bool

	// backendID is stamped onto CallTool/ReadResource/GetPrompt's returned
	// result, mirroring the real core's post-routing BackendID assignment
	// (core_calls.go). Empty by default, matching a fake that never resolves
	// a backend.
	backendID string

	// gotCtx captures the context handed to CallTool, so a test can assert a
	// value set on the inbound request survives into the core call unmodified.
	gotCtx context.Context
}

func (f *modernFakeCore) ListTools(context.Context, *auth.Identity) ([]vmcp.Tool, error) {
	return f.tools, f.listErr
}

func (f *modernFakeCore) ListResources(context.Context, *auth.Identity) ([]vmcp.Resource, error) {
	return f.resources, nil
}

func (f *modernFakeCore) ListResourceTemplates(context.Context, *auth.Identity) ([]vmcp.ResourceTemplate, error) {
	return f.templates, nil
}

func (f *modernFakeCore) ListPrompts(context.Context, *auth.Identity) ([]vmcp.Prompt, error) {
	return f.prompts, nil
}

func (f *modernFakeCore) Discover(context.Context, *auth.Identity) (core.DiscoverCapabilities, error) {
	return f.discoverCaps, f.discoverErr
}

func (f *modernFakeCore) CheckToolCall(context.Context, *auth.Identity, string, map[string]any) error {
	f.checkCalled = true
	return f.checkToolErr
}

func (f *modernFakeCore) CheckResourceRead(context.Context, *auth.Identity, string) error {
	f.checkCalled = true
	return f.checkResourceErr
}

func (f *modernFakeCore) CheckPromptGet(context.Context, *auth.Identity, string) error {
	f.checkCalled = true
	return f.checkPromptErr
}

func (f *modernFakeCore) CallTool(
	ctx context.Context, _ *auth.Identity, _ string, _ map[string]any, _ map[string]any,
) (*vmcp.ToolCallResult, error) {
	f.callCalled = true
	f.gotCtx = ctx
	if f.callToolErr != nil {
		return nil, f.callToolErr
	}
	return &vmcp.ToolCallResult{Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "ok"}}, BackendID: f.backendID}, nil
}

func (f *modernFakeCore) ReadResource(
	ctx context.Context, _ *auth.Identity, uri string,
) (*vmcp.ResourceReadResult, error) {
	f.callCalled = true
	f.gotCtx = ctx
	if f.readResourceErr != nil {
		return nil, f.readResourceErr
	}
	return &vmcp.ResourceReadResult{Contents: []vmcp.ResourceContent{{URI: uri, Text: "body"}}, BackendID: f.backendID}, nil
}

func (f *modernFakeCore) GetPrompt(
	ctx context.Context, _ *auth.Identity, _ string, _ map[string]any,
) (*vmcp.PromptGetResult, error) {
	f.callCalled = true
	f.gotCtx = ctx
	if f.getPromptErr != nil {
		return nil, f.getPromptErr
	}
	return &vmcp.PromptGetResult{
		Messages:  []vmcp.PromptMessage{{Role: "user", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "hi"}}},
		BackendID: f.backendID,
	}, nil
}

func (f *modernFakeCore) Complete(
	ctx context.Context, _ *auth.Identity, _ vmcp.CompletionRef, _, _ string, _ map[string]string,
) (*vmcp.CompletionResult, error) {
	f.callCalled = true
	f.gotCtx = ctx
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	return &vmcp.CompletionResult{Values: []string{"opt1", "opt2"}, Total: 2}, nil
}

// dispatchModernTest builds a Server over fakeCore and drives dispatchModern
// directly (Step 3 wires this into the real handler chain), returning the
// decoded JSON-RPC envelope and the recorder for status/header assertions.
func dispatchModernTest(
	reqCtx context.Context, t *testing.T, fakeCore *modernFakeCore, authzEnabled bool, parsed *mcpparser.ParsedMCPRequest,
) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	s := &Server{
		config:           &Config{Name: testServerName, Version: testServerVersion},
		core:             fakeCore,
		authzGateEnabled: authzEnabled,
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()

	s.dispatchModern(rec, req, parsed)

	var body map[string]any
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	}
	return rec, body
}

// TestDispatchModern_ControlFlow covers the guards that must short-circuit
// before any core call: a batch gets -32600, and an unrecognized method gets
// -32601. None of these may reach the core (modernFakeCore's embedded nil
// core.VMCP panics if they did). The notification guard is NOT in this table
// -- see TestDispatchModern_NotificationRealParser: a hand-built
// ParsedMCPRequest{IsRequest:false} would not have caught the regression
// where the real parser always sets IsRequest true.
func TestDispatchModern_ControlFlow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		parsed     *mcpparser.ParsedMCPRequest
		wantStatus int
		wantCode   float64
	}{
		{
			name:       "batch request returns -32600 invalid request",
			parsed:     &mcpparser.ParsedMCPRequest{Method: "tools/call", IsRequest: true, IsBatch: true, ID: "1"},
			wantStatus: http.StatusOK,
			wantCode:   -32600,
		},
		{
			name:       "unknown method returns -32601 method not found",
			parsed:     &mcpparser.ParsedMCPRequest{Method: "roots/list", IsRequest: true, ID: "1"},
			wantStatus: http.StatusNotFound,
			wantCode:   -32601,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec, body := dispatchModernTest(t.Context(), t, &modernFakeCore{}, true, tt.parsed)

			assert.Equal(t, tt.wantStatus, rec.Code)
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok, "expected a JSON-RPC error envelope")
			assert.Equal(t, tt.wantCode, errObj["code"])
		})
	}
}

// TestDispatchModern_NotificationRealParser drives the REAL pkg/mcp parser
// (ParsingMiddleware, the same middleware installed in front of dispatchModern
// in production) over a genuine no-id JSON-RPC notification body, rather than
// a hand-built ParsedMCPRequest. This matters because parser.go's real parse
// path (parseMCPRequest) always sets IsRequest:true for a decoded
// jsonrpc2.Request -- calls AND notifications alike -- so `!parsed.IsRequest`
// never fires for a real notification; only an absent id does. A fabricated
// ParsedMCPRequest{IsRequest:false} would pass either check and silently mask
// that bug, which is exactly what happened here.
func TestDispatchModern_NotificationRealParser(t *testing.T) {
	t.Parallel()

	body := []byte(`{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	var parsed *mcpparser.ParsedMCPRequest
	handler := mcpparser.ParsingMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		parsed = mcpparser.GetParsedMCPRequest(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	require.NotNil(t, parsed, "the real parser must produce a ParsedMCPRequest for a notification body")
	require.True(t, parsed.IsRequest, "the real parser sets IsRequest true even for a notification")
	require.Nil(t, parsed.ID, "a notification has no JSON-RPC id")

	fc := &modernFakeCore{}
	rec, _ := dispatchModernTest(t.Context(), t, fc, false, parsed)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Empty(t, rec.Body.Bytes())
	assert.False(t, fc.callCalled, "a notification must not reach the core")
}

// TestDispatchModern_PingRealParser drives ping through the REAL
// ParsingMiddleware + classifyingHandler chain with genuine Modern signaling
// (MCP-Protocol-Version header, Mcp-Method header, body _meta) rather than a
// hand-built ParsedMCPRequest -- guarding against the ping case regressing
// back to the unrecognized-method default (-32601/404), which a fabricated
// request would not catch. ping is deliberately absent from
// nameRequiredMethods, so no Mcp-Name header is sent.
func TestDispatchModern_PingRealParser(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "ping",
		"params": {
			"_meta": {
				"io.modelcontextprotocol/protocolVersion": "2026-07-28",
				"io.modelcontextprotocol/clientCapabilities": {}
			}
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", mcpparser.MCPVersionModern)
	req.Header.Set("Mcp-Method", "ping")

	s := classifyingHandlerTestServer()
	nextCalled := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true })

	rec := httptest.NewRecorder()
	mcpparser.ParsingMiddleware(s.classifyingHandler(next)).ServeHTTP(rec, req)

	require.False(t, nextCalled, "a well-formed Modern ping must dispatch, not fall through to the SDK")
	require.Equal(t, http.StatusOK, rec.Code, "must not be the 404 a mis-routed -32601 would produce")
	require.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{}}`, rec.Body.String())

	fc, ok := s.core.(*modernFakeCore)
	require.True(t, ok)
	assert.False(t, fc.callCalled, "ping must not reach the core")
}

// TestDispatchModern_MethodRouting spot-checks that each method routes to the
// matching core call and produces an envelope with the expected resultType
// and top-level result key -- deep envelope shape (cacheable, serverInfo,
// empty-collection marshalling) is already covered by Step 1's
// modern_envelope_test.go. ping is the one exception: it wants a bare {}
// result (see the case's doc comment in modern_dispatch.go), so it opts out
// of the resultType/wantKey assertions via wantBareResult.
func TestDispatchModern_MethodRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		parsed         *mcpparser.ParsedMCPRequest
		fakeCore       *modernFakeCore
		wantKey        string // key expected inside result
		wantBareResult bool   // ping: result == {}, no resultType/_meta
	}{
		{
			name:     "tools/list",
			parsed:   &mcpparser.ParsedMCPRequest{Method: "tools/list", IsRequest: true, ID: "1"},
			fakeCore: &modernFakeCore{tools: []vmcp.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}},
			wantKey:  "tools",
		},
		{
			name:     "resources/list",
			parsed:   &mcpparser.ParsedMCPRequest{Method: "resources/list", IsRequest: true, ID: "1"},
			fakeCore: &modernFakeCore{resources: []vmcp.Resource{{Name: "info", URI: "embedded:info"}}},
			wantKey:  "resources",
		},
		{
			name:   "resources/templates/list",
			parsed: &mcpparser.ParsedMCPRequest{Method: "resources/templates/list", IsRequest: true, ID: "1"},
			fakeCore: &modernFakeCore{
				templates: []vmcp.ResourceTemplate{{Name: "logs", URITemplate: "file:///{date}.txt"}},
			},
			wantKey: "resourceTemplates",
		},
		{
			name:     "prompts/list",
			parsed:   &mcpparser.ParsedMCPRequest{Method: "prompts/list", IsRequest: true, ID: "1"},
			fakeCore: &modernFakeCore{prompts: []vmcp.Prompt{{Name: "review"}}},
			wantKey:  "prompts",
		},
		{
			name:     "server/discover",
			parsed:   &mcpparser.ParsedMCPRequest{Method: "server/discover", IsRequest: true, ID: "1"},
			fakeCore: &modernFakeCore{tools: []vmcp.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}},
			wantKey:  "capabilities",
		},
		{
			name:     "tools/call",
			parsed:   &mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: "echo", IsRequest: true, ID: "1"},
			fakeCore: &modernFakeCore{},
			wantKey:  "content",
		},
		{
			name:     "resources/read",
			parsed:   &mcpparser.ParsedMCPRequest{Method: "resources/read", ResourceID: "embedded:info", IsRequest: true, ID: "1"},
			fakeCore: &modernFakeCore{},
			wantKey:  "contents",
		},
		{
			name:     "prompts/get",
			parsed:   &mcpparser.ParsedMCPRequest{Method: "prompts/get", ResourceID: "review", IsRequest: true, ID: "1"},
			fakeCore: &modernFakeCore{},
			wantKey:  "messages",
		},
		{
			name: "completion/complete",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "completion/complete", IsRequest: true, ID: "1",
				Params: json.RawMessage(`{"ref":{"type":"ref/prompt","name":"review"},"argument":{"name":"style","value":"terse"}}`),
			},
			fakeCore: &modernFakeCore{},
			wantKey:  "completion",
		},
		{
			name:           "ping",
			parsed:         &mcpparser.ParsedMCPRequest{Method: "ping", IsRequest: true, ID: "1"},
			fakeCore:       &modernFakeCore{},
			wantBareResult: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec, body := dispatchModernTest(t.Context(), t, tt.fakeCore, false, tt.parsed)

			require.Equal(t, http.StatusOK, rec.Code)
			result, ok := body["result"].(map[string]any)
			require.True(t, ok, "expected a JSON-RPC result envelope, got %v", body)
			if tt.wantBareResult {
				assert.Empty(t, result, "ping must return a bare {} result with no resultType/_meta")
				assert.False(t, tt.fakeCore.callCalled, "ping must not reach the core")
				return
			}
			assert.Equal(t, "complete", result["resultType"])
			assert.Contains(t, result, tt.wantKey)
		})
	}
}

// TestDispatchModern_ListError asserts a core List* error maps to -32603
// (HTTP 200), same posture as the other error paths -- guards the list
// branch against silently swallowing an aggregation failure.
func TestDispatchModern_ListError(t *testing.T) {
	t.Parallel()

	parsed := &mcpparser.ParsedMCPRequest{Method: "tools/list", IsRequest: true, ID: "1"}
	fc := &modernFakeCore{listErr: errors.New("aggregation exploded")}

	rec, body := dispatchModernTest(t.Context(), t, fc, false, parsed)

	assert.Equal(t, http.StatusOK, rec.Code)
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "expected a JSON-RPC error envelope, got %v", body)
	assert.Equal(t, float64(jsonRPCCodeInternalError), errObj["code"])
}

// TestDispatchModern_Discover asserts server/discover's flags reflect exactly
// what the fake core admits: an empty admitted set advertises no capability
// (no descriptor arrays either way), and a populated set flags only the
// features that came back non-empty.
func TestDispatchModern_Discover(t *testing.T) {
	t.Parallel()

	parsed := &mcpparser.ParsedMCPRequest{Method: "server/discover", IsRequest: true, ID: "1"}

	t.Run("nothing admitted -- only the static completions capability", func(t *testing.T) {
		t.Parallel()

		rec, body := dispatchModernTest(t.Context(), t, &modernFakeCore{}, false, parsed)

		require.Equal(t, http.StatusOK, rec.Code)
		result, ok := body["result"].(map[string]any)
		require.True(t, ok, "got %v", body)
		caps, ok := result["capabilities"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, map[string]any{"completions": map[string]any{}}, caps,
			"completions is unconditional; no admitted list-backed feature must advertise nothing else")
	})

	t.Run("admitted view flags only the populated features", func(t *testing.T) {
		t.Parallel()

		fc := &modernFakeCore{
			discoverCaps: core.DiscoverCapabilities{HasTools: true, HasResourceTemplates: true},
		}
		rec, body := dispatchModernTest(t.Context(), t, fc, false, parsed)

		require.Equal(t, http.StatusOK, rec.Code)
		result, ok := body["result"].(map[string]any)
		require.True(t, ok, "got %v", body)
		caps, ok := result["capabilities"].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, caps, "tools")
		assert.Contains(t, caps, "resources", "a resource template alone must still set the resources flag")
		assert.Contains(t, caps, "completions", "completions is unconditional")
		assert.NotContains(t, caps, "prompts")
	})

	t.Run("a core Discover error maps to -32603", func(t *testing.T) {
		t.Parallel()

		fc := &modernFakeCore{discoverErr: errors.New("aggregation exploded")}
		rec, body := dispatchModernTest(t.Context(), t, fc, false, parsed)

		assert.Equal(t, http.StatusOK, rec.Code)
		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok, "expected a JSON-RPC error envelope, got %v", body)
		assert.Equal(t, float64(jsonRPCCodeInternalError), errObj["code"])
	})
}

// TestDispatchModern_Complete covers the completion/complete dispatch path:
// successful routing to core.Complete with no pre-dispatch Check* gate (see
// call_gate.go's comment on why completion/complete is intentionally not
// wire-gated), ref-type-scoped deny-message reclassification on
// ErrAuthorizationFailed (mirroring the SDK path's coreCompletionHandler), a
// non-authz core error mapping to -32603, and malformed params rejected
// before the core is ever reached.
func TestDispatchModern_Complete(t *testing.T) {
	t.Parallel()

	validPromptRef := json.RawMessage(
		`{"ref":{"type":"ref/prompt","name":"review"},"argument":{"name":"style","value":"terse"}}`)
	validResourceRef := json.RawMessage(
		`{"ref":{"type":"ref/resource","uri":"file:///{date}.txt"},"argument":{"name":"date","value":"2026"}}`)

	t.Run("success returns the completion envelope with no Check* call", func(t *testing.T) {
		t.Parallel()

		parsed := &mcpparser.ParsedMCPRequest{Method: "completion/complete", IsRequest: true, ID: "1", Params: validPromptRef}
		fc := &modernFakeCore{}

		rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

		require.Equal(t, http.StatusOK, rec.Code)
		result, ok := body["result"].(map[string]any)
		require.True(t, ok, "got %v", body)
		assert.Equal(t, "complete", result["resultType"])
		completion, ok := result["completion"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, []any{"opt1", "opt2"}, completion["values"])
		assert.True(t, fc.callCalled)
		assert.False(t, fc.checkCalled, "completion/complete has no pre-dispatch Check* gate")
	})

	t.Run("ErrAuthorizationFailed on a prompt ref reclassifies to 403 with the prompt-get deny message", func(t *testing.T) {
		t.Parallel()

		parsed := &mcpparser.ParsedMCPRequest{Method: "completion/complete", IsRequest: true, ID: "1", Params: validPromptRef}
		fc := &modernFakeCore{completeErr: fmt.Errorf("%w: denied", vmcp.ErrAuthorizationFailed)}

		rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, float64(mcpparser.JSONRPCCodeDenied), errObj["code"])
		assert.Equal(t, vmcp.DenyMessagePromptGet, errObj["message"])
	})

	t.Run("ErrAuthorizationFailed on a resource ref reclassifies to 403 with the resource-read deny message", func(t *testing.T) {
		t.Parallel()

		parsed := &mcpparser.ParsedMCPRequest{Method: "completion/complete", IsRequest: true, ID: "1", Params: validResourceRef}
		fc := &modernFakeCore{completeErr: fmt.Errorf("%w: denied", vmcp.ErrAuthorizationFailed)}

		rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, vmcp.DenyMessageResourceRead, errObj["message"])
	})

	t.Run("a non-authz core error maps to -32603", func(t *testing.T) {
		t.Parallel()

		parsed := &mcpparser.ParsedMCPRequest{Method: "completion/complete", IsRequest: true, ID: "1", Params: validPromptRef}
		fc := &modernFakeCore{completeErr: errors.New("backend exploded")}

		rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

		assert.Equal(t, http.StatusOK, rec.Code)
		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, float64(jsonRPCCodeInternalError), errObj["code"])
	})

	t.Run("malformed params rejected before the core is reached", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name   string
			params json.RawMessage
		}{
			{name: "missing ref", params: json.RawMessage(`{"argument":{"name":"x","value":"y"}}`)},
			{
				name:   "ref missing type",
				params: json.RawMessage(`{"ref":{"name":"review"},"argument":{"name":"x","value":"y"}}`),
			},
			{
				name:   "ref not an object",
				params: json.RawMessage(`{"ref":"review","argument":{"name":"x","value":"y"}}`),
			},
			{
				name:   "missing argument.name",
				params: json.RawMessage(`{"ref":{"type":"ref/prompt","name":"review"},"argument":{"value":"y"}}`),
			},
			{
				name:   "empty argument.name",
				params: json.RawMessage(`{"ref":{"type":"ref/prompt","name":"review"},"argument":{"name":"","value":"y"}}`),
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				parsed := &mcpparser.ParsedMCPRequest{
					Method: "completion/complete", IsRequest: true, ID: "1", Params: tt.params,
				}
				fc := &modernFakeCore{}

				rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

				assert.Equal(t, http.StatusBadRequest, rec.Code)
				errObj, ok := body["error"].(map[string]any)
				require.True(t, ok, "got %v", body)
				assert.Equal(t, float64(jsonRPCCodeInvalidParams), errObj["code"])
				assert.False(t, fc.callCalled, "malformed params must not reach the core")
			})
		}
	})
}

// TestDispatchModern_AuthzGate is the security-load-bearing table: it drives
// the re-homed gate for each of the three gated methods (tools/call,
// resources/read, prompts/get) through every branch authzCallGate itself
// supports, PLUS the dispatch-time (TOCTOU) reclassification that only exists
// here because dispatchModern -- unlike the gate -- also owns the real call.
func TestDispatchModern_AuthzGate(t *testing.T) {
	t.Parallel()

	deny := fmt.Errorf("%w: policy said no", vmcp.ErrAuthorizationFailed)
	infra := errors.New("aggregation exploded")

	type gatedCase struct {
		method     string
		resourceID string
		wantKey    string
		denyMsg    string
	}
	cases := []gatedCase{
		{method: "tools/call", resourceID: "secret-tool", wantKey: "content", denyMsg: vmcp.DenyMessageToolCall},
		{method: "resources/read", resourceID: "file://secret", wantKey: "contents", denyMsg: vmcp.DenyMessageResourceRead},
		{method: "prompts/get", resourceID: "secret-prompt", wantKey: "messages", denyMsg: vmcp.DenyMessagePromptGet},
	}

	for _, c := range cases {
		parsed := &mcpparser.ParsedMCPRequest{Method: c.method, ResourceID: c.resourceID, IsRequest: true, ID: "1"}

		t.Run(c.method+"/denied pre-dispatch returns 403", func(t *testing.T) {
			t.Parallel()
			fc := &modernFakeCore{checkToolErr: deny, checkResourceErr: deny, checkPromptErr: deny}

			rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

			assert.Equal(t, http.StatusForbidden, rec.Code)
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, float64(mcpparser.JSONRPCCodeDenied), errObj["code"])
			assert.Equal(t, c.denyMsg, errObj["message"])
			assert.True(t, fc.checkCalled, "Check* must have been invoked")
			assert.False(t, fc.callCalled, "the real call must NOT run once Check* denies")
		})

		t.Run(c.method+"/[HIGH] denied at dispatch time (TOCTOU) still returns 403 not -32603", func(t *testing.T) {
			t.Parallel()
			// Check* allows (nil), but the real call denies -- the aggregation
			// re-run at dispatch time disagrees with the pre-flight check.
			fc := &modernFakeCore{callToolErr: deny, readResourceErr: deny, getPromptErr: deny}

			rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

			assert.Equal(t, http.StatusForbidden, rec.Code, "a dispatch-time authz denial must audit as 403/denied")
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, float64(mcpparser.JSONRPCCodeDenied), errObj["code"],
				"must be the denial code, never -32603 -- that would audit as failure instead of denied")
			assert.Equal(t, c.denyMsg, errObj["message"])
			assert.True(t, fc.checkCalled)
			assert.True(t, fc.callCalled, "the real call must still run when Check* allows")
		})

		t.Run(c.method+"/infra error at Check* fails open, dispatch still happens", func(t *testing.T) {
			t.Parallel()
			fc := &modernFakeCore{checkToolErr: infra, checkResourceErr: infra, checkPromptErr: infra}

			rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

			assert.Equal(t, http.StatusOK, rec.Code, "an infra error at Check* must not become a 403")
			result, ok := body["result"].(map[string]any)
			require.True(t, ok, "dispatch must still have proceeded and returned a result, got %v", body)
			assert.Contains(t, result, c.wantKey)
			assert.True(t, fc.callCalled)
		})

		t.Run(c.method+"/non-authz error at dispatch time maps to -32603 not 403", func(t *testing.T) {
			t.Parallel()
			fc := &modernFakeCore{callToolErr: infra, readResourceErr: infra, getPromptErr: infra}

			rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

			assert.Equal(t, http.StatusOK, rec.Code)
			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, float64(jsonRPCCodeInternalError), errObj["code"])
		})

		t.Run(c.method+"/authz disabled skips Check* and dispatches", func(t *testing.T) {
			t.Parallel()
			// Even a would-be-denying Check* must never run when the gate is off.
			fc := &modernFakeCore{checkToolErr: deny, checkResourceErr: deny, checkPromptErr: deny}

			rec, body := dispatchModernTest(t.Context(), t, fc, false, parsed)

			assert.Equal(t, http.StatusOK, rec.Code)
			result, ok := body["result"].(map[string]any)
			require.True(t, ok, "got %v", body)
			assert.Contains(t, result, c.wantKey)
			assert.False(t, fc.checkCalled, "Check* must not run when authzGateEnabled is false")
			assert.True(t, fc.callCalled)
		})
	}
}

// TestDispatchModern_ArgumentsShape asserts the raw-params shape guard on
// tools/call and prompts/get: the parser (handleNamedResourceMethod)
// silently coerces a present-but-non-object "arguments" value to nil --
// indistinguishable from "absent" -- so the guard must inspect parsed.Params
// directly, and must run BEFORE the authz gate and the real call.
// resources/read has no arguments and is intentionally not covered here.
func TestDispatchModern_ArgumentsShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		method     string
		resourceID string
		wantKey    string
	}{
		{method: "tools/call", resourceID: "echo", wantKey: "content"},
		{method: "prompts/get", resourceID: "review", wantKey: "messages"},
	}

	for _, c := range cases {
		t.Run(c.method+"/non-object arguments rejected before Check* or the call", func(t *testing.T) {
			t.Parallel()
			for _, raw := range []string{`"a string"`, `[1,2,3]`} {
				parsed := &mcpparser.ParsedMCPRequest{
					Method: c.method, ResourceID: c.resourceID, IsRequest: true, ID: "1",
					Params: json.RawMessage(`{"name":"` + c.resourceID + `","arguments":` + raw + `}`),
				}
				fc := &modernFakeCore{}

				rec, body := dispatchModernTest(t.Context(), t, fc, true, parsed)

				assert.Equal(t, http.StatusBadRequest, rec.Code, "-32602 invalid params maps to HTTP 400")
				errObj, ok := body["error"].(map[string]any)
				require.True(t, ok, "expected a JSON-RPC error envelope for arguments=%s, got %v", raw, body)
				assert.Equal(t, float64(jsonRPCCodeInvalidParams), errObj["code"])
				assert.False(t, fc.checkCalled, "Check* must not run for invalid arguments shape")
				assert.False(t, fc.callCalled, "the real call must not run for invalid arguments shape")
			}
		})

		t.Run(c.method+"/absent arguments proceeds", func(t *testing.T) {
			t.Parallel()
			parsed := &mcpparser.ParsedMCPRequest{
				Method: c.method, ResourceID: c.resourceID, IsRequest: true, ID: "1",
				Params: json.RawMessage(`{"name":"` + c.resourceID + `"}`),
			}
			fc := &modernFakeCore{}

			rec, body := dispatchModernTest(t.Context(), t, fc, false, parsed)

			assert.Equal(t, http.StatusOK, rec.Code)
			result, ok := body["result"].(map[string]any)
			require.True(t, ok, "got %v", body)
			assert.Contains(t, result, c.wantKey)
			assert.True(t, fc.callCalled)
		})

		t.Run(c.method+"/object arguments proceeds", func(t *testing.T) {
			t.Parallel()
			parsed := &mcpparser.ParsedMCPRequest{
				Method: c.method, ResourceID: c.resourceID, IsRequest: true, ID: "1",
				Params: json.RawMessage(`{"name":"` + c.resourceID + `","arguments":{"x":1}}`),
			}
			fc := &modernFakeCore{}

			rec, body := dispatchModernTest(t.Context(), t, fc, false, parsed)

			assert.Equal(t, http.StatusOK, rec.Code)
			result, ok := body["result"].(map[string]any)
			require.True(t, ok, "got %v", body)
			assert.Contains(t, result, c.wantKey)
			assert.True(t, fc.callCalled)
		})

		t.Run(c.method+"/explicit null arguments proceeds", func(t *testing.T) {
			t.Parallel()
			// hasNonObjectArguments relies on json.Unmarshal("null", &raw.Arguments)
			// leaving raw.Arguments nil with no error -- an explicit JSON null is a
			// legitimate no-args call, same as an absent field, not a rejected shape.
			parsed := &mcpparser.ParsedMCPRequest{
				Method: c.method, ResourceID: c.resourceID, IsRequest: true, ID: "1",
				Params: json.RawMessage(`{"name":"` + c.resourceID + `","arguments":null}`),
			}
			fc := &modernFakeCore{}

			rec, body := dispatchModernTest(t.Context(), t, fc, false, parsed)

			assert.Equal(t, http.StatusOK, rec.Code)
			result, ok := body["result"].(map[string]any)
			require.True(t, ok, "got %v", body)
			assert.Contains(t, result, c.wantKey)
			assert.True(t, fc.callCalled)
		})
	}
}

// contextProbeKey is a private key used only to prove r.Context() reaches the
// core call unmodified (not detached, not re-wrapped): headerforward reads
// forwarded-header state off this exact context per call, so a detach would
// silently break backend auth on every Modern request.
type contextProbeKey struct{}

func TestDispatchModern_ContextPassthrough(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(t.Context(), contextProbeKey{}, "probe-value")
	parsed := &mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: "echo", IsRequest: true, ID: "1"}
	fc := &modernFakeCore{}

	_, _ = dispatchModernTest(ctx, t, fc, false, parsed)

	require.NotNil(t, fc.gotCtx)
	assert.Equal(t, "probe-value", fc.gotCtx.Value(contextProbeKey{}),
		"dispatchModern must pass r.Context() through to core.CallTool unmodified")
}

// TestDispatchModern_LabelsAuditBackend mirrors TestServeHandlersLabelAuditBackend
// (serve_session_test.go) for the Modern path: after a successful tools/call,
// resources/read, or prompts/get, the dispatcher must write the registry-resolved
// backend name into the audit BackendInfo carried in the request context, using the
// same s.backendDisplayName resolution the Serve path's handlers use. completion/complete
// is deliberately excluded -- see dispatchModernComplete's doc comment on why that gap
// is pre-existing on both paths, not something this fix should introduce for Modern only.
func TestDispatchModern_LabelsAuditBackend(t *testing.T) {
	t.Parallel()

	// BackendID "backend-x" != Name "github-mcp": a pass-through would record the ID.
	reg := vmcp.NewImmutableRegistry([]vmcp.Backend{{ID: "backend-x", Name: "github-mcp"}})

	tests := []struct {
		name   string
		parsed *mcpparser.ParsedMCPRequest
	}{
		{name: "tools/call", parsed: &mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: "echo", IsRequest: true, ID: "1"}},
		{
			name: "resources/read",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "resources/read", ResourceID: "file://a", IsRequest: true, ID: "1",
			},
		},
		{
			name: "prompts/get",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "prompts/get", ResourceID: "review", IsRequest: true, ID: "1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Server{
				config:          &Config{Name: testServerName, Version: testServerVersion},
				core:            &modernFakeCore{backendID: "backend-x"},
				backendRegistry: reg,
			}

			bi := &audit.BackendInfo{}
			ctx := audit.WithBackendInfo(t.Context(), bi)
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil).WithContext(ctx)
			rec := httptest.NewRecorder()

			s.dispatchModern(rec, req, tt.parsed)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "github-mcp", bi.BackendName,
				"the Modern dispatcher must label the audit event with the registry-resolved backend name")
		})
	}
}

// TestDispatchModern_NoBackendIDSkipsAuditLabel locks in the composite-tool case: a
// result with an empty BackendID (executeComposite never sets one, core_calls.go) must
// not touch the audit BackendInfo at all, matching the "no single serving backend"
// semantics documented on vmcp.ToolCallResult.BackendID.
func TestDispatchModern_NoBackendIDSkipsAuditLabel(t *testing.T) {
	t.Parallel()

	parsed := &mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: "echo", IsRequest: true, ID: "1"}
	fc := &modernFakeCore{} // backendID left empty

	bi := &audit.BackendInfo{}
	ctx := audit.WithBackendInfo(t.Context(), bi)
	rec, _ := dispatchModernTest(ctx, t, fc, false, parsed)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, bi.BackendName, "an empty BackendID must leave the audit label untouched")
}
