// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpratelimit "github.com/stacklok/toolhive/pkg/vmcp/ratelimit"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	"github.com/stacklok/toolhive/pkg/vmcp/session/optimizerdec"
)

// These tests cover the Serve-path optimizer wiring (#5538): when the optimizer is
// enabled on the Serve path, tools/list advertises only find_tool/call_tool sourced
// from core.ListTools; call_tool dispatches the inner tool through core.CallTool with
// its real name (closing the deferred inner-target admission gap); identity binding is
// enforced on both meta-tools; and cross-pod re-injection re-advertises the pair. The
// legacy server.New optimizer path is unchanged and covered by
// TestIntegration_SessionManagement_OptimizerMode (s.core == nil).

// dispatchOptimizer is a test optimizer.Optimizer that mirrors the real optimizer's
// dispatch without its SQLite/embedding store: FindTool returns the tools it was built
// over (so find_tool's result is observable), and CallTool looks the inner tool up by
// name and invokes its handler — exercising the coreToolHandler → core.CallTool path
// that closes the inner-target admission gap.
type dispatchOptimizer struct {
	tools map[string]server.ServerTool
	defs  []mcp.Tool
}

var _ optimizer.Optimizer = (*dispatchOptimizer)(nil)

func (o *dispatchOptimizer) FindTool(_ context.Context, _ optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
	return &optimizer.FindToolOutput{Tools: o.defs}, nil
}

func (o *dispatchOptimizer) CallTool(ctx context.Context, input optimizer.CallToolInput) (*mcp.CallToolResult, error) {
	tool, ok := o.tools[input.ToolName]
	if !ok {
		return mcp.NewToolResultError(fmt.Sprintf("tool not found: %s", input.ToolName)), nil
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = input.ToolName
	req.Params.Arguments = input.Parameters
	return tool.Handler(ctx, req)
}

// recordingOptimizerFactory builds dispatchOptimizers and counts how many times it is
// invoked. The count is the double-indexing guard (AC6): on the Serve path the factory
// must be called exactly once per session (by the Serve layer), never also by the
// session-factory decorator.
type recordingOptimizerFactory struct {
	calls atomic.Int32
}

func (f *recordingOptimizerFactory) build(_ context.Context, tools []server.ServerTool) (optimizer.Optimizer, error) {
	f.calls.Add(1)
	toolMap := make(map[string]server.ServerTool, len(tools))
	defs := make([]mcp.Tool, 0, len(tools))
	for _, t := range tools {
		toolMap[t.Tool.Name] = t
		defs = append(defs, t.Tool)
	}
	return &dispatchOptimizer{tools: toolMap, defs: defs}, nil
}

var initBody = map[string]any{
	"jsonrpc": "2.0",
	"id":      1,
	"method":  "initialize",
	"params": map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
	},
}

