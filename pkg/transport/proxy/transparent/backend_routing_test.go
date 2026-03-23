// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"net/http"
	"net/http/httptest"
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
func startProxy(t *testing.T, targetURL string, opts ...Option) (proxy *TransparentProxy, addr string) {
	t.Helper()
	proxy = NewTransparentProxyWithOptions(
		"127.0.0.1", 0, targetURL,
		nil, nil, nil,
		false, false, "sse",
		nil, nil, "", false,
		nil,
		opts...,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(func() {
		cancel()
		_ = proxy.Stop(ctx)
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
	specificBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		specificHit.Store(true)
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
	sess.SetMetadata("backend_url", specificBackend.URL)
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

	assert.True(t, specificHit.Load(), "request should have been forwarded to specificBackend")
	assert.False(t, defaultHit.Load(), "request should NOT have been forwarded to defaultBackend")
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
	assert.Equal(t, backend.URL, sess.GetMetadata()["backend_url"])
}
