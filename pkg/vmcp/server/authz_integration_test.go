// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/codemode"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// rpcErrorFields is the subset of a JSON-RPC error envelope the authz tests assert on.
type rpcErrorFields struct {
	code    int
	message string
	present bool
}

// parseRPCError extracts the JSON-RPC error code/message from a response body. It
// reports present=false when the body carries no top-level "error" object (e.g. a
// successful result).
func parseRPCError(t *testing.T, body []byte) rpcErrorFields {
	t.Helper()
	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil || env.Error == nil {
		return rpcErrorFields{}
	}
	return rpcErrorFields{code: env.Error.Code, message: env.Error.Message, present: true}
}

// newCedarAuthzTestServer builds a vMCP server via the rerouted server.New (→ core.New +
// Serve) backed by the real "echo" MCP backend at backendURL. An AuthMiddleware injects a
// fixed authenticated principal (the Cedar authorizer must resolve one — a nil identity
// denies on missing-principal), and a Cedar authz.Config compiled from policies is supplied
// via Config.Authz. This exercises the full Config.Authz → deriveCoreConfig → core admission
// chain that replaced the legacy HTTP authz middleware on the Serve path.
func newCedarAuthzTestServer(t *testing.T, backendURL string, policies ...string) *httptest.Server {
	t.Helper()
	return buildCedarAuthzServer(t, backendURL, nil, nil, policies...)
}

// newCedarAuthzCodeModeServer is newCedarAuthzTestServer with code mode enabled, so
// the core is wrapped by the codemode decorator. It proves the pre-dispatch gate's
// codemode carve-out: execute_tool_script is admitted (the feature flag is the grant)
// while a directly-denied tool still 403s.
func newCedarAuthzCodeModeServer(t *testing.T, backendURL string, policies ...string) *httptest.Server {
	t.Helper()
	return buildCedarAuthzServer(t, backendURL, &codemode.Config{}, nil, policies...)
}

