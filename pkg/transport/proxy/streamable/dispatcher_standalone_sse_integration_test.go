// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// establishSession POSTs an "initialize" request to the proxy and returns the
// Mcp-Session-Id the server assigns. startProxyWithBackend's fake backend
// answers any ID-bearing request (including "initialize") with a generic
// success result, which is sufficient here: session assignment happens in
// resolveSessionForRequest based on the request method alone, independent of
// the backend's response payload.
func establishSession(t *testing.T, port int) string {
	t.Helper()

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint)
	initJSON := `{"jsonrpc":"2.0","id":"1","method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":{"name":"sse-test","version":"0.0.0"},"capabilities":{}}}`
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte(initJSON))) //nolint:noctx // test-only
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)

	sessID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessID, "server should set Mcp-Session-Id header on initialize response")
	return sessID
}

// openGETStream issues a GET request to the proxy's MCP endpoint carrying
// sessID as Mcp-Session-Id, and returns the live response along with a
// buffered reader over its body. The caller owns closing resp.Body (register
// with t.Cleanup).
func openGETStream(ctx context.Context, t *testing.T, port int, sessID string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint), nil)
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", sessID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	return resp, bufio.NewReader(resp.Body)
}

// readSSEData reads from r until it finds an SSE "data:" line (skipping
// comment/keep-alive lines), and returns its payload. It fails the test if no
// data line arrives within the timeout, per the "always bound blocking
// receives" testing rule.
func readSSEData(t *testing.T, r *bufio.Reader) string {
	t.Helper()

	type result struct {
		payload string
		err     error
	}
	ch := make(chan result, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				ch <- result{err: err}
				return
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if data, ok := strings.CutPrefix(trimmed, "data:"); ok {
				ch <- result{payload: strings.TrimSpace(data)}
				return
			}
			// Skip blank lines, ":keep-alive" comments, and any other SSE fields.
		}
	}()

	select {
	case res := <-ch:
		require.NoError(t, res.err)
		return res.payload
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an SSE data line")
		return ""
	}
}

// TestStandaloneSSE_NotificationReachesConnectedClient verifies a GLOBAL
// notification (see listChangedNotificationMethods) pushed via
// ForwardResponseToClients is delivered as a data: frame on a connected GET
// SSE stream.
func TestStandaloneSSE_NotificationReachesConnectedClient(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	t.Cleanup(cancel)

	sessID := establishSession(t, port)
	resp, reader := openGETStream(ctx, t, port, sessID)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "stream was not registered")

	notification, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, notification))

	payload := readSSEData(t, reader)
	assert.Contains(t, payload, "notifications/tools/list_changed")
}

// TestStandaloneSSE_NonGlobalNotificationDoesNotReachClient verifies a
// notifications/progress message with no registered progress route (see
// dispatcher.go's routeProgress) pushed via ForwardResponseToClients is NOT
// delivered to a connected GET standalone SSE stream: progress belongs on its
// originating request's own SSE stream, never the standalone one, and an
// unrecognized/unregistered token is dropped rather than guessed at (see
// TestPostSSE_ProgressIsolationBetweenSessions for the positive, routed case).
func TestStandaloneSSE_NonGlobalNotificationDoesNotReachClient(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	t.Cleanup(cancel)

	sessID := establishSession(t, port)
	resp, reader := openGETStream(ctx, t, port, sessID)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "stream was not registered")

	nonGlobal, err := jsonrpc2.NewNotification("notifications/progress", map[string]any{"pct": 50})
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, nonGlobal))

	// Prove the non-global notification did NOT reach the client by racing it
	// against a global notification sent right after: if the non-global one
	// had been delivered, it would be the first data: frame read here.
	global, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, global))

	payload := readSSEData(t, reader)
	assert.Contains(t, payload, "notifications/tools/list_changed")
	assert.NotContains(t, payload, "notifications/progress")
}

// TestStandaloneSSE_FanOutToConcurrentClients verifies a single GLOBAL
// notification (see listChangedNotificationMethods) is delivered to every session with
// a concurrently connected GET SSE stream. It uses two DISTINCT sessions --
// per MCP 2025-11-25, a single session's stream must never receive a
// notification more than once, so this test uses two different sessions to
// exercise multi-session fan-out without exercising (or accidentally
// validating) same-session duplication.
func TestStandaloneSSE_FanOutToConcurrentClients(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	t.Cleanup(cancel)

	sessID1 := establishSession(t, port)
	sessID2 := establishSession(t, port)

	resp1, reader1 := openGETStream(ctx, t, port, sessID1)
	t.Cleanup(func() { _ = resp1.Body.Close() })
	resp2, reader2 := openGETStream(ctx, t, port, sessID2)
	t.Cleanup(func() { _ = resp2.Body.Close() })

	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 2 },
		time.Second, 5*time.Millisecond, "both streams were not registered")

	notification, err := jsonrpc2.NewNotification("notifications/resources/list_changed", nil)
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, notification))

	assert.Contains(t, readSSEData(t, reader1), "notifications/resources/list_changed")
	assert.Contains(t, readSSEData(t, reader2), "notifications/resources/list_changed")
}

// TestStandaloneSSE_ClientDisconnectDrainsRegistry verifies that closing a GET
// SSE client connection deregisters its stream, leaving the registry empty.
func TestStandaloneSSE_ClientDisconnectDrainsRegistry(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	t.Cleanup(cancel)

	sessID := establishSession(t, port)
	getCtx, getCancel := context.WithCancel(ctx)
	resp, _ := openGETStream(getCtx, t, port, sessID)

	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "stream was not registered")

	getCancel()
	_ = resp.Body.Close()

	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 0 },
		time.Second, 5*time.Millisecond, "stream was not deregistered after client disconnect")
}

