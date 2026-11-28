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
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/mcp/instructions"
	"github.com/stacklok/toolhive/pkg/transport/types"
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
	return initializeResultWithVersion(req, "1.0.0")
}

// initializeResultWithVersion is initializeResultFor with the given version.
func initializeResultWithVersion(req *jsonrpc2.Request, version string) jsonrpc2.Message {
	resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{
		"protocolVersion": testProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "fake-stdio-server", "version": version},
	}, nil)
	return resp
}

// strictInitializeResponder returns a backend responder that reproduces
// go-sdk v1.7+ server-session semantics: the first initialize is answered with
// an InitializeResult and every later one is rejected with the same
// `duplicate "initialize" received` error ServerSession.initialize returns
// (go-sdk mcp/server.go:1978). go-sdk rejects a repeated
// notifications/initialized the same way (mcp/server.go:1424).
//
// Tests use this rather than a permissive stub so they assert the outcome that
// actually matters -- a later client's handshake succeeds -- instead of only
// counting how many messages reached the backend. A duplicate that escapes the
// cache fails the handshake here exactly as it does against a real server.
func strictInitializeResponder() func(*jsonrpc2.Request) jsonrpc2.Message {
	return strictInitializeResponderWithVersion("1.0.0")
}

// strictInitializeResponderWithVersion is strictInitializeResponder whose
// InitializeResult carries the given serverInfo version.
func strictInitializeResponderWithVersion(version string) func(*jsonrpc2.Request) jsonrpc2.Message {
	var mu sync.Mutex
	var handshakeDone bool
	return func(req *jsonrpc2.Request) jsonrpc2.Message {
		if req.Method != "initialize" {
			resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
			return resp
		}
		mu.Lock()
		defer mu.Unlock()
		if handshakeDone {
			resp, _ := jsonrpc2.NewResponse(req.ID, nil,
				jsonrpc2.NewError(-32603, `duplicate "initialize" received`))
			return resp
		}
		handshakeDone = true
		return initializeResultWithVersion(req, version)
	}
}

// startInitTestProxy starts a proxy with middlewares on a free port and
// returns it with its /mcp endpoint URL.
func startInitTestProxy(t *testing.T, middlewares ...types.NamedMiddleware) (*HTTPProxy, string) {
	t.Helper()
	port := getFreePort(t)
	proxy := NewHTTPProxy("localhost", port, nil, middlewares)
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

// postInitializedNotification sends notifications/initialized from a new
// session and requires it to be acknowledged.
func postInitializedNotification(t *testing.T, proxy *HTTPProxy, url string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url,
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
	require.NoError(t, err)
	sid := uuid.NewString()
	require.NoError(t, proxy.ensureSession(req, sid))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // test-local URL
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, resp.StatusCode,
		"every client's initialized notification must still be acknowledged")
	_ = resp.Body.Close()
}

// TestEveryClientHandshakeSucceedsAgainstStrictBackend verifies the behaviour
// #5890 reports: with a backend that rejects a repeated initialize, every
// client after the first used to fail. Only the first handshake may reach the
// single stdio session; the rest are answered from the cached InitializeResult
// with their own JSON-RPC id echoed back.
func TestEveryClientHandshakeSucceedsAgainstStrictBackend(t *testing.T) {
	t.Parallel()

	proxy, url := startInitTestProxy(t)
	backend := startFakeStdioBackend(t.Context(), proxy, strictInitializeResponder())

	var results []any
	for i := range 3 {
		decoded := postInitialize(t, url, fmt.Sprintf("client-%d", i))
		assert.Equal(t, fmt.Sprintf("client-%d", i), decoded["id"],
			"each client must get its own JSON-RPC id back, not the first client's")
		assert.Nil(t, decoded["error"],
			"no client may see the backend's duplicate-initialize rejection")
		require.NotNil(t, decoded["result"], "handshake should succeed")
		results = append(results, decoded["result"])
	}

	assert.Equal(t, 1, backend.count("initialize"),
		"only the first handshake may reach the single stdio session")
	assert.Equal(t, results[0], results[1], "cached result must match the original")
	assert.Equal(t, results[0], results[2], "cached result must match the original")
}

