// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp"
)

// modernBodyWithVersion builds a single Modern JSON-RPC request body carrying the
// reserved io.modelcontextprotocol/* _meta keys ClassifyRevision inspects.
func modernBodyWithVersion(id int, method, version string) string {
	return fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%d,"method":%q,"params":{"_meta":{`+
			`"io.modelcontextprotocol/protocolVersion":%q,`+
			`"io.modelcontextprotocol/clientCapabilities":{}}}}`,
		id, method, version)
}

// modernBody builds a well-formed Modern request for the version this build supports.
func modernBody(id int, method string) string {
	return modernBodyWithVersion(id, method, "2026-07-28")
}

// TestModernConcurrentRequestsAreNotMixed is the Modern analogue of
// TestSessionlessConcurrentRequestsAreNotMixed, and the critical confidentiality
// guard for the stateless routing path: two concurrent Modern POSTs sharing
// JSON-RPC id 1 must each receive their own payload. One request additionally
// carries a foreign Mcp-Session-Id header — it must NOT fall through to the
// shared-key session path (which would collapse both requests onto
// compositeKey(sessID, idKey) and leak one client's response to the other).
//
// t.Setenv requires a non-parallel test; the short proxy timeout makes a
// regression fail fast rather than hanging on the 60s default.
func TestModernConcurrentRequestsAreNotMixed(t *testing.T) {
	t.Setenv(proxyRequestTimeoutEnv, "3s")

	port := pickFreePort(t)
	proxy := NewHTTPProxy("127.0.0.1", port, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() { _ = proxy.Stop(ctx) })

	time.Sleep(50 * time.Millisecond)

	// Barrier backend: buffer both requests before responding, so both proxy
	// waiters are registered first — the precondition under which a shared
	// routing token would overwrite one waiter. Echo req.Method to prove each
	// client got its own payload.
	go func() {
		buffered := make([]*jsonrpc2.Request, 0, 2)
		for len(buffered) < 2 {
			select {
			case msg := <-proxy.GetMessageChannel():
				if req, ok := msg.(*jsonrpc2.Request); ok && req.ID.IsValid() {
					buffered = append(buffered, req)
				}
			case <-ctx.Done():
				return
			}
		}
		for _, req := range buffered {
			result := map[string]any{"echoed_method": req.Method}
			resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
			_ = proxy.ForwardResponseToClients(ctx, resp)
		}
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint)

	type result struct {
		method string
		status int
		body   map[string]any
		err    error
	}

	// Both POSTs are Modern and share JSON-RPC id 1. sessionID is a foreign
	// Mcp-Session-Id header the Modern path must ignore; empty means no header.
	fire := func(method, sessionID string) result {
		req, err := http.NewRequest(
			http.MethodPost, url, bytes.NewReader([]byte(modernBody(1, method))))
		if err != nil {
			return result{method: method, err: err}
		}
		req.Header.Set("Content-Type", "application/json")
		if sessionID != "" {
			req.Header.Set("Mcp-Session-Id", sessionID)
		}
		client := &http.Client{Timeout: 8 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return result{method: method, err: err}
		}
		defer func() {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
		var decoded map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&decoded)
		return result{method: method, status: resp.StatusCode, body: decoded}
	}

	resCh := make(chan result, 2)
	go func() { resCh <- fire("tools/list", "foreign-session-id") }()
	go func() { resCh <- fire("resources/list", "") }()

	received := map[string]result{}
	for i := 0; i < 2; i++ {
		select {
		case r := <-resCh:
			received[r.method] = r
		case <-time.After(15 * time.Second):
			t.Fatalf("timeout waiting for concurrent Modern responses; received so far: %v", received)
		}
	}

	for _, method := range []string{"tools/list", "resources/list"} {
		r := received[method]
		require.NoError(t, r.err, "client %q HTTP error", method)
		require.Equal(t, http.StatusOK, r.status, "client %q HTTP status", method)
		require.NotNil(t, r.body, "client %q empty body", method)
		res, ok := r.body["result"].(map[string]any)
		require.True(t, ok, "client %q missing result: %v", method, r.body)
		assert.Equal(t, method, res["echoed_method"],
			"client %q received the other client's payload (response cross-talk)", method)
	}
}

// TestModernRequestIgnoresClientSessionID verifies that a Modern POST carrying a
// bogus Mcp-Session-Id is served statelessly: it returns 200 (a Legacy request
// with an unknown session id returns 404, so status discriminates the paths),
// sets no Mcp-Session-Id response header, and never registers the foreign id
// with the session manager.
func TestModernRequestIgnoresClientSessionID(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = proxy.Stop(ctx) })

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint)

	const bogusSession = "bogus-session-id"
	req, err := http.NewRequest(
		http.MethodPost, url, bytes.NewReader([]byte(modernBody(1, "tools/list"))))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", bogusSession)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, resp.Header.Get("Mcp-Session-Id"),
		"Modern response must not set a session header")

	_, ok := proxy.sessionManager.Get(bogusSession)
	assert.False(t, ok, "Modern path must not register the foreign session id")
}

// TestModernNeverReusesClientSessionIDAsRoutingToken is the mutation-check
// analogue of the transparent proxy's TestGuardUnknownSessionFiresDespiteForgedModernRevision.
// Where that test proves an UNKNOWN session ID still triggers the Legacy
// guard, this one closes the complementary gap: it registers a REAL, known
// session in sessionManager and proves resolveSessionForRequest still mints
// a fresh routing token instead of reusing it.
//
// TestModernRequestIgnoresClientSessionID (above) only exercises a bogus,
// unregistered session ID, so it cannot detect a regression that consults
// sessionManager as a fallback before minting: on a lookup miss such code
// would still mint a fresh token and pass that test. Using a real registered
// session here closes that hole -- this test FAILS if the Modern branch is
// ever re-keyed on Mcp-Session-Id presence (i.e. reuses a known client SID as
// the routing token, or calls sessionManager.Get/AddWithID for it at all).
func TestModernNeverReusesClientSessionIDAsRoutingToken(t *testing.T) {
	t.Parallel()

	proxy := NewHTTPProxy("127.0.0.1", 0, nil, nil)
	t.Cleanup(func() { _ = proxy.Stop(context.Background()) })

	knownSessionID := uuid.New().String()
	require.NoError(t, proxy.sessionManager.AddWithID(knownSessionID))

	newModernRequest := func() *jsonrpc2.Request {
		req, err := jsonrpc2.NewCall(jsonrpc2.Int64ID(1), "tools/call", json.RawMessage(
			`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",`+
				`"io.modelcontextprotocol/clientCapabilities":{}}}`))
		require.NoError(t, err)
		return req
	}

	resolve := func() string {
		httpReq := httptest.NewRequest(http.MethodPost, StreamableHTTPEndpoint, http.NoBody)
		httpReq.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)
		httpReq.Header.Set("Mcp-Session-Id", knownSessionID)

		token, setHeader, err := proxy.resolveSessionForRequest(httptest.NewRecorder(), httpReq, newModernRequest())
		require.NoError(t, err)
		assert.False(t, setHeader, "Modern branch must never ask the client to adopt a session")
		return token
	}

	token1 := resolve()
	token2 := resolve()

	assert.NotEqual(t, knownSessionID, token1, "Modern branch must not reuse the known client session id as its routing token")
	assert.NotEqual(t, knownSessionID, token2, "Modern branch must not reuse the known client session id as its routing token")
	assert.NotEqual(t, token1, token2, "each Modern request must mint its own fresh routing token")

	_, err := uuid.Parse(token1)
	assert.NoError(t, err, "routing token must be a freshly minted UUID, not a passthrough of client input")
	_, err = uuid.Parse(token2)
	assert.NoError(t, err, "routing token must be a freshly minted UUID, not a passthrough of client input")
}

// TestModernClassificationErrorsReturn400 verifies that ClassifyRevision errors
// on the single-request path are rendered as HTTP 400 JSON-RPC error responses
// with the spec-defined error code.
func TestModernClassificationErrorsReturn400(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = proxy.Stop(ctx) })

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint)

	tests := []struct {
		name        string
		body        string
		protoHeader string
		wantCode    int64
	}{
		{
			name:        "header/body version mismatch",
			body:        modernBodyWithVersion(1, "tools/list", "2026-07-28"),
			protoHeader: "2025-11-25",
			wantCode:    mcp.CodeHeaderMismatch,
		},
		{
			name:     "unsupported body version",
			body:     modernBodyWithVersion(1, "tools/list", "1999-01-01"),
			wantCode: mcp.CodeUnsupportedProtocolVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(tt.body)))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			if tt.protoHeader != "" {
				req.Header.Set("MCP-Protocol-Version", tt.protoHeader)
			}

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)

			var decoded struct {
				Error struct {
					Code int64 `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))
			assert.Equal(t, tt.wantCode, decoded.Error.Code)
		})
	}
}