// TestStandaloneSSE_SecondGetOnSameSessionEvictsFirst verifies that opening a
// second GET SSE stream for the same session evicts the first: the first
// connection's handler returns (its stop channel is closed) and the registry
// retains exactly one stream for the session.
func TestStandaloneSSE_SecondGetOnSameSessionEvictsFirst(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	t.Cleanup(cancel)

	sessID := establishSession(t, port)

	firstCtx, firstCancel := context.WithCancel(ctx)
	t.Cleanup(firstCancel)
	firstResp, _ := openGETStream(firstCtx, t, port, sessID)
	t.Cleanup(func() { _ = firstResp.Body.Close() })

	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "first stream was not registered")

	secondResp, _ := openGETStream(ctx, t, port, sessID)
	t.Cleanup(func() { _ = secondResp.Body.Close() })

	// Still exactly one stream for the session (the second evicted the first),
	// not two.
	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "registry must retain exactly one stream per session")

	// The first connection's handler must observe eviction and return, ending
	// its response body from the server side (the client observes EOF).
	firstClosed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, firstResp.Body)
		firstClosed <- err
	}()

	select {
	case err := <-firstClosed:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("first GET stream was not closed after eviction")
	}
}

// TestStandaloneSSE_GetSessionDecisionMatrix verifies handleGet's session
// resolution: a missing Mcp-Session-Id header is rejected with 400, an
// unknown one with 404, and a known one opens the SSE stream with 200.
func TestStandaloneSSE_GetSessionDecisionMatrix(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	_, ctx, cancel := startProxyWithBackend(t, port)
	t.Cleanup(cancel)

	sessID := establishSession(t, port)

	tests := []struct {
		name       string
		sessID     string
		setHeader  bool
		wantStatus int
	}{
		{name: "missing header", setHeader: false, wantStatus: http.StatusBadRequest},
		{name: "unknown session", sessID: "bogus-session-id", setHeader: true, wantStatus: http.StatusNotFound},
		{name: "known session", sessID: sessID, setHeader: true, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reqCtx, reqCancel := context.WithCancel(ctx)
			t.Cleanup(reqCancel)

			req, err := http.NewRequestWithContext(reqCtx, http.MethodGet,
				fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint), nil)
			require.NoError(t, err)
			if tt.setHeader {
				req.Header.Set("Mcp-Session-Id", tt.sessID)
			}

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			if tt.wantStatus == http.StatusOK {
				assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
			}
		})
	}
}

// TestStandaloneSSE_StopWhileStreamOpenReturnsCleanly verifies Stop returns
// without error, and unblocks the open GET handler, even while a client is
// connected to the standalone SSE stream.
func TestStandaloneSSE_StopWhileStreamOpenReturnsCleanly(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy := NewHTTPProxy("127.0.0.1", port, nil, nil)
	bgCtx := context.Background()
	require.NoError(t, proxy.Start(bgCtx))

	// Backend responder so establishSession's initialize gets a response.
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				if req, ok := msg.(*jsonrpc2.Request); ok && req.ID.IsValid() {
					result := map[string]any{"ok": true}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(bgCtx, resp)
				}
			case <-proxy.shutdownCh:
				return
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)

	sessID := establishSession(t, port)

	// Independent context so the GET request outlives proxy.Stop below.
	resp, _ := openGETStream(context.Background(), t, port, sessID)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "stream was not registered")

	stopCtx, stopCancel := context.WithTimeout(bgCtx, 5*time.Second)
	defer stopCancel()

	done := make(chan error, 1)
	go func() { done <- proxy.Stop(stopCtx) }()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return while a GET SSE stream was open")
	}

	assert.Equal(t, 0, proxy.serverStreams.streamCount(), "streams must be closed by Stop")
}

// TestStandaloneSSE_ListChangedRefiltersThroughExistingMiddleware is a
// security regression test: it proves the standalone GET SSE stream does not
// bypass tool-filter middleware. With a tool-filter configured to hide
// "filtered_tool":
//  1. a notifications/tools/list_changed notification still reaches the
//     client over the GET stream (fan-out isn't blocked by middleware);
//  2. a subsequent tools/list POST response omits the filtered tool (the
//     existing filtering behavior is unaffected by this change); and
//  3. a tools/call POST for the filtered tool is still blocked.
//
// All three requests flow through the same applyMiddlewares-wrapped handler
// (see Start's mux.Handle), so there is no separate, unfiltered code path for
// the GET stream.
func TestStandaloneSSE_ListChangedRefiltersThroughExistingMiddleware(t *testing.T) {
	t.Parallel()

	filterOpts := []mcp.ToolMiddlewareOption{mcp.WithToolsFilter("allowed_tool")}
	listMw, err := mcp.NewListToolsMappingMiddleware(filterOpts...)
	require.NoError(t, err)
	callMw, err := mcp.NewToolCallMappingMiddleware(filterOpts...)
	require.NoError(t, err)

	middlewares := []types.NamedMiddleware{
		{Name: "tool-list-filter", Function: listMw},
		{Name: "tool-call-filter", Function: callMw},
	}

	port := pickFreePort(t)
	proxy := NewHTTPProxy("127.0.0.1", port, nil, middlewares)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})
	time.Sleep(50 * time.Millisecond)

	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					continue
				}
				var result any
				if req.Method == "tools/list" {
					result = map[string]any{
						"tools": []map[string]any{
							{"name": "allowed_tool", "description": "kept"},
							{"name": "filtered_tool", "description": "hidden"},
						},
					}
				} else {
					result = map[string]any{"ok": true}
				}
				resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
				_ = proxy.ForwardResponseToClients(ctx, resp)
			case <-ctx.Done():
				return
			}
		}
	}()

	sessID := establishSession(t, port)

	// (1) notifications/tools/list_changed reaches the GET SSE client.
	getResp, reader := openGETStream(ctx, t, port, sessID)
	t.Cleanup(func() { _ = getResp.Body.Close() })
	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "stream was not registered")

	notification, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, notification))
	assert.Contains(t, readSSEData(t, reader), "notifications/tools/list_changed")

	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint)

	// (2) tools/list POST response omits the filtered tool.
	listBody := `{"jsonrpc":"2.0","id":"list-1","method":"tools/list"}`
	listResp, err := http.Post(endpoint, "application/json", strings.NewReader(listBody)) //nolint:noctx // test-only
	require.NoError(t, err)
	t.Cleanup(func() { _ = listResp.Body.Close() })
	listRespBody, err := io.ReadAll(listResp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(listRespBody), "allowed_tool")
	assert.NotContains(t, string(listRespBody), "filtered_tool")

	// (3) tools/call POST for the filtered tool is still blocked.
	callBody := `{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"filtered_tool"}}`
	callResp, err := http.Post(endpoint, "application/json", strings.NewReader(callBody)) //nolint:noctx // test-only
	require.NoError(t, err)
	t.Cleanup(func() { _ = callResp.Body.Close() })
	assert.Equal(t, http.StatusBadRequest, callResp.StatusCode)
}

