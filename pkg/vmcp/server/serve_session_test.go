// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/ratelimit"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	sessionfactorymocks "github.com/stacklok/toolhive/pkg/vmcp/session/mocks"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
	sessionmocks "github.com/stacklok/toolhive/pkg/vmcp/session/types/mocks"
)

// These tests exercise the session-creation wiring and SDK hooks relocated into
// Serve (#5440). They drive the SDK session lifecycle through the relocated
// vmcpSessionMgr + mcpServer directly, mounting the Streamable HTTP server WITHOUT
// the authenticated discovery middleware (relocated by #5442). That keeps the
// test within this task's scope while proving the hooks fire and two-phase session
// creation runs identically when Serve is exercised directly. The full HTTP suite
// stays on server.New (its parity gate) in session_management_integration_test.go.

// toolSessionState tracks observable behaviour of the mock session factory below.
type toolSessionState struct {
	makeWithIDCalled atomic.Bool

	// sinkMu guards capturedSink: the sink passed to MakeSessionWithID (#5748),
	// captured so tests can invoke it directly to simulate an asynchronous
	// backend notification firing after registration.
	sinkMu       sync.Mutex
	capturedSink vmcpsession.ListChangedSink
}

// sink returns the ListChangedSink MakeSessionWithID most recently received, or
// nil if none was supplied.
func (s *toolSessionState) sink() vmcpsession.ListChangedSink {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	return s.capturedSink
}

// newToolSessionFactory creates a MockMultiSessionFactory whose MakeSessionWithID
// returns a MockMultiSession advertising the given tools, mirroring the proven mock
// in session_management_integration_test.go so GetAdaptedTools produces SDK tools.
func newToolSessionFactory(
	t *testing.T, ctrl *gomock.Controller, tools []vmcp.Tool,
) (*sessionfactorymocks.MockMultiSessionFactory, *toolSessionState) {
	t.Helper()
	state := &toolSessionState{}
	factory := sessionfactorymocks.NewMockMultiSessionFactory(ctrl)
	factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend, sink ...vmcpsession.ListChangedSink,
		) (vmcpsession.MultiSession, error) {
			state.makeWithIDCalled.Store(true)
			if len(sink) > 0 {
				state.sinkMu.Lock()
				state.capturedSink = sink[0]
				state.sinkMu.Unlock()
			}
			mock := sessionmocks.NewMockMultiSession(ctrl)
			mock.EXPECT().ID().Return(id).AnyTimes()
			mock.EXPECT().UpdatedAt().Return(time.Time{}).AnyTimes()
			mock.EXPECT().CreatedAt().Return(time.Time{}).AnyTimes()
			mock.EXPECT().Type().Return(transportsession.SessionType("")).AnyTimes()
			mock.EXPECT().GetData().Return(nil).AnyTimes()
			mock.EXPECT().SetData(gomock.Any()).AnyTimes()
			// Identity binding so Terminate takes the storage.Delete path; the sentinel
			// value is sufficient for tests that do not validate the binding content.
			mock.EXPECT().GetMetadata().Return(map[string]string{
				vmcpsession.MetadataKeyIdentityBinding: "unauthenticated",
			}).AnyTimes()
			// enforceSessionBinding reads the binding via the single-key accessor.
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
	return factory, state
}

// fakeCore is a configurable core.VMCP for Serve-path tests. It advertises a fixed
// tool/resource set, counts ListTools/ListResources/CallTool invocations (so tests
// can assert the "exactly once per session" aggregation contract), and resolves
// Lookup* against the advertised set. It is the Serve-path counterpart to the mock
// session factory: the factory establishes the session record, the core supplies the
// advertised set and call routing.
type fakeCore struct {
	tools             []vmcp.Tool
	resources         []vmcp.Resource
	resourceTemplates []vmcp.ResourceTemplate
	prompts           []vmcp.Prompt

	listToolsCalls             atomic.Int32
	listResourcesCalls         atomic.Int32
	listResourceTemplatesCalls atomic.Int32
	listPromptsCalls           atomic.Int32
	callToolCalls              atomic.Int32
	readResourceCalls          atomic.Int32
	getPromptCalls             atomic.Int32
	lastCallToolName           atomic.Value // string
	lastCallToolArgs           atomic.Value // map[string]any
	lastReadURI                atomic.Value // string
	lastGetPromptName          atomic.Value // string
	lastGetPromptArgs          atomic.Value // map[string]any

	completeCalls   atomic.Int32
	lastCompleteRef atomic.Value // vmcp.CompletionRef
	completeValues  []string     // returned by Complete when completeErr is nil

	callErr           error // when set, CallTool returns it (e.g. vmcp.ErrAuthorizationFailed)
	readErr           error // when set, ReadResource returns it
	promptErr         error // when set, GetPrompt returns it (e.g. vmcp.ErrAuthorizationFailed)
	completeErr       error // when set, Complete returns it (e.g. vmcp.ErrAuthorizationFailed)
	lookupResourceErr error // when set, LookupResource returns it for an ADVERTISED URI (admission denial)

	// invalidateCacheCalls counts InvalidateCapabilityCache invocations, so tests
	// covering the list_changed sink can assert the cache was re-swept (#5748).
	invalidateCacheCalls atomic.Int32
}

var _ core.VMCP = (*fakeCore)(nil)

func (f *fakeCore) ListTools(context.Context, *auth.Identity) ([]vmcp.Tool, error) {
	f.listToolsCalls.Add(1)
	return f.tools, nil
}

func (f *fakeCore) CallTool(
	_ context.Context, _ *auth.Identity, name string, args map[string]any, _ map[string]any,
) (*vmcp.ToolCallResult, error) {
	f.callToolCalls.Add(1)
	f.lastCallToolName.Store(name)
	if args != nil {
		f.lastCallToolArgs.Store(args)
	}
	if f.callErr != nil {
		return nil, f.callErr
	}
	return &vmcp.ToolCallResult{Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "ok"}}}, nil
}

func (f *fakeCore) ListResources(context.Context, *auth.Identity) ([]vmcp.Resource, error) {
	f.listResourcesCalls.Add(1)
	return f.resources, nil
}

func (f *fakeCore) ListResourceTemplates(context.Context, *auth.Identity) ([]vmcp.ResourceTemplate, error) {
	f.listResourceTemplatesCalls.Add(1)
	return f.resourceTemplates, nil
}

func (f *fakeCore) ReadResource(_ context.Context, _ *auth.Identity, uri string) (*vmcp.ResourceReadResult, error) {
	f.readResourceCalls.Add(1)
	f.lastReadURI.Store(uri)
	if f.readErr != nil {
		return nil, f.readErr
	}
	return &vmcp.ResourceReadResult{Contents: []vmcp.ResourceContent{{URI: uri, Text: "resource-body"}}}, nil
}

func (f *fakeCore) ListPrompts(context.Context, *auth.Identity) ([]vmcp.Prompt, error) {
	f.listPromptsCalls.Add(1)
	return f.prompts, nil
}

func (f *fakeCore) GetPrompt(
	_ context.Context, _ *auth.Identity, name string, args map[string]any,
) (*vmcp.PromptGetResult, error) {
	f.getPromptCalls.Add(1)
	f.lastGetPromptName.Store(name)
	if args != nil {
		f.lastGetPromptArgs.Store(args)
	}
	if f.promptErr != nil {
		return nil, f.promptErr
	}
	return &vmcp.PromptGetResult{
		Description: "prompt-desc",
		Messages: []vmcp.PromptMessage{
			{Role: "user", Content: vmcp.Content{Type: vmcp.ContentTypeText, Text: "prompt-body"}},
		},
	}, nil
}