// registerServeOptimizerSession builds a Serve server with the optimizer enabled
// (OptimizerFactory + AdvertiseFromCore) over vmcpCore, registers one anonymous
// session via the SDK initialize path, and returns the server, session ID, HTTP
// base URL, and recording factory. Mirrors registerServeSession in optimizer mode.
func registerServeOptimizerSession(
	t *testing.T, vmcpCore core.VMCP, tools []vmcp.Tool,
) (*Server, string, string, *recordingOptimizerFactory) {
	t.Helper()
	ctrl := gomock.NewController(t)
	factory, _ := newToolSessionFactory(t, ctrl, tools)
	optFactory := &recordingOptimizerFactory{}

	srv, err := Serve(context.Background(), vmcpCore, &ServerConfig{
		SessionTTL: time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{
			Base:              factory,
			OptimizerFactory:  optFactory.build,
			AdvertiseFromCore: true,
		},
		BackendRegistry: vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	streamable := server.NewStreamableHTTPServer(
		srv.mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithSessionIdManager(srv.vmcpSessionMgr),
	)
	ts := httptest.NewServer(streamable)
	t.Cleanup(ts.Close)

	initResp := postServeMCP(t, ts.URL, initBody, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	require.Eventually(t, func() bool { _, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID); return ok },
		2*time.Second, 10*time.Millisecond, "session should be registered")
	return srv, sessionID, ts.URL, optFactory
}

// optimizerMetaHandlers returns the Serve-path find_tool/call_tool SDK handlers for a
// registered session, keyed by tool name, by invoking the same builder registration
// uses (serveSessionTools).
func optimizerMetaHandlers(
	t *testing.T, srv *Server, sessionID string,
) map[string]server.ToolHandlerFunc {
	t.Helper()
	tools, err := srv.serveSessionTools(context.Background(), sessionID, nil)
	require.NoError(t, err)
	handlers := make(map[string]server.ToolHandlerFunc, len(tools))
	for _, tool := range tools {
		handlers[tool.Tool.Name] = tool.Handler
	}
	return handlers
}

// serveToolNames issues a tools/list against a Serve test server and returns the
// advertised tool names. Empty on a non-200 or undecodable response.
func serveToolNames(t *testing.T, baseURL, sessionID string) []string {
	t.Helper()
	resp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "tools/list",
		"params":  map[string]any{},
	}, sessionID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var body struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	names := make([]string, 0, len(body.Result.Tools))
	for _, tool := range body.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// TestServeOptimizerAdvertisesOnlyFindAndCallTool is the Serve-path counterpart to
// TestIntegration_SessionManagement_OptimizerMode: with the optimizer enabled, tools/list
// advertises exactly {find_tool, call_tool} and hides the raw core tools. It also proves
// AC6 (no double-indexing): the optimizer factory is invoked exactly once per session —
// by the Serve layer, not also by a session-factory decorator.
func TestServeOptimizerAdvertisesOnlyFindAndCallTool(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{
		{Name: "tool-a", Description: "first"},
		{Name: "tool-b", Description: "second"},
	}}
	_, sessionID, baseURL, optFactory := registerServeOptimizerSession(t, fc, fc.tools)

	require.Eventually(t, func() bool {
		for _, n := range serveToolNames(t, baseURL, sessionID) {
			if n == optimizerdec.FindToolName {
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond, "find_tool should appear once the optimizer tools are injected")

	names := serveToolNames(t, baseURL, sessionID)
	assert.Contains(t, names, optimizerdec.FindToolName)
	assert.Contains(t, names, optimizerdec.CallToolName)
	assert.NotContains(t, names, "tool-a", "raw core tools must not be directly advertised in optimizer mode")
	assert.NotContains(t, names, "tool-b")
	assert.Len(t, names, 2, "only find_tool and call_tool should be advertised in optimizer mode")

	// AC6: the factory ran once (Serve layer), not twice (a decorator would double-index).
	assert.Equal(t, int32(1), optFactory.calls.Load(),
		"the optimizer factory must be invoked exactly once per session (no double-indexing)")
	// The advertised set came from the core's single aggregation at registration.
	assert.Equal(t, int32(1), fc.listToolsCalls.Load(),
		"the optimizer must be built over the core's single registration-time aggregation")
}

// TestServeOptimizerFindToolReturnsCoreTools proves find_tool searches the core's
// advertised set: the result carries the tools the optimizer was built over.
func TestServeOptimizerFindToolReturnsCoreTools(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "tool-a"}, {Name: "tool-b"}}}
	srv, sessionID, _, _ := registerServeOptimizerSession(t, fc, fc.tools)

	handler := optimizerMetaHandlers(t, srv, sessionID)[optimizerdec.FindToolName]
	require.NotNil(t, handler)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      optimizerdec.FindToolName,
		Arguments: map[string]any{"tool_description": "anything"},
	}}
	res, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)

	// Decode the result text into the typed output and assert the matched tool names
	// exactly, rather than substring-matching the whole body (which could pass on an
	// unrelated field). Real search/ranking is out of scope per the issue — the test
	// optimizer returns the whole advertised set — so this asserts find_tool surfaces
	// the core's set, not the relevance ordering.
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok, "find_tool result content must be text")
	var out optimizer.FindToolOutput
	require.NoError(t, json.Unmarshal([]byte(text.Text), &out))
	names := make([]string, 0, len(out.Tools))
	for _, tl := range out.Tools {
		names = append(names, tl.Name)
	}
	assert.ElementsMatch(t, []string{"tool-a", "tool-b"}, names,
		"find_tool must return exactly the core's advertised set")
}

// TestServeOptimizerToolHandlerRejectsUnknownMetaTool locks in the defensive default
// branch of optimizerToolHandler: a definition advertised by OptimizerTools() without a
// wired handler must fail at registration (a non-nil error), not silently produce a nil
// handler that would only blow up at call time.
func TestServeOptimizerToolHandlerRejectsUnknownMetaTool(t *testing.T) {
	t.Parallel()

	srv := &Server{}
	handler, err := srv.optimizerToolHandler("sess", "bogus", &dispatchOptimizer{})
	require.Error(t, err)
	assert.Nil(t, handler)
	assert.ErrorContains(t, err, "bogus")
}