// ------------------------- Routing security regressions -------------------------
//
// The tests below exercise the FULL per-session routing table introduced to
// replace the placeholder global-only filter (progress correlation, resource
// subscriptions, logging reconciliation, and the sampling/elicitation
// rejection path). Each is a security regression: it proves one session's
// (or the backend's) activity is never delivered to a session that has no
// right to see it.

// postSSE POSTs body to the proxy's MCP endpoint with Accept:
// text/event-stream, for sessID (empty omits the Mcp-Session-Id header
// entirely). It returns once response headers arrive (handleSingleRequestSSE
// flushes headers before waiting for any request-scoped data), so the
// returned reader can be used to read SSE frames as they are written,
// including interim progress frames before the final response.
func postSSE(ctx context.Context, t *testing.T, port int, sessID, body string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint), strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if sessID != "" {
		req.Header.Set("Mcp-Session-Id", sessID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp, bufio.NewReader(resp.Body)
}

// postJSON POSTs body to the proxy's MCP endpoint (no Accept header, so the
// proxy answers with a plain JSON response) for sessID, and returns the
// response along with its fully-read body.
func postJSON(t *testing.T, port int, sessID, body string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, //nolint:noctx // test-only
		fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint), strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if sessID != "" {
		req.Header.Set("Mcp-Session-Id", sessID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, respBody
}

// extractMetaProgressToken reads params._meta.progressToken as a string. It
// simulates what a real MCP backend does with an incoming request: read back
// whatever progressToken the caller (here, the proxy, after
// rewriteMetaProgressToken) put in _meta, to later echo it verbatim in a
// notifications/progress reply. It deliberately avoids test assertions (no
// *testing.T) since it runs on backend goroutines, not the test goroutine.
func extractMetaProgressToken(params json.RawMessage) (string, bool) {
	var m map[string]any
	if err := json.Unmarshal(params, &m); err != nil {
		return "", false
	}
	meta, ok := m["_meta"].(map[string]any)
	if !ok {
		return "", false
	}
	token, ok := meta["progressToken"].(string)
	return token, ok
}

// startProxyOnly starts an HTTPProxy on port with no automatic backend
// responder attached, for tests that need a custom backend goroutine (e.g. to
// track subscribe/unsubscribe call counts, or react differently per request).
func startProxyOnly(t *testing.T, port int) (*HTTPProxy, context.Context) {
	t.Helper()
	proxy := NewHTTPProxy("127.0.0.1", port, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})
	time.Sleep(50 * time.Millisecond)
	return proxy, ctx
}

// TestPostSSE_ProgressIsolationBetweenSessions is the progress-isolation
// SECURITY regression: two DIFFERENT sessions each send a POST-SSE request
// carrying the SAME client-visible progressToken ("1"). The backend emits a
// progress notification correlated to session A's (proxy-minted, internal)
// progress token only; the test proves it reaches ONLY A's SSE stream (with
// the token restored to "1"), and that B's stream never receives a progress
// frame at all -- proving isolation holds even when both sessions happen to
// choose the identical client-facing token.
func TestPostSSE_ProgressIsolationBetweenSessions(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx := startProxyOnly(t, port)

	// The backend only emits progress for the request tagged "which":"A", so
	// the test controls precisely which session's progress token is used,
	// without racing on request arrival order.
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					continue
				}
				go func(req *jsonrpc2.Request) {
					if req.Method == "tools/call" {
						var params map[string]any
						_ = json.Unmarshal(req.Params, &params)
						if params["which"] == "A" {
							if token, ok := extractMetaProgressToken(req.Params); ok {
								notif, nErr := jsonrpc2.NewNotification("notifications/progress",
									map[string]any{"progressToken": token, "progress": 1})
								require.NoError(t, nErr)
								_ = proxy.ForwardResponseToClients(ctx, notif)
							}
						}
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{"ok": true}, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				}(req)
			case <-ctx.Done():
				return
			}
		}
	}()

	sessA := establishSession(t, port)
	sessB := establishSession(t, port)

	bodyA := `{"jsonrpc":"2.0","id":"call-a","method":"tools/call","params":{"which":"A","_meta":{"progressToken":"1"}}}`
	bodyB := `{"jsonrpc":"2.0","id":"call-b","method":"tools/call","params":{"which":"B","_meta":{"progressToken":"1"}}}`

	respA, readerA := postSSE(ctx, t, port, sessA, bodyA)
	t.Cleanup(func() { _ = respA.Body.Close() })
	respB, readerB := postSSE(ctx, t, port, sessB, bodyB)
	t.Cleanup(func() { _ = respB.Body.Close() })

	// A's stream: progress frame (token restored to "1"), then the final response.
	progressA := readSSEData(t, readerA)
	assert.Contains(t, progressA, "notifications/progress")
	assert.Contains(t, progressA, `"progressToken":"1"`)
	finalA := readSSEData(t, readerA)
	assert.Contains(t, finalA, `"id":"call-a"`)

	// B's stream: its ONLY frame is the final response -- no progress ever, even
	// though B asked for the identical client-visible token "1".
	onlyFrameB := readSSEData(t, readerB)
	assert.Contains(t, onlyFrameB, `"id":"call-b"`)
	assert.NotContains(t, onlyFrameB, "notifications/progress")
}

