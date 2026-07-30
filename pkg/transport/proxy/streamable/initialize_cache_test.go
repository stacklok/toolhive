// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// fakeStdioBackend stands in for the single stdio MCP session behind the
// proxy: it drains the proxy's outbound message channel, records every method
// that reaches it, and answers id-bearing requests via respond.
type fakeStdioBackend struct {
	mu      sync.Mutex
	methods []string
}

func (b *fakeStdioBackend) record(method string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.methods = append(b.methods, method)
}

// count returns how many times method reached the backend.
func (b *fakeStdioBackend) count(method string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, m := range b.methods {
		if m == method {
			n++
		}
	}
	return n
}

// startFakeStdioBackend wires a fakeStdioBackend to proxy and returns it.
// respond is consulted only for id-bearing requests; notifications are
// recorded and dropped, as a real backend would handle them.
func startFakeStdioBackend(
	ctx context.Context, proxy *HTTPProxy, respond func(*jsonrpc2.Request) jsonrpc2.Message,
) *fakeStdioBackend {
	backend := &fakeStdioBackend{}
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok {
					continue
				}
				backend.record(req.Method)
				if !req.ID.IsValid() {
					continue
				}
				if reply := respond(req); reply != nil {
					_ = proxy.ForwardResponseToClients(ctx, reply)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return backend
}

// testProtocolVersion is the protocol version the fake backend negotiates.
const testProtocolVersion = "2025-11-25"

// initializeResultFor builds a realistic InitializeResult response for req.
func initializeResultFor(req *jsonrpc2.Request) jsonrpc2.Message {
	resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{
		"protocolVersion": testProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "fake-stdio-server", "version": "1.0.0"},
	}, nil)
	return resp
}

// startInitTestProxy starts a proxy on a free port and returns it with its
// /mcp endpoint URL.
func startInitTestProxy(t *testing.T) (*HTTPProxy, string) {
	t.Helper()
	port := getFreePort(t)
	proxy := NewHTTPProxy("localhost", port, nil, nil)
	require.NoError(t, proxy.Start(t.Context()))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = proxy.Stop(stopCtx)
	})
	// Give the listener a moment to accept connections.
	time.Sleep(100 * time.Millisecond)
	return proxy, fmt.Sprintf("http://localhost:%d%s", port, StreamableHTTPEndpoint)
}

// postInitialize sends an initialize request and returns the decoded JSON-RPC
// response body.
func postInitialize(t *testing.T, url string, id string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(
		`{"jsonrpc":"2.0","id":%q,"method":"initialize","params":{"protocolVersion":%q}}`,
		id, testProtocolVersion)
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte(body))) //nolint:gosec // test-local URL
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var decoded map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))
	return decoded
}

// TestInitializeReachesBackendOnceAndIsCachedThereafter verifies that only the
// first client handshake is forwarded to the single stdio session, and that
// every later client is answered from the cached InitializeResult with its own
// JSON-RPC id echoed back.
//
// Without the cache each handshake reaches the backend, and go-sdk v1.7+
// rejects every one after the first with `duplicate "initialize" received`
// (#5890).
func TestInitializeReachesBackendOnceAndIsCachedThereafter(t *testing.T) {
	t.Parallel()

	proxy, url := startInitTestProxy(t)
	backend := startFakeStdioBackend(t.Context(), proxy, func(req *jsonrpc2.Request) jsonrpc2.Message {
		return initializeResultFor(req)
	})

	var results []any
	for i := range 3 {
		decoded := postInitialize(t, url, fmt.Sprintf("client-%d", i))
		assert.Equal(t, fmt.Sprintf("client-%d", i), decoded["id"],
			"each client must get its own JSON-RPC id back, not the first client's")
		require.NotNil(t, decoded["result"], "handshake should succeed")
		results = append(results, decoded["result"])
	}

	assert.Equal(t, 1, backend.count("initialize"),
		"only the first handshake may reach the single stdio session")
	assert.Equal(t, results[0], results[1], "cached result must match the original")
	assert.Equal(t, results[0], results[2], "cached result must match the original")
}

// TestConcurrentInitializeReachesBackendOnce verifies that simultaneous
// handshakes are single-flighted: without the lock held across the first
// upstream round trip, every concurrent client would see an empty cache and
// race a second initialize to the backend.
func TestConcurrentInitializeReachesBackendOnce(t *testing.T) {
	t.Parallel()

	proxy, url := startInitTestProxy(t)
	backend := startFakeStdioBackend(t.Context(), proxy, func(req *jsonrpc2.Request) jsonrpc2.Message {
		// Slow the first handshake so the others pile up behind it.
		time.Sleep(150 * time.Millisecond)
		return initializeResultFor(req)
	})

	const clients = 5
	var wg sync.WaitGroup
	for i := range clients {
		wg.Go(func() {
			decoded := postInitialize(t, url, fmt.Sprintf("client-%d", i))
			assert.NotNil(t, decoded["result"], "every concurrent handshake should succeed")
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for concurrent handshakes to complete")
	}

	assert.Equal(t, 1, backend.count("initialize"),
		"concurrent handshakes must collapse into a single upstream initialize")
}

// TestFailedInitializeIsNotCached verifies that a backend error is not stored,
// so a transient failure cannot pin every future client to one bad response.
func TestFailedInitializeIsNotCached(t *testing.T) {
	t.Parallel()

	proxy, url := startInitTestProxy(t)

	var attempts int
	var mu sync.Mutex
	backend := startFakeStdioBackend(t.Context(), proxy, func(req *jsonrpc2.Request) jsonrpc2.Message {
		mu.Lock()
		attempts++
		first := attempts == 1
		mu.Unlock()
		if first {
			resp, _ := jsonrpc2.NewResponse(req.ID, nil, jsonrpc2.NewError(-32603, "backend not ready"))
			return resp
		}
		return initializeResultFor(req)
	})

	failed := postInitialize(t, url, "client-0")
	assert.NotNil(t, failed["error"], "first handshake should surface the backend error")

	succeeded := postInitialize(t, url, "client-1")
	assert.NotNil(t, succeeded["result"], "second handshake should be retried against the backend")

	assert.Equal(t, 2, backend.count("initialize"),
		"a failed handshake must not be cached; the next client must retry for real")
}

// TestDuplicateInitializedNotificationIsNotForwarded verifies that only the
// first notifications/initialized reaches the backend. The backend completed a
// single handshake, so a second initialized notification is out of lifecycle
// for it; later ones are acknowledged locally instead.
func TestDuplicateInitializedNotificationIsNotForwarded(t *testing.T) {
	t.Parallel()

	proxy, url := startInitTestProxy(t)
	backend := startFakeStdioBackend(t.Context(), proxy, func(req *jsonrpc2.Request) jsonrpc2.Message {
		return initializeResultFor(req)
	})

	for range 3 {
		resp, err := http.Post(url, "application/json", //nolint:gosec // test-local URL
			bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
		require.NoError(t, err)
		require.Equal(t, http.StatusAccepted, resp.StatusCode,
			"every client's initialized notification must still be acknowledged")
		_ = resp.Body.Close()
	}

	// The backend goroutine records asynchronously; give it a moment to drain.
	assert.Eventually(t, func() bool {
		return backend.count("notifications/initialized") >= 1
	}, 2*time.Second, 20*time.Millisecond, "the first notification should reach the backend")

	assert.Equal(t, 1, backend.count("notifications/initialized"),
		"only one initialized notification may reach the single stdio session")
}