// buildCedarAuthzServer builds the vMCP test server. A non-nil codeModeCfg enables
// the codemode decorator; a non-nil auditCfg enables the audit middleware; a nil
// policies slice leaves Authz unset (allow-all, gate not installed) — used by the
// no-Authz parity guard.
func buildCedarAuthzServer(
	t *testing.T, backendURL string, codeModeCfg *codemode.Config, auditCfg *audit.Config, policies ...string,
) *httptest.Server {
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

	// Inject an authenticated identity on every request so the session binds to it at
	// initialize and the Cedar authorizer can resolve the principal on subsequent calls.
	// The principal defaults to the fixed "user-123" (all existing authz tests rely on
	// this), but a request carrying X-Test-Principal derives the identity from that header
	// instead — letting a single running server authenticate different callers as distinct
	// principals (see TestRegression_PerSessionToolProjection_DivergesByPrincipal), without
	// changing behavior for any test that never sets the header. The MCP parser is composed
	// inside it, mirroring the production incoming-auth factory (see pkg/vmcp/auth/factory):
	// audit and authz read parsed MCP data from the request context, and the audit
	// middleware sits between auth and the parser applied in Handler.
	identityMiddleware := func(next http.Handler) http.Handler {
		withParser := mcpparser.ParsingMiddleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := r.Header.Get("X-Test-Principal")
			if principal == "" {
				principal = "user-123"
			}
			id := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
				Subject: principal,
				Name:    "Test User",
				Claims:  map[string]any{"sub": principal, "name": "Test User"},
			}}
			withParser.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
		})
	}

	// A nil policies slice means "no authz": leave Config.Authz nil so the gate is not
	// installed (the no-Authz parity guard exercises this). Otherwise compile the Cedar
	// policies into the config the core admission seam consumes.
	var authzCfg *authorizers.Config
	if policies != nil {
		authzCfg, err = authorizers.NewConfig(cedar.Config{
			Version: "1.0",
			Type:    cedar.ConfigType,
			Options: &cedar.ConfigOptions{Policies: policies, EntitiesJSON: "[]"},
		})
		require.NoError(t, err)
	}

	srv, err := server.New(
		t.Context(),
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
			AuditConfig:    auditCfg,
			CodeModeConfig: codeModeCfg,
		},
		router.NewSessionRouter(&vmcp.RoutingTable{}),
		backendClient,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	handler, err := srv.Handler(t.Context())
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

		// #5827: a policy-denied direct tools/call is now rejected by the pre-dispatch
		// gate as HTTP 403 + JSON-RPC error code 403 — NOT the old 200 + SDK "not found"
		// that leaked filtered-vs-nonexistent. The gate decision is deterministic (it
		// re-runs core admission), so no polling is needed.
		resp := client.CallTool("echo", map[string]any{"input": "hello"})
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a tool denied by policy must be rejected with HTTP 403, body: %s", string(body))
		rpcErr := parseRPCError(t, body)
		require.True(t, rpcErr.present, "the 403 must carry a JSON-RPC error envelope, body: %s", string(body))
		assert.Equal(t, mcpparser.JSONRPCCodeDenied, rpcErr.code, "the JSON-RPC error code must be 403")
		assert.Equal(t, "call denied by authorization policy", rpcErr.message)
		assert.NotContains(t, string(body), "hello", "a filtered tool must never reach the backend")

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

		// Denied call: #5827 — the pre-dispatch gate rejects the arg-gated denial as
		// HTTP 403 + JSON-RPC error code 403 (NOT the old 200 + IsError), with a generic
		// message that never leaks the authorizer/policy detail.
		denyResp := client.CallTool("echo", map[string]any{"input": "blocked"})
		defer denyResp.Body.Close()
		denyBody, err := io.ReadAll(denyResp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, denyResp.StatusCode, "body: %s", string(denyBody))
		rpcErr := parseRPCError(t, denyBody)
		require.True(t, rpcErr.present, "the 403 must carry a JSON-RPC error envelope, body: %s", string(denyBody))
		assert.Equal(t, mcpparser.JSONRPCCodeDenied, rpcErr.code, "the JSON-RPC error code must be 403")
		assert.Equal(t, "call denied by authorization policy", rpcErr.message,
			"an arg-gated policy denial must surface the genericized authorization message")
		assert.NotContains(t, string(denyBody), "cedar", "the authorizer implementation detail must not leak")
		assert.NotContains(t, string(denyBody), "forbid", "the policy text must not leak")
	})
}

// TestIntegration_RealBackend_CedarAuthz_NoEnumerationOracle proves the denial carries
// no information about whether the named tool exists: under a default-deny policy a
// nonexistent tool is rejected with the SAME 403 + message as a real-but-denied tool.
// A separate permissive policy documents the flip side — when the name IS permitted,
// the gate admits and the SDK's own -32602 "not found" surfaces at 200, so authz never
// masks nonexistence for an allowed name.
func TestIntegration_RealBackend_CedarAuthz_NoEnumerationOracle(t *testing.T) {
	t.Parallel()

	t.Run("nonexistent tool under default-deny is an identical 403", func(t *testing.T) {
		t.Parallel()

		backendURL := startRealMCPBackend(t)
		ts := newCedarAuthzTestServer(t, backendURL,
			`permit(principal, action == Action::"call_tool", resource == Tool::"unrelated");`)

		client := NewMCPTestClient(t, ts.URL)
		client.InitializeSession()

		resp := client.CallTool("does-not-exist", map[string]any{"input": "x"})
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "body: %s", string(body))
		rpcErr := parseRPCError(t, body)
		require.True(t, rpcErr.present, "body: %s", string(body))
		assert.Equal(t, mcpparser.JSONRPCCodeDenied, rpcErr.code)
		assert.Equal(t, "call denied by authorization policy", rpcErr.message,
			"a nonexistent tool must be denied identically to a real one (no enumeration oracle)")
		assert.NotContains(t, string(body), "does-not-exist", "the tool name must not leak in the denial")
	})

	t.Run("permitted nonexistent name yields the SDK's -32602 at 200", func(t *testing.T) {
		t.Parallel()

		backendURL := startRealMCPBackend(t)
		// Permit call_tool on any resource: the gate admits, so a nonexistent name reaches
		// the SDK and gets the standard invalid-params "not found" — authz does not mask it.
		ts := newCedarAuthzTestServer(t, backendURL,
			`permit(principal, action == Action::"call_tool", resource);`)

		client := NewMCPTestClient(t, ts.URL)
		client.InitializeSession()

		resp := client.CallTool("ghost-tool", map[string]any{"input": "x"})
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"a permitted-but-nonexistent name must not be gated, body: %s", string(body))
		rpcErr := parseRPCError(t, body)
		require.True(t, rpcErr.present, "body: %s", string(body))
		assert.Equal(t, -32602, rpcErr.code, "the SDK's INVALID_PARAMS code must surface for an unknown tool")
	})
}

