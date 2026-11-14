// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

// dnsRebindingForeignHost is the Host a DNS-rebinding page sends: the
// attacker's own name, which it has re-pointed at 127.0.0.1.
const dnsRebindingForeignHost = "attacker.example"

// TestRegression_DNSRebinding_LoopbackListenerRejectsForeignHost pins vMCP's
// DNS-rebinding protection: on a loopback listener, a request whose Host header
// names a non-loopback value is rejected with 403 before it reaches any MCP
// dispatcher.
//
// The rule must hold identically for both client-facing MCP revisions. Legacy
// (2025-11-25) requests reach go-sdk's StreamableHTTPHandler, which has its own
// localhost protection (cross-replica sessions rehydrated by mcpcompat's
// serveRehydrated bypass that handler), but Modern (2026-07-28) requests are
// served by dispatchModern and never reach go-sdk, so the check has to sit in
// front of the whole MCP route. The Modern rows (tools/call, tools/list, ...) are the
// ones that regress if that front-door check is removed; the Legacy rows also
// regress if a future change threads WithDisableLocalhostProtection(true) into
// the Serve path's streamableOpts (server.go Handler) and drops the front door.
//
// Only the MCP route is guarded. /health, /ping, /readyz, /status,
// /api/backends/health, /metrics, /.well-known/ and the embedded auth server
// routes are deliberately left unguarded: they are unauthenticated by design,
// expose only operational metadata, and must keep answering local probers and
// Host-preserving proxies whatever Host they send.
//
// Every 403 row also asserts the "invalid Host header" body, which shows the 403
// came from a Host check and not from something else (e.g. the authorization
// gate). For Legacy rows go-sdk writes the same body, so the body alone does not
// tell the guard from go-sdk's own check.
//
// Positive controls (loopback Host values go-sdk accepts) must be served, so
// the negative rows cannot pass vacuously (e.g. if the whole POST path were
// broken).
func TestRegression_DNSRebinding_LoopbackListenerRejectsForeignHost(t *testing.T) {
	t.Parallel()

	legacyInit := mustJSON(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "dns-rebinding-test", "version": "1.0"},
		},
	})

	tests := []struct {
		name       string
		method     string
		path       string
		host       string // empty means "leave the client-assigned default (loopback) Host"
		body       []byte
		headers    map[string]string
		wantStatus int
		// wantCoreCalled, when non-nil, asserts whether a tools/call reached the core.
		wantCoreCalled *bool
	}{
		// Legacy (2025-11-25): served by go-sdk behind the front door.
		{
			name: "legacy initialize with foreign Host is rejected", method: http.MethodPost, path: "/mcp",
			host: dnsRebindingForeignHost, body: legacyInit, headers: legacyHeaders(), wantStatus: http.StatusForbidden,
		},
		{
			name: "legacy initialize with loopback Host is accepted", method: http.MethodPost, path: "/mcp",
			body: legacyInit, headers: legacyHeaders(), wantStatus: http.StatusOK,
		},
		{
			name: "legacy GET stream with foreign Host is rejected", method: http.MethodGet, path: "/mcp",
			host: dnsRebindingForeignHost, headers: map[string]string{"Accept": "text/event-stream"},
			wantStatus: http.StatusForbidden,
		},
		// A made-up session ID makes this row pass even without the front door
		// (go-sdk answers the same 403); see
		// TestRegression_DNSRebinding_ForeignHostDeleteKeepsSession.
		{
			name: "legacy DELETE with foreign Host is rejected", method: http.MethodDelete, path: "/mcp",
			host: dnsRebindingForeignHost, headers: map[string]string{"Mcp-Session-Id": "some-session"},
			wantStatus: http.StatusForbidden,
		},

		// Modern (2026-07-28): served by dispatchModern, never by go-sdk.
		{
			name: "modern tools/call with foreign Host is rejected and never dispatched", method: http.MethodPost,
			path: "/mcp", host: dnsRebindingForeignHost, body: modernBody(t, "tools/call", map[string]any{"name": "echo"}),
			headers: modernHeaders("tools/call", "echo"), wantStatus: http.StatusForbidden, wantCoreCalled: new(false),
		},
		{
			name: "modern tools/list with foreign Host is rejected", method: http.MethodPost, path: "/mcp",
			host: dnsRebindingForeignHost, body: modernBody(t, "tools/list", nil),
			headers: modernHeaders("tools/list", ""), wantStatus: http.StatusForbidden,
		},
		{
			name: "modern resources/read with foreign Host is rejected", method: http.MethodPost, path: "/mcp",
			host: dnsRebindingForeignHost, body: modernBody(t, "resources/read", map[string]any{"uri": "file:///x"}),
			headers: modernHeaders("resources/read", "file:///x"), wantStatus: http.StatusForbidden,
		},
		{
			name: "modern prompts/get with foreign Host is rejected", method: http.MethodPost, path: "/mcp",
			host: dnsRebindingForeignHost, body: modernBody(t, "prompts/get", map[string]any{"name": "p"}),
			headers: modernHeaders("prompts/get", "p"), wantStatus: http.StatusForbidden,
		},
		{
			name: "modern server/discover with foreign Host is rejected", method: http.MethodPost, path: "/mcp",
			host: dnsRebindingForeignHost, body: modernBody(t, "server/discover", nil),
			headers: modernHeaders("server/discover", ""), wantStatus: http.StatusForbidden,
		},
		{
			name: "modern tools/call with loopback Host is dispatched", method: http.MethodPost, path: "/mcp",
			body: modernBody(t, "tools/call", map[string]any{"name": "echo"}), headers: modernHeaders("tools/call", "echo"),
			wantStatus: http.StatusOK, wantCoreCalled: new(true),
		},
		{
			name: "modern tools/call with localhost Host is dispatched", method: http.MethodPost, path: "/mcp",
			host: "localhost", body: modernBody(t, "tools/call", map[string]any{"name": "echo"}),
			headers: modernHeaders("tools/call", "echo"), wantStatus: http.StatusOK, wantCoreCalled: new(true),
		},
		{
			name: "modern tools/call with IPv6 loopback Host is dispatched", method: http.MethodPost, path: "/mcp",
			host: "::1", body: modernBody(t, "tools/call", map[string]any{"name": "echo"}),
			headers: modernHeaders("tools/call", "echo"), wantStatus: http.StatusOK, wantCoreCalled: new(true),
		},

		// The "/" catch-all serves the MCP handler, so the guard covers any path.
		{
			name: "modern tools/call on a non-MCP path with foreign Host is rejected", method: http.MethodPost,
			path: "/other", host: dnsRebindingForeignHost, body: modernBody(t, "tools/call", map[string]any{"name": "echo"}),
			headers: modernHeaders("tools/call", "echo"), wantStatus: http.StatusForbidden, wantCoreCalled: new(false),
		},

		// Non-MCP routes are deliberately unguarded.
		{
			name: "liveness probe with foreign Host is still served", method: http.MethodGet, path: "/health",
			host: dnsRebindingForeignHost, wantStatus: http.StatusOK,
		},
		{
			name: "ping probe with foreign Host is still served", method: http.MethodGet, path: "/ping",
			host: dnsRebindingForeignHost, wantStatus: http.StatusOK,
		},
		{
			name: "readiness probe with foreign Host is still served", method: http.MethodGet, path: "/readyz",
			host: dnsRebindingForeignHost, wantStatus: http.StatusOK,
		},
		{
			name: "status with foreign Host is still served", method: http.MethodGet, path: "/status",
			host: dnsRebindingForeignHost, wantStatus: http.StatusOK,
		},
		{
			name: "backend health with foreign Host is still served", method: http.MethodGet, path: "/api/backends/health",
			host: dnsRebindingForeignHost, wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fc := &fakeCore{tools: []vmcp.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}}
			ts := newDNSRebindingTestServer(t, fc)

			req, err := http.NewRequestWithContext(context.Background(), tt.method, ts.URL+tt.path, bytes.NewReader(tt.body))
			require.NoError(t, err)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			if tt.host != "" {
				// Set the Host FIELD (not a header): net/http writes it as the
				// Host header on the wire, and the server-side checks inspect
				// http.Request.Host. Keep the listener's port so only the
				// hostname differs from the positive control.
				_, port, err := net.SplitHostPort(ts.Listener.Addr().String())
				require.NoError(t, err)
				req.Host = net.JoinHostPort(tt.host, port)
			}

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Equal(t, tt.wantStatus, resp.StatusCode,
				"%s %s with Host=%q must yield status %d; body: %s", tt.method, tt.path, req.Host, tt.wantStatus, body)
			if tt.wantStatus == http.StatusForbidden {
				assert.Contains(t, string(body), "invalid Host header",
					"the 403 body must be the Host-check body, not another 403 (e.g. the authorization gate's)")
			}
			if tt.wantCoreCalled != nil {
				assert.Equal(t, *tt.wantCoreCalled, fc.callToolCalls.Load() > 0,
					"tools/call reaching the core (Host=%q)", req.Host)
			}
		})
	}
}