// TestPostSSE_ProgressTokenClearedAfterRequestCompletes verifies a
// progress route is dropped once its POST-SSE request completes: a
// notifications/progress echoing that same (now-stale) token sent AFTER
// completion must be dropped rather than misdelivered.
func TestPostSSE_ProgressTokenClearedAfterRequestCompletes(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx := startProxyOnly(t, port)

	capturedToken := make(chan string, 1)
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					continue
				}
				if req.Method == "tools/call" {
					if token, ok := extractMetaProgressToken(req.Params); ok {
						capturedToken <- token
					}
				}
				resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{"ok": true}, nil)
				_ = proxy.ForwardResponseToClients(ctx, resp)
			case <-ctx.Done():
				return
			}
		}
	}()

	sessID := establishSession(t, port)
	body := `{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"_meta":{"progressToken":"1"}}}`

	resp, reader := postSSE(ctx, t, port, sessID, body)
	t.Cleanup(func() { _ = resp.Body.Close() })

	final := readSSEData(t, reader)
	assert.Contains(t, final, `"id":"call-1"`)

	var ptGlobal string
	select {
	case ptGlobal = <-capturedToken:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for backend to observe the rewritten progress token")
	}

	// The request has completed (we already read its final frame), so its
	// progress route must be gone: this lookup must fail.
	require.Eventually(t, func() bool {
		_, found := proxy.routing.lookupProgressToken(ptGlobal)
		return !found
	}, time.Second, 5*time.Millisecond, "progress token must be dropped once its request completes")
}

// TestPostSSE_ResourcesUpdated_SubscribersOnlyAndUpstreamRefCounted is the
// resources/subscribe|unsubscribe SECURITY + ref-counting regression:
//   - A subscribes uri1, B subscribes uri2: a resources/updated{uri1}
//     notification reaches ONLY A (delivered on A's standalone GET stream),
//     never B.
//   - A THIRD session subscribing to uri1 does not cause a second upstream
//     resources/subscribe call for uri1 (ref-counted).
//   - Upstream resources/unsubscribe for uri1 is sent only once the LAST of
//     its subscribers unsubscribes.
func TestPostSSE_ResourcesUpdated_SubscribersOnlyAndUpstreamRefCounted(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx := startProxyOnly(t, port)

	var mu sync.Mutex
	subscribeCalls := map[string]int{}
	unsubscribeCalls := map[string]int{}

	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					continue
				}
				var params map[string]any
				_ = json.Unmarshal(req.Params, &params)
				uri, _ := params["uri"].(string)

				mu.Lock()
				switch req.Method {
				case "resources/subscribe":
					subscribeCalls[uri]++
				case "resources/unsubscribe":
					unsubscribeCalls[uri]++
				}
				mu.Unlock()

				resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
				_ = proxy.ForwardResponseToClients(ctx, resp)
			case <-ctx.Done():
				return
			}
		}
	}()

	sessA := establishSession(t, port)
	sessB := establishSession(t, port)
	sessC := establishSession(t, port)
	sessD := establishSession(t, port)

	getA, readerA := openGETStream(ctx, t, port, sessA)
	t.Cleanup(func() { _ = getA.Body.Close() })
	getB, readerB := openGETStream(ctx, t, port, sessB)
	t.Cleanup(func() { _ = getB.Body.Close() })

	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 2 },
		time.Second, 5*time.Millisecond, "both GET streams were not registered")

	subscribe := func(sessID, uri string) {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"sub-%s","method":"resources/subscribe","params":{"uri":%q}}`, sessID, uri)
		resp, respBody := postJSON(t, port, sessID, body)
		require.Equal(t, http.StatusOK, resp.StatusCode, "subscribe must succeed: %s", string(respBody))
	}
	unsubscribe := func(sessID, uri string) {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"unsub-%s","method":"resources/unsubscribe","params":{"uri":%q}}`, sessID, uri)
		resp, respBody := postJSON(t, port, sessID, body)
		require.Equal(t, http.StatusOK, resp.StatusCode, "unsubscribe must succeed: %s", string(respBody))
	}

	// A, C, D all subscribe to uri1 (three subscribers); B subscribes to uri2.
	subscribe(sessA, "uri1")
	subscribe(sessC, "uri1")
	subscribe(sessD, "uri1")
	subscribe(sessB, "uri2")

	mu.Lock()
	assert.Equal(t, 1, subscribeCalls["uri1"], "only the FIRST subscribe for uri1 must reach the backend")
	assert.Equal(t, 1, subscribeCalls["uri2"], "uri2's subscribe must independently reach the backend once")
	mu.Unlock()

	// updated{uri1} reaches only A (the only GET-stream-connected subscriber of
	// uri1); B (subscribed only to uri2) must receive nothing.
	notif, err := jsonrpc2.NewNotification("notifications/resources/updated", map[string]any{"uri": "uri1"})
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, notif))

	assert.Contains(t, readSSEData(t, readerA), "uri1")

	// Prove B got nothing by racing a global sentinel right after.
	sentinel, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, sentinel))
	assert.Contains(t, readSSEData(t, readerB), "notifications/tools/list_changed")

	// Unsubscribe A and C (neither is last: D remains) -- no upstream call yet.
	unsubscribe(sessA, "uri1")
	unsubscribe(sessC, "uri1")
	mu.Lock()
	assert.Equal(t, 0, unsubscribeCalls["uri1"], "upstream unsubscribe must not fire before the LAST subscriber leaves")
	mu.Unlock()

	// D is last: NOW the upstream unsubscribe must fire.
	unsubscribe(sessD, "uri1")
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return unsubscribeCalls["uri1"] == 1
	}, time.Second, 5*time.Millisecond, "upstream unsubscribe must fire once the last subscriber leaves")
}