// TestIntegration_RealBackend_CedarAuthz_ResourceAndPromptDenied proves the gate covers
// resources/read and prompts/get, not just tools/call: a denied read/get is rejected as
// HTTP 403 + JSON-RPC 403 with the kind-specific message.
func TestIntegration_RealBackend_CedarAuthz_ResourceAndPromptDenied(t *testing.T) {
	t.Parallel()

	// Permit only an unrelated tool so every resource/prompt is default-denied.
	const denyAllReadsAndPrompts = `permit(principal, action == Action::"call_tool", resource == Tool::"unrelated");`

	t.Run("resources/read denied", func(t *testing.T) {
		t.Parallel()

		backendURL := startRealMCPBackend(t)
		ts := newCedarAuthzTestServer(t, backendURL, denyAllReadsAndPrompts)

		client := NewMCPTestClient(t, ts.URL)
		client.InitializeSession()

		resp := client.ReadResource("file://secret")
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "body: %s", string(body))
		rpcErr := parseRPCError(t, body)
		require.True(t, rpcErr.present, "body: %s", string(body))
		assert.Equal(t, mcpparser.JSONRPCCodeDenied, rpcErr.code)
		assert.Equal(t, "read denied by authorization policy", rpcErr.message)
	})

	t.Run("prompts/get denied", func(t *testing.T) {
		t.Parallel()

		backendURL := startRealMCPBackend(t)
		ts := newCedarAuthzTestServer(t, backendURL, denyAllReadsAndPrompts)

		client := NewMCPTestClient(t, ts.URL)
		client.InitializeSession()

		resp := client.GetPrompt("secret-prompt")
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode, "body: %s", string(body))
		rpcErr := parseRPCError(t, body)
		require.True(t, rpcErr.present, "body: %s", string(body))
		assert.Equal(t, mcpparser.JSONRPCCodeDenied, rpcErr.code)
		assert.Equal(t, "prompt denied by authorization policy", rpcErr.message)
	})
}

// TestIntegration_RealBackend_NoAuthz_UnknownToolIsSDKError is the byte-parity guard:
// with no Authz configured the gate is NOT installed, so an unknown tool falls through
// to the SDK and gets -32602 at 200 — exactly today's behavior, unchanged by the gate.
func TestIntegration_RealBackend_NoAuthz_UnknownToolIsSDKError(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	// nil policies (no variadic args) ⇒ Config.Authz stays nil ⇒ no gate.
	ts := newCedarAuthzTestServer(t, backendURL)

	client := NewMCPTestClient(t, ts.URL)
	client.InitializeSession()

	resp := client.CallTool("unknown-tool", map[string]any{"input": "x"})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"without Authz the gate is absent; the SDK handles unknown tools at 200, body: %s", string(body))
	rpcErr := parseRPCError(t, body)
	require.True(t, rpcErr.present, "body: %s", string(body))
	assert.Equal(t, -32602, rpcErr.code, "the SDK's INVALID_PARAMS code must surface unchanged")
}

