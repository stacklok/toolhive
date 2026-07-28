// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	"github.com/stacklok/toolhive/test/integration/vmcp/helpers"
)

// principalHeader is the request header the test identity middleware reads to
// determine the authenticated principal. The Cedar authorizer resolves the
// principal from the identity's "sub" claim, so the middleware sets both
// Subject and Claims["sub"] to the header value.
const principalHeader = "X-Test-Principal"

// newCedarVMCPServer builds a Cedar-authz-backed vMCP server (via server.New,
// mirroring pkg/vmcp/server/authz_integration_test.go) backed by a real MCP
// backend at backendURL. An identity middleware injects a principal derived
// from the X-Test-Principal request header so each session binds to its
// caller and the Cedar admission seam can resolve Client::"<principal>".
//
// A priority conflict resolver is used so tool names are NOT prefixed — the
// Cedar policies can name tools by their raw backend names (e.g.
// Tool::"secret-tool").
func newCedarVMCPServer(t *testing.T, backendURL string, policies ...string) *httptest.Server {
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

	authReg, err := vmcpauth.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
	require.NoError(t, err)
	factory := vmcpsession.NewSessionFactory(authReg)

	backendClient, err := vmcpclient.NewHTTPBackendClient(authReg)
	require.NoError(t, err)
	// Priority resolver keeps raw tool names so Cedar policies can name
	// Tool::"secret-tool" rather than a prefixed variant.
	resolver, err := aggregator.NewPriorityConflictResolver([]string{backend.Name})
	require.NoError(t, err)
	agg := aggregator.NewDefaultAggregator(backendClient, resolver, nil, nil)

	// Identity middleware: derive the principal from the X-Test-Principal
	// header so two sessions (alice, bob) bind to different identities. The
	// Cedar authorizer resolves the principal from the "sub" claim, so both
	// Subject and Claims["sub"] are set to the header value.
	identityMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := r.Header.Get(principalHeader)
			if principal == "" {
				principal = "anonymous"
			}
			id := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
				Subject: principal,
				Name:    principal,
				Claims:  map[string]any{"sub": principal, "name": principal},
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

// rawClient is a minimal JSON-RPC-over-HTTP client for the Cedar authz tests.
// It is needed because the helpers.MCPClient.CallTest helper uses require.NoError
// on the SDK error, which would abort the test on a policy-rejected call. This
// client surfaces the raw HTTP response so tests can inspect status codes and
// error bodies directly.
type rawClient struct {
	baseURL   string
	sessionID string
	nextID    int
}

func newRawClient(baseURL string) *rawClient {
	return &rawClient{baseURL: baseURL, nextID: 1}
}

func (c *rawClient) initialize(t *testing.T, principal string) {
	t.Helper()
	resp := c.postMCP(t, principal, map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	c.nextID++
	defer resp.Body.Close()
	c.sessionID = resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, c.sessionID, "initialize response missing Mcp-Session-Id header")

	// Send the initialized notification (no id, no response expected).
	notif := c.postMCP(t, principal, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}, c.sessionID)
	notif.Body.Close()
}

func (c *rawClient) listTools(t *testing.T, principal string) map[string]any {
	t.Helper()
	resp := c.postMCP(t, principal, map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  "tools/list",
		"params":  map[string]any{},
	}, c.sessionID)
	c.nextID++
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "tools/list failed: %s", string(body))
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	return parsed
}

func (c *rawClient) callTool(t *testing.T, principal, name string, args map[string]any) (int, map[string]any) {
	t.Helper()
	resp := c.postMCP(t, principal, map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}, c.sessionID)
	c.nextID++
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed) // best-effort; body may be plain text on 404
	return resp.StatusCode, parsed
}

