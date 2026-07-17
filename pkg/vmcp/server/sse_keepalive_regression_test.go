// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

// TestRegression_SSEKeepAlive_PeriodicBytesOnIdleStream verifies that an idle
// SSE stream opened against the Serve-path streamable server keeps producing
// bytes (a keep-alive ping or heartbeat) within a short window. A regression
// that drops the heartbeat wiring will cause this test to fail: no bytes arrive
// on an idle GET stream, so proxies/load balancers eventually close it.
//
// The streamable server mounts the SDK keep-alive over the configured
// HeartbeatInterval, so the test sets a short interval (100ms) and reads with a
// 500ms deadline — generous relative to the interval but tight enough that a
// missing keep-alive is caught rather than hanging the suite.
func TestRegression_SSEKeepAlive_PeriodicBytesOnIdleStream(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	testTool := vmcp.Tool{Name: "keepalive-tool", Description: "keep-alive regression anchor"}
	factory, _ := newToolSessionFactory(t, ctrl, []vmcp.Tool{testTool})
	fc := &fakeCore{tools: []vmcp.Tool{testTool}}

	srv, err := Serve(context.Background(), fc, &ServerConfig{
		SessionTTL:           time.Minute,
		HeartbeatInterval:    100 * time.Millisecond,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	// Serve through the real Handler wiring rather than a hand-built streamable
	// server. This exercises the ServerConfig.HeartbeatInterval → Config →
	// Handler → WithHeartbeatInterval path that server.go adds, so removing that
	// wiring line in Handler regresses this test (no keep-alive bytes arrive).
	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	// initialize → obtain a session ID for the subsequent GET stream.
	initResp := postServeMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "keepalive-test", "version": "1.0"},
		},
	}, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode, "initialize should succeed")
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "session ID should be returned in Mcp-Session-Id header")

	// Open a long-lived SSE GET stream. The stream must stay open and emit
	// keep-alive bytes even though no client request is in flight.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET stream should open with 200")

	// Read with a 500ms deadline. The keep-alive interval is 100ms, so at least
	// one ping/heartbeat should land within the deadline; a missing keep-alive
	// surfaces as a read timeout with zero bytes — the regression. The streamable
	// server's body is not a net.Conn, so SetReadDeadline is unavailable; drive
	// the read on a goroutine and race it against a timer.
	type readResult struct {
		data []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := resp.Body.Read(buf)
		ch <- readResult{data: buf[:n], err: err}
	}()

	var got []byte
	select {
	case r := <-ch:
		got = r.data
	case <-time.After(500 * time.Millisecond):
	}

	// At least some bytes must arrive — the keep-alive ping, an SSE comment
	// (`:` prefix), or a JSON-RPC ping request frame. Zero bytes within the
	// window means the keep-alive wiring is broken; fail loudly as a regression.
	if len(got) == 0 {
		t.Fatal("no bytes received on idle SSE stream within 500ms; keep-alive/heartbeat " +
			"is not emitting periodic bytes (regression: proxies will close idle streams)")
	}

	body := string(got)
	t.Logf("received keep-alive bytes on idle SSE stream: %q", body)
	// Accept any of: a JSON-RPC ping request, an SSE comment, or any SSE data
	// frame. The point is that bytes flow; their exact form is shim-defined.
	hasPing := strings.Contains(body, `"method":"ping"`) ||
		strings.Contains(body, `"method": "ping"`)
	hasSSEComment := strings.Contains(body, ":")
	hasDataFrame := strings.Contains(body, "data:")
	assert.True(t, hasPing || hasSSEComment || hasDataFrame,
		"keep-alive bytes should be an SSE comment, a data frame, or a JSON-RPC ping; got %q", body)
}