func (f *fakeCore) Complete(
	_ context.Context, _ *auth.Identity, ref vmcp.CompletionRef, _, _ string, _ map[string]string,
) (*vmcp.CompletionResult, error) {
	f.completeCalls.Add(1)
	f.lastCompleteRef.Store(ref)
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	return &vmcp.CompletionResult{Values: f.completeValues, Total: len(f.completeValues)}, nil
}

func (f *fakeCore) LookupTool(_ context.Context, _ *auth.Identity, name string) (*vmcp.Tool, error) {
	for i := range f.tools {
		if f.tools[i].Name == name {
			tool := f.tools[i]
			return &tool, nil
		}
	}
	return nil, vmcp.ErrNotFound
}

func (f *fakeCore) LookupResource(_ context.Context, _ *auth.Identity, uri string) (*vmcp.Resource, error) {
	for i := range f.resources {
		if f.resources[i].URI == uri {
			// An advertised resource whose admission is denied: exercises the
			// subscribe/completion admission-denial path (the URI is known, but the
			// caller may not read it).
			if f.lookupResourceErr != nil {
				return nil, f.lookupResourceErr
			}
			res := f.resources[i]
			return &res, nil
		}
	}
	return nil, vmcp.ErrNotFound
}

func (*fakeCore) LookupPrompt(context.Context, *auth.Identity, string) (*vmcp.Prompt, error) {
	return nil, vmcp.ErrNotFound
}

// Check* mirror the Call*/Read* error injection so a pre-flight gate over this fake
// sees the same allow/deny the call path would.
func (f *fakeCore) CheckToolCall(context.Context, *auth.Identity, string, map[string]any) error {
	return f.callErr
}

func (f *fakeCore) CheckResourceRead(context.Context, *auth.Identity, string) error {
	return f.readErr
}

func (*fakeCore) CheckPromptGet(context.Context, *auth.Identity, string) error { return nil }

func (*fakeCore) ListBackends(context.Context, *auth.Identity, bool) ([]vmcp.Backend, error) {
	return nil, nil
}

func (*fakeCore) LookupBackend(context.Context, *auth.Identity, string) (*vmcp.Backend, error) {
	return nil, vmcp.ErrNotFound
}

func (*fakeCore) Close() error { return nil }

func (*fakeCore) BackendHealth() health.Reporter { return nil }

func (f *fakeCore) InvalidateCapabilityCache() { f.invalidateCacheCalls.Add(1) }

// TestServeRegistersSessionHooks verifies that Serve registers the OnRegisterSession
// hook and wires two-phase session creation: an MCP initialize triggers the hook,
// which creates the session record (MakeSessionWithID) and injects the core's advertised
// tools, so a subsequent tools/list advertises them. The OnBeforeListTools hook also runs
// (and no-ops, since the tools are already present on this pod). On the Serve path the
// advertised set comes from the core, called exactly once at registration.
func TestServeRegistersSessionHooks(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	testTool := vmcp.Tool{Name: "serve-tool", Description: "a relocated-wiring test tool"}
	factory, state := newToolSessionFactory(t, ctrl, []vmcp.Tool{testTool})
	// The advertised set comes from the core, not the factory; the factory only
	// establishes the bound session record.
	fc := &fakeCore{tools: []vmcp.Tool{testTool}}

	srv, err := Serve(context.Background(), fc, &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	// Mount the Streamable HTTP server on the relocated mcpServer + vmcpSessionMgr.
	// The discovery middleware is guarded off on the Serve path (s.core != nil), so
	// the "/" MCP route is exercised here directly against the core-sourced session.
	streamable := server.NewStreamableHTTPServer(
		srv.mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithSessionIdManager(srv.vmcpSessionMgr),
	)
	ts := httptest.NewServer(streamable)
	t.Cleanup(ts.Close)

	// initialize → fires OnRegisterSession → CreateSession → tool injection.
	initResp := postServeMCP(t, ts.URL, map[string]any{
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
	require.Equal(t, http.StatusOK, initResp.StatusCode, "initialize should succeed")

	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "session ID should be returned in Mcp-Session-Id header")

	// The OnRegisterSession hook may complete asynchronously after the response.
	require.Eventually(t, state.makeWithIDCalled.Load, 2*time.Second, 10*time.Millisecond,
		"OnRegisterSession hook should have created the session via MakeSessionWithID")

	// Source-of-truth (A3): the core's ListTools is the single aggregation, called
	// exactly once at registration — not once per advertised tool, not re-run by the factory.
	require.Eventually(t, func() bool { return fc.listToolsCalls.Load() == 1 }, 2*time.Second, 10*time.Millisecond,
		"core.ListTools should be called exactly once per session at registration")

	// tools/list runs the OnBeforeListTools hook and returns the per-session tools
	// injected during registration (sourced from the core). It must not trigger another
	// core aggregation — the set is fixed at initialize.
	listResp := postServeMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}, sessionID)
	defer listResp.Body.Close()

	body, err := io.ReadAll(listResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listResp.StatusCode, "tools/list should succeed; body: %s", string(body))
	assert.Contains(t, string(body), testTool.Name,
		"tools/list should advertise the tool the core supplied at registration")
	assert.Equal(t, int32(1), fc.listToolsCalls.Load(),
		"tools/list must reuse the fixed-at-initialize set, not re-aggregate via the core")
}

// fakeSDKSession is a minimal server.ClientSession + server.SessionWithTools used to
// drive lazyInjectSessionTools directly (the SDK's session-context plumbing is otherwise
// only reachable over HTTP, via MCPServer.WithContext). Its tool store is the observable
// surface the injection asserts against.
type fakeSDKSession struct {
	id    string
	tools map[string]server.ServerTool
}

var (
	_ server.ClientSession    = (*fakeSDKSession)(nil)
	_ server.SessionWithTools = (*fakeSDKSession)(nil)
)

func (f *fakeSDKSession) SessionID() string                                 { return f.id }
func (*fakeSDKSession) Initialize()                                         {}
func (*fakeSDKSession) Initialized() bool                                   { return true }
func (*fakeSDKSession) NotificationChannel() chan<- mcp.JSONRPCNotification { return nil }
func (f *fakeSDKSession) GetSessionTools() map[string]server.ServerTool     { return f.tools }
func (f *fakeSDKSession) SetSessionTools(t map[string]server.ServerTool)    { f.tools = t }