// TestIntegration_RealBackend_CodeMode_Authz is the regression guard for the codemode
// carve-out: with both code mode and Authz enabled, the execute_tool_script meta-tool is
// admitted by the gate (200) — it is not in the admission seam and would otherwise be
// denied under a permit-list policy — while a directly-called denied tool still 403s.
func TestIntegration_RealBackend_CodeMode_Authz(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	// Permit "echo" (so a script can call it) but nothing grants the execute_tool_script
	// meta-tool itself — the gate must admit it via the codemode carve-out, not policy.
	ts := newCedarAuthzCodeModeServer(t, backendURL,
		`permit(principal, action == Action::"call_tool", resource == Tool::"echo");`)

	client := NewMCPTestClient(t, ts.URL)
	client.InitializeSession()
	waitForEchoTool(t, ts.URL, client.SessionID())

	// execute_tool_script is admitted by the gate and runs the script (which calls the
	// permitted "echo"), returning 200 — NOT a 403.
	scriptResp := client.CallTool("execute_tool_script", map[string]any{
		"script": `return echo(input="hi from script")`,
	})
	defer scriptResp.Body.Close()
	scriptBody, err := io.ReadAll(scriptResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, scriptResp.StatusCode,
		"execute_tool_script must be admitted by the gate's codemode carve-out, body: %s", string(scriptBody))
	assert.NotEqual(t, mcpparser.JSONRPCCodeDenied, parseRPCError(t, scriptBody).code,
		"the code-mode meta-tool must never be 403'd")
	// Prove the script actually RAN (not a 200 + IsError): the echo backend returns its
	// input verbatim, so the script's return value must carry "hi from script".
	assert.Contains(t, string(scriptBody), "hi from script",
		"the admitted script must execute and return the permitted tool's output")

	// A directly-called denied tool is still gated to 403.
	denyResp := client.CallTool("unrelated", map[string]any{"input": "x"})
	defer denyResp.Body.Close()
	denyBody, err := io.ReadAll(denyResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, denyResp.StatusCode, "body: %s", string(denyBody))
	assert.Equal(t, mcpparser.JSONRPCCodeDenied, parseRPCError(t, denyBody).code)
}

// TestIntegration_CedarAuthzDenialIsAudited proves that on the vMCP Serve path a
// policy-denied tools/call still produces an audit event with outcome "denied".
// The pre-dispatch authorization gate runs inside the audit middleware, so the
// 403 it writes must be captured as the event outcome. This is the vMCP
// counterpart of the proxyrunner-path guard in pkg/runner
// (TestAuthzDecisionIsAudited).
func TestIntegration_CedarAuthzDenialIsAudited(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	auditLogPath := filepath.Join(t.TempDir(), "audit.log")
	// Permit only an unrelated tool: "echo" is default-denied.
	ts := buildCedarAuthzServer(t, backendURL, nil,
		&audit.Config{Component: "vmcp-server", LogFile: auditLogPath},
		`permit(principal, action == Action::"call_tool", resource == Tool::"unrelated");`)

	client := NewMCPTestClient(t, ts.URL)
	client.InitializeSession()

	resp := client.CallTool("echo", map[string]any{"input": "hello"})
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "body: %s", string(body))

	// The audit event is written after the response is flushed, so poll briefly.
	require.Eventually(t, func() bool {
		return findToolCallAuditEvent(t, auditLogPath) != nil
	}, 5*time.Second, 50*time.Millisecond, "a tools/call audit event must be emitted for the denied call")

	event := findToolCallAuditEvent(t, auditLogPath)
	assert.Equal(t, "denied", event["outcome"],
		"a policy-denied tools/call must be audited with outcome denied")
}

