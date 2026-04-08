// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// TestWithPodHeadlessServiceStoresPodURL verifies that when WithPodHeadlessService is configured,
// an initialize response causes the session's backend_url to be a pod-specific headless DNS URL
// (e.g. http://myserver-N.mcp-myserver-headless.default.svc.cluster.local:<port>) rather than
// the ClusterIP targetURI.
func TestWithPodHeadlessServiceStoresPodURL(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New().String()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", sessionID)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	const (
		statefulSetName = "myserver"
		serviceName     = "mcp-myserver-headless"
		namespace       = "default"
		replicas        = int32(3)
	)

	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, backend.URL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, "", false,
		nil,
		WithPodHeadlessService(statefulSetName, serviceName, namespace, replicas),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})
	require.NoError(t, proxy.Start(ctx))
	addr := proxy.listener.Addr().String()

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

	// The URL must use headless DNS, not the ClusterIP backend.URL.
	assert.NotEqual(t, backend.URL, backendURL, "backend_url should be pod-specific, not ClusterIP")
	assert.Contains(t, backendURL, ".mcp-myserver-headless.default.svc.cluster.local",
		"backend_url should contain the headless service DNS suffix")
	assert.Contains(t, backendURL, "myserver-",
		"backend_url should contain the StatefulSet pod name prefix")

	// Ordinal must be in range [0, replicas).
	parsedURL, err := url.Parse(backendURL)
	require.NoError(t, err)
	host := parsedURL.Hostname()
	// host is e.g. "myserver-2.mcp-myserver-headless.default.svc.cluster.local"
	var ordinal int
	n, err := fmt.Sscanf(host, "myserver-%d.", &ordinal)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.GreaterOrEqual(t, ordinal, 0)
	assert.Less(t, ordinal, int(replicas))
}

// TestWithPodHeadlessServiceSingleReplica verifies that WithPodHeadlessService with replicas=1
// routes to ordinal 0 via headless DNS, not the static ClusterIP targetURI.
func TestWithPodHeadlessServiceSingleReplica(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New().String()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", sessionID)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	proxy := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, backend.URL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, "", false,
		nil,
		WithPodHeadlessService("myserver", "mcp-myserver-headless", "default", 1),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = proxy.Stop(stopCtx)
	})
	require.NoError(t, proxy.Start(ctx))
	addr := proxy.listener.Addr().String()

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

	// With replicas=1, headless DNS is still used — ordinal is always 0.
	assert.NotEqual(t, backend.URL, backendURL, "backend_url should use headless DNS, not static ClusterIP")
	assert.Contains(t, backendURL, "myserver-0.mcp-myserver-headless.default.svc.cluster.local",
		"single-replica should always route to pod ordinal 0 via headless DNS")
}
