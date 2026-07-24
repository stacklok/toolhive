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
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// getFreePort returns a free port by binding to port 0 and getting the assigned port
func getFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// TestHTTPRequestIgnoresNotifications tests that HTTP requests ignore notifications
// and only return the actual response. This addresses the fix for issue #1568.
//
//nolint:paralleltest // Test starts HTTP server
func TestHTTPRequestIgnoresNotifications(t *testing.T) {
	// Get an available port dynamically
	port := getFreePort(t)
	proxy := NewHTTPProxy("localhost", port, nil, nil)
	ctx := t.Context()

	// Start the proxy server
	err := proxy.Start(ctx)
	require.NoError(t, err)
	defer proxy.Stop(ctx)

	// Give the server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Simulate MCP server that sends notifications before responses
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				// Send notification first (should be ignored by HTTP handler)
				notification, _ := jsonrpc2.NewNotification("progress", map[string]interface{}{
					"status": "processing",
				})
				proxy.ForwardResponseToClients(ctx, notification)

				// Finally send the actual response
				if req, ok := msg.(*jsonrpc2.Request); ok && req.ID.IsValid() {
					response, _ := jsonrpc2.NewResponse(req.ID, "operation complete", nil)
					proxy.ForwardResponseToClients(ctx, response)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	proxyURL := fmt.Sprintf("http://localhost:%d%s", port, StreamableHTTPEndpoint)

	// Test single request
	requestJSON := `{"jsonrpc": "2.0", "method": "test.method", "id": "req-123"}`
	resp, err := http.Post(proxyURL, "application/json", bytes.NewReader([]byte(requestJSON)))
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should get the response, not notifications
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var responseData map[string]interface{}
	err = json.NewDecoder(resp.Body).Decode(&responseData)
	require.NoError(t, err)

	// Verify we got the actual response (proving notifications were ignored)
	assert.Equal(t, "2.0", responseData["jsonrpc"])
	assert.Equal(t, "req-123", responseData["id"])
	assert.Equal(t, "operation complete", responseData["result"])

	// A batch request is rejected outright (see TestBatchRequestsRejected and
	// #5745), not executed — batching was removed in MCP 2025-06-18.
	batchJSON := `[{"jsonrpc": "2.0", "method": "test.batch", "id": "batch-1"}]`
	resp2, err := http.Post(proxyURL, "application/json", bytes.NewReader([]byte(batchJSON)))
	require.NoError(t, err)
	defer resp2.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode)
	batchBody, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)
	assert.Contains(t, string(batchBody), `"code":-32600`)
}

// TestHTTPProxy_StartMountsAuthDiscoveryEndpoint is an integration test that
// starts a real proxy with an authInfoHandler and verifies the RFC 9728
// discovery endpoint responds correctly.
//
//nolint:paralleltest // Test starts/stops HTTP server
func TestHTTPProxy_StartMountsAuthDiscoveryEndpoint(t *testing.T) {
	const wantBody = `{"resource":"https://example.com"}`

	sentinel := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(wantBody))
	})

	port := getFreePort(t)
	proxy := NewHTTPProxy("localhost", port, nil, nil, WithAuthInfoHandler(sentinel))
	ctx := t.Context()

	require.NoError(t, proxy.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})

	// streamable Start() returns before the goroutine binds — poll until ready.
	url := fmt.Sprintf("http://localhost:%d/.well-known/oauth-protected-resource", port)
	var resp *http.Response
	require.Eventually(t, func() bool {
		var err error
		resp, err = http.Get(url) //nolint:gosec // test-only URL construction
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond, "server did not become ready in time")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "https://example.com", body["resource"])
}

// syncMapLen counts entries in a sync.Map for test assertions.
func syncMapLen(m *sync.Map) int {
	n := 0
	m.Range(func(_, _ any) bool { n++; return true })
	return n
}

// TestModernLoadDoesNotAccumulateState drives a burst of concurrent Modern
// (2026-07-28) requests through the full per-request lifecycle --
// resolveSessionForRequest mints a routing token, doRequest registers a
// waiter/idRestore entry keyed on it and deletes both via its deferred
// cleanup once the response arrives -- and proves nothing is left behind.
// This is the regression test for the unbounded-growth concern behind
// toolhive-core#169.
//
// waiters/idRestore/sessionManager are asserted as hard, exact-zero checks:
// createWaiter's cleanup runs synchronously inside doRequest before
// handleSingleRequest ever writes the HTTP response, so by the time every
// client in the burst has read its response, every cleanup has already run --
// no settle delay needed, and a regression that skips cleanup fails this
// deterministically.
//
// The goroutine count is checked too, but only as a generous, best-effort
// bound: this package's request path spawns no per-request goroutine, so any
// growth here would come from net/http's own idle-connection handling, not
// from code owned by this package -- hence the loose tolerance rather than a
// strict equality.
func TestModernLoadDoesNotAccumulateState(t *testing.T) {
	port := pickFreePort(t)
	proxy, ctx, cancel := startProxyWithBackend(t, port)
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = proxy.Stop(ctx) })

	url := fmt.Sprintf("http://127.0.0.1:%d%s", port, StreamableHTTPEndpoint)

	const requestCount = 1000
	// Cap in-flight requests well under the proxy's 100-slot message-channel
	// buffer. Firing all 1000 at once can exceed that buffer and produce
	// dropped-response 504s (#5952, a proxy backpressure gap) --
	// unrelated to the accumulation invariant this test checks, so throttle
	// around it rather than let it flake the run.
	const maxInFlight = 50
	sem := make(chan struct{}, maxInFlight)
	baseline := runtime.NumGoroutine()

	var wg sync.WaitGroup
	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(id int) {
			defer wg.Done()
			defer func() { <-sem }()

			req, err := http.NewRequest(
				http.MethodPost, url, bytes.NewReader([]byte(modernBody(id, "tools/list"))))
			if !assert.NoError(t, err) {
				return
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if !assert.NoError(t, err) {
				return
			}
			defer func() {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		}(i)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for the request burst to complete")
	}

	assert.Zero(t, proxy.sessionManager.Count(), "Modern requests must never register a session")
	assert.Zero(t, syncMapLen(&proxy.waiters), "waiters must be fully cleaned up once all responses are delivered")
	assert.Zero(t, syncMapLen(&proxy.idRestore), "idRestore must be fully cleaned up once all responses are delivered")

	http.DefaultClient.CloseIdleConnections()
	assert.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline+20
	}, time.Second, 20*time.Millisecond, "goroutine count did not settle back near baseline after the burst")
}