// TestPostSSE_LoggingDroppedAndUpstreamLevelIsMaxAcrossSessions verifies:
//   - a notifications/message the backend sends is NEVER delivered to any
//     connected client (shared-backend logs are unattributable to a session);
//   - the upstream logging/setLevel level the backend observes is the MAXIMUM
//     verbosity requested across all sessions, not merely the last session's
//     own request (which would silently under-serve an earlier, more verbose
//     requester).
func TestPostSSE_LoggingDroppedAndUpstreamLevelIsMaxAcrossSessions(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx := startProxyOnly(t, port)

	var mu sync.Mutex
	var observedLevels []string

	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					continue
				}
				if req.Method == "logging/setLevel" {
					var params map[string]any
					_ = json.Unmarshal(req.Params, &params)
					level, _ := params["level"].(string)
					mu.Lock()
					observedLevels = append(observedLevels, level)
					mu.Unlock()
				}
				resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
				_ = proxy.ForwardResponseToClients(ctx, resp)
			case <-ctx.Done():
				return
			}
		}
	}()

	sessA := establishSession(t, port)
	getA, readerA := openGETStream(ctx, t, port, sessA)
	t.Cleanup(func() { _ = getA.Body.Close() })
	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "GET stream was not registered")

	setLevel := func(sessID, level string) {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":"lvl-%s","method":"logging/setLevel","params":{"level":%q}}`, sessID, level)
		resp, respBody := postJSON(t, port, sessID, body)
		require.Equal(t, http.StatusOK, resp.StatusCode, "setLevel must succeed: %s", string(respBody))
	}

	sessB := establishSession(t, port)

	// reconcileUpstreamLogLevel runs in a background goroutine (fire-and-forget
	// from the client's perspective, see its doc comment), so each step below
	// waits for its own reconciliation to be observed before issuing the next
	// setLevel -- otherwise the two reconciliations could race each other
	// on the shared backend channel and land out of order.
	setLevel(sessA, "warning") // rank 4: first request always reconciles upstream
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(observedLevels) == 1 && observedLevels[0] == "warning"
	}, 2*time.Second, 5*time.Millisecond, "upstream must reconcile to sessA's warning level")

	setLevel(sessB, "debug") // rank 7 > 4: raises the max, reconciles upstream again
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(observedLevels) == 2 && observedLevels[1] == "debug"
	}, 2*time.Second, 5*time.Millisecond, "upstream must reconcile to the new max (debug)")

	// rank 3 < 7 (current max): must NOT reconcile upstream a third time.
	// require.Never (rather than a fixed sleep) actively fails on an unexpected
	// extra reconcile, including a slow-but-present one that a sleep would miss.
	setLevel(sessA, "error")
	require.Never(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(observedLevels) > 2
	}, 500*time.Millisecond, 20*time.Millisecond,
		"a session lowering below the current max must not reconcile upstream")

	// A logging notification the backend sends is dropped, not delivered.
	logMsg, err := jsonrpc2.NewNotification("notifications/message",
		map[string]any{"level": "debug", "data": "secret log line"})
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, logMsg))

	sentinel, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, sentinel))

	got := readSSEData(t, readerA)
	assert.Contains(t, got, "notifications/tools/list_changed")
	assert.NotContains(t, got, "secret log line")
}

// TestPostSSE_SamplingRequestNeverReachesClientAndBackendGetsError verifies a
// server-initiated sampling/createMessage request is NEVER delivered to any
// connected client, and instead unblocks the backend with a JSON-RPC error
// response on its own messageCh (see rejectServerRequestToBackend).
func TestPostSSE_SamplingRequestNeverReachesClientAndBackendGetsError(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx := startProxyOnly(t, port)

	// backendReceivedError captures the rejection response the backend loop
	// below observes on messageCh, so the test can assert on it without a
	// second, racing reader of the same channel.
	backendReceivedError := make(chan *jsonrpc2.Response, 1)

	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				switch m := msg.(type) {
				case *jsonrpc2.Request:
					if !m.ID.IsValid() {
						continue
					}
					resp, _ := jsonrpc2.NewResponse(m.ID, map[string]any{"ok": true}, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case *jsonrpc2.Response:
					backendReceivedError <- m
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	sessID := establishSession(t, port)
	getResp, reader := openGETStream(ctx, t, port, sessID)
	t.Cleanup(func() { _ = getResp.Body.Close() })
	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "GET stream was not registered")

	samplingReq, err := jsonrpc2.NewCall(jsonrpc2.StringID("sampling-1"), "sampling/createMessage",
		map[string]any{"messages": []any{}})
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, samplingReq))

	// The rejection error is written back to the backend on messageCh.
	select {
	case resp := <-backendReceivedError:
		assert.Equal(t, "sampling-1", resp.ID.Raw())
		require.Error(t, resp.Error)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the backend to receive the rejection error")
	}

	// Prove the sampling request never reached the client by racing a global
	// sentinel right after.
	sentinel, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, sentinel))

	got := readSSEData(t, reader)
	assert.Contains(t, got, "notifications/tools/list_changed")
	assert.NotContains(t, got, "sampling/createMessage")
}

