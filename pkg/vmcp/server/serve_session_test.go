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
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
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
	factory.EXPECT().MakeSessionWithID(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend) (vmcpsession.MultiSession, error) {
			state.makeWithIDCalled.Store(true)
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
	tools     []vmcp.Tool
	resources []vmcp.Resource
	prompts   []vmcp.Prompt

	listToolsCalls     atomic.Int32
	listResourcesCalls atomic.Int32
	callToolCalls      atomic.Int32
	readResourceCalls  atomic.Int32
	lastCallToolName   atomic.Value // string
	lastReadURI        atomic.Value // string

	callErr error // when set, CallTool returns it (e.g. vmcp.ErrAuthorizationFailed)
	readErr error // when set, ReadResource returns it
}

var _ core.VMCP = (*fakeCore)(nil)

func (f *fakeCore) ListTools(context.Context, *auth.Identity) ([]vmcp.Tool, error) {
	f.listToolsCalls.Add(1)
	return f.tools, nil
}

func (f *fakeCore) CallTool(
	_ context.Context, _ *auth.Identity, name string, _ map[string]any, _ map[string]any,
) (*vmcp.ToolCallResult, error) {
	f.callToolCalls.Add(1)
	f.lastCallToolName.Store(name)
	if f.callErr != nil {
		return nil, f.callErr
	}
	return &vmcp.ToolCallResult{Content: []vmcp.Content{{Type: vmcp.ContentTypeText, Text: "ok"}}}, nil
}

func (f *fakeCore) ListResources(context.Context, *auth.Identity) ([]vmcp.Resource, error) {
	f.listResourcesCalls.Add(1)
	return f.resources, nil
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
	return f.prompts, nil
}

func (*fakeCore) GetPrompt(
	context.Context, *auth.Identity, string, map[string]any,
) (*vmcp.PromptGetResult, error) {
	return &vmcp.PromptGetResult{}, nil
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
			res := f.resources[i]
			return &res, nil
		}
	}
	return nil, vmcp.ErrNotFound
}

func (*fakeCore) LookupPrompt(context.Context, *auth.Identity, string) (*vmcp.Prompt, error) {
	return nil, vmcp.ErrNotFound
}

func (*fakeCore) Close() error { return nil }

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

	// Wait until the session is fully registered with adapted tools in the manager.
	require.Eventually(t, func() bool {
		tools, gErr := srv.vmcpSessionMgr.GetAdaptedTools(sessionID)
		return gErr == nil && len(tools) > 0
	}, 2*time.Second, 10*time.Millisecond, "session should be registered with adapted tools")

	// Empty-store (cross-pod) branch: a fresh SDK session with no tools gets them injected.
	rehydrated := &fakeSDKSession{id: sessionID, tools: map[string]server.ServerTool{}}
	srv.lazyInjectSessionTools(srv.mcpServer.WithContext(context.Background(), rehydrated))
	assert.Contains(t, rehydrated.tools, testTool.Name,
		"an empty per-session store should be re-injected from the vMCP session manager")

	// No-op branch: a populated store is left untouched (early return before GetAdaptedTools).
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

	require.Eventually(t, func() bool { _, ok := srv.vmcpSessionMgr.GetMultiSession(sessionID); return ok },
		2*time.Second, 10*time.Millisecond, "session should be registered")

	// Anonymous caller (no token) matches the anonymous binding — allowed.
	require.NoError(t, srv.enforceSessionBinding(sessionID, nil),
		"anonymous caller must be permitted on an anonymous session")

	// A caller presenting a token on an anonymous session is a session-upgrade attack.
	err = srv.enforceSessionBinding(sessionID, &auth.Identity{Token: "attacker-token"})
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
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	require.Eventually(t, func() bool { _, ok := srv.vmcpSessionMgr.GetMultiSession(sessionID); return ok },
		2*time.Second, 10*time.Millisecond, "session should be registered")
	return srv, sessionID, ts.URL
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
			res, err := srv.coreToolHandler(sessionID, "t")(context.Background(), req)
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
	res, err := srv.coreToolHandler(sessionID, "t")(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError)
	body, _ := json.Marshal(res)
	assert.Contains(t, string(body), "Unauthorized")
	assert.Equal(t, int32(0), fc.callToolCalls.Load(), "core.CallTool must not be reached on a binding failure")

	require.Eventually(t, func() bool { _, ok := srv.vmcpSessionMgr.GetMultiSession(sessionID); return !ok },
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

		contents, err := srv.coreResourceHandler(sessionID, uri)(context.Background(), mcp.ReadResourceRequest{})
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

		_, err := srv.coreResourceHandler(sessionID, uri)(context.Background(), mcp.ReadResourceRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read denied by authorization policy")
		assert.NotContains(t, err.Error(), "cedar said no", "the underlying authorizer error must not leak")
	})
}

// TestServeOmitsPrompts locks in the intentional prompt omission: even when the core
// advertises a prompt, the Serve path injects only tools/resources (the SDK has no
// per-session prompt support), so a prompts/list does not surface it.
func TestServeOmitsPrompts(t *testing.T) {
	t.Parallel()

	const promptName = "serve-only-prompt"
	fc := &fakeCore{prompts: []vmcp.Prompt{{Name: promptName}}}
	_, sessionID, baseURL := registerServeSession(t, fc)

	resp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "prompts/list",
		"params":  map[string]any{},
	}, sessionID)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.NotContains(t, string(body), promptName,
		"the Serve path must not advertise prompts even when the core supplies them")
}

// postServeMCP sends a JSON-RPC POST to the given Streamable HTTP base URL. It is the
// package-server-internal analogue of the postMCP helper in the external suite.
func postServeMCP(t *testing.T, baseURL string, body map[string]any, sessionID string) *http.Response {
	t.Helper()
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/mcp", bytes.NewReader(rawBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}