// TestIntegration_CedarAuthzDenial_ModernPath_IsAudited is the Modern (2026-07-28)
// counterpart of TestIntegration_CedarAuthzDenialIsAudited. The Modern stateless
// dispatcher bypasses the SDK server (and its CallGate), so it re-homes the
// pre-dispatch authorization gate itself; this proves that gate end-to-end through
// the fully assembled server rather than only in the dispatchModern unit tests. A
// policy-denied Modern tools/call must surface as HTTP 403 + JSON-RPC 403 AND be
// audited with outcome "denied" (audit -> parsing -> classification ->
// dispatchModern), confirming the audit middleware wraps the re-homed gate exactly
// as it wraps the SDK CallGate on the Legacy path.
func TestIntegration_CedarAuthzDenial_ModernPath_IsAudited(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	auditLogPath := filepath.Join(t.TempDir(), "audit.log")
	// Permit only an unrelated tool: "echo" is default-denied, so the re-homed gate
	// in dispatchModern rejects the call before it reaches the backend.
	ts := buildCedarAuthzServer(t, backendURL, nil,
		&audit.Config{Component: "vmcp-server", LogFile: auditLogPath},
		`permit(principal, action == Action::"call_tool", resource == Tool::"unrelated");`)

	resp, decoded := postModern(t, ts.URL, "tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"input": "hello modern"},
	}, 1, "echo")
	defer resp.Body.Close()

	require.Equal(t, http.StatusForbidden, resp.StatusCode, "decoded: %+v", decoded)
	errObj, ok := decoded["error"].(map[string]any)
	require.True(t, ok, "the 403 must carry a JSON-RPC error envelope: %+v", decoded)
	assert.EqualValues(t, mcpparser.JSONRPCCodeDenied, errObj["code"],
		"a policy-denied Modern tools/call must surface JSON-RPC 403")

	// The audit event is written after the response is flushed, so poll briefly.
	require.Eventually(t, func() bool {
		return findToolCallAuditEvent(t, auditLogPath) != nil
	}, 5*time.Second, 50*time.Millisecond,
		"a tools/call audit event must be emitted for the denied Modern call")

	event := findToolCallAuditEvent(t, auditLogPath)
	assert.Equal(t, "denied", event["outcome"],
		"a policy-denied Modern tools/call must be audited with outcome denied")
}

