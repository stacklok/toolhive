package streamable

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

	proxy := NewHTTPProxy("127.0.0.1", port, "test-container", nil)
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
	proxy := NewHTTPProxy("127.0.0.1", port, "test-container", nil)
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
	proxy := NewHTTPProxy("127.0.0.1", port, "test-container", nil)
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
	proxy := NewHTTPProxy("127.0.0.1", port, "test-container", nil)
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