// TestServeLazyInjectsToolsForRehydratedSession exercises the cross-pod re-injection
// branch of lazyInjectSessionTools that TestServeRegistersSessionHooks does not reach:
// a session exists in the vMCP session manager, but a fresh SDK ClientSession (as on a
// second pod, where OnRegisterSession never fired) has an empty per-session tool store.
// The before-list/before-call hooks must re-inject the tools from the session manager.
func TestServeLazyInjectsToolsForRehydratedSession(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	testTool := vmcp.Tool{Name: "serve-tool", Description: "a relocated-wiring test tool"}
	factory, _ := newToolSessionFactory(t, ctrl, []vmcp.Tool{testTool})
	fc := &fakeCore{tools: []vmcp.Tool{testTool}}

	srv, err := Serve(context.Background(), fc, &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	// Register a session in the vMCP session manager via the SDK initialize path.
	streamable := server.NewStreamableHTTPServer(
		srv.mcpServer,
		server.WithEndpointPath("/mcp"),
		server.WithSessionIdManager(srv.vmcpSessionMgr),
	)
	ts := httptest.NewServer(streamable)
	t.Cleanup(ts.Close)

	initResp := postServeMCP(t, ts.URL, map[string]any{
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

	// Wait until the session is fully registered in the manager.
	require.Eventually(t, func() bool {
		_, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID)
		return ok
	}, 2*time.Second, 10*time.Millisecond, "session should be registered")

	// Empty-store (cross-pod) branch: a fresh SDK session with no tools gets them injected.
	rehydrated := &fakeSDKSession{id: sessionID, tools: map[string]server.ServerTool{}}
	srv.lazyInjectSessionTools(srv.mcpServer.WithContext(context.Background(), rehydrated))
	assert.Contains(t, rehydrated.tools, testTool.Name,
		"an empty per-session store should be re-injected from the vMCP session manager")

	// No-op branch: a populated store is left untouched (early return before re-derivation).
	populated := &fakeSDKSession{id: sessionID, tools: map[string]server.ServerTool{
		"preexisting": {Tool: mcp.Tool{Name: "preexisting"}},
	}}
	srv.lazyInjectSessionTools(srv.mcpServer.WithContext(context.Background(), populated))
	assert.NotContains(t, populated.tools, testTool.Name, "a populated store must not be modified (no-op)")
	assert.Contains(t, populated.tools, "preexisting")
}

// TestServeReturnsErrorWhenSessionManagerConstructionFails verifies Serve surfaces a
// sessionmanager.New failure — the path the closeStorageOnErr guard protects. It
// confirms the guarded path is reached (the failure occurs AFTER session data storage
// is built, distinct from config validation: ErrorContains("CacheCapacity") +
// NotErrorIs(ErrInvalidConfig)). It does NOT directly observe sessionDataStorage.Close()
// — the storage is constructed internally and the test holds no handle to it; the
// close itself is the same defer-guard pattern carried over verbatim from server.New.
// A negative CacheCapacity is the cheapest forced failure.
func TestServeReturnsErrorWhenSessionManagerConstructionFails(t *testing.T) {
	t.Parallel()

	srv, err := Serve(context.Background(), &stubVMCP{}, &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: testMinimalFactory(), CacheCapacity: -1},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.Error(t, err)
	assert.Nil(t, srv)
	// Construction failure from sessionmanager.New (after the storage is built, so the
	// closeStorageOnErr guard runs) — not a config-validation error.
	assert.NotErrorIs(t, err, vmcp.ErrInvalidConfig)
	assert.ErrorContains(t, err, "CacheCapacity")
}

// TestBuildSessionDataStorage verifies provider selection: nil/empty/"memory"
// (case-insensitive) yields in-process storage; any unknown provider is rejected.
// The "redis" path is covered separately by TestBuildSessionDataStorageRedis, which
// cannot be parallel (it uses t.Setenv).
func TestBuildSessionDataStorage(t *testing.T) {
	t.Parallel()

	t.Run("in-process providers", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name    string
			storage *vmcpconfig.SessionStorageConfig
		}{
			{"nil config", nil},
			{"empty provider", &vmcpconfig.SessionStorageConfig{Provider: ""}},
			{"memory provider", &vmcpconfig.SessionStorageConfig{Provider: "memory"}},
			{"mixed-case memory provider", &vmcpconfig.SessionStorageConfig{Provider: "Memory"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				ds, err := buildSessionDataStorage(context.Background(), &Config{
					SessionTTL:     time.Minute,
					SessionStorage: tc.storage,
				})
				require.NoError(t, err)
				require.NotNil(t, ds)
				assert.IsType(t, &transportsession.LocalSessionDataStorage{}, ds)
				t.Cleanup(func() { _ = ds.Close() })
			})
		}
	})

	t.Run("unsupported provider is rejected", func(t *testing.T) {
		t.Parallel()
		ds, err := buildSessionDataStorage(context.Background(), &Config{
			SessionTTL:     time.Minute,
			SessionStorage: &vmcpconfig.SessionStorageConfig{Provider: "postgres"},
		})
		require.Error(t, err)
		assert.Nil(t, ds)
		assert.ErrorContains(t, err, "unsupported session storage provider")
	})
}

// TestBuildSessionDataStorageRedis verifies the "redis" provider takes the Redis path
// (reading THV_SESSION_REDIS_PASSWORD). It is a separate, non-parallel test because
// t.Setenv is incompatible with parallel tests (and any parallel ancestor).
func TestBuildSessionDataStorageRedis(t *testing.T) {
	t.Setenv(vmcpconfig.RedisPasswordEnvVar, "test-password")
	// Unreachable address so the construction-time Ping fails fast, proving the redis
	// branch was taken (in-process storage would not error here).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ds, err := buildSessionDataStorage(ctx, &Config{
		SessionTTL: time.Minute,
		SessionStorage: &vmcpconfig.SessionStorageConfig{
			Provider: "redis",
			Address:  "127.0.0.1:1",
		},
	})
	require.Error(t, err)
	assert.Nil(t, ds)
	// Bind to the Redis connection-failure wrap so a future config/unsupported-provider
	// error can't satisfy this test. ("redis" alone is unsuitable — the unsupported-
	// provider error text also lists "redis".)
	assert.ErrorContains(t, err, "redis: failed to connect")
}

