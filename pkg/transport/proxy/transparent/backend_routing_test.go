// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/session"
)

// startProxy starts a TransparentProxy for testing and returns its listen address.
func startProxy(t *testing.T, targetURL string) (proxy *TransparentProxy, addr string) {
	t.Helper()
	proxy = NewTransparentProxyWithOptions(
		"127.0.0.1", 0, targetURL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, "", false,
		nil,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})
	require.NoError(t, proxy.Start(ctx))
	addr = proxy.listener.Addr().String()
	return proxy, addr
}

// TestRewriteRoutesViaBackendURL verifies that a request with a session whose
// metadata contains "backend_url" is forwarded to that URL, not the static targetURI.
func TestRewriteRoutesViaBackendURL(t *testing.T) {
	t.Parallel()

	var specificHit atomic.Bool
	var specificPath atomic.Value
	specificBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		specificHit.Store(true)
		specificPath.Store(r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer specificBackend.Close()

	var defaultHit atomic.Bool
	defaultBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defaultHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer defaultBackend.Close()

	proxy, addr := startProxy(t, defaultBackend.URL)

	// Pre-populate a session with backend_url pointing to specificBackend
	sessionID := uuid.New().String()
	sess := session.NewProxySession(sessionID)
	sess.SetMetadata(sessionMetadataBackendURL, specificBackend.URL)
	require.NoError(t, proxy.sessionManager.AddSession(sess))

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	require.True(t, specificHit.Load(), "request should have been forwarded to specificBackend")
	assert.False(t, defaultHit.Load(), "request should NOT have been forwarded to defaultBackend")
	assert.Equal(t, "/mcp", specificPath.Load(), "original request path should be preserved after backend_url rewrite")
}

// TestRewriteFallsBackToStaticTargetWhenNoBackendURL verifies that a request
// with a session that has no "backend_url" metadata is forwarded to the static targetURI.
func TestRewriteFallsBackToStaticTargetWhenNoBackendURL(t *testing.T) {
	t.Parallel()

	var defaultHit atomic.Bool
	defaultBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defaultHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer defaultBackend.Close()

	proxy, addr := startProxy(t, defaultBackend.URL)

	// Session with no backend_url — should fall back to static target
	sessionID := uuid.New().String()
	sess := session.NewProxySession(sessionID)
	require.NoError(t, proxy.sessionManager.AddSession(sess))

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.True(t, defaultHit.Load(), "request should have been forwarded to the static defaultBackend")
}

// TestRewriteFallsBackToStaticTargetForNonAbsoluteBackendURL verifies that a
// session whose backend_url is a relative/schemeless URL (e.g. "mcp-server-0:8080")
// does NOT overwrite the outbound URL's scheme/host; the request falls back to
// the static targetURI instead of silently routing to an empty host.
func TestRewriteFallsBackToStaticTargetForNonAbsoluteBackendURL(t *testing.T) {
	t.Parallel()

	var defaultHit atomic.Bool
	defaultBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defaultHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer defaultBackend.Close()

	proxy, addr := startProxy(t, defaultBackend.URL)

	// Session with a schemeless "host:port" backend_url — url.Parse succeeds
	// but returns empty Scheme and Host, which must not corrupt the outbound URL.
	sessionID := uuid.New().String()
	sess := session.NewProxySession(sessionID)
	sess.SetMetadata(sessionMetadataBackendURL, "mcp-server-0:8080")
	require.NoError(t, proxy.sessionManager.AddSession(sess))

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.True(t, defaultHit.Load(), "request should have fallen back to the static defaultBackend")
}

// TestRoundTripReturns404ForUnknownSession verifies that a non-initialize
// request with an unrecognized session ID is rejected with HTTP 404 and a
// JSON-RPC error body containing code -32001 for MCP session recovery.
func TestRoundTripReturns404ForUnknownSession(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	tt := &tracingTransport{base: http.DefaultTransport, p: NewTransparentProxyWithOptions(
		"localhost", 0, backend.URL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, "", false,
		nil,
	)}

	req, err := http.NewRequest(http.MethodPost, backend.URL+"/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", uuid.New().String()) // unknown

	resp, err := tt.RoundTrip(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Contains(t, string(body), `"code":-32001`)
}

// TestRoundTripAllowsInitializeWithUnknownSession verifies that an initialize
// call is forwarded even when its Mcp-Session-Id is not yet in the session store.
func TestRoundTripAllowsInitializeWithUnknownSession(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", uuid.New().String())
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	tt := &tracingTransport{base: http.DefaultTransport, p: NewTransparentProxyWithOptions(
		"localhost", 0, backend.URL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, "", false,
		nil,
	)}

	req, err := http.NewRequest(http.MethodPost, backend.URL+"/mcp",
		strings.NewReader(`{"method":"initialize"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", uuid.New().String()) // unknown but initialize

	resp, err := tt.RoundTrip(req)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusBadRequest, resp.StatusCode)
	_ = resp.Body.Close()
}

// TestRoundTripAllowsBatchInitializeWithUnknownSession verifies that a JSON-RPC
// batch payload containing an initialize call is forwarded even when its
// Mcp-Session-Id is not yet in the session store.
func TestRoundTripAllowsBatchInitializeWithUnknownSession(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", uuid.New().String())
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	tt := &tracingTransport{base: http.DefaultTransport, p: NewTransparentProxyWithOptions(
		"localhost", 0, backend.URL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, "", false,
		nil,
	)}

	req, err := http.NewRequest(http.MethodPost, backend.URL+"/mcp",
		strings.NewReader(`[{"method":"initialize"},{"method":"tools/list"}]`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", uuid.New().String()) // unknown but batch contains initialize

	resp, err := tt.RoundTrip(req)
	require.NoError(t, err)
	require.NotEqual(t, http.StatusBadRequest, resp.StatusCode)
	_ = resp.Body.Close()
}

// TestRoundTripStoresBackendURLOnInitialize verifies that when an initialize
// response returns Mcp-Session-Id, the created session's backend_url = p.targetURI.
func TestRoundTripStoresBackendURLOnInitialize(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New().String()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", sessionID)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	proxy, addr := startProxy(t, backend.URL)

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/mcp",
		strings.NewReader(`{"method":"initialize"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	sess, ok := proxy.sessionManager.Get(normalizeSessionID(sessionID))
	require.True(t, ok, "session should have been created by RoundTrip")
	backendURL, ok := sess.GetMetadataValue(sessionMetadataBackendURL)
	require.True(t, ok, "session should have backend_url metadata")
	assert.Equal(t, backend.URL, backendURL)
}

// TestRoundTripStoresInitBodyOnInitialize verifies that the raw JSON-RPC initialize
// request body is stored in session metadata so the proxy can transparently
// re-initialize the backend session if the pod is later replaced.
func TestRoundTripStoresInitBodyOnInitialize(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New().String()
	const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", sessionID)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	proxy, addr := startProxy(t, backend.URL)

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/mcp",
		strings.NewReader(initBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	sess, ok := proxy.sessionManager.Get(normalizeSessionID(sessionID))
	require.True(t, ok, "session should have been created")
	stored, exists := sess.GetMetadataValue(sessionMetadataInitBody)
	require.True(t, exists, "init_body should be stored in session metadata")
	assert.Equal(t, initBody, stored)
}

// TestRoundTripReinitializesOnBackend404 verifies that when the backend pod returns
// 404 (session lost after restart on the same IP), the proxy transparently
// re-initializes the backend session and replays the original request — client sees 200.
func TestRoundTripReinitializesOnBackend404(t *testing.T) {
	t.Parallel()

	// staleBackend simulates a pod that has lost its in-memory session state.
	var staleHit atomic.Int32
	staleBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staleHit.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer staleBackend.Close()

	// freshBackend simulates a healthy pod: returns a session ID on initialize
	// and 200 for all other requests.
	freshSessionID := uuid.New().String()
	var freshHit atomic.Int32
	freshBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		freshHit.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"initialize"`) {
			w.Header().Set("Mcp-Session-Id", freshSessionID)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer freshBackend.Close()

	// targetURI (ClusterIP) points to freshBackend; the session has staleBackend as backend_url.
	proxy, addr := startProxy(t, freshBackend.URL)

	clientSessionID := uuid.New().String()
	sess := session.NewProxySession(clientSessionID)
	sess.SetMetadata(sessionMetadataBackendURL, staleBackend.URL)
	sess.SetMetadata(sessionMetadataInitBody, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	require.NoError(t, proxy.sessionManager.AddSession(sess))

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", clientSessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "client should see 200 after transparent re-init")
	assert.GreaterOrEqual(t, staleHit.Load(), int32(1), "stale backend should have been hit")
	assert.GreaterOrEqual(t, freshHit.Load(), int32(2), "fresh backend should receive initialize + replay")

	// Session should now have backend_sid mapping to the new backend session.
	updated, ok := proxy.sessionManager.Get(normalizeSessionID(clientSessionID))
	require.True(t, ok, "session should still exist after re-init")
	backendSID, exists := updated.GetMetadataValue(sessionMetadataBackendSID)
	require.True(t, exists, "backend_sid should be set after re-init")
	assert.Equal(t, freshSessionID, backendSID, "backend_sid must be the raw value the backend issued, not normalized")
}

// TestRoundTripReinitializesPreservesNonUUIDBackendSessionID verifies that when the
// backend issues a non-UUID Mcp-Session-Id on re-initialization, the proxy stores
// and forwards the raw value — not a UUID v5 hash of it — on all subsequent requests.
//
// The normalization bug only manifests on the request AFTER the replay: the replay
// sets Mcp-Session-Id directly from newBackendSID (bypassing Rewrite), but subsequent
// requests go through the Rewrite closure which reads backend_sid from session metadata.
// If backend_sid was stored as normalizeSessionID(newBackendSID), Rewrite would send
// the wrong (hashed) value and the backend would reject every subsequent request.
func TestRoundTripReinitializesPreservesNonUUIDBackendSessionID(t *testing.T) {
	t.Parallel()

	// Non-UUID opaque token, as some MCP servers issue.
	const nonUUIDSessionID = "opaque-session-token-abc123"

	staleBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer staleBackend.Close()

	// receivedSIDs tracks Mcp-Session-Id values arriving on non-initialize requests,
	// in order. Index 0 = replay (direct from reinitializeAndReplay), index 1 = second
	// client request (routed through Rewrite reading backend_sid from session metadata).
	var (
		receivedMu   sync.Mutex
		receivedSIDs []string
	)
	freshBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"initialize"`) {
			w.Header().Set("Mcp-Session-Id", nonUUIDSessionID)
			w.WriteHeader(http.StatusOK)
			return
		}
		receivedMu.Lock()
		receivedSIDs = append(receivedSIDs, r.Header.Get("Mcp-Session-Id"))
		receivedMu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer freshBackend.Close()

	proxy, addr := startProxy(t, freshBackend.URL)

	clientSessionID := uuid.New().String()
	sess := session.NewProxySession(clientSessionID)
	sess.SetMetadata(sessionMetadataBackendURL, staleBackend.URL)
	sess.SetMetadata(sessionMetadataInitBody, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	require.NoError(t, proxy.sessionManager.AddSession(sess))

	doRequest := func() *http.Response {
		ctx := context.Background()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://"+addr+"/mcp",
			strings.NewReader(`{"method":"tools/list"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Mcp-Session-Id", clientSessionID)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return resp
	}

	// First request: triggers re-init. The replay (inside reinitializeAndReplay) sets
	// Mcp-Session-Id directly, so receivedSIDs[0] is always the raw value regardless
	// of what is stored in session metadata.
	resp1 := doRequest()
	_ = resp1.Body.Close()
	require.Equal(t, http.StatusOK, resp1.StatusCode)

	// Second request: goes through the Rewrite closure, which reads backend_sid from
	// session metadata. This is where the normalization bug manifests — if backend_sid
	// was stored as normalizeSessionID(nonUUIDSessionID), Rewrite would forward the
	// wrong hashed value and receivedSIDs[1] would not equal nonUUIDSessionID.
	resp2 := doRequest()
	_ = resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	receivedMu.Lock()
	defer receivedMu.Unlock()
	require.Len(t, receivedSIDs, 2, "fresh backend should have received replay + second request")
	assert.Equal(t, nonUUIDSessionID, receivedSIDs[0], "replay must forward raw non-UUID session ID")
	assert.Equal(t, nonUUIDSessionID, receivedSIDs[1], "subsequent request via Rewrite must forward raw non-UUID session ID")
}

// TestRoundTripReinitializesOnDialError verifies that when the proxy cannot reach
// the stored pod IP (dial error — pod rescheduled to a new IP), it transparently
// re-initializes the backend session via the ClusterIP and replays the original
// request — the client sees a 200.
func TestRoundTripReinitializesOnDialError(t *testing.T) {
	t.Parallel()

	// Create a server and immediately close it so its URL refuses connections.
	dead := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	freshSessionID := uuid.New().String()
	var freshHit atomic.Int32
	freshBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		freshHit.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"initialize"`) {
			w.Header().Set("Mcp-Session-Id", freshSessionID)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer freshBackend.Close()

	proxy, addr := startProxy(t, freshBackend.URL)

	clientSessionID := uuid.New().String()
	sess := session.NewProxySession(clientSessionID)
	sess.SetMetadata(sessionMetadataBackendURL, deadURL)
	sess.SetMetadata(sessionMetadataInitBody, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	require.NoError(t, proxy.sessionManager.AddSession(sess))

	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://"+addr+"/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", clientSessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "client should see 200 after transparent re-init on dial error")
	assert.GreaterOrEqual(t, freshHit.Load(), int32(2), "fresh backend should receive initialize + replay")
}