// TestPostSSE_FailedFirstSubscribeIsNotRecordedAndSecondSubscriberRetries is
// the FIX 1 regression from #5744's review: interceptSubscribe used to record
// a subscription BEFORE forwarding the FIRST resources/subscribe upstream, so
// a backend rejection (or timeout) still left the uri's subscription table
// entry in place -- every later session's subscribe for the SAME uri was then
// dedup-served a synthesized success and never actually reached the backend,
// permanently starving the uri of a real subscription.
//
// This proves all three parts of the fix: (a) the FIRST client whose backend
// subscribe fails gets the real JSON-RPC error back, not a synthesized
// success; (b) the routing table has NO subscriber recorded for the uri after
// that failure; and (c) a SECOND client subscribing to the SAME uri actually
// forwards upstream again (the backend's subscribe call count increments to
// 2, proving it was not dedup-served) rather than being silently starved of a
// real subscription.
func TestPostSSE_FailedFirstSubscribeIsNotRecordedAndSecondSubscriberRetries(t *testing.T) {
	t.Parallel()

	const uri = "err-uri"

	port := pickFreePort(t)
	proxy, ctx := startProxyOnly(t, port)

	var mu sync.Mutex
	subscribeCalls := 0

	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					continue
				}
				if req.Method != "resources/subscribe" {
					resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
					continue
				}

				mu.Lock()
				subscribeCalls++
				callNum := subscribeCalls
				mu.Unlock()

				// The FIRST upstream resources/subscribe call fails (e.g. the
				// resource does not exist); every subsequent call succeeds.
				var resp *jsonrpc2.Response
				if callNum == 1 {
					resp = &jsonrpc2.Response{ID: req.ID, Error: jsonrpc2.NewError(-32002, "resource not found")}
				} else {
					resp, _ = jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
				}
				_ = proxy.ForwardResponseToClients(ctx, resp)
			case <-ctx.Done():
				return
			}
		}
	}()

	sessA := establishSession(t, port)
	sessB := establishSession(t, port)

	// (a) sessA's subscribe gets the real backend error back.
	subBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":"sub-a","method":"resources/subscribe","params":{"uri":%q}}`, uri)
	respA, bodyA := postJSON(t, port, sessA, subBody)
	assert.Equal(t, http.StatusOK, respA.StatusCode, "a JSON-RPC error is still an HTTP 200 envelope")
	assert.Contains(t, string(bodyA), "resource not found", "the client must see the real backend error")
	assert.Contains(t, string(bodyA), `"error"`)

	// (b) the routing table must have NO subscriber recorded for uri.
	assert.Empty(t, proxy.routing.subscribersOf(uri),
		"a failed first subscribe must never be recorded in the routing table")

	// (c) sessB's subscribe for the SAME uri must actually reach the backend
	// again (call count 2), not be dedup-served a synthesized success.
	subBodyB := fmt.Sprintf(`{"jsonrpc":"2.0","id":"sub-b","method":"resources/subscribe","params":{"uri":%q}}`, uri)
	respB, bodyB := postJSON(t, port, sessB, subBodyB)
	assert.Equal(t, http.StatusOK, respB.StatusCode)
	assert.NotContains(t, string(bodyB), `"error"`, "sessB's subscribe should succeed against the backend")

	mu.Lock()
	assert.Equal(t, 2, subscribeCalls, "sessB's subscribe must actually forward upstream, not be deduped")
	mu.Unlock()

	assert.ElementsMatch(t, []string{sessB}, proxy.routing.subscribersOf(uri),
		"only sessB (the successful subscriber) should be recorded")
}

// TestPostSSE_AuthzDeniedSubscribeNeverRecordedInRouting is the authz
// non-bypass SECURITY regression: a resources/subscribe request blocked by
// authorization middleware must NEVER be recorded in sessionRouter's
// subscription table, so a later resources/updated for that uri reaches
// nobody -- proving the routing table only ever reflects requests that were
// actually admitted upstream, not merely attempted by a client.
func TestPostSSE_AuthzDeniedSubscribeNeverRecordedInRouting(t *testing.T) {
	t.Parallel()

	denyAll := types.NamedMiddleware{
		Name: "deny-all",
		Function: func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			})
		},
	}

	port := pickFreePort(t)
	proxy := NewHTTPProxy("127.0.0.1", port, nil, []types.NamedMiddleware{denyAll})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})
	time.Sleep(50 * time.Millisecond)

	body := `{"jsonrpc":"2.0","id":"sub-1","method":"resources/subscribe","params":{"uri":"secret-uri"}}`
	req, err := http.NewRequest(http.MethodPost, //nolint:noctx // test-only
		fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint), strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "does-not-matter-authz-denies-first")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	assert.Nil(t, proxy.routing.subscribersOf("secret-uri"),
		"an authz-denied subscribe must never be recorded in the routing table")
}