// TestServeHandlerSkipsDiscoveryAndRoutesCallThroughCore drives the FULL shared
// Handler (not the bare streamable server) against a Serve-built server. It proves
// the discovery middleware is guarded off on the Serve path: a Serve-built server
// has a nil discoveryMgr, so applying discovery would nil-deref — serving succeeds
// only because s.core != nil skips it. It also proves a tools/call is routed through
// core.CallTool with the advertised tool name.
func TestServeHandlerSkipsDiscoveryAndRoutesCallThroughCore(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	testTool := vmcp.Tool{Name: "serve-tool", Description: "a Serve-path test tool"}
	factory, _ := newToolSessionFactory(t, ctrl, []vmcp.Tool{testTool})
	fc := &fakeCore{tools: []vmcp.Tool{testTool}}

	srv, err := Serve(context.Background(), fc, &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	// Build the full middleware chain + mux. On the Serve path this MUST NOT apply
	// the discovery middleware (which would nil-deref on the nil discoveryMgr).
	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	initResp := postServeMCP(t, ts.URL, map[string]any{
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
	require.Equal(t, http.StatusOK, initResp.StatusCode,
		"initialize should succeed through the full Handler (discovery middleware skipped on the Serve path)")
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	require.Eventually(t, func() bool { return fc.listToolsCalls.Load() >= 1 }, 2*time.Second, 10*time.Millisecond,
		"OnRegisterSession should source the advertised set from the core")

	callResp := postServeMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": "serve-tool", "arguments": map[string]any{}},
	}, sessionID)
	defer callResp.Body.Close()
	body, err := io.ReadAll(callResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, callResp.StatusCode, "tools/call should succeed; body: %s", string(body))

	assert.Equal(t, int32(1), fc.callToolCalls.Load(), "tools/call must route through core.CallTool")
	name, _ := fc.lastCallToolName.Load().(string)
	assert.Equal(t, "serve-tool", name, "core.CallTool must receive the advertised tool name")
	assert.Contains(t, string(body), "ok", "the core's tool result should reach the client")
}

// TestServeEnforcesSessionBinding verifies the Serve call path enforces the session's
// identity binding (via the session layer) before reaching the core, even though calls
// route through core.CallTool rather than the binding-enforcing MultiSession decorator.
// The factory binds the session as anonymous, so a caller presenting a token is a
// session-upgrade attack and must be rejected.
func TestServeEnforcesSessionBinding(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	testTool := vmcp.Tool{Name: "serve-tool"}
	factory, _ := newToolSessionFactory(t, ctrl, []vmcp.Tool{testTool})
	fc := &fakeCore{tools: []vmcp.Tool{testTool}}

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

	initResp := postServeMCP(t, ts.URL, map[string]any{
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

	require.Eventually(t, func() bool { _, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID); return ok },
		2*time.Second, 10*time.Millisecond, "session should be registered")

	// Anonymous caller (no token) matches the anonymous binding — allowed.
	require.NoError(t, srv.enforceSessionBinding(context.Background(), sessionID, nil),
		"anonymous caller must be permitted on an anonymous session")

	// A caller presenting a token on an anonymous session is a session-upgrade attack.
	err = srv.enforceSessionBinding(context.Background(), sessionID, &auth.Identity{Token: "attacker-token"})
	require.Error(t, err)
	assert.ErrorIs(t, err, sessiontypes.ErrUnauthorizedCaller,
		"a token presented to an anonymous session must be rejected by the session-layer binding check")
}

// registerServeSession builds a Serve server backed by fc plus a mock session factory,
// registers one anonymous session via the SDK initialize path, and returns the server,
// the session ID, and the test HTTP base URL. The mock factory only establishes the
// (anonymous-bound) session record; capabilities come from fc (the core).
func registerServeSession(t *testing.T, fc *fakeCore) (*Server, string, string) {
	t.Helper()
	return registerServeSessionWithRegistry(t, fc, vmcp.NewImmutableRegistry([]vmcp.Backend{}))
}

// registerServeSessionWithRegistry is registerServeSession with a caller-supplied
// backend registry, so a test can exercise registration-time backend-name resolution
// (backendDisplayName maps a tool/resource BackendID to the registry's display name).
func registerServeSessionWithRegistry(
	t *testing.T, fc *fakeCore, reg vmcp.BackendRegistry,
) (*Server, string, string) {
	t.Helper()
	ctrl := gomock.NewController(t)
	factory, _ := newToolSessionFactory(t, ctrl, fc.tools)

	srv, err := Serve(context.Background(), fc, &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      reg,
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

	initResp := postServeMCP(t, ts.URL, map[string]any{
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
	require.Eventually(t, func() bool { _, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID); return ok },
		2*time.Second, 10*time.Millisecond, "session should be registered")
	return srv, sessionID, ts.URL
}

// registerServeSessionCapturingSink is registerServeSession plus the
// toolSessionState so a test can retrieve the ListChangedSink (#5748)
// MakeSessionWithID received for the registered session and invoke it
// directly, simulating an asynchronous backend notification firing after
// registration completes.
func registerServeSessionCapturingSink(t *testing.T, fc *fakeCore) (srv *Server, sessionID, baseURL string, state *toolSessionState) {
	t.Helper()
	ctrl := gomock.NewController(t)
	factory, state := newToolSessionFactory(t, ctrl, fc.tools)

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

	initResp := postServeMCP(t, ts.URL, map[string]any{
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
	sessionID = initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	require.Eventually(t, func() bool { _, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID); return ok },
		2*time.Second, 10*time.Millisecond, "session should be registered")
	require.Eventually(t, func() bool { return state.sink() != nil },
		2*time.Second, 10*time.Millisecond, "MakeSessionWithID should have received a non-nil sink")
	return srv, sessionID, ts.URL, state
}

// TestListChangedSink_EndToEnd_ResyncsRegisteredSession drives a real session
// registration over HTTP, then invokes the captured sink directly (simulating
// a backend's asynchronous notifications/tools/list_changed) and verifies,
// via a real tools/list request, that the session's advertised set reflects
// the core's now-changed tool set — including a REMOVED tool disappearing —
// and that the cache was invalidated (#5748).
func TestListChangedSink_EndToEnd_ResyncsRegisteredSession(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "kept"}, {Name: "removed"}}}
	_, sessionID, baseURL, state := registerServeSessionCapturingSink(t, fc)

	listResp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	}, sessionID)
	env, _ := readServeJSONRPC(t, listResp)
	listResp.Body.Close()
	before := toolNamesFromListResult(t, env)
	assert.ElementsMatch(t, []string{"kept", "removed"}, before)

	// The core's advertised set changes (as if the backend's own tool set
	// changed): "removed" is gone, "added" is new.
	fc.tools = []vmcp.Tool{{Name: "kept"}, {Name: "added"}}

	sink := state.sink()
	require.NotNil(t, sink)
	sink(context.Background(), "some-backend", "tools")

	require.Eventually(t, func() bool {
		return fc.invalidateCacheCalls.Load() >= 1
	}, 2*time.Second, 10*time.Millisecond, "sink must invalidate the capability cache")

	require.Eventually(t, func() bool {
		listResp := postServeMCP(t, baseURL, map[string]any{
			"jsonrpc": "2.0", "id": 3, "method": "tools/list", "params": map[string]any{},
		}, sessionID)
		defer listResp.Body.Close()
		env, _ := readServeJSONRPC(t, listResp)
		got := toolNamesFromListResult(t, env)
		return assert.ObjectsAreEqualValues([]string{"added", "kept"}, sortedCopy(got))
	}, 2*time.Second, 10*time.Millisecond, "tools/list must reflect the resynced (replaced) tool set")
}

// toolNamesFromListResult extracts the tool names from a tools/list JSON-RPC
// response envelope.
func toolNamesFromListResult(t *testing.T, env map[string]any) []string {
	t.Helper()
	result, ok := env["result"].(map[string]any)
	require.True(t, ok, "tools/list response must have a result object; env: %v", env)
	rawTools, ok := result["tools"].([]any)
	require.True(t, ok, "result.tools must be an array; result: %v", result)
	names := make([]string, 0, len(rawTools))
	for _, rt := range rawTools {
		tm, ok := rt.(map[string]any)
		require.True(t, ok)
		name, _ := tm["name"].(string)
		names = append(names, name)
	}
	return names
}

// sortedCopy returns a sorted copy of ss for order-independent comparison.
func sortedCopy(ss []string) []string {
	out := make([]string, len(ss))
	copy(out, ss)
	sort.Strings(out)
	return out
}

// TestServeCoreToolHandler exercises the Serve tool handler's error/denial branches by
// invoking the handler closure directly against a registered (anonymous) session:
// the happy path returns the core result; an ErrAuthorizationFailed is genericized so the
// underlying authorizer detail never leaks; any other core error is forwarded; and a
// non-map arguments payload is rejected before the core is reached.
func TestServeCoreToolHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		args            any
		callErr         error
		wantIsError     bool
		wantContains    string
		wantNotContains string
		wantCoreCalled  bool
	}{
		{"happy path returns the core result", map[string]any{}, nil, false, "ok", "", true},
		{
			"authz denial is genericized", map[string]any{},
			fmt.Errorf("%w: cedar said no", vmcp.ErrAuthorizationFailed), true,
			"call denied by authorization policy", "cedar said no", true,
		},
		{"non-auth error is forwarded", map[string]any{}, errors.New("backend boom"), true, "backend boom", "", true},
		{"non-map arguments rejected before the core", "not-an-object", nil, true, "invalid input", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}, callErr: tc.callErr}
			srv, sessionID, _ := registerServeSession(t, fc)

			req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "t", Arguments: tc.args}}
			res, err := srv.coreToolHandler(sessionID, "t", "")(context.Background(), req)
			// Tool errors/denials surface as IsError results, not Go errors.
			require.NoError(t, err)
			require.NotNil(t, res)
			assert.Equal(t, tc.wantIsError, res.IsError)

			body, mErr := json.Marshal(res)
			require.NoError(t, mErr)
			assert.Contains(t, string(body), tc.wantContains)
			if tc.wantNotContains != "" {
				assert.NotContains(t, string(body), tc.wantNotContains,
					"the underlying authorizer error must not leak to the client")
			}

			want := int32(0)
			if tc.wantCoreCalled {
				want = 1
			}
			assert.Equal(t, want, fc.callToolCalls.Load())
		})
	}
}

