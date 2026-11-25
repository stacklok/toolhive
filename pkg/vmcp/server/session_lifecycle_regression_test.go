// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// readServeJSONRPC reads an HTTP response body from the Serve-path streamable
// server and unmarshals the JSON-RPC envelope into a map. The streamable server
// is configured with JSONResponse:true, so the body is a single application/json
// document (not an SSE stream). It also returns the raw body bytes for
// substring assertions.
func readServeJSONRPC(t *testing.T, resp *http.Response) (map[string]any, []byte) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var env map[string]any
	require.NoError(t, json.Unmarshal(body, &env), "body: %s", string(body))
	return env, body
}

// serveDelete sends an HTTP DELETE to /mcp with the given session ID.
func serveDelete(t *testing.T, baseURL, sessionID string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodDelete, baseURL+"/mcp", nil,
	)
	require.NoError(t, err)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestRegression_TerminatedSessionRejected verifies that a session terminated
// via the SDK's DELETE path (which drives vmcpSessionMgr.Terminate under the
// hood and forgets the SDK's local session) is no longer accepted for
// subsequent requests: a tools/list POST with the dead session ID must yield a
// 4xx response — the SDK's Validate rejects the unknown/terminated session
// before any handler runs — not a successful JSON-RPC result.
//
// Note: calling vmcpSessionMgr.Terminate alone does NOT immediately evict the
// SDK's in-memory local session (lazy eviction — the entry is closed when the
// cache is later re-checked). Only the DELETE path forgets the SDK session in
// lockstep with storage termination, so the rejection is observable on the
// next request. This mirrors the proven assertion in
// TestIntegration_SessionManagement_Termination.
func TestRegression_TerminatedSessionRejected(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, baseURL := registerServeSession(t, fc)

	// Terminate via the SDK DELETE path: this calls vmcpSessionMgr.Terminate
	// (storage delete) AND forgets the SDK's local session, so the next request
	// is rejected by Validate rather than served from a stale local copy.
	delResp := serveDelete(t, baseURL, sessionID)
	defer delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode, "DELETE should return 200")

	// The termination must be reflected in storage before we assert.
	require.Eventually(t, func() bool {
		_, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID)
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "session must be gone from the manager after Terminate")

	resp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}, sessionID)
	defer resp.Body.Close()

	// A terminated session must NOT be accepted: reject with a 4xx (the SDK's
	// Validate returns 404 for an unknown/terminated session). A 200 here would
	// mean a terminated session was still serving requests — a security regression.
	assert.GreaterOrEqual(t, resp.StatusCode, 400,
		"terminated session must be rejected, got status %d", resp.StatusCode)
	assert.Less(t, resp.StatusCode, 500,
		"rejection should be a client error (4xx), not a server error; got %d", resp.StatusCode)
}

// TestRegression_DELETE_TerminationEvictsCache verifies that an HTTP DELETE to
// /mcp with a valid Mcp-Session-Id terminates the session: the response is 200
// (the streamable server rewrites the SDK's 204 to 200 for mcp-go compatibility)
// and the session is subsequently absent from vmcpSessionMgr.
func TestRegression_DELETE_TerminationEvictsCache(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, baseURL := registerServeSession(t, fc)

	delResp := serveDelete(t, baseURL, sessionID)
	defer delResp.Body.Close()

	// The streamable server rewrites 204 → 200 and forwards the termination to
	// the SessionIdManager, so DELETE returns 200 OK on the happy path.
	assert.Equal(t, http.StatusOK, delResp.StatusCode,
		"DELETE on a live session should return 200 (got %d)", delResp.StatusCode)

	// The session must be evicted from the vMCP session manager.
	require.Eventually(t, func() bool {
		_, ok := srv.vmcpSessionMgr.GetMultiSession(context.Background(), sessionID)
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "session must be evicted from vmcpSessionMgr after DELETE")
}

// TestRegression_SessionIdentityBinding_SecondPrincipalRejected is a thin
// regression re-assertion that the Serve call path enforces the session's
// identity binding: a session bound to the anonymous identity rejects a caller
// presenting a different principal. The exhaustive test lives in
// TestServeEnforcesSessionBinding; this guards the regression at the tool-handler
// boundary by invoking coreToolHandler directly with a non-anonymous identity.
func TestRegression_SessionIdentityBinding_SecondPrincipalRejected(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, _ := registerServeSession(t, fc)

	// The registered session is bound to "unauthenticated" (anonymous) by
	// newToolSessionFactory. A caller presenting a token is a session-upgrade
	// attack and must be rejected before the core is reached.
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Token: "attacker-token"})
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "t", Arguments: map[string]any{}}}

	res, err := srv.coreToolHandler(sessionID, "t", "")(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.IsError, "a second principal must be rejected with an error result")

	body, err := json.Marshal(res)
	require.NoError(t, err)
	assert.Contains(t, string(body), "Unauthorized",
		"the rejection must carry an unauthorized message")
	// The core must not have been reached on a binding failure.
	assert.Equal(t, int32(0), fc.callToolCalls.Load(),
		"core.CallTool must not be reached when the binding check fails")
}