// TestHandleDelete_PurgesRoutingState verifies handleDelete purges a
// session's subscription and log-level state from routing, alongside
// deleting the session itself, AND (FIX 2 in #5744's review) tears down the
// session's standalone GET SSE stream: its handler goroutine must return
// (ending the response from the server side) and the registry must no longer
// retain an entry for the deleted session -- neither survives waiting for the
// client to eventually close the underlying socket on its own.
func TestHandleDelete_PurgesRoutingState(t *testing.T) {
	t.Parallel()

	port := pickFreePort(t)
	proxy, ctx := startProxyOnly(t, port)
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				if req, ok := msg.(*jsonrpc2.Request); ok && req.ID.IsValid() {
					resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	sessID := establishSession(t, port)
	postJSON(t, port, sessID,
		`{"jsonrpc":"2.0","id":"sub-1","method":"resources/subscribe","params":{"uri":"uri1"}}`)
	require.NotEmpty(t, proxy.routing.subscribersOf("uri1"), "subscription must be recorded before delete")

	getResp, _ := openGETStream(ctx, t, port, sessID)
	t.Cleanup(func() { _ = getResp.Body.Close() })
	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "GET stream was not registered")

	delReq, err := http.NewRequest(http.MethodDelete, //nolint:noctx // test-only
		fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint), nil)
	require.NoError(t, err)
	delReq.Header.Set("Mcp-Session-Id", sessID)
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	t.Cleanup(func() { _ = delResp.Body.Close() })
	require.Equal(t, http.StatusNoContent, delResp.StatusCode)

	assert.Nil(t, proxy.routing.subscribersOf("uri1"), "subscription must be purged by handleDelete")

	// The GET stream's handler goroutine must observe the closed stop channel
	// and return, ending the response body from the server side (the client
	// observes EOF) rather than staying open to keep receiving broadcasts.
	getClosed := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, getResp.Body)
		getClosed <- err
	}()
	select {
	case err := <-getClosed:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("GET stream was not torn down by handleDelete")
	}
	assert.Equal(t, 0, proxy.serverStreams.streamCount(),
		"registry must no longer retain the deleted session's stream")
}

// TestReapServerStreams_ClosesStreamForInactiveSession is the FIX 2 reaper
// regression from #5744's review: reapRoutingState used to leave
// serverStreams entirely untouched, so a session whose owning client
// disappeared without ever sending DELETE (and without the proxy observing
// its GET request's context ending) would keep its standalone GET stream --
// and the handler goroutine consuming it -- alive indefinitely. This verifies
// reapServerStreams closes (and removes) the stream for a session isActive no
// longer reports as live, while leaving a still-active session's stream
// alone, using a real serverStreamRegistry and a stub isActive rather than
// the production reapRoutingState goroutine's real timer (too slow to wait
// out in a unit test).
func TestReapServerStreams_ClosesStreamForInactiveSession(t *testing.T) {
	t.Parallel()

	proxy := NewHTTPProxy("127.0.0.1", 0, nil, nil)
	goneStream := proxy.serverStreams.register("gone-session")
	proxy.serverStreams.register("still-here-session")

	proxy.reapServerStreams(func(sess string) bool { return sess != "gone-session" })

	assert.Equal(t, 1, proxy.serverStreams.streamCount(),
		"only the inactive session's stream should be closed")
	select {
	case <-goneStream.stop:
		// Expected: the reaper closed it.
	case <-time.After(2 * time.Second):
		t.Fatal("expired session's stream was not closed by the reaper")
	}
}

// TestSessionRouter_Reap_IntegratesWithProxy verifies the reaper wired into
// HTTPProxy.Start drops routing state for a session isActive reports as
// gone, using a real sessionRouter and a stub isActive rather than the
// production reapRoutingState goroutine's real timer (which runs on
// sessionTTL/2 -- too slow to wait out in a unit test).
func TestSessionRouter_Reap_IntegratesWithProxy(t *testing.T) {
	t.Parallel()

	proxy := NewHTTPProxy("127.0.0.1", 0, nil, nil)
	proxy.routing.addSubscription("uri1", "gone-session")
	proxy.routing.setLogLevel("gone-session", 7)
	proxy.routing.addSubscription("uri1", "still-here-session")

	proxy.routing.reap(func(sess string) bool { return sess != "gone-session" })

	assert.ElementsMatch(t, []string{"still-here-session"}, proxy.routing.subscribersOf("uri1"))
}