func TestServeCoreToolHandlerCodedErrorReturnsStructuredToolResult(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{
		tools:   []vmcp.Tool{{Name: "t"}},
		callErr: &ratelimit.RateLimitedError{RetryAfter: time.Second},
	}
	srv, sessionID, _ := registerServeSession(t, fc)

	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}}
	res, err := srv.coreToolHandler(sessionID, "t", "")(context.Background(), req)

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	sc, ok := res.StructuredContent.(map[string]any)
	require.True(t, ok, "coded errors should carry structured content")
	assert.EqualValues(t, ratelimit.CodeRateLimited, sc["code"])
	assert.Equal(t, ratelimit.MessageRateLimited, sc["message"])
	data, ok := sc["data"].(map[string]any)
	require.True(t, ok, "coded errors should carry structured data")
	assert.EqualValues(t, 1, data["retryAfterSeconds"])
	assert.Equal(t, int32(1), fc.callToolCalls.Load())
}

// TestServeToolCallTerminatesOnBindingFailure proves the documented fail-closed side
// effect: a binding rejection on the Serve call path terminates the session and never
// reaches the core. The session is anonymous, so a caller presenting a token is a
// session-upgrade attack.
func TestServeToolCallTerminatesOnBindingFailure(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, _ := registerServeSession(t, fc)

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Token: "attacker-token"})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}}
	res, err := srv.coreToolHandler(sessionID, "t", "")(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	body, _ := json.Marshal(res)
	assert.Contains(t, string(body), "Unauthorized")
	assert.Equal(t, int32(0), fc.callToolCalls.Load(), "core.CallTool must not be reached on a binding failure")

	require.Eventually(t, func() bool { _, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID); return !ok },
		2*time.Second, 10*time.Millisecond, "a binding failure must terminate the session (fail-closed)")
}

// TestServeCoreResourceHandler covers the Serve resource path: the core's resource is
// advertised by the registration builder, the handler routes a read through
// core.ReadResource, and an ErrAuthorizationFailed is genericized.
func TestServeCoreResourceHandler(t *testing.T) {
	t.Parallel()

	const uri = "file:///doc.txt"

	t.Run("advertises and routes the resource through core.ReadResource", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resources: []vmcp.Resource{{Name: "doc", URI: uri}}}
		srv, sessionID, _ := registerServeSession(t, fc)

		resources, err := srv.coreSessionResources(context.Background(), sessionID, nil)
		require.NoError(t, err)
		require.Len(t, resources, 1)
		assert.Equal(t, uri, resources[0].Resource.URI)

		contents, err := srv.coreResourceHandler(sessionID, uri, "")(context.Background(), mcp.ReadResourceRequest{})
		require.NoError(t, err)
		require.NotEmpty(t, contents)
		gotURI, _ := fc.lastReadURI.Load().(string)
		assert.Equal(t, uri, gotURI, "the handler must route the read through core.ReadResource with the URI")
	})

	t.Run("authorization denial yields a generic message", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{
			resources: []vmcp.Resource{{Name: "doc", URI: uri}},
			readErr:   fmt.Errorf("%w: cedar said no", vmcp.ErrAuthorizationFailed),
		}
		srv, sessionID, _ := registerServeSession(t, fc)

		_, err := srv.coreResourceHandler(sessionID, uri, "")(context.Background(), mcp.ReadResourceRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read denied by authorization policy")
		assert.NotContains(t, err.Error(), "cedar said no", "the underlying authorizer error must not leak")
	})
}

// TestServeCoreResourceTemplateHandler covers the Serve resource-template path:
// the core's resource template is advertised by the registration builder, the
// handler routes a read of an EXPANDED URI (taken from the request, not a fixed
// URI) through core.ReadResource, and an ErrAuthorizationFailed is genericized.
func TestServeCoreResourceTemplateHandler(t *testing.T) {
	t.Parallel()

	const uriTemplate = "file:///logs/{date}.txt"
	const expandedURI = "file:///logs/2025-01-01.txt"

	t.Run("advertises the template and routes the expanded read through core.ReadResource", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resourceTemplates: []vmcp.ResourceTemplate{
			{Name: "Daily log", URITemplate: uriTemplate, MimeType: "text/plain"},
		}}
		srv, sessionID, _ := registerServeSession(t, fc)

		templates, err := srv.coreSessionResourceTemplates(t.Context(), sessionID, nil)
		require.NoError(t, err)
		require.Len(t, templates, 1)
		assert.Equal(t, uriTemplate, templates[0].Template.URITemplate)
		assert.Equal(t, "text/plain", templates[0].Template.MIMEType)

		// The template handler reads the concrete URI from the request, not a fixed URI.
		req := mcp.ReadResourceRequest{Params: mcp.ReadResourceParams{URI: expandedURI}}
		contents, err := srv.coreResourceTemplateHandler(sessionID, "")(t.Context(), req)
		require.NoError(t, err)
		require.NotEmpty(t, contents)

		gotURI, _ := fc.lastReadURI.Load().(string)
		assert.Equal(t, expandedURI, gotURI,
			"the template handler must route the read through core.ReadResource with the expanded URI")
	})

	t.Run("admission filtering withholds a denied template", func(t *testing.T) {
		t.Parallel()
		// The core (fakeCore stands in for it) returns the admission-filtered set;
		// an empty set means no template is advertised for this identity.
		fc := &fakeCore{resourceTemplates: nil}
		srv, sessionID, _ := registerServeSession(t, fc)

		templates, err := srv.coreSessionResourceTemplates(t.Context(), sessionID, nil)
		require.NoError(t, err)
		assert.Empty(t, templates, "a denied/withheld template must not be advertised")
	})

	t.Run("authorization denial yields a generic message", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{
			resourceTemplates: []vmcp.ResourceTemplate{{Name: "Daily log", URITemplate: uriTemplate}},
			readErr:           fmt.Errorf("%w: cedar said no", vmcp.ErrAuthorizationFailed),
		}
		srv, sessionID, _ := registerServeSession(t, fc)

		req := mcp.ReadResourceRequest{Params: mcp.ReadResourceParams{URI: expandedURI}}
		_, err := srv.coreResourceTemplateHandler(sessionID, "")(t.Context(), req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read denied by authorization policy")
		assert.NotContains(t, err.Error(), "cedar said no", "the underlying authorizer error must not leak")
	})
}

