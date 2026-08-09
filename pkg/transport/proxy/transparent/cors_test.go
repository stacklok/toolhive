// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/middleware/origin"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// corsBackend mimics a containerized MCP server behind `thv run`: POST and GET
// succeed, OPTIONS returns 405 (no CORS handling of its own).
func corsBackend(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "event: endpoint\ndata: /sse?sessionId=abc123\n\n")
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startCORSProxy boots a TransparentProxy in front of backendURL with the origin
// middleware allow-listing allowedOrigin (mirroring `thv run`/`thv proxy`) and
// any extra functional options.
func startCORSProxy(t *testing.T, backendURL, allowedOrigin string, opts ...Option) *TransparentProxy {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	middlewares := []types.NamedMiddleware{{
		Name:     origin.MiddlewareType,
		Function: origin.NewHandler([]string{allowedOrigin}),
	}}
	p := NewTransparentProxyWithOptions(
		"127.0.0.1",
		0,
		backendURL,
		nil, // prometheusHandler
		nil, // authInfoHandler
		nil, // prefixHandlers
		false,
		false, // isRemote
		"sse",
		nil, // onHealthCheckFailed
		nil, // onUnauthorizedResponse
		"",  // endpointPrefix
		false,
		middlewares,
		opts...,
	)
	if err := p.Start(ctx); err != nil {
		t.Fatalf("proxy.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = p.Stop(stopCtx)
	})
	return p
}

func doCORSRequest(t *testing.T, method, url, allowedOrigin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	require.NoError(t, err)
	if allowedOrigin != "" {
		req.Header.Set("Origin", allowedOrigin)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp
}

// The core issue-#4297 regression pair: with WithAllowedOrigins the proxy
// answers the preflight itself (204 + ACAO) instead of relaying the backend's
// 405, and an allowed-origin GET carries ACAO.
func TestCORSProxy_WithAllowedOrigins(t *testing.T) {
	t.Parallel()
	const allowedOrigin = "http://localhost:6274"
	backend := corsBackend(t)
	proxy := startCORSProxy(t, backend.URL, allowedOrigin, WithAllowedOrigins([]string{allowedOrigin}))
	base := "http://" + proxy.ListenerAddr()

	preflight := doCORSRequest(t, http.MethodOptions, base+"/mcp", allowedOrigin)
	assert.Equal(t, http.StatusNoContent, preflight.StatusCode,
		"preflight must be answered by the proxy, not relayed as the backend's 405")
	assert.Equal(t, allowedOrigin, preflight.Header.Get("Access-Control-Allow-Origin"))
	t.Logf("OPTIONS /mcp + allowed origin -> status=%d ACAO=%q", preflight.StatusCode, preflight.Header.Get("Access-Control-Allow-Origin"))

	get := doCORSRequest(t, http.MethodGet, base+"/mcp", allowedOrigin)
	assert.Equal(t, http.StatusOK, get.StatusCode)
	assert.Equal(t, allowedOrigin, get.Header.Get("Access-Control-Allow-Origin"))
	t.Logf("GET /mcp + allowed origin -> status=%d ACAO=%q", get.StatusCode, get.Header.Get("Access-Control-Allow-Origin"))
}

// Control: without WithAllowedOrigins the behavior is unchanged — the OPTIONS
// preflight is forwarded and the backend's 405 is relayed, with no ACAO.
func TestCORSProxy_WithoutOptionIsUnchanged(t *testing.T) {
	t.Parallel()
	const allowedOrigin = "http://localhost:6274"
	backend := corsBackend(t)
	proxy := startCORSProxy(t, backend.URL, allowedOrigin)
	base := "http://" + proxy.ListenerAddr()

	preflight := doCORSRequest(t, http.MethodOptions, base+"/mcp", allowedOrigin)
	assert.Equal(t, http.StatusMethodNotAllowed, preflight.StatusCode,
		"without the option the backend 405 must be preserved")
	assert.Equal(t, "", preflight.Header.Get("Access-Control-Allow-Origin"))

	get := doCORSRequest(t, http.MethodGet, base+"/mcp", allowedOrigin)
	assert.Equal(t, http.StatusOK, get.StatusCode)
	assert.Equal(t, "", get.Header.Get("Access-Control-Allow-Origin"))
}

// The origin middleware must remain the authoritative request-time gate: a
// disallowed origin still gets 403 and no ACAO, even with CORS active.
func TestCORSProxy_DisallowedOriginStill403(t *testing.T) {
	t.Parallel()
	const allowedOrigin = "http://localhost:6274"
	backend := corsBackend(t)
	proxy := startCORSProxy(t, backend.URL, allowedOrigin, WithAllowedOrigins([]string{allowedOrigin}))
	base := "http://" + proxy.ListenerAddr()

	get := doCORSRequest(t, http.MethodGet, base+"/mcp", "http://evil.example")
	assert.Equal(t, http.StatusForbidden, get.StatusCode, "origin middleware must still reject disallowed origins")
	assert.Equal(t, "", get.Header.Get("Access-Control-Allow-Origin"))
	assert.True(t, strings.Contains(strings.ToLower(get.Header.Get("Content-Type")), "application/json"))
}
