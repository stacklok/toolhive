// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	sessionfactorymocks "github.com/stacklok/toolhive/pkg/vmcp/session/mocks"
	sessionmocks "github.com/stacklok/toolhive/pkg/vmcp/session/types/mocks"
)

// These tests exercise the session-creation wiring and SDK hooks relocated into
// Serve (#5440). They drive the SDK session lifecycle through the relocated
// vmcpSessionMgr + mcpServer directly, mounting the Streamable HTTP server WITHOUT
// the authenticated discovery middleware (relocated by #5441/#5442). That keeps the
// test within this task's scope while proving the hooks fire and two-phase session
// creation runs identically when Serve is exercised directly. The full HTTP suite
// stays on server.New (its parity gate) in session_management_integration_test.go.

// toolSessionState tracks observable behaviour of the mock session factory below.
type toolSessionState struct {
	makeWithIDCalled atomic.Bool
	mu               sync.Mutex
	lastSession      *sessionmocks.MockMultiSession
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
			state.mu.Lock()
			state.lastSession = mock
			state.mu.Unlock()
			return mock, nil
		}).AnyTimes()
	return factory, state
}

// TestServeRegistersSessionHooks verifies that Serve registers the OnRegisterSession
// hook and wires two-phase session creation: an MCP initialize triggers the hook,
// which creates the session (MakeSessionWithID) and injects its tools, so a
// subsequent tools/list advertises them. The OnBeforeListTools hook also runs (and
// no-ops, since the tools are already present on this pod).
func TestServeRegistersSessionHooks(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	testTool := vmcp.Tool{Name: "serve-tool", Description: "a relocated-wiring test tool"}
	factory, state := newToolSessionFactory(t, ctrl, []vmcp.Tool{testTool})

	srv, err := Serve(context.Background(), &stubVMCP{}, &ServerConfig{
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	// Mount the Streamable HTTP server on the relocated mcpServer + vmcpSessionMgr,
	// bypassing the not-yet-relocated discovery middleware (#5441/#5442).
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

	// tools/list runs the OnBeforeListTools hook and returns the per-session tools
	// injected during registration.
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
		"tools/list should advertise the tool injected by the OnRegisterSession hook")
}

// TestServeClosesStorageOnSessionManagerError verifies the closeStorageOnErr guard:
// when sessionmanager.New fails after the session data storage is built, Serve
// returns the error (so the deferred guard closes the storage and its cleanup
// goroutine does not leak). A negative CacheCapacity is the cheapest forced failure.
func TestServeClosesStorageOnSessionManagerError(t *testing.T) {
	t.Parallel()

	srv, err := Serve(context.Background(), &stubVMCP{}, &ServerConfig{
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