// TestInitializeCacheKeepsTheBackendResultUnderInstructions verifies that the
// instructions middleware sets the instructions of every handshake, including
// those the cache answers, and that the cache keeps the backend's own result:
// the configured text is applied once per response, never compounded.
func TestInitializeCacheKeepsTheBackendResultUnderInstructions(t *testing.T) {
	t.Parallel()

	middleware, err := mcp.NewInstructionsMiddleware(mcp.InstructionsMiddlewareParams{
		Instructions: instructions.Config{Value: "policy"},
	})
	require.NoError(t, err)
	proxy, url := startInitTestProxy(t,
		types.NamedMiddleware{Name: mcp.ParserMiddlewareType, Function: mcp.ParsingMiddleware},
		types.NamedMiddleware{Name: mcp.InstructionsMiddlewareType, Function: middleware},
	)
	backend := startFakeStdioBackend(t.Context(), proxy, func(req *jsonrpc2.Request) jsonrpc2.Message {
		resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{
			"protocolVersion": testProtocolVersion,
			"capabilities":    map[string]any{},
			"serverInfo":      map[string]any{"name": "fake-stdio-server", "version": "1.0.0"},
			"instructions":    "server text",
		}, nil)
		return resp
	})

	for i := range 3 {
		decoded := postInitialize(t, url, fmt.Sprintf("client-%d", i))
		result, ok := decoded["result"].(map[string]any)
		require.True(t, ok, "handshake should succeed: %+v", decoded)
		assert.Equal(t, "server text\n\npolicy", result["instructions"])
	}
	assert.Equal(t, 1, backend.count("initialize"), "later handshakes are answered from the cache")
}

