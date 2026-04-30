// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// startProxyWithBackend starts an HTTP proxy on the given port and a simple backend goroutine
// that responds to JSON-RPC requests by echoing a minimal success result.
func startProxyWithBackend(t *testing.T, port int) (*HTTPProxy, context.Context, context.CancelFunc) {
	t.Helper()

	proxy := NewHTTPProxy("127.0.0.1", port, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())

	require.NoError(t, proxy.Start(ctx), "proxy start")
	t.Cleanup(func() { _ = proxy.Stop(ctx) })

	// Give the server a moment to start listening
	time.Sleep(50 * time.Millisecond)

	// Backend responder
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				// Only respond to requests with IDs
				if req, ok := msg.(*jsonrpc2.Request); ok && req.ID.IsValid() {
					result := map[string]any{"ok": true}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return proxy, ctx, cancel
}

// TestGETReturns405 validates that GET on MCP endpoint returns 405 (server does not offer SSE here).
func TestGETReturns405(t *testing.T) {
	t.Parallel()

	const port = 8101
	proxy := NewHTTPProxy("127.0.0.1", port, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, proxy.Start(ctx), "proxy start")
	defer func() { _ = proxy.Stop(ctx) }()

	time.Sleep(50 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:8101"+StreamableHTTPEndpoint, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

// TestDeleteTerminatesSession validates DELETE ends session with 204 and subsequent use returns 404.
func TestDeleteTerminatesSession(t *testing.T) {
	t.Parallel()

	const port = 8102
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	url := "http://127.0.0.1:8102" + StreamableHTTPEndpoint

	// Initialize without session header - server should assign one in response header
	initJSON := `{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"spec-test","version":"0.0.0"},"capabilities":{}}}`
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte(initJSON)))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	sessID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessID, "server should set Mcp-Session-Id header on initialize response")

	// DELETE the session
	delReq, _ := http.NewRequest(http.MethodDelete, url, nil)
	delReq.Header.Set("Mcp-Session-Id", sessID)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	defer delResp.Body.Close()
	assert.Equal(t, http.StatusNoContent, delResp.StatusCode)

	// Use the same session id again for a regular request - expect 404
	reqJSON := `{"jsonrpc":"2.0","id":"2","method":"tools/list","params":{}}`
	postReq, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(reqJSON)))
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("Mcp-Session-Id", sessID)
	postResp, err := http.DefaultClient.Do(postReq)
	require.NoError(t, err)
	defer postResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, postResp.StatusCode)
	assert.Equal(t, "application/json", postResp.Header.Get("Content-Type"))
	postBody, err := io.ReadAll(postResp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(postBody), `"code":-32001`)
	assert.Contains(t, string(postBody), `"id":"2"`)
}

// TestInitializeSetsSessionHeader ensures server assigns Mcp-Session-Id on initialize when client omits it.
func TestInitializeSetsSessionHeader(t *testing.T) {
	t.Parallel()

	const port = 8103
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	url := "http://127.0.0.1:8103" + StreamableHTTPEndpoint

	initJSON := `{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"spec-test","version":"0.0.0"},"capabilities":{}}}`
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte(initJSON)))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	sessID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessID, "server should set Mcp-Session-Id header")
}

// TestPOSTNotificationOnlyAccepted checks single notification POST returns 202 with no body.
func TestPOSTNotificationOnlyAccepted(t *testing.T) {
	t.Parallel()

	const port = 8104
	// No backend needed for notification-only submission, but starting is fine.
	proxy := NewHTTPProxy("127.0.0.1", port, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, proxy.Start(ctx), "proxy start")
	defer func() { _ = proxy.Stop(ctx) }()

	time.Sleep(50 * time.Millisecond)

	url := "http://127.0.0.1:8104" + StreamableHTTPEndpoint
	// Notification (no id)
	notif := `{"jsonrpc":"2.0","method":"progress","params":{"pct":50}}`

	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(notif)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 0, len(body), "202 should have no body")
}

// TestBatchOnlyNotificationsAccepted returns 202 and no body per spec when no requests are present.
func TestBatchOnlyNotificationsAccepted(t *testing.T) {
	t.Parallel()

	const port = 8105
	proxy := NewHTTPProxy("127.0.0.1", port, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, proxy.Start(ctx), "proxy start")
	defer func() { _ = proxy.Stop(ctx) }()

	time.Sleep(50 * time.Millisecond)

	url := "http://127.0.0.1:8105" + StreamableHTTPEndpoint

	// Batch of only notifications (no ids)
	batch := `[{"jsonrpc":"2.0","method":"progress","params":{"pct":10}},{"jsonrpc":"2.0","method":"progress","params":{"pct":20}}]`
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(batch)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 0, len(body))
}

