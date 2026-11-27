// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
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

	t.Run("the session stays ended until every overlapping DELETE is done", func(t *testing.T) {
		t.Parallel()
		var o openRequests
		done1 := o.endSession("a")
		done2 := o.endSession("a")

		done1()
		done1() // a second call must not end the other DELETE's hold
		ctx, end := o.begin(t.Context(), "a")
		defer end()
		assert.ErrorIs(t, context.Cause(ctx), errSessionTerminated,
			"the second DELETE is still closing the session")

		done2()
		ctx, end2 := o.begin(t.Context(), "a")
		defer end2()
		assert.NoError(t, ctx.Err(), "both DELETEs are done")
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

// TestForeignDeleteKeepsOwnerCall checks that a DELETE from a caller who does
// not own the session is rejected before it can cancel anything, and that the
// owner's own DELETE then cancels the call.
func TestForeignDeleteKeepsOwnerCall(t *testing.T) {
	t.Parallel()

	baseURL, fc := newOwnershipTestServerWithCore(t, ownershipBearerMiddleware, "")
	callCtx := make(chan context.Context, 1)
	fc.callHook = func(ctx context.Context) {
		callCtx <- ctx
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
	sessionID := initializeOwnedSession(t, baseURL, ownerToken)

	go func() {
		// The result does not matter: the test reads the call's context.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		raw, _ := json.Marshal(jsonRPCRequest("tools/call", map[string]any{
			"name": ownershipTestTool, "arguments": map[string]any{},
		}))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mcp", bytes.NewReader(raw))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+ownerToken)
		req.Header.Set("Mcp-Session-Id", sessionID)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}()
	var ctx context.Context
	select {
	case ctx = <-callCtx:
	case <-time.After(5 * time.Second):
		t.Fatal("tools/call never reached the core")
	}

	foreign := ownedRequest(t, baseURL, http.MethodDelete, foreignToken, sessionID, nil)
	assert.Equal(t, http.StatusNotFound, foreign.status, "body: %s", foreign.body)
	require.NoError(t, ctx.Err(), "a foreign DELETE must not cancel the owner's call")

	owner := ownedRequest(t, baseURL, http.MethodDelete, ownerToken, sessionID, nil)
	assert.Equal(t, http.StatusOK, owner.status, "body: %s", owner.body)
	assert.ErrorIs(t, context.Cause(ctx), errSessionTerminated, "the owner's DELETE cancels the call")
}

// TestHandlersSkipEndedSession checks that a request starting on a session
// whose DELETE is in progress returns at once, before the binding check that
// would load the session again. A zero Server is enough: the handlers return
// before they use anything else.
func TestHandlersSkipEndedSession(t *testing.T) {
	t.Parallel()

	tools := map[string]func(*Server) server.ToolHandlerFunc{
		"tools/call": func(s *Server) server.ToolHandlerFunc { return s.coreToolHandler("a", "t", "b") },
		"find_tool":  func(s *Server) server.ToolHandlerFunc { return s.optimizerFindToolHandler("a", nil) },
	}
	for name, handler := range tools {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := &Server{}
			defer s.openRequests.endSession("a")()
			result, err := handler(s)(t.Context(), mcp.CallToolRequest{})
			require.NoError(t, err)
			assert.True(t, result.IsError)
		})
	}

	t.Run("resources/read", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		defer s.openRequests.endSession("a")()
		_, err := s.coreResourceHandler("a", "file:///r", "b")(t.Context(), mcp.ReadResourceRequest{})
		assert.ErrorIs(t, err, errSessionTerminated)
	})

	t.Run("prompts/get", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		defer s.openRequests.endSession("a")()
		_, err := s.corePromptHandler("a", "p", "b")(t.Context(), mcp.GetPromptRequest{})
		assert.ErrorIs(t, err, errSessionTerminated)
	})
}