// TestConcurrentInitializeReachesBackendOnce verifies that simultaneous
// handshakes are single-flighted: without the lock held across the first
// upstream round trip, every concurrent client would see an empty cache and
// race a second initialize to the backend.
func TestConcurrentInitializeReachesBackendOnce(t *testing.T) {
	t.Parallel()

	proxy, url := startInitTestProxy(t)
	strict := strictInitializeResponder()
	backend := startFakeStdioBackend(t.Context(), proxy, func(req *jsonrpc2.Request) jsonrpc2.Message {
		// Slow the first handshake so the others pile up behind it.
		time.Sleep(150 * time.Millisecond)
		return strict(req)
	})

	const clients = 5
	var wg sync.WaitGroup
	for i := range clients {
		wg.Go(func() {
			id := fmt.Sprintf("client-%d", i)
			decoded := postInitialize(t, url, id)
			assert.Nil(t, decoded["error"],
				"a handshake that raced past the cache would be rejected by the backend")
			assert.NotNil(t, decoded["result"], "every concurrent handshake should succeed")
			// Waiters share one singleflight result, which carries the id of
			// whichever caller performed the forward. Each response must be
			// re-addressed to its own caller.
			assert.Equal(t, id, decoded["id"],
				"a waiter must never receive another caller's JSON-RPC id")
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
	backend := startFakeStdioBackend(t.Context(), proxy, strictInitializeResponder())

	for range 3 {
		postInitializedNotification(t, proxy, url)
	}

	// The backend goroutine records asynchronously; give it a moment to drain.
	assert.Eventually(t, func() bool {
		return backend.count("notifications/initialized") >= 1
	}, 2*time.Second, 20*time.Millisecond, "the first notification should reach the backend")

	assert.Equal(t, 1, backend.count("notifications/initialized"),
		"only one initialized notification may reach the single stdio session")
}

// TestInitializeSurvivesFirstCallerDisconnect verifies that a client giving up
// mid-handshake does not cancel the shared upstream call for everyone else.
//
// The handshake is collapsed by singleflight, and singleflight runs the shared
// function with whichever context the first caller supplied. Passing that
// context straight through would let one client's disconnect fail every waiter,
// so interceptInitialize detaches it with context.WithoutCancel.
func TestInitializeSurvivesFirstCallerDisconnect(t *testing.T) {
	t.Parallel()

	proxy, url := startInitTestProxy(t)
	strict := strictInitializeResponder()
	backend := startFakeStdioBackend(t.Context(), proxy, func(req *jsonrpc2.Request) jsonrpc2.Message {
		// Long enough that the first caller aborts while the handshake is in
		// flight, short enough to keep the test quick.
		time.Sleep(400 * time.Millisecond)
		return strict(req)
	})

	// First caller: gives up well before the backend replies.
	var wg sync.WaitGroup
	wg.Go(func() {
		ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
		defer cancel()
		body := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%q,"method":"initialize","params":{"protocolVersion":%q}}`,
			"quitter", testProtocolVersion)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(body)))
		if !assert.NoError(t, err) {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req) //nolint:bodyclose // err path returns no body
		assert.Error(t, err, "the first caller is expected to abort mid-handshake")
		if err == nil {
			_ = resp.Body.Close()
		}
	})

	// Give the first caller time to enter the flight and then abort.
	time.Sleep(150 * time.Millisecond)

	// Second caller arrives while the shared handshake is still running.
	decoded := postInitialize(t, url, "survivor")
	assert.Nil(t, decoded["error"],
		"the shared handshake must not be cancelled by the first caller disconnecting")
	require.NotNil(t, decoded["result"], "second caller should still complete its handshake")
	assert.Equal(t, "survivor", decoded["id"])

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for the aborting caller to finish")
	}

	assert.Equal(t, 1, backend.count("initialize"),
		"the disconnect must not cause a second upstream handshake")
}

// TestReinitializeBackend verifies each outcome of the replay after a
// re-attach, and that the next client's handshake still succeeds.
func TestReinitializeBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		priorHandshake  bool // a client completes a handshake before the re-attach
		replaced        bool // the re-attach reached a new backend process
		dropReplay      bool // the backend never answers the replayed initialize
		wantVersion     string
		wantInitialize  int // initialize requests the backend saw, the next client's included
		wantInitialized int
	}{
		{
			name: "replaced backend is initialized again", priorHandshake: true, replaced: true,
			wantVersion: "2.0.0", wantInitialize: 2, wantInitialized: 1,
		},
		{
			name: "backend that kept running keeps the cache", priorHandshake: true,
			wantVersion: "1.0.0", wantInitialize: 2, wantInitialized: 0,
		},
		{
			name:        "nothing to replay before the first handshake",
			wantVersion: "1.0.0", wantInitialize: 1, wantInitialized: 0,
		},
		{
			// The next client's notifications/initialized is forwarded again.
			name: "unanswered replay sends the next handshake upstream", priorHandshake: true, replaced: true,
			dropReplay: true, wantVersion: "2.0.0", wantInitialize: 3, wantInitialized: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy, url := startInitTestProxy(t)
			var process atomic.Pointer[func(*jsonrpc2.Request) jsonrpc2.Message]
			first := strictInitializeResponder()
			process.Store(&first)
			var dropNext atomic.Bool
			backend := startFakeStdioBackend(t.Context(), proxy, func(req *jsonrpc2.Request) jsonrpc2.Message {
				if dropNext.CompareAndSwap(true, false) {
					return nil
				}
				return (*process.Load())(req)
			})

			if tt.priorHandshake {
				require.NotNil(t, postInitialize(t, url, "client-0")["result"])
			}
			if tt.replaced {
				next := strictInitializeResponderWithVersion("2.0.0")
				process.Store(&next)
			}
			dropNext.Store(tt.dropReplay)

			timeout := 5 * time.Second
			if tt.dropReplay {
				timeout = 300 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), timeout)
			defer cancel()
			proxy.ReinitializeBackend(ctx)

			decoded := postInitialize(t, url, "client-1")
			assert.Nil(t, decoded["error"], "the handshake after the re-attach must succeed")
			result, ok := decoded["result"].(map[string]any)
			require.True(t, ok, "handshake should return a result")
			assert.Equal(t, tt.wantVersion, result["serverInfo"].(map[string]any)["version"],
				"the result must come from the backend process now attached")
			if tt.dropReplay {
				postInitializedNotification(t, proxy, url)
			}

			assert.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, tt.wantInitialize, backend.count("initialize"))
				assert.Equal(c, tt.wantInitialized, backend.count("notifications/initialized"))
			}, 2*time.Second, 20*time.Millisecond)
		})
	}
}

// TestMalformedInitializeReplyBecomesInternalError pins an intentional
// behaviour change made when the flight started sharing an id-free handshake
// rather than a whole response.
//
// A JSON-RPC success carrying neither an error nor a result is malformed for
// initialize -- InitializeResult requires protocolVersion, capabilities and
// serverInfo. It used to be forwarded to the client verbatim; it is now
// reported as an internal error, and nothing is cached, so the next client
// still gets a real handshake attempt.
func TestMalformedInitializeReplyBecomesInternalError(t *testing.T) {
	t.Parallel()

	proxy, url := startInitTestProxy(t)
	var mu sync.Mutex
	var calls int
	backend := startFakeStdioBackend(t.Context(), proxy, func(req *jsonrpc2.Request) jsonrpc2.Message {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			// Success envelope with no result and no error.
			return &jsonrpc2.Response{ID: req.ID}
		}
		return initializeResultFor(req)
	})

	malformed := postInitialize(t, url, "client-0")
	require.NotNil(t, malformed["error"], "a result-less success must not be replayed to the client")
	assert.Equal(t, "client-0", malformed["id"])

	recovered := postInitialize(t, url, "client-1")
	assert.Nil(t, recovered["error"], "the malformed reply must not be cached")
	require.NotNil(t, recovered["result"], "the next client should get a real handshake")

	assert.Equal(t, 2, backend.count("initialize"),
		"a malformed reply must leave the cache empty so the next client retries")
}