// TestBatchMixedNotificationsAndRequest returns an array with one response corresponding to the request id.
func TestBatchMixedNotificationsAndRequest(t *testing.T) {
	t.Parallel()

	const port = 8106
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	url := "http://127.0.0.1:8106" + StreamableHTTPEndpoint

	// Batch includes a notification and a request "r1"
	batch := `[{"jsonrpc":"2.0","method":"progress","params":{"pct":99}},{"jsonrpc":"2.0","id":"r1","method":"tools/list","params":{}}]`
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(batch)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var arr []map[string]any
	err = json.NewDecoder(resp.Body).Decode(&arr)
	require.NoError(t, err)
	require.Len(t, arr, 1)

	// Response should correspond to id "r1"
	assert.Equal(t, "2.0", arr[0]["jsonrpc"])
	assert.Equal(t, "r1", arr[0]["id"])
	// And include a result map as sent by backend
	_, hasResult := arr[0]["result"]
	assert.True(t, hasResult, "batch response should include result")
}

// TestDeleteUnknownSessionReturnsJSONRPCError verifies that DELETE with an
// unknown session ID returns HTTP 404 with a JSON-RPC error body.
func TestDeleteUnknownSessionReturnsJSONRPCError(t *testing.T) {
	t.Parallel()

	const port = 8107
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	url := "http://127.0.0.1:8107" + StreamableHTTPEndpoint

	delReq, _ := http.NewRequest(http.MethodDelete, url, nil)
	delReq.Header.Set("Mcp-Session-Id", "bogus-session-id")
	resp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"code":-32001`)
	assert.Contains(t, string(body), `"id":null`)
}

// TestBatchWithStaleSessionReturnsJSONRPCError verifies that a batch POST
// with a stale session ID returns HTTP 404 with a JSON-RPC error body.
func TestBatchWithStaleSessionReturnsJSONRPCError(t *testing.T) {
	t.Parallel()

	const port = 8108
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	url := "http://127.0.0.1:8108" + StreamableHTTPEndpoint

	batch := `[{"jsonrpc":"2.0","id":"b1","method":"tools/list","params":{}},{"jsonrpc":"2.0","id":"b2","method":"tools/list","params":{}}]`
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(batch)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "expired-session-id")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"code":-32001`)
}

// TestSingleRequestWithStaleSessionIncludesRequestID verifies that a single
// POST with a stale session ID returns a JSON-RPC error that includes the
// request's original ID.
func TestSingleRequestWithStaleSessionIncludesRequestID(t *testing.T) {
	t.Parallel()

	const port = 8109
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	defer cancel()
	defer func() { _ = proxy.Stop(ctx) }()

	url := "http://127.0.0.1:8109" + StreamableHTTPEndpoint

	reqJSON := `{"jsonrpc":"2.0","id":"test-42","method":"tools/list","params":{}}`
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(reqJSON)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "expired-session-id")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"code":-32001`)
	assert.Contains(t, string(body), `"id":"test-42"`)
}

// pickFreePort returns a TCP port the OS reports as available. There is a small
// race window before the proxy binds it, but that is the same pattern other
// streamable tests follow and is acceptable here.
func pickFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}

// TestSessionlessConcurrentRequestsAreNotMixed verifies that two concurrent
// sessionless POSTs sharing a JSON-RPC id each receive their own response
// payload. Regression test: a previous change collapsed every sessionless
// request onto compositeKey("", idKey), causing the in-process waiters /
// idRestore sync.Maps to silently overwrite — symptoms were response
// cross-talk (one client receiving the other's payload, with the JSON-RPC id
// rewritten back to its own) and a request-timeout for the losing client.
//
// t.Setenv requires a non-parallel test; the trade-off is acceptable for a
// single regression test that needs a short proxy timeout to fail fast.
func TestSessionlessConcurrentRequestsAreNotMixed(t *testing.T) {
	// Cap per-request timeout so this test fails in seconds, not the 60s
	// default, when the bug regresses.
	t.Setenv(proxyRequestTimeoutEnv, "3s")

	port := pickFreePort(t)
	proxy := NewHTTPProxy("127.0.0.1", port, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() { _ = proxy.Stop(ctx) })

	time.Sleep(50 * time.Millisecond)

	// Backend uses a synchronization barrier: collect both incoming requests
	// before sending any response, so both proxy waiters are registered first
	// — that's the precondition under which the bug overwrites one waiter.
	// Echo req.Method back in the result so we can prove each client got its
	// own payload, not the other's.
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

	// Both POSTs share the same JSON-RPC id (1) and omit Mcp-Session-Id.
	fire := func(method string) result {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":{}}`, method)
		client := &http.Client{Timeout: 8 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader([]byte(body)))
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
	go func() { resCh <- fire("tools/list") }()
	go func() { resCh <- fire("resources/list") }()

	received := map[string]result{}
	for i := 0; i < 2; i++ {
		select {
		case r := <-resCh:
			received[r.method] = r
		case <-time.After(15 * time.Second):
			t.Fatalf("timeout waiting for concurrent sessionless responses; received so far: %v", received)
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