// TestServeOptimizerCallToolRoutesThroughCore proves call_tool dispatches the inner
// invocation through core.CallTool with the REAL inner tool name — the path that closes
// the deferred inner-target admission gap (the inner name is what the core's admission
// seam authorizes).
func TestServeOptimizerCallToolRoutesThroughCore(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "tool-a"}}}
	srv, sessionID, _, _ := registerServeOptimizerSession(t, fc, fc.tools)

	handler := optimizerMetaHandlers(t, srv, sessionID)[optimizerdec.CallToolName]
	require.NotNil(t, handler)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name: optimizerdec.CallToolName,
		Arguments: map[string]any{
			"tool_name":  "tool-a",
			"parameters": map[string]any{"k": "v"},
		},
	}}
	res, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)

	require.Equal(t, int32(1), fc.callToolCalls.Load(), "call_tool must route the inner invocation through core.CallTool")
	got, _ := fc.lastCallToolName.Load().(string)
	assert.Equal(t, "tool-a", got, "the core must receive the REAL inner tool name, not call_tool")
}

// TestServeOptimizerCallToolInnerAdmissionDenied proves the closed gap: when the core
// denies the inner target, call_tool returns a generic authorization message and never
// leaks the underlying authorizer detail.
func TestServeOptimizerCallToolInnerAdmissionDenied(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{
		tools:   []vmcp.Tool{{Name: "tool-a"}},
		callErr: fmt.Errorf("%w: cedar said no", vmcp.ErrAuthorizationFailed),
	}
	srv, sessionID, _, _ := registerServeOptimizerSession(t, fc, fc.tools)

	handler := optimizerMetaHandlers(t, srv, sessionID)[optimizerdec.CallToolName]
	require.NotNil(t, handler)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{
		Name:      optimizerdec.CallToolName,
		Arguments: map[string]any{"tool_name": "tool-a", "parameters": map[string]any{}},
	}}
	res, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError, "an inner-target denial must surface as an error result")

	body, _ := json.Marshal(res)
	assert.Contains(t, string(body), "call denied by authorization policy")
	assert.NotContains(t, string(body), "cedar said no", "the underlying authorizer detail must not leak")
	assert.Equal(t, int32(1), fc.callToolCalls.Load(), "the inner target must reach the core admission seam")
}

// singleUseToolLimiter is an in-memory per-tool limiter for the optimizer
// integration test. Calls for other names are deliberately unlimited, so the
// test also proves the optimizer passes the resolved backend tool name.
type singleUseToolLimiter struct {
	toolName string
	calls    atomic.Int32
}

func (l *singleUseToolLimiter) Allow(
	_ context.Context, toolName, _ string,
) (*ratelimit.Decision, error) {
	if toolName != l.toolName {
		return &ratelimit.Decision{Allowed: true}, nil
	}
	if l.calls.Add(1) > 1 {
		return &ratelimit.Decision{Allowed: false, RetryAfter: time.Minute}, nil
	}
	return &ratelimit.Decision{Allowed: true}, nil
}

// TestServeOptimizerCallToolUsesResolvedNameForRateLimiting composes the real
// rate-limit decorator below the Serve-layer optimizer. The first call is
// allowed and reaches the core; the second call to the same resolved tool is
// denied before delegation.
func TestServeOptimizerCallToolUsesResolvedNameForRateLimiting(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "tool-a"}}}
	limiter := &singleUseToolLimiter{toolName: "tool-a"}
	decorated := vmcpratelimit.NewDecorator(fc, limiter)
	_, sessionID, baseURL, _ := registerServeOptimizerSession(t, decorated, fc.tools)

	requestBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": optimizerdec.CallToolName,
			"arguments": map[string]any{
				"tool_name":  "tool-a",
				"parameters": map[string]any{},
			},
		},
	}

	firstResp := postServeMCP(t, baseURL, requestBody, sessionID)
	defer firstResp.Body.Close()
	require.Equal(t, http.StatusOK, firstResp.StatusCode)
	var first struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.NewDecoder(firstResp.Body).Decode(&first))
	require.Nil(t, first.Error)
	assert.False(t, first.Result.IsError)

	secondResp := postServeMCP(t, baseURL, requestBody, sessionID)
	defer secondResp.Body.Close()
	require.Equal(t, http.StatusOK, secondResp.StatusCode)
	var second struct {
		Result struct {
			IsError           bool           `json:"isError"`
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
		Error map[string]any `json:"error"`
	}
	require.NoError(t, json.NewDecoder(secondResp.Body).Decode(&second))
	require.Nil(t, second.Error, "coded tool failures should stay tool results, not JSON-RPC errors")
	assert.True(t, second.Result.IsError)
	assert.EqualValues(t, ratelimit.CodeRateLimited, second.Result.StructuredContent["code"])
	assert.Equal(t, ratelimit.MessageRateLimited, second.Result.StructuredContent["message"])
	data, ok := second.Result.StructuredContent["data"].(map[string]any)
	require.True(t, ok, "rate-limit data should survive optimizer wrapping")
	assert.EqualValues(t, 60, data["retryAfterSeconds"])
	assert.Equal(t, int32(2), limiter.calls.Load(), "the limiter must receive the resolved tool name on both calls")
	assert.Equal(t, int32(1), fc.callToolCalls.Load(), "the denied call must not reach the core")
	got, _ := fc.lastCallToolName.Load().(string)
	assert.Equal(t, "tool-a", got)
}