// waitForClientTool polls c.ListTools until toolName appears in the advertised
// set, or the deadline elapses. The poll condition is best-effort: any
// transport/read error returns false so the poll simply retries, and it never
// calls require/FailNow inside the closure (testify runs the condition on its
// own goroutine, where FailNow would Goexit off the test goroutine, hang the
// poll to its ceiling, and misreport the failure).
func waitForClientTool(t *testing.T, c *MCPTestClient, toolName string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp := c.ListTools()
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false
		}
		return resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"`+toolName+`"`)
	}, 5*time.Second, 50*time.Millisecond,
		"client's tools/list must advertise "+toolName)
}

// listToolsBody returns the client's current tools/list response body as a
// string. Unlike waitForClientTool's poll closure, this runs on the test
// goroutine, so requiring a clean read/200 here is safe.
func listToolsBody(t *testing.T, c *MCPTestClient) string {
	t.Helper()
	resp := c.ListTools()
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "tools/list must succeed")
	return string(body)
}

// TestRegression_PerSessionToolProjection_DivergesByPrincipal pins the fixed
// per-principal Cedar admission behind the mcp-go -> go-sdk migration
// (toolhive-core v0.0.32, #5742): two clients authenticated as DIFFERENT
// principals, against the SAME running server instance, must get divergent
// tools/list projections when a Cedar policy permits a tool for one principal
// only. Before the fix, the server's identity middleware stamped every request
// with a single fixed principal, so per-session admission could not diverge by
// caller — this test would have failed (or passed vacuously) against that
// regression.
func TestRegression_PerSessionToolProjection_DivergesByPrincipal(t *testing.T) {
	t.Parallel()

	backendURL := startEchoPingBackend(t)
	// The Cedar principal for a JWT-claims-derived caller is Client::"<sub>"
	// (cedar authorizeToolCall). Give each principal a DISTINCT positively-
	// permitted tool -- alice may call echo, bob may call ping -- so the two
	// sessions' tools/list projections (gated by these call_tool permissions)
	// must diverge in BOTH directions on the same running server.
	ts := newCedarAuthzTestServer(t, backendURL,
		`permit(principal == Client::"alice", action == Action::"call_tool", resource == Tool::"echo");`,
		`permit(principal == Client::"bob", action == Action::"call_tool", resource == Tool::"ping");`)

	alice := NewMCPTestClient(t, ts.URL).WithPrincipal("alice")
	alice.InitializeSession()
	bob := NewMCPTestClient(t, ts.URL).WithPrincipal("bob")
	bob.InitializeSession()

	// Wait until EACH session has registered its OWN permitted tool. This is
	// the anti-vacuity guard: it proves both sessions finished their
	// (asynchronous) registration and each principal resolved to its own
	// identity. Without it, an empty tools/list (registration still pending)
	// would be indistinguishable from a correctly-filtered one, so a real
	// cross-principal projection leak could pass unnoticed.
	waitForClientTool(t, alice, "echo")
	waitForClientTool(t, bob, "ping")

	// Divergence in both directions: alice sees echo but NOT bob's ping; bob
	// sees ping but NOT alice's echo. A regression that collapsed all sessions
	// onto one fixed principal, or shared a cached projection across principals,
	// would leak the other principal's tool here.
	aliceTools := listToolsBody(t, alice)
	assert.Contains(t, aliceTools, `"echo"`, "alice must see her permitted tool echo")
	assert.NotContains(t, aliceTools, `"ping"`,
		"alice must NOT see bob's tool ping on the SAME server instance")

	bobTools := listToolsBody(t, bob)
	assert.Contains(t, bobTools, `"ping"`, "bob must see his permitted tool ping")
	assert.NotContains(t, bobTools, `"echo"`,
		"bob must NOT see alice's tool echo on the SAME server instance")
}

// TestRegression_PerSessionToolCall_DeniedForUnprivilegedPrincipal is the
// call-path counterpart of TestRegression_PerSessionToolProjection_DivergesByPrincipal:
// on the SAME running server, a tools/call for a tool permitted to one
// principal (alice) must succeed, while the identical call from a different
// principal (bob) on their own concurrently-registered session must be denied
// by the pre-dispatch authorization gate (403), not silently allowed through a
// shared/fixed identity.
func TestRegression_PerSessionToolCall_DeniedForUnprivilegedPrincipal(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newCedarAuthzTestServer(t, backendURL,
		`permit(principal == Client::"alice", action == Action::"call_tool", resource == Tool::"echo");`)

	alice := NewMCPTestClient(t, ts.URL).WithPrincipal("alice")
	alice.InitializeSession()
	bob := NewMCPTestClient(t, ts.URL).WithPrincipal("bob")
	bob.InitializeSession()

	// Wait for alice's session to register echo before calling it.
	waitForClientTool(t, alice, "echo")

	aliceResp := alice.CallTool("echo", map[string]any{"input": "hello"})
	defer aliceResp.Body.Close()
	aliceBody, err := io.ReadAll(aliceResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, aliceResp.StatusCode,
		"alice is permitted to call echo; body: %s", string(aliceBody))
	assert.Contains(t, string(aliceBody), "hello", "alice's permitted call must reach the real backend")

	bobResp := bob.CallTool("echo", map[string]any{"input": "hello"})
	defer bobResp.Body.Close()
	bobBody, err := io.ReadAll(bobResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, bobResp.StatusCode,
		"bob is not permitted to call echo on the SAME running server; body: %s", string(bobBody))
	rpcErr := parseRPCError(t, bobBody)
	require.True(t, rpcErr.present, "the 403 must carry a JSON-RPC error envelope, body: %s", string(bobBody))
	assert.Equal(t, mcpparser.JSONRPCCodeDenied, rpcErr.code,
		"bob's denied call must surface the genericized authorization JSON-RPC error code")
}

// findToolCallAuditEvent reads the newline-delimited JSON audit log at path and
// returns the first "mcp_tool_call" event, or nil if none is present yet.
func findToolCallAuditEvent(t *testing.T, path string) map[string]any {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event["type"] == "mcp_tool_call" {
			return event
		}
	}
	return nil
}
