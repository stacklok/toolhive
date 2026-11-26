// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

func TestOpenRequests(t *testing.T) {
	t.Parallel()

	t.Run("endSession cancels the session's open requests only", func(t *testing.T) {
		t.Parallel()
		var o openRequests
		ctxA, endA := o.begin(t.Context(), "a")
		defer endA()
		ctxB, endB := o.begin(t.Context(), "b")
		defer endB()

		o.endSession("a")()

		require.ErrorIs(t, ctxA.Err(), context.Canceled)
		assert.ErrorIs(t, context.Cause(ctxA), errSessionTerminated)
		assert.NoError(t, ctxB.Err())
	})

	t.Run("a request that starts while the session ends is cancelled", func(t *testing.T) {
		t.Parallel()
		var o openRequests
		done := o.endSession("a")

		ctx, end := o.begin(t.Context(), "a")
		defer end()
		assert.ErrorIs(t, context.Cause(ctx), errSessionTerminated)

		done()
		ctx, end2 := o.begin(t.Context(), "a")
		defer end2()
		assert.NoError(t, ctx.Err(), "once the DELETE is done the session ID is not remembered")
	})

	t.Run("end forgets the request", func(t *testing.T) {
		t.Parallel()
		var o openRequests
		_, end := o.begin(t.Context(), "a")
		end()
		o.mu.Lock()
		defer o.mu.Unlock()
		assert.Empty(t, o.sessions)
	})

	t.Run("a request without a session is not tracked", func(t *testing.T) {
		t.Parallel()
		var o openRequests
		ctx, end := o.begin(t.Context(), "")
		defer end()
		o.endSession("")()
		assert.NoError(t, ctx.Err())
	})
}

// TestDeleteCancelsOpenToolCall reproduces a client's DELETE on a Legacy
// session whose tools/call is still running. The transport closes the
// session by waiting for its open requests, so without cancellation the
// DELETE waits as long as the backend call does.
func TestDeleteCancelsOpenToolCall(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	cause := make(chan error, 1)
	fc := &fakeCore{
		tools: []vmcp.Tool{{Name: "wait"}},
		callHook: func(ctx context.Context) {
			close(started)
			select {
			case <-ctx.Done():
				cause <- context.Cause(ctx)
			case <-time.After(10 * time.Second):
				cause <- nil
			}
		},
	}
	baseURL, sessionID := serveFullChainSession(t, fc)

	callDone := make(chan int, 1)
	go func() {
		resp, err := doServeMCP(baseURL, map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": "wait", "arguments": map[string]any{}},
		}, sessionID)
		if err != nil {
			callDone <- 0
			return
		}
		_ = resp.Body.Close()
		callDone <- resp.StatusCode
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("tools/call never reached the core")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, baseURL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", sessionID)
	begin := time.Now()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "DELETE must not wait for the open call to finish")
	_ = resp.Body.Close()
	t.Logf("DELETE took %s", time.Since(begin))

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.ErrorIs(t, <-cause, errSessionTerminated, "the open call is cancelled by the DELETE")
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled tools/call never returned")
	}
}

// serveFullChainSession serves fc through Server.Handler, the full HTTP chain
// a deployed vMCP runs (session ownership included), opens a Legacy session
// and returns the server URL and the session ID.
func serveFullChainSession(t *testing.T, fc *fakeCore) (string, string) {
	t.Helper()
	factory, _ := newToolSessionFactory(t, gomock.NewController(t), fc.tools)
	cfg := testMinimalServeConfig()
	cfg.SessionManagerConfig = &sessionmanager.FactoryConfig{Base: factory}
	srv, err := Serve(t.Context(), fc, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	handler, err := srv.Handler(t.Context())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	initResp := postServeMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	require.Eventually(t, func() bool {
		_, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID)
		return ok
	}, 2*time.Second, 10*time.Millisecond, "session should be registered")
	return ts.URL, sessionID
}
