// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// headerCapturingBackend is a minimal streamable-HTTP MCP fake that records
// inbound request headers keyed by JSON-RPC method. The test asserts that a
// user-configured HeaderForward header reaches the backend on POST-INITIALIZE
// traffic — see issue #5289. The startup capability-discovery path was fixed
// in PR #5239; per-session HTTP traffic is still missing the wrap.
type headerCapturingBackend struct {
	t *testing.T

	mu              sync.Mutex
	headersByMethod map[string]http.Header
}

func newHeaderCapturingBackend(t *testing.T) (*headerCapturingBackend, string) {
	t.Helper()
	fb := &headerCapturingBackend{
		t:               t,
		headersByMethod: make(map[string]http.Header),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", fb.handle)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return fb, ts.URL + "/mcp"
}

func (f *headerCapturingBackend) headersFor(method string) http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.headersByMethod[method]
}

func (f *headerCapturingBackend) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Streamable-HTTP transports may open a GET for server-pushed
		// notifications; rejecting it cleanly is fine for this test.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Errorf("headerCapturingBackend: read body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	var msg struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
	}
	if err := json.Unmarshal(body, &msg); err != nil {
		f.t.Errorf("headerCapturingBackend: decode: %v body=%s", err, string(body))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.headersByMethod[msg.Method] = r.Header.Clone()
	f.mu.Unlock()

	// Notifications (no id, e.g. notifications/initialized) get an empty 202.
	if len(msg.ID) == 0 || string(msg.ID) == "null" {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	switch msg.Method {
	case string(mcp.MethodInitialize):
		w.Header().Set("Mcp-Session-Id", "header-forward-test-session")
		f.writeResult(w, msg.ID, map[string]any{
			"protocolVersion": mcp.LATEST_PROTOCOL_VERSION,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{"name": "header-forward-fake", "version": "0.0.0"},
		})
	case string(mcp.MethodToolsList):
		f.writeResult(w, msg.ID, map[string]any{
			"tools": []mcp.Tool{{Name: "echo", Description: "echo tool"}},
		})
	case string(mcp.MethodToolsCall):
		f.writeResult(w, msg.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "ok"},
			},
			"isError": false,
		})
	default:
		f.writeResult(w, msg.ID, map[string]any{})
	}
}

func (f *headerCapturingBackend) writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"result":  result,
	}); err != nil {
		f.t.Errorf("headerCapturingBackend: encode result: %v", err)
	}
}

// TestHTTPSession_AppliesHeaderForwardToPostInitializeRequests is the red-phase
// regression test for issue #5289. PR #5239 fixed HeaderForward for the vMCP
// backend client (used for startup capability discovery) but did not extend the
// fix to the session-side connector at pkg/vmcp/session/internal/backend.
// As a result, user-configured headers (e.g. X-MCP-Toolsets for GitHub MCP)
// never reach the backend on per-session requests like tools/call.
//
// The test asserts that, after the connector completes Initialize, a
// subsequent CallTool carries the configured plaintext header on the wire.
// On main today it fails because the connector's transport chain does not
// include a header-forward round-tripper — see createMCPClient in
// mcp_session.go (the chain is http.DefaultTransport → authRoundTripper →
// identityRoundTripper, with no HeaderForward stage).
func TestHTTPSession_AppliesHeaderForwardToPostInitializeRequests(t *testing.T) {
	t.Parallel()

	const (
		headerName  = "X-MCP-Toolsets"
		headerValue = "projects,issues,pull_requests,users,repos"
	)

	fb, url := newHeaderCapturingBackend(t)

	target := &vmcp.BackendTarget{
		WorkloadID:    "header-forward-backend",
		WorkloadName:  "header-forward-backend",
		BaseURL:       url,
		TransportType: "streamable-http",
		HeaderForward: &vmcp.HeaderForwardConfig{
			AddPlaintextHeaders: map[string]string{
				headerName: headerValue,
			},
		},
	}

	registry := newTestRegistry(t)
	connector := NewHTTPConnector(registry)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, caps, err := connector(ctx, target, nil, "")
	require.NoError(t, err, "connector must initialise the backend successfully")
	require.NotNil(t, sess, "connector returned nil session")
	require.NotNil(t, caps, "connector returned nil capability list")
	t.Cleanup(func() { _ = sess.Close() })

	// Make a single MCP call AFTER initialize completes. tools/call exercises
	// the same transport chain as initialize but is unambiguously a
	// post-handshake request — which is exactly where the regression lives.
	_, err = sess.CallTool(ctx, "echo", map[string]any{}, nil)
	require.NoError(t, err, "post-initialize CallTool must succeed")

	// The recorded inbound headers for the tools/call request must include the
	// user-configured forward header. This is the single assertion target:
	// the test fails for exactly one reason — header missing on the recorded
	// post-initialize request.
	gotHeaders := fb.headersFor(string(mcp.MethodToolsCall))
	require.NotNil(t, gotHeaders, "backend never received a tools/call request")
	assert.Equal(t, headerValue, gotHeaders.Get(headerName),
		"HeaderForward.AddPlaintextHeaders must reach the backend on post-initialize requests")
}