// TestServeResourceTemplatesEndToEnd drives the resource-templates lane over the
// full Streamable HTTP protocol (the conformance gap this lane closes):
//
//   - resources/templates/list advertises the backend's template, and
//   - resources/read of an EXPANDED URI matching the template is served (routed
//     through core.ReadResource) and returns contents, and
//   - a denied read returns an authorization error rather than -32002
//     "Resource not found" — the pre-fix symptom was that vMCP never registered
//     the template, so every templated read fell through to not-found.
func TestServeResourceTemplatesEndToEnd(t *testing.T) {
	t.Parallel()

	const uriTemplate = "file:///logs/{date}.txt"
	const expandedURI = "file:///logs/2025-01-01.txt"

	t.Run("lists the template and serves an expanded read", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resourceTemplates: []vmcp.ResourceTemplate{
			{Name: "Daily log", URITemplate: uriTemplate, MimeType: "text/plain"},
		}}
		_, sessionID, baseURL := registerServeSession(t, fc)

		// resources/templates/list must advertise the backend's template.
		listResp := postServeMCP(t, baseURL, map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "resources/templates/list",
			"params":  map[string]any{},
		}, sessionID)
		defer listResp.Body.Close()
		require.Equal(t, http.StatusOK, listResp.StatusCode)

		env, raw := readServeJSONRPC(t, listResp)
		result, ok := env["result"].(map[string]any)
		require.True(t, ok, "list must have a result; body: %s", string(raw))
		templates, ok := result["resourceTemplates"].([]any)
		require.True(t, ok, "result.resourceTemplates must be present; body: %s", string(raw))
		require.Len(t, templates, 1)
		tmpl := templates[0].(map[string]any)
		assert.Equal(t, uriTemplate, tmpl["uriTemplate"])

		// resources/read of a URI expanded from the template must be served through
		// core.ReadResource and return contents (not -32002).
		readResp := postServeMCP(t, baseURL, map[string]any{
			"jsonrpc": "2.0",
			"id":      3,
			"method":  "resources/read",
			"params":  map[string]any{"uri": expandedURI},
		}, sessionID)
		defer readResp.Body.Close()
		require.Equal(t, http.StatusOK, readResp.StatusCode)

		readEnv, readRaw := readServeJSONRPC(t, readResp)
		require.NotContains(t, readEnv, "error", "a templated read must not error; body: %s", string(readRaw))
		readResult, ok := readEnv["result"].(map[string]any)
		require.True(t, ok, "read must have a result; body: %s", string(readRaw))
		contents, ok := readResult["contents"].([]any)
		require.True(t, ok, "result.contents must be present; body: %s", string(readRaw))
		require.NotEmpty(t, contents)

		gotURI, _ := fc.lastReadURI.Load().(string)
		assert.Equal(t, expandedURI, gotURI, "the expanded URI must reach core.ReadResource")
	})

	t.Run("denied templated read returns an authorization error, not -32002", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{
			resourceTemplates: []vmcp.ResourceTemplate{{Name: "Daily log", URITemplate: uriTemplate}},
			readErr:           fmt.Errorf("%w: cedar said no", vmcp.ErrAuthorizationFailed),
		}
		_, sessionID, baseURL := registerServeSession(t, fc)

		readResp := postServeMCP(t, baseURL, map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "resources/read",
			"params":  map[string]any{"uri": expandedURI},
		}, sessionID)
		defer readResp.Body.Close()

		env, raw := readServeJSONRPC(t, readResp)
		readErr, ok := env["error"].(map[string]any)
		require.True(t, ok, "a denied read must return a JSON-RPC error; body: %s", string(raw))

		// The whole point: the template is registered, so the read reaches the
		// handler and is DENIED — it is not the SDK's -32002 "Resource not found"
		// that the pre-fix (never-registered) path returned.
		if code, ok := readErr["code"].(float64); ok {
			assert.NotEqual(t, float64(-32002), code, "denial must not surface as -32002 Resource not found")
		}
		msg, _ := readErr["message"].(string)
		assert.Contains(t, msg, "read denied by authorization policy")
		assert.NotContains(t, msg, "cedar said no", "the underlying authorizer detail must not leak")
	})
}

// TestServeCorePromptHandler covers the Serve prompt path: the core's prompt is
// advertised by the registration builder, the handler routes a get through
// core.GetPrompt (widening the string args to map[string]any and converting the
// result), and an ErrAuthorizationFailed is genericized to the prompt denial message.
func TestServeCorePromptHandler(t *testing.T) {
	t.Parallel()

	const promptName = "greeting"

	t.Run("advertises and routes the prompt through core.GetPrompt", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{prompts: []vmcp.Prompt{{
			Name:        promptName,
			Description: "a greeting prompt",
			Arguments:   []vmcp.PromptArgument{{Name: "name", Description: "who to greet", Required: true}},
		}}}
		srv, sessionID, _ := registerServeSession(t, fc)

		prompts, err := srv.coreSessionPrompts(t.Context(), sessionID, nil)
		require.NoError(t, err)
		require.Len(t, prompts, 1)
		assert.Equal(t, promptName, prompts[0].Prompt.Name)
		require.Len(t, prompts[0].Prompt.Arguments, 1)
		assert.Equal(t, "name", prompts[0].Prompt.Arguments[0].Name)
		assert.True(t, prompts[0].Prompt.Arguments[0].Required)

		req := mcp.GetPromptRequest{Params: mcp.GetPromptParams{
			Name:      promptName,
			Arguments: map[string]string{"name": "world"},
		}}
		result, err := srv.corePromptHandler(sessionID, promptName, "")(t.Context(), req)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "prompt-desc", result.Description)
		require.Len(t, result.Messages, 1)

		gotName, _ := fc.lastGetPromptName.Load().(string)
		assert.Equal(t, promptName, gotName, "the handler must route the get through core.GetPrompt with the name")
		gotArgs, _ := fc.lastGetPromptArgs.Load().(map[string]any)
		assert.Equal(t, "world", gotArgs["name"], "the handler must widen the string prompt args to map[string]any")
	})

	t.Run("authorization denial yields the prompt denial message", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{
			prompts:   []vmcp.Prompt{{Name: promptName}},
			promptErr: fmt.Errorf("%w: cedar said no", vmcp.ErrAuthorizationFailed),
		}
		srv, sessionID, _ := registerServeSession(t, fc)

		_, err := srv.corePromptHandler(sessionID, promptName, "")(t.Context(), mcp.GetPromptRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), vmcp.DenyMessagePromptGet)
		assert.NotContains(t, err.Error(), "cedar said no", "the underlying authorizer error must not leak")
	})
}

// TestServeCoreCompletionHandler covers coreCompletionHandler's admission-denial
// mapping directly (mirroring TestServeCorePromptHandler): completion reuses the
// underlying capability's deny wording, so a Cedar-denied prompt ref surfaces the
// prompt deny message and a denied resource ref surfaces the resource deny
// message — and neither leaks the raw authorizer detail.
func TestServeCoreCompletionHandler(t *testing.T) {
	t.Parallel()

	const promptName = "greeting"
	const uri = "file:///doc.txt"

	t.Run("prompt-ref authorization denial yields the prompt deny message", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{
			prompts:     []vmcp.Prompt{{Name: promptName}},
			completeErr: fmt.Errorf("%w: cedar said no", vmcp.ErrAuthorizationFailed),
		}
		srv, sessionID, _ := registerServeSession(t, fc)

		ctx := sdkSessionContext(t.Context(), srv, sessionID)
		_, err := srv.coreCompletionHandler(ctx, mcp.CompleteRequest{Params: mcp.CompleteParams{
			Ref:      mcp.PromptReference{Type: vmcp.CompletionRefTypePrompt, Name: promptName},
			Argument: mcp.CompleteArgument{Name: "name", Value: "wor"},
		}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), vmcp.DenyMessagePromptGet)
		assert.NotContains(t, err.Error(), "cedar said no", "the underlying authorizer error must not leak")
	})

	t.Run("resource-ref authorization denial yields the resource deny message", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{
			resources:   []vmcp.Resource{{Name: "doc", URI: uri}},
			completeErr: fmt.Errorf("%w: cedar said no", vmcp.ErrAuthorizationFailed),
		}
		srv, sessionID, _ := registerServeSession(t, fc)

		ctx := sdkSessionContext(t.Context(), srv, sessionID)
		_, err := srv.coreCompletionHandler(ctx, mcp.CompleteRequest{Params: mcp.CompleteParams{
			Ref:      mcp.ResourceReference{Type: vmcp.CompletionRefTypeResource, URI: uri},
			Argument: mcp.CompleteArgument{Name: "path", Value: "d"},
		}})
		require.Error(t, err)
		assert.Contains(t, err.Error(), vmcp.DenyMessageResourceRead)
		assert.NotContains(t, err.Error(), "cedar said no", "the underlying authorizer error must not leak")
	})
}