// TestRegression_DNSRebinding_ForeignHostDeleteKeepsSession pins that a Legacy
// DELETE with a foreign Host and a live session ID does not end the session.
// mcpcompat's StreamableHTTPServer forgets the session before delegating to
// go-sdk (whose own Host check then answers 403) and terminates it in the
// shared session manager afterwards whatever the status; that termination is
// what the final tools/list observes. Only the front-door guard keeps the
// session alive: a rebinding page that learned a session ID could otherwise end
// it despite the 403.
func TestRegression_DNSRebinding_ForeignHostDeleteKeepsSession(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}}
	ts := newDNSRebindingTestServer(t, fc)

	initResp := postServeMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "dns-rebinding-test", "version": "1.0"},
		},
	}, "")
	_, _ = io.Copy(io.Discard, initResp.Body)
	_ = initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	listTools := func() (int, string) {
		resp := postServeMCP(t, ts.URL, map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
		}, sessionID)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}

	// Positive control: the session is live before the forged DELETE, so the
	// post-DELETE check cannot pass vacuously.
	status, body := listTools()
	require.Equal(t, http.StatusOK, status, "session must be live before the DELETE; body: %s", body)
	require.Contains(t, body, `"echo"`)

	_, port, err := net.SplitHostPort(ts.Listener.Addr().String())
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, ts.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", sessionID)
	req.Host = net.JoinHostPort(dnsRebindingForeignHost, port)
	delResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer delResp.Body.Close()
	delBody, err := io.ReadAll(delResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, delResp.StatusCode, "body: %s", delBody)
	assert.Contains(t, string(delBody), "invalid Host header")

	status, body = listTools()
	assert.Equal(t, http.StatusOK, status,
		"the session must survive a foreign-Host DELETE that was answered 403; body: %s", body)
	assert.Contains(t, body, `"echo"`)
}

// newDNSRebindingTestServer serves the full vMCP handler (Serve → Handler) on
// an httptest loopback listener.
func newDNSRebindingTestServer(t *testing.T, fc *fakeCore) *httptest.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	factory, _ := newToolSessionFactory(t, ctrl, fc.tools)

	cfg := &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	}

	srv, err := Serve(context.Background(), fc, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func legacyHeaders() map[string]string {
	return map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json, text/event-stream",
	}
}

// modernHeaders returns the headers a Modern (2026-07-28) client sends;
// mcpName is the Mcp-Name value, omitted when empty.
func modernHeaders(method, mcpName string) map[string]string {
	h := map[string]string{
		"Content-Type":         "application/json",
		"Accept":               "application/json, text/event-stream",
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method":           method,
	}
	if mcpName != "" {
		h["Mcp-Name"] = mcpName
	}
	return h
}

// modernBody builds a Modern JSON-RPC request body carrying the reserved
// io.modelcontextprotocol/* _meta keys.
func modernBody(t *testing.T, method string, params map[string]any) []byte {
	t.Helper()
	p := map[string]any{}
	for k, v := range params {
		p[k] = v
	}
	p["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	return mustJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": p})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
