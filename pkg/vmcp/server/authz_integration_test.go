// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// newCedarAuthzTestServer builds a vMCP server via the rerouted server.New (→ core.New +
// Serve) backed by the real "echo" MCP backend at backendURL. An AuthMiddleware injects a
// fixed authenticated principal (the Cedar authorizer must resolve one — a nil identity
// denies on missing-principal), and a Cedar authz.Config compiled from policies is supplied
// via Config.Authz. This exercises the full Config.Authz → deriveCoreConfig → core admission
// chain that replaced the legacy HTTP authz middleware on the Serve path.
func newCedarAuthzTestServer(t *testing.T, backendURL string, policies ...string) *httptest.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	backend := vmcp.Backend{
		ID:            "real-backend",
		Name:          "real-backend",
		BaseURL:       backendURL,
		TransportType: "streamable-http",
	}
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{backend}).AnyTimes()
	mockBackendRegistry.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&backend).AnyTimes()

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy(),
	))
	factory := vmcpsession.NewSessionFactory(authReg)

	backendClient, err := vmcpclient.NewHTTPBackendClient(authReg)
	require.NoError(t, err)
	// A priority resolver keeps raw tool names ("echo", not "real-backend_echo") so the
	// Cedar policies below can name Tool::"echo".
	resolver, err := aggregator.NewPriorityConflictResolver([]string{backend.Name})
	require.NoError(t, err)
	agg := aggregator.NewDefaultAggregator(backendClient, resolver, nil, nil)

	// Inject a fixed authenticated identity on every request so the session binds to it at
	// initialize and the Cedar authorizer can resolve the principal on subsequent calls.
	identityMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
				Subject: "user-123",
				Name:    "Test User",
				Claims:  map[string]any{"sub": "user-123", "name": "Test User"},
			}}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
		})
	}

	authzCfg, err := authorizers.NewConfig(cedar.Config{
		Version: "1.0",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{Policies: policies, EntitiesJSON: "[]"},
	})
	require.NoError(t, err)

	srv, err := server.New(
		context.Background(),
		&server.Config{
			// Name is non-empty: deriveCoreConfig forwards the raw server name to the core,
			// and the Cedar admission seam requires it (resource entities are scoped to
			// MCP::"<name>"). This is the raw-name-for-authz parity the reroute preserves.
			Name:           "test-vmcp",
			Host:           "127.0.0.1",
			Port:           0,
			SessionTTL:     5 * time.Minute,
			SessionFactory: factory,
			Aggregator:     agg,
			AuthMiddleware: identityMiddleware,
			Authz:          authzCfg,
		},
		router.NewSessionRouter(&vmcp.RoutingTable{}),
		backendClient,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// TestIntegration_RealBackend_CedarAuthzGatesToolCall proves the core admission seam — the
// authorization boundary the New/Serve reroute moved off the HTTP authz middleware and onto
// Config.Authz — enforces a Cedar policy end-to-end over HTTP through the rerouted server.New.
// It covers both gates the core now owns: the list filter (FilterTools) and the call gate
// (AllowToolCall, whose denial is genericized so the authorizer detail never leaks).
func TestIntegration_RealBackend_CedarAuthzGatesToolCall(t *testing.T) {
	t.Parallel()

	t.Run("list filter: an unpermitted tool is neither advertised nor callable", func(t *testing.T) {
		t.Parallel()

		backendURL := startRealMCPBackend(t)
		// Permit only an unrelated tool: the principal resolves, but "echo" is default-denied,
		// so the core's FilterTools drops it and it is never registered with the session.
		ts := newCedarAuthzTestServer(t, backendURL,
			`permit(principal, action == Action::"call_tool", resource == Tool::"unrelated");`)

		client := NewMCPTestClient(t, ts.URL)
		client.InitializeSession()

		// "echo" is filtered out, so there is no list signal to wait on; poll the call until
		// the session is established and the SDK rejects the unregistered tool (transient
		// pre-registration 404s are tolerated and retried).
		var lastBody string
		require.Eventually(t, func() bool {
			resp := client.CallTool("echo", map[string]any{"input": "hello"})
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			lastBody = string(b)
			return resp.StatusCode == http.StatusOK && bytes.Contains(b, []byte("not found"))
		}, 5*time.Second, 50*time.Millisecond,
			"a tool filtered out by the Cedar policy must be rejected as an unknown tool")
		assert.NotContains(t, lastBody, "hello", "a filtered tool must never reach the backend")

		// And it must be absent from tools/list.
		listResp := client.ListTools()
		defer listResp.Body.Close()
		listBody, err := io.ReadAll(listResp.Body)
		require.NoError(t, err)
		assert.NotContains(t, string(listBody), `"echo"`,
			"a tool denied by policy must not be advertised in tools/list")
	})

	t.Run("call gate: an advertised tool's call is denied by an arg-gated policy", func(t *testing.T) {
		t.Parallel()

		backendURL := startRealMCPBackend(t)
		// "echo" is permitted (so it is advertised and callable), but a forbid clause denies
		// the specific call whose "input" argument is "blocked". The forbid does not fire at
		// list time (no call args), so the tool is still advertised — this is the only way to
		// reach the AllowToolCall gate, since the list filter and call gate otherwise agree.
		ts := newCedarAuthzTestServer(t, backendURL,
			`permit(principal, action == Action::"call_tool", resource == Tool::"echo");`,
			`forbid(principal, action == Action::"call_tool", resource == Tool::"echo") `+
				`when { context has arg_input && context.arg_input == "blocked" };`)

		client := NewMCPTestClient(t, ts.URL)
		client.InitializeSession()
		waitForEchoTool(t, ts.URL, client.SessionID())

		// Positive control: a call whose args satisfy the policy reaches the backend.
		okResp := client.CallTool("echo", map[string]any{"input": "hello"})
		defer okResp.Body.Close()
		okBody, err := io.ReadAll(okResp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, okResp.StatusCode, "body: %s", string(okBody))
		assert.Contains(t, string(okBody), "hello", "a permitted call must return the backend result")

		// Denied call: the core admission seam rejects it with a generic message that never
		// leaks the underlying authorizer/policy detail.
		denyResp := client.CallTool("echo", map[string]any{"input": "blocked"})
		defer denyResp.Body.Close()
		denyBody, err := io.ReadAll(denyResp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, denyResp.StatusCode, "body: %s", string(denyBody))
		assert.Contains(t, string(denyBody), "call denied by authorization policy",
			"an arg-gated policy denial must surface the genericized authorization message")
		assert.NotContains(t, string(denyBody), "cedar", "the authorizer implementation detail must not leak")
		assert.NotContains(t, string(denyBody), "forbid", "the policy text must not leak")
	})
}