// TestServeCompletionEndToEnd drives the completion/complete lane over the full
// Streamable HTTP protocol (the conformance gap this lane closes): the SDK
// auto-advertises the completions capability (WithCompletionHandler is wired at
// construction), and a completion/complete request for a prompt ref routes through
// core.Complete and returns the backend's candidate values.
func TestServeCompletionEndToEnd(t *testing.T) {
	t.Parallel()

	const promptName = "greeting"

	fc := &fakeCore{
		prompts:        []vmcp.Prompt{{Name: promptName, Arguments: []vmcp.PromptArgument{{Name: "name"}}}},
		completeValues: []string{"world", "worf", "wormhole"},
	}
	_, sessionID, baseURL := registerServeSession(t, fc)

	resp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "completion/complete",
		"params": map[string]any{
			"ref":      map[string]any{"type": "ref/prompt", "name": promptName},
			"argument": map[string]any{"name": "name", "value": "wor"},
		},
	}, sessionID)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	env, raw := readServeJSONRPC(t, resp)
	require.NotContains(t, env, "error", "completion must not error; body: %s", string(raw))
	result, ok := env["result"].(map[string]any)
	require.True(t, ok, "completion must have a result; body: %s", string(raw))
	completion, ok := result["completion"].(map[string]any)
	require.True(t, ok, "result.completion must be present; body: %s", string(raw))
	values, ok := completion["values"].([]any)
	require.True(t, ok, "result.completion.values must be present; body: %s", string(raw))
	require.Len(t, values, 3)
	assert.Equal(t, "world", values[0])

	require.Equal(t, int32(1), fc.completeCalls.Load(), "the request must route through core.Complete exactly once")
	gotRef, _ := fc.lastCompleteRef.Load().(vmcp.CompletionRef)
	assert.Equal(t, vmcp.CompletionRefTypePrompt, gotRef.Type)
	assert.Equal(t, promptName, gotRef.Name, "the prompt ref must reach core.Complete")
}

// TestServeSubscriptionsEndToEnd drives resources/subscribe and resources/unsubscribe
// over the full Streamable HTTP protocol (the conformance gap these ack-level lanes
// close): initialize advertises resources.subscribe=true, subscribing to an advertised
// resource URI succeeds, and an unknown URI is rejected. vMCP accepts the subscription
// at ack level; it does NOT forward backend resources/updated notifications (out of
// scope). The pre-fix symptom was -32601 "server does not support resource subscriptions".
func TestServeSubscriptionsEndToEnd(t *testing.T) {
	t.Parallel()

	const uri = "file:///doc.txt"

	t.Run("initialize advertises subscribe support", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resources: []vmcp.Resource{{Name: "doc", URI: uri}}}
		ctrl := gomock.NewController(t)
		factory, _ := newToolSessionFactory(t, ctrl, fc.tools)
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

		initResp := postServeMCP(t, ts.URL, map[string]any{
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

		env, raw := readServeJSONRPC(t, initResp)
		result, ok := env["result"].(map[string]any)
		require.True(t, ok, "initialize must have a result; body: %s", string(raw))
		caps, ok := result["capabilities"].(map[string]any)
		require.True(t, ok, "result.capabilities must be present; body: %s", string(raw))
		resources, ok := caps["resources"].(map[string]any)
		require.True(t, ok, "capabilities.resources must be advertised; body: %s", string(raw))
		assert.Equal(t, true, resources["subscribe"], "resources.subscribe must be advertised; body: %s", string(raw))
	})

	t.Run("subscribe and unsubscribe an advertised resource succeed", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resources: []vmcp.Resource{{Name: "doc", URI: uri}}}
		_, sessionID, baseURL := registerServeSession(t, fc)

		subResp := postServeMCP(t, baseURL, map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "resources/subscribe",
			"params":  map[string]any{"uri": uri},
		}, sessionID)
		defer subResp.Body.Close()
		require.Equal(t, http.StatusOK, subResp.StatusCode)

		subEnv, subRaw := readServeJSONRPC(t, subResp)
		require.NotContains(t, subEnv, "error", "subscribe must succeed (regression: -32601); body: %s", string(subRaw))
		_, ok := subEnv["result"]
		require.True(t, ok, "subscribe must return a result; body: %s", string(subRaw))

		unsubResp := postServeMCP(t, baseURL, map[string]any{
			"jsonrpc": "2.0",
			"id":      3,
			"method":  "resources/unsubscribe",
			"params":  map[string]any{"uri": uri},
		}, sessionID)
		defer unsubResp.Body.Close()
		require.Equal(t, http.StatusOK, unsubResp.StatusCode)

		unsubEnv, unsubRaw := readServeJSONRPC(t, unsubResp)
		require.NotContains(t, unsubEnv, "error", "unsubscribe must succeed; body: %s", string(unsubRaw))
	})

	t.Run("subscribe to an unknown resource is rejected", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resources: []vmcp.Resource{{Name: "doc", URI: uri}}}
		_, sessionID, baseURL := registerServeSession(t, fc)

		resp := postServeMCP(t, baseURL, map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "resources/subscribe",
			"params":  map[string]any{"uri": "file:///unknown.txt"},
		}, sessionID)
		defer resp.Body.Close()

		env, raw := readServeJSONRPC(t, resp)
		_, ok := env["error"].(map[string]any)
		require.True(t, ok, "subscribing to an unadvertised resource must return an error; body: %s", string(raw))
	})
}

// TestServeCoreSubscribeHandler covers coreSubscribeHandler's branches directly: an
// advertised resource is accepted (nil), an unknown/admission-denied URI is rejected,
// and a session-binding failure is enforced before the resource lookup.
func TestServeCoreSubscribeHandler(t *testing.T) {
	t.Parallel()

	const uri = "file:///doc.txt"

	t.Run("advertised resource is accepted", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resources: []vmcp.Resource{{Name: "doc", URI: uri}}}
		srv, sessionID, _ := registerServeSession(t, fc)

		ctx := sdkSessionContext(t.Context(), srv, sessionID)
		err := srv.coreSubscribeHandler(ctx, "resources/subscribe", uri)
		require.NoError(t, err)
	})

	t.Run("unknown resource is rejected", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resources: []vmcp.Resource{{Name: "doc", URI: uri}}}
		srv, sessionID, _ := registerServeSession(t, fc)

		ctx := sdkSessionContext(t.Context(), srv, sessionID)
		err := srv.coreSubscribeHandler(ctx, "resources/subscribe", "file:///nope.txt")
		require.Error(t, err)
		assert.ErrorIs(t, err, vmcp.ErrNotFound)
	})

	t.Run("admission-denied advertised resource is rejected, not acked", func(t *testing.T) {
		t.Parallel()
		// The URI is advertised, but LookupResource applies the same admission
		// decision resources/read enforces and denies it: subscribe must reject
		// rather than silently ack.
		fc := &fakeCore{
			resources:         []vmcp.Resource{{Name: "doc", URI: uri}},
			lookupResourceErr: fmt.Errorf("%w: cedar said no", vmcp.ErrAuthorizationFailed),
		}
		srv, sessionID, _ := registerServeSession(t, fc)

		ctx := sdkSessionContext(t.Context(), srv, sessionID)
		err := srv.coreSubscribeHandler(ctx, "resources/subscribe", uri)
		require.Error(t, err)
		assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)
	})

	t.Run("session binding is enforced", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resources: []vmcp.Resource{{Name: "doc", URI: uri}}}
		srv, _, _ := registerServeSession(t, fc)

		// An unknown session ID has no binding record, so enforceSessionBinding fails
		// closed before the resource lookup.
		ctx := sdkSessionContext(t.Context(), srv, "not-a-real-session")
		err := srv.coreSubscribeHandler(ctx, "resources/subscribe", uri)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unauthorized")
	})
}

// sdkSessionContext returns ctx carrying an SDK ClientSession for sessionID, the
// mechanism the global completion/subscribe handlers use to recover the session ID
// (server.ClientSessionFromContext). It mirrors the SDK's per-request context
// plumbing without going over HTTP.
func sdkSessionContext(ctx context.Context, srv *Server, sessionID string) context.Context {
	return srv.mcpServer.WithContext(ctx, &fakeSDKSession{id: sessionID, tools: map[string]server.ServerTool{}})
}