// TestServeOptimizerEnforcesSessionBinding proves both meta-tools enforce the session's
// identity binding (anti-hijack): an attacker presenting a token on an anonymous session
// is rejected, the session is terminated (fail-closed), and no optimizer work is done.
func TestServeOptimizerEnforcesSessionBinding(t *testing.T) {
	t.Parallel()

	for _, toolName := range []string{optimizerdec.FindToolName, optimizerdec.CallToolName} {
		t.Run(toolName, func(t *testing.T) {
			t.Parallel()
			fc := &fakeCore{tools: []vmcp.Tool{{Name: "tool-a"}}}
			srv, sessionID, _, _ := registerServeOptimizerSession(t, fc, fc.tools)
			handler := optimizerMetaHandlers(t, srv, sessionID)[toolName]
			require.NotNil(t, handler)

			ctx := auth.WithIdentity(context.Background(), &auth.Identity{Token: "attacker-token"})
			req := mcp.CallToolRequest{Params: mcp.CallToolParams{
				Name: toolName,
				Arguments: map[string]any{
					"tool_description": "x",
					"tool_name":        "tool-a",
					"parameters":       map[string]any{},
				},
			}}
			res, err := handler(ctx, req)
			require.NoError(t, err)
			require.NotNil(t, res)
			assert.True(t, res.IsError)
			body, _ := json.Marshal(res)
			assert.Contains(t, string(body), "Unauthorized")

			assert.Equal(t, int32(0), fc.callToolCalls.Load(),
				"a binding failure must reject before any inner core call")
			require.Eventually(t, func() bool { _, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID); return !ok },
				2*time.Second, 10*time.Millisecond, "a binding failure must terminate the session (fail-closed)")
		})
	}
}

// TestServeOptimizerLazyInjectsForRehydratedSession proves cross-pod re-injection (AC5):
// a fresh SDK session (as on a second pod, where OnRegisterSession never fired) gets
// find_tool/call_tool re-injected — not the raw core tools — rebuilt over a fresh
// core.ListTools.
func TestServeOptimizerLazyInjectsForRehydratedSession(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "tool-a"}}}
	srv, sessionID, _, optFactory := registerServeOptimizerSession(t, fc, fc.tools)

	// Capture the registration-time counts so we can prove the rehydration REBUILDS
	// (the half of AC5 that distinguishes the Serve path from the legacy GetAdaptedTools):
	// a fresh core.ListTools aggregation feeds a freshly built optimizer.
	listBefore := fc.listToolsCalls.Load()
	buildsBefore := optFactory.calls.Load()

	rehydrated := &fakeSDKSession{id: sessionID, tools: map[string]server.ServerTool{}}
	srv.lazyInjectSessionTools(srv.mcpServer.WithContext(context.Background(), rehydrated))

	assert.Contains(t, rehydrated.tools, optimizerdec.FindToolName,
		"cross-pod re-injection must advertise find_tool")
	assert.Contains(t, rehydrated.tools, optimizerdec.CallToolName,
		"cross-pod re-injection must advertise call_tool")
	assert.NotContains(t, rehydrated.tools, "tool-a",
		"cross-pod re-injection must not advertise raw core tools in optimizer mode")

	assert.Equal(t, listBefore+1, fc.listToolsCalls.Load(),
		"re-injection must re-derive from a fresh core.ListTools, not a cached set")
	assert.Equal(t, buildsBefore+1, optFactory.calls.Load(),
		"re-injection must rebuild the optimizer over the freshly aggregated set")
}