// TestPostSSE_ConcurrentLastUnsubscribeAndFirstSubscribe_SameURIAreOrdered is
// the FIX 1 regression from #5744's review: interceptSessionScopedRequest's
// ref-count decision and the upstream forward it triggers were not atomic
// with each other across sessions, so a concurrent last-unsubscribe (sessA)
// and first-subscribe (sessB) for the SAME uri could reach the backend out of
// order, leaving the backend unsubscribed while the routing table said sessB
// was subscribed -- sessB would then silently stop receiving
// resources/updated. handlePost now serializes both the ref-count decision
// and the upstream forward per-uri via uriLocks (see
// resourceSubscriptionURI), so the backend must observe sessA's unsubscribe
// and sessB's subscribe in an order consistent with the FINAL ref count
// (subscribed, by sessB alone) -- and sessB must actually receive a
// subsequent resources/updated for the uri.
//
// This proves ordering deterministically, not via a timing race: the backend
// gates its response to sessA's unsubscribe until the test explicitly
// releases it (releaseUnsub), and sessB's subscribe request is only issued
// after the test has confirmed -- via a channel, never a sleep -- that the
// backend has already received sessA's unsubscribe, i.e. that sessA's
// handlePost is inside its uriLocks critical section, blocked waiting for
// that gated backend response. With the fix, sessB's handlePost cannot send
// ANYTHING to the backend until sessA's entire critical section (decision,
// forward, backend response, HTTP response write) has completed and released
// uriLocks for the uri: that is a structural guarantee of the mutex, not a
// probabilistic race, so the backend's observed order is deterministic
// regardless of goroutine scheduling. Run with -race: the property under
// test is ordering, and a data race would indicate the lock is not doing its
// job.
func TestPostSSE_ConcurrentLastUnsubscribeAndFirstSubscribe_SameURIAreOrdered(t *testing.T) {
	t.Parallel()

	const uri = "shared-uri"

	port := pickFreePort(t)
	proxy, ctx := startProxyOnly(t, port)

	var mu sync.Mutex
	var order []string

	unsubReceived := make(chan struct{})
	releaseUnsub := make(chan struct{})

	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					continue
				}
				switch req.Method {
				case "resources/unsubscribe":
					mu.Lock()
					order = append(order, "unsubscribe")
					mu.Unlock()
					close(unsubReceived)
					<-releaseUnsub // hold the response until the test releases it
				case "resources/subscribe":
					mu.Lock()
					order = append(order, "subscribe")
					mu.Unlock()
				}
				resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
				_ = proxy.ForwardResponseToClients(ctx, resp)
			case <-ctx.Done():
				return
			}
		}
	}()

	sessA := establishSession(t, port)
	sessB := establishSession(t, port)

	// sessB's GET stream is opened up front so it can observe the eventual
	// resources/updated once it becomes uri's subscriber.
	getB, readerB := openGETStream(ctx, t, port, sessB)
	t.Cleanup(func() { _ = getB.Body.Close() })
	require.Eventually(t, func() bool { return proxy.serverStreams.streamCount() == 1 },
		time.Second, 5*time.Millisecond, "sessB's GET stream was not registered")

	// Precondition: sessA is the sole (first) subscriber of uri.
	subA, subABody := postJSON(t, port, sessA,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":"sub-a","method":"resources/subscribe","params":{"uri":%q}}`, uri))
	require.Equal(t, http.StatusOK, subA.StatusCode, "sessA's initial subscribe must succeed: %s", string(subABody))
	require.ElementsMatch(t, []string{sessA}, proxy.routing.subscribersOf(uri))

	mu.Lock()
	order = nil // only the concurrent unsubscribe/subscribe pair below is under test
	mu.Unlock()

	// sessA's last-unsubscribe fires concurrently; it will block in the
	// backend's gate (above) until releaseUnsub is closed below.
	unsubDone := make(chan struct{})
	go func() {
		defer close(unsubDone)
		resp, body := postJSON(t, port, sessA,
			fmt.Sprintf(`{"jsonrpc":"2.0","id":"unsub-a","method":"resources/unsubscribe","params":{"uri":%q}}`, uri))
		assert.Equal(t, http.StatusOK, resp.StatusCode, "sessA's unsubscribe must succeed: %s", string(body))
	}()

	select {
	case <-unsubReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the backend to receive sessA's unsubscribe")
	}

	// sessB's first-subscribe is issued only now, while sessA's request is
	// provably still holding uriLocks for uri (blocked on the gated backend
	// response above). With the fix, sessB's handlePost must block acquiring
	// the SAME per-uri lock until sessA's request fully completes.
	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		resp, body := postJSON(t, port, sessB,
			fmt.Sprintf(`{"jsonrpc":"2.0","id":"sub-b","method":"resources/subscribe","params":{"uri":%q}}`, uri))
		assert.Equal(t, http.StatusOK, resp.StatusCode, "sessB's subscribe must succeed: %s", string(body))
	}()

	// THE discriminating check: while sessA's unsubscribe is still gated
	// (its handlePost provably still holds uriLocks for uri), sessB's
	// subscribe decision must not have taken effect yet. Without FIX 1, sessB's
	// decision is not ordered by any per-uri lock and would run as soon as
	// sessB's request is scheduled -- independent of sessA's still-in-flight
	// upstream forward -- so this would very quickly observe sessB already
	// recorded as a subscriber, which is exactly the desynchronization the fix
	// prevents. With the fix this is a structural guarantee (sessB's
	// handlePost is blocked acquiring the same lock sessA holds), so it holds
	// for the ENTIRE waitFor window below, not merely at one sampled instant.
	require.Never(t, func() bool {
		return len(proxy.routing.subscribersOf(uri)) > 0
	}, 200*time.Millisecond, 5*time.Millisecond,
		"sessB's subscribe decision must not take effect while sessA's unsubscribe is still gated on the backend")

	close(releaseUnsub)

	select {
	case <-unsubDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sessA's unsubscribe request to complete")
	}
	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sessB's subscribe request to complete")
	}

	// The backend must have observed the unsubscribe strictly before the
	// subscribe: the order matching the FINAL ref count (subscribed, by
	// sessB), never the reverse -- the reverse would leave the backend
	// unsubscribed while the routing table said sessB was subscribed, the
	// #5744 bug this test guards against.
	mu.Lock()
	assert.Equal(t, []string{"unsubscribe", "subscribe"}, order,
		"backend must observe sessA's unsubscribe strictly before sessB's subscribe")
	mu.Unlock()

	assert.ElementsMatch(t, []string{sessB}, proxy.routing.subscribersOf(uri),
		"routing table must end with sessB as the sole subscriber")

	// sessB must actually receive a subsequent resources/updated for uri,
	// proving the backend and the routing table ended up CONSISTENT with each
	// other (both "subscribed"), not merely that the routing table says so.
	notif, err := jsonrpc2.NewNotification("notifications/resources/updated", map[string]any{"uri": uri})
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(ctx, notif))
	assert.Contains(t, readSSEData(t, readerB), uri)
}