// TestServeHandlersLabelAuditBackend verifies the Serve-path audit labelling (#5512):
// the per-session tool/resource handlers write the pre-resolved backend name into the
// audit BackendInfo carried in the request context — the mechanism that lets the Serve
// path drop the backend-enrichment middleware. It also locks in AC1 of #5493: labelling
// is an O(1) write, never a re-aggregation (core.ListTools stays at its single
// registration-time call).
func TestServeHandlersLabelAuditBackend(t *testing.T) {
	t.Parallel()

	t.Run("tool handler labels the backend and does not re-aggregate", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
		srv, sessionID, _ := registerServeSession(t, fc)
		require.Equal(t, int32(1), fc.listToolsCalls.Load(), "registration aggregates exactly once")

		bi := &audit.BackendInfo{}
		ctx := audit.WithBackendInfo(context.Background(), bi)
		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}}
		res, err := srv.coreToolHandler(sessionID, "t", "github-mcp")(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.False(t, res.IsError)

		assert.Equal(t, "github-mcp", bi.BackendName,
			"the tool handler must label the audit event with the serving backend")
		assert.Equal(t, int32(1), fc.listToolsCalls.Load(),
			"labelling must not re-aggregate — core.ListTools stays at the single registration call")
	})

	t.Run("resource handler labels the backend and does not re-aggregate", func(t *testing.T) {
		t.Parallel()
		const uri = "file:///doc.txt"
		fc := &fakeCore{resources: []vmcp.Resource{{Name: "doc", URI: uri}}}
		srv, sessionID, _ := registerServeSession(t, fc)
		listResourcesAtRegistration := fc.listResourcesCalls.Load()

		bi := &audit.BackendInfo{}
		ctx := audit.WithBackendInfo(context.Background(), bi)
		_, err := srv.coreResourceHandler(sessionID, uri, "docs-backend")(ctx, mcp.ReadResourceRequest{})
		require.NoError(t, err)
		assert.Equal(t, "docs-backend", bi.BackendName)
		assert.Equal(t, listResourcesAtRegistration, fc.listResourcesCalls.Load(),
			"labelling must not re-aggregate — core.ListResources is not called again during the resource read")
	})

	t.Run("prompt handler labels the backend and does not re-aggregate", func(t *testing.T) {
		t.Parallel()
		const promptName = "greeting"
		fc := &fakeCore{prompts: []vmcp.Prompt{{Name: promptName}}}
		srv, sessionID, _ := registerServeSession(t, fc)
		listPromptsAtRegistration := fc.listPromptsCalls.Load()

		bi := &audit.BackendInfo{}
		ctx := audit.WithBackendInfo(context.Background(), bi)
		_, err := srv.corePromptHandler(sessionID, promptName, "prompts-backend")(ctx, mcp.GetPromptRequest{})
		require.NoError(t, err)
		assert.Equal(t, "prompts-backend", bi.BackendName)
		assert.Equal(t, listPromptsAtRegistration, fc.listPromptsCalls.Load(),
			"labelling must not re-aggregate — core.ListPrompts is not called again during the prompt get")
	})

	t.Run("no BackendInfo in context is a safe no-op", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
		srv, sessionID, _ := registerServeSession(t, fc)

		req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}}
		res, err := srv.coreToolHandler(sessionID, "t", "github-mcp")(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.False(t, res.IsError, "a missing BackendInfo must not break the call")
	})
}

// TestBackendDisplayName covers backendDisplayName's three branches directly (the
// handler tests above pass the name as a literal, so they don't exercise it). It also
// locks in the deliberate orphan non-parity: a backend advertised by the core but
// absent from the registry resolves to the raw BackendID, where the legacy
// minimal-target fallback would leave WorkloadName empty.
func TestBackendDisplayName(t *testing.T) {
	t.Parallel()

	reg := vmcp.NewImmutableRegistry([]vmcp.Backend{{ID: "backend-x", Name: "github-mcp"}})
	srv := &Server{backendRegistry: reg}

	tests := []struct {
		name      string
		backendID string
		want      string
	}{
		{"empty ID resolves to empty", "", ""},
		{"registered ID resolves to the display name", "backend-x", "github-mcp"},
		{"orphan ID falls back to the raw ID (not legacy's empty WorkloadName)", "ghost", "ghost"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, srv.backendDisplayName(context.Background(), tc.backendID))
		})
	}
}

// TestServeResolvesBackendNameEndToEnd exercises the full Serve-path resolution wiring
// that the literal-name handler tests bypass: coreSessionTools resolves a tool's
// BackendID through backendDisplayName (against the session's registry) and bakes the
// result into the handler closure, which writes it to the audit BackendInfo. The
// registry maps the ID to a DISTINCT Name, so the assertion proves the ID was resolved
// (not passed through).
func TestServeResolvesBackendNameEndToEnd(t *testing.T) {
	t.Parallel()

	// BackendID "backend-x" != Name "github-mcp": a pass-through would record the ID.
	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t", BackendID: "backend-x"}}}
	reg := vmcp.NewImmutableRegistry([]vmcp.Backend{{ID: "backend-x", Name: "github-mcp"}})
	srv, sessionID, _ := registerServeSessionWithRegistry(t, fc, reg)

	// Rebuild the tools the way registration did: the handler closure carries the
	// registry-resolved backend name.
	tools, err := srv.coreSessionTools(context.Background(), sessionID, nil)
	require.NoError(t, err)
	require.Len(t, tools, 1)

	bi := &audit.BackendInfo{}
	ctx := audit.WithBackendInfo(context.Background(), bi)
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}}
	res, err := tools[0].Handler(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)
	assert.Equal(t, "github-mcp", bi.BackendName,
		"the handler must label the audit event with the registry-resolved name, not the raw BackendID")
}

// TestServeServesPrompts locks in end-to-end per-session prompt serving: the core
// advertises a prompt, the Serve path injects it via SessionWithPrompts, and a
// prompts/list surfaces it while a prompts/get routes through core.GetPrompt and
// returns the core result (the fix for the "unknown prompt" -32602 regression).
func TestServeServesPrompts(t *testing.T) {
	t.Parallel()

	const promptName = "serve-prompt"
	fc := &fakeCore{prompts: []vmcp.Prompt{{Name: promptName, Description: "a served prompt"}}}
	_, sessionID, baseURL := registerServeSession(t, fc)

	listResp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "prompts/list",
		"params":  map[string]any{},
	}, sessionID)
	defer listResp.Body.Close()
	listBody, err := io.ReadAll(listResp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(listBody), promptName,
		"the Serve path must advertise prompts injected from the core")

	getResp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "prompts/get",
		"params":  map[string]any{"name": promptName},
	}, sessionID)
	defer getResp.Body.Close()
	getBody, err := io.ReadAll(getResp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, getResp.StatusCode, "prompts/get should succeed; body: %s", string(getBody))
	assert.Contains(t, string(getBody), "prompt-body",
		"prompts/get must return the core result, not a -32602 unknown-prompt error")
	assert.NotContains(t, string(getBody), "-32602",
		"prompts/get must not report the prompt as unknown")
	assert.Equal(t, int32(1), fc.getPromptCalls.Load(),
		"prompts/get must route through core.GetPrompt exactly once")
}

// postServeMCP sends a JSON-RPC POST to the given Streamable HTTP base URL. It is the
// package-server-internal analogue of the postMCP helper in the external suite.
func postServeMCP(t *testing.T, baseURL string, body map[string]any, sessionID string) *http.Response {
	t.Helper()
	resp, err := doServeMCP(baseURL, body, sessionID)
	require.NoError(t, err)
	return resp
}

// doServeMCP performs a tools POST and returns the response or an error. It never
// calls require/assert, so it is safe to invoke from worker goroutines where
// FailNow/Goexit would run off the test goroutine and misreport. postServeMCP
// wraps it for the common test-goroutine case.
func doServeMCP(baseURL string, body map[string]any, sessionID string) (*http.Response, error) {
	rawBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/mcp", bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	return http.DefaultClient.Do(req)
}
