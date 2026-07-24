// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	sessionfactorymocks "github.com/stacklok/toolhive/pkg/vmcp/session/mocks"
	sessionmocks "github.com/stacklok/toolhive/pkg/vmcp/session/types/mocks"
)

// perToolCore is a fakeCore variant whose CallTool returns a result text equal
// to the called tool's name. This lets the context-isolation test detect
// cross-contamination: each concurrent call must observe its own tool name.
type perToolCore struct {
	fakeCore
}

func (p *perToolCore) CallTool(
	_ context.Context, _ *auth.Identity, name string, _ map[string]any, _ map[string]any,
) (*vmcp.ToolCallResult, error) {
	p.callToolCalls.Add(1)
	p.lastCallToolName.Store(name)
	return &vmcp.ToolCallResult{
		Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: name}},
	}, nil
}

// newPerToolSessionFactory mirrors newToolSessionFactory but wires a per-tool
// CallTool result on the mock MultiSession so the SDK tool handler routes
// through perToolCore (whose result carries the tool name). The mock session's
// CallTool is only a fallback; the Serve path routes through coreToolHandler →
// core.CallTool, so perToolCore.CallTool is the one that runs.
func newPerToolSessionFactory(
	t *testing.T, ctrl *gomock.Controller, tools []vmcp.Tool,
) *sessionfactorymocks.MockMultiSessionFactory {
	t.Helper()
	factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, _ vmcpsession.ListChangedSink) (vmcpsession.MultiSession, error) {
			mock := sessionmocks.NewMockMultiSession(ctrl)
			mock.EXPECT().ID().Return(id).AnyTimes()
			mock.EXPECT().UpdatedAt().Return(time.Time{}).AnyTimes()
			mock.EXPECT().CreatedAt().Return(time.Time{}).AnyTimes()
			mock.EXPECT().Type().Return(transportsession.SessionType("")).AnyTimes()
			mock.EXPECT().GetData().Return(nil).AnyTimes()
			mock.EXPECT().SetData(gomock.Any()).AnyTimes()
			mock.EXPECT().GetMetadata().Return(map[string]string{
				vmcpsession.MetadataKeyIdentityBinding: "unauthenticated",
			}).AnyTimes()
			mock.EXPECT().GetMetadataValue(vmcpsession.MetadataKeyIdentityBinding).
				Return("unauthenticated", true).AnyTimes()
			mock.EXPECT().SetMetadata(gomock.Any(), gomock.Any()).AnyTimes()
			toolsCopy := make([]vmcp.Tool, len(tools))
			copy(toolsCopy, tools)
			mock.EXPECT().Tools().Return(toolsCopy).AnyTimes()
			mock.EXPECT().AllTools().Return(toolsCopy).AnyTimes()
			mock.EXPECT().Resources().Return(nil).AnyTimes()
			mock.EXPECT().Prompts().Return(nil).AnyTimes()
			mock.EXPECT().BackendSessions().Return(nil).AnyTimes()
			rt := &vmcp.RoutingTable{Tools: make(map[string]*vmcp.BackendTarget, len(tools))}
			for _, tool := range tools {
				rt.Tools[tool.Name] = &vmcp.BackendTarget{WorkloadID: tool.Name}
			}
			mock.EXPECT().GetRoutingTable().Return(rt).AnyTimes()
			mock.EXPECT().ReadResource(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
			mock.EXPECT().GetPrompt(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
			mock.EXPECT().CallTool(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(&vmcp.ToolCallResult{Content: []vmcp.Content{{Type: "text", Text: "ok"}}}, nil).AnyTimes()
			mock.EXPECT().Close().Return(nil).AnyTimes()
			return mock, nil
		}).AnyTimes()
	return factory
}

// TestRegression_ConcurrentToolCalls_NoAuditBleed fires two tools/call POSTs
// concurrently against the same session and asserts each response carries its
// OWN tool's result text — no cross-contamination. The Serve path sets the
// audit BackendInfo per-request via the handler closure (coreToolHandler writes
// the pre-resolved backend name), so concurrent calls must not bleed a backend
// name or result from one request into another.
//
// The audit BackendInfo is attached per-request by the audit middleware (a fresh
// *BackendInfo per request, see pkg/audit/auditor.go Middleware), and the Serve
// handler writes to it from the per-request context — so the isolation surface
// under test is the result text and the observed backend label. We assert each
// call's result text matches the tool it invoked.
func TestRegression_ConcurrentToolCalls_NoAuditBleed(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	tools := []vmcp.Tool{{Name: "tool-a"}, {Name: "tool-b"}}
	fc := &perToolCore{fakeCore: fakeCore{tools: tools}}
	factory := newPerToolSessionFactory(t, ctrl, tools)

	srv, err := Serve(context.Background(), fc, &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
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
	baseURL := ts.URL

	initResp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	require.Eventually(t, func() bool {
		_, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID)
		return ok
	}, 2*time.Second, 10*time.Millisecond, "session should be registered")

	type callOutcome struct {
		toolName string
		body     string
		err      error
	}

	var wg sync.WaitGroup
	outcomes := make([]callOutcome, len(tools))
	wg.Add(len(tools))
	for i, tool := range tools {
		i, tool := i, tool
		go func() {
			defer wg.Done()
			// doServeMCP returns an error instead of calling require: FailNow from a
			// worker goroutine only runs Goexit off the test goroutine and misreports.
			// All assertions happen on the test goroutine after wg.Wait() below.
			resp, doErr := doServeMCP(baseURL, map[string]any{
				"jsonrpc": "2.0",
				"id":      2 + i,
				"method":  "tools/call",
				"params": map[string]any{
					"name":      tool.Name,
					"arguments": map[string]any{},
				},
			}, sessionID)
			if doErr != nil {
				outcomes[i] = callOutcome{toolName: tool.Name, err: doErr}
				return
			}
			defer resp.Body.Close()
			body, readErr := io.ReadAll(resp.Body)
			outcomes[i] = callOutcome{toolName: tool.Name, body: string(body), err: readErr}
		}()
	}

	// Fail-fast wait: never block indefinitely on a WaitGroup.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent tool calls to complete")
	}

	// Each call must have returned its OWN tool name in the result text, proving
	// no result cross-contamination between the concurrent requests.
	for _, o := range outcomes {
		require.NoError(t, o.err)
		assert.Contains(t, o.body, o.toolName,
			"concurrent call for %q must carry its own tool's result; got %s", o.toolName, o.body)
	}

	// The core must have been reached exactly once per tool (2 total) — no
	// duplicated or dropped calls.
	assert.Equal(t, int32(len(tools)), fc.callToolCalls.Load(),
		"core.CallTool must be called exactly once per concurrent request")

	// Verify coreToolHandler labels the request-scoped audit BackendInfo with the
	// backend name it was constructed with. Each goroutine owns a freshly-allocated
	// BackendInfo and passes its own backend name, so this is NOT a shared-state
	// isolation test — it asserts the handler copies the resolved backend name into
	// the per-request BackendInfo (rather than dropping or swapping it). Running it
	// concurrently additionally lets the race detector flag any accidental sharing
	// of the labelling surface across invocations.
	type labelOutcome struct {
		want   string
		got    string
		hasRes bool
		err    error
	}
	var bgWG sync.WaitGroup
	bgWG.Add(len(tools))
	labelOutcomes := make([]labelOutcome, len(tools))
	for i, tool := range tools {
		i, tool := i, tool
		go func() {
			defer bgWG.Done()
			// No require/assert here: collect results and assert on the test
			// goroutine after Wait, since FailNow is only safe there.
			bi := &audit.BackendInfo{}
			ctx := audit.WithBackendInfo(context.Background(), bi)
			req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{}}}
			res, err := srv.coreToolHandler(sessionID, tool.Name, tool.Name)(ctx, req)
			labelOutcomes[i] = labelOutcome{want: tool.Name, got: bi.BackendName, hasRes: res != nil, err: err}
		}()
	}
	bgDone := make(chan struct{})
	go func() { bgWG.Wait(); close(bgDone) }()
	select {
	case <-bgDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for backend-label goroutines")
	}

	// Assert on the test goroutine (require/FailNow is only safe here). Each
	// handler invocation must have labelled its own BackendInfo with the backend
	// name it was given.
	for _, o := range labelOutcomes {
		require.NoError(t, o.err)
		require.True(t, o.hasRes, "coreToolHandler must return a non-nil result")
		assert.Equal(t, o.want, o.got,
			"coreToolHandler must copy the resolved backend name %q into the request-scoped audit BackendInfo",
			o.want)
	}
}