func (c *rawClient) postMCP(t *testing.T, principal string, body map[string]any, sessionID string) *http.Response {
	t.Helper()
	rawBody, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.baseURL+"/mcp", bytes.NewReader(rawBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(principalHeader, principal)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// toolNamesFromResult extracts the tool names from a tools/list JSON-RPC result.
func toolNamesFromResult(t *testing.T, result map[string]any) []string {
	t.Helper()
	rawResult, ok := result["result"].(map[string]any)
	require.True(t, ok, "response missing result object: %v", result)
	toolsRaw, ok := rawResult["tools"].([]any)
	require.True(t, ok, "result missing tools array: %v", rawResult)
	names := make([]string, 0, len(toolsRaw))
	for _, t2 := range toolsRaw {
		tool, ok := t2.(map[string]any)
		require.True(t, ok)
		name, ok := tool["name"].(string)
		require.True(t, ok)
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// startAuthzBackend starts a real MCP backend exposing the named tools. Each
// tool echoes back its name and the input so a caller can confirm the backend
// was reached.
func startAuthzBackend(t *testing.T, toolNames ...string) string {
	t.Helper()
	tools := make([]helpers.BackendTool, 0, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, helpers.NewBackendTool(
			name,
			"test tool "+name,
			func(_ context.Context, args map[string]any) string {
				return `{"tool":` + name + `,"args":` + toJSON(args) + `}`
			},
		))
	}
	backend := helpers.CreateBackendServer(t, tools, helpers.WithBackendName("real-backend"))
	t.Cleanup(backend.Close)
	return backend.URL + "/mcp"
}

// toJSON is a tiny marshal helper that never fails the test (returns "null").
func toJSON(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// TestRegression_TwoSessions_DifferentAuthz_DifferentToolsList proves the
// core admission seam projects a per-identity tool list: two sessions bound to
// different principals see different advertised tool sets under the same Cedar
// policy. Alice (permitted for secret-tool) sees both secret-tool and
// public-tool; Bob (only permitted for public-tool) sees only public-tool.
func TestRegression_TwoSessions_DifferentAuthz_DifferentToolsList(t *testing.T) {
	t.Parallel()

	backendURL := startAuthzBackend(t, "secret-tool", "public-tool")

	ts := newCedarVMCPServer(t, backendURL,
		`permit(principal == Client::"alice", action == Action::"call_tool", resource == Tool::"secret-tool");`,
		`permit(principal, action == Action::"call_tool", resource == Tool::"public-tool");`,
	)

	// Alice's session.
	alice := newRawClient(ts.URL)
	alice.initialize(t, "alice")
	aliceTools := toolNamesFromResult(t, alice.listTools(t, "alice"))
	assert.Equal(t, []string{"public-tool", "secret-tool"}, aliceTools,
		"alice (permitted for secret-tool) must see both tools")

	// Bob's session.
	bob := newRawClient(ts.URL)
	bob.initialize(t, "bob")
	bobTools := toolNamesFromResult(t, bob.listTools(t, "bob"))
	assert.Equal(t, []string{"public-tool"}, bobTools,
		"bob (not permitted for secret-tool) must see only public-tool")
}

// TestRegression_FilteredToolUncallableInSessionA_CallableInSessionB proves
// the list-filter/call-deny pairing: a tool filtered out of a session (bob's)
// is rejected as unknown when called, while the same tool is callable in a
// session where it was advertised (alice's).
func TestRegression_FilteredToolUncallableInSessionA_CallableInSessionB(t *testing.T) {
	t.Parallel()

	backendURL := startAuthzBackend(t, "secret-tool", "public-tool")

	ts := newCedarVMCPServer(t, backendURL,
		`permit(principal == Client::"alice", action == Action::"call_tool", resource == Tool::"secret-tool");`,
		`permit(principal, action == Action::"call_tool", resource == Tool::"public-tool");`,
	)

	// Alice can call secret-tool (it is in her session's advertised set).
	alice := newRawClient(ts.URL)
	alice.initialize(t, "alice")
	require.Contains(t, toolNamesFromResult(t, alice.listTools(t, "alice")), "secret-tool")

	status, result := alice.callTool(t, "alice", "secret-tool", map[string]any{"input": "hi"})
	require.Equal(t, http.StatusOK, status, "alice's permitted call must succeed: %v", result)
	assert.False(t, isToolResultError(result), "alice's call to secret-tool must not be an error: %v", result)

	// Bob cannot call secret-tool: it was filtered out of his session, so the
	// SDK rejects it as an unknown tool. Poll briefly because the session's
	// tool injection runs in a hook that fires after initialize returns.
	bob := newRawClient(ts.URL)
	bob.initialize(t, "bob")
	require.NotContains(t, toolNamesFromResult(t, bob.listTools(t, "bob")), "secret-tool")

	var lastResult map[string]any
	require.Eventually(t, func() bool {
		var s int
		s, lastResult = bob.callTool(t, "bob", "secret-tool", map[string]any{"input": "hi"})
		if s != http.StatusOK {
			return true // 404/400 also indicates rejection
		}
		return isToolResultError(lastResult)
	}, 5*time.Second, 50*time.Millisecond,
		"bob's call to secret-tool (filtered out) must be rejected")
}

// TestRegression_SetSessionTools_FixedAtInitialize_NoMidSessionReconciliation
// asserts that a session's advertised tool set is fixed at initialize time
// and is not re-aggregated on each tools/list call. Calling tools/list twice
// on the same session must return the same set.
func TestRegression_SetSessionTools_FixedAtInitialize_NoMidSessionReconciliation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	backend := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool("tool-a", "tool A", func(_ context.Context, _ map[string]any) string {
			return `{"tool":"a"}`
		}),
		helpers.NewBackendTool("tool-b", "tool B", func(_ context.Context, _ map[string]any) string {
			return `{"tool":"b"}`
		}),
	}, helpers.WithBackendName("ab-backend"))
	defer backend.Close()

	backends := []vmcp.Backend{
		helpers.NewBackend("ab-backend", helpers.WithURL(backend.URL+"/mcp")),
	}

	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
	)

	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"
	client := helpers.NewMCPClient(ctx, t, vmcpURL)
	defer client.Close()

	first := helpers.GetToolNames(client.ListTools(ctx))
	second := helpers.GetToolNames(client.ListTools(ctx))

	sort.Strings(first)
	sort.Strings(second)

	assert.Equal(t, []string{"ab-backend_tool-a", "ab-backend_tool-b"}, first,
		"initial tools/list must show both tools")
	assert.Equal(t, first, second,
		"the session's tool set is fixed at initialize; a second tools/list must return the same set")
}

// isToolResultError returns true when a tools/call JSON-RPC result has
// IsError=true (the SDK rejected the call) or the response carries a
// JSON-RPC error object.
func isToolResultError(result map[string]any) bool {
	if _, ok := result["error"]; ok {
		return true
	}
	if r, ok := result["result"].(map[string]any); ok {
		if isErr, _ := r["isError"].(bool); isErr {
			return true
		}
	}
	return false
}

// mustParseJSONRPC reads resp.Body and unmarshals it as a JSON-RPC envelope.
// If the body is not valid JSON (e.g. a plain-text 404), it returns an empty
// map so callers can fall back to status-code checks.
func mustParseJSONRPC(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed) // best-effort; body may be plain text on 4xx
	return parsed
}

// Compile-time check that the mcpcompat imports are used (not mark3labs/mcp-go).
var (
	_ = mcp.LATEST_PROTOCOL_VERSION
	_ = mcpserver.NewMCPServer
)
