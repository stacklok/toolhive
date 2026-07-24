// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Modern (2026-07-28) raw HTTP client helper
// ---------------------------------------------------------------------------

// postModern sends a single Modern (2026-07-28) stateless JSON-RPC request to
// baseURL+"/mcp", hand-rolled per pkg/mcp/revision.go's wire contract rather
// than through go-sdk (importing it here would force an MVS bump): the
// MCP-Protocol-Version and Mcp-Method headers are mandatory on every Modern
// POST, Mcp-Name is set when non-empty (required only for tools/call,
// resources/read, prompts/get -- see nameRequiredMethods in revision.go), and
// _meta carries the reserved io.modelcontextprotocol/protocolVersion and
// clientCapabilities keys ClassifyRevision requires to admit the request as
// Modern in the first place.
//
// id == nil sends a notification: the "id" key is omitted from the body
// entirely (not set to JSON null), matching how parseMCPRequest distinguishes
// a call from a notification (dispatchModern, and jsonrpc2 before it, key off
// id being ABSENT, not nil).
//
// Returns the raw *http.Response (body re-readable: it is buffered and
// restored) alongside the JSON-RPC envelope decoded into a generic map, or a
// nil map for a body-less response (e.g. a notification's 202).
func postModern(
	t *testing.T, baseURL, method string, params map[string]any, id any, mcpName string,
) (*http.Response, map[string]any) {
	t.Helper()

	// Copy before mutating caller input (go-style rule): we inject _meta below,
	// so clone the caller's params and any nested _meta rather than writing through.
	params = maps.Clone(params)
	if params == nil {
		params = map[string]any{}
	}
	meta, _ := params["_meta"].(map[string]any)
	meta = maps.Clone(meta)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["io.modelcontextprotocol/protocolVersion"] = "2026-07-28"
	meta["io.modelcontextprotocol/clientCapabilities"] = map[string]any{}
	params["_meta"] = meta

	body := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}
	if id != nil {
		body["id"] = id
	}
	payload, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodPost, baseURL+"/mcp", bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	if mcpName != "" {
		req.Header.Set("Mcp-Name", mcpName)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	// Best-effort decode: a request that never reaches the Modern dispatcher
	// (e.g. a malformed request rejected before dispatch) can come back as a
	// plain-text error rather than JSON. Callers that expect a JSON-RPC
	// envelope assert on decoded's fields directly, which fails informatively
	// (nil map) if decoding didn't happen.
	var decoded map[string]any
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, &decoded)
	}
	return resp, decoded
}

// ---------------------------------------------------------------------------
// Integration tests -- Modern (2026-07-28) stateless dispatch, real backend
// ---------------------------------------------------------------------------

// TestIntegration_Modern_RealBackend_ToolCall verifies the load-bearing
// end-to-end round-trip: a Modern tools/call request travels through the real
// middleware chain (parsing/classification/dispatch), a real core, and a real
// backend, and comes back as a Modern envelope with no session established.
func TestIntegration_Modern_RealBackend_ToolCall(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealModernTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"input": "hello modern"},
	}, 1, "echo")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
	assert.Empty(t, resp.Header.Get("Mcp-Session-Id"), "Modern responses must never carry a session ID")

	result, ok := decoded["result"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	assert.Equal(t, "complete", result["resultType"])
	content, ok := result["content"].([]any)
	require.True(t, ok && len(content) == 1)
	first := content[0].(map[string]any)
	assert.Equal(t, "text", first["type"])
	assert.Equal(t, "hello modern", first["text"])
	// IsError has an omitempty JSON tag, so a successful call omits the key
	// entirely rather than marshaling it as false.
	assert.NotEqual(t, true, result["isError"], "tool call must not be marked as an error")
}

// TestIntegration_Modern_RealBackend_KillSwitchOff verifies that with the
// Modern dispatch kill-switch at its default (off), a well-formed Modern
// tools/call request is NOT served by dispatchModern: it falls through to the
// SDK path, which has no session for this request and so cannot produce a
// Modern envelope (no "resultType" in the response, and no 200 as
// TestIntegration_Modern_RealBackend_ToolCall gets with the switch on).
func TestIntegration_Modern_RealBackend_KillSwitchOff(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "tools/call", map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"input": "hello modern"},
	}, 1, "echo")
	defer resp.Body.Close()

	assert.NotEqual(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
	result, _ := decoded["result"].(map[string]any)
	assert.NotContains(t, result, "resultType", "must not be served by dispatchModern: decoded: %+v", decoded)
}

// TestIntegration_Modern_RealBackend_ToolsList verifies tools/list against the
// real backend's discovered tool set, with the Modern cacheability envelope.
func TestIntegration_Modern_RealBackend_ToolsList(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealModernTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "tools/list", nil, 1, "")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
	result, ok := decoded["result"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	assert.Equal(t, "complete", result["resultType"])
	assert.Equal(t, "private", result["cacheScope"])
	_, hasTTL := result["ttlMs"]
	assert.True(t, hasTTL, "ttlMs must be present even when zero")

	tools, ok := result["tools"].([]any)
	require.True(t, ok && len(tools) == 1, "expected exactly the echo tool: %+v", result)
	assert.Equal(t, "echo", tools[0].(map[string]any)["name"])
}

// TestIntegration_Modern_RealBackend_Discover verifies server/discover reports
// capability presence derived from the real backend's actual tool set: tools
// and completions present (the echo backend has a tool; completions is
// unconditional), resources and prompts absent (the echo backend exposes
// neither).
func TestIntegration_Modern_RealBackend_Discover(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealModernTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "server/discover", nil, 1, "")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
	result, ok := decoded["result"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	assert.Equal(t, "private", result["cacheScope"])

	caps, ok := result["capabilities"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	_, hasTools := caps["tools"]
	_, hasCompletions := caps["completions"]
	_, hasResources := caps["resources"]
	_, hasPrompts := caps["prompts"]
	assert.True(t, hasTools, "echo backend has a tool")
	assert.True(t, hasCompletions, "completions is advertised unconditionally")
	assert.False(t, hasResources, "echo backend exposes no resources")
	assert.False(t, hasPrompts, "echo backend exposes no prompts")
}

// TestIntegration_Modern_RealBackend_Complete verifies completion/complete
// routes to the core rather than 404ing as an unknown method. The echo
// backend has no prompts, so the referenced name is unroutable; core.Complete
// treats that leniently (empty candidates, not an error -- see
// coreVMCP.Complete), so this asserts a clean 200 completion object rather
// than a protocol-level rejection.
func TestIntegration_Modern_RealBackend_Complete(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealModernTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "completion/complete", map[string]any{
		"ref":      map[string]any{"type": "ref/prompt", "name": "nonexistent"},
		"argument": map[string]any{"name": "a", "value": ""},
	}, 1, "")
	defer resp.Body.Close()

	require.NotEqual(t, http.StatusNotFound, resp.StatusCode,
		"completion/complete must not be treated as an unknown method: decoded: %+v", decoded)
	require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
	result, ok := decoded["result"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	completion, ok := result["completion"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	assert.Equal(t, []any{}, completion["values"], "unroutable ref yields empty candidates, not an error")
}

// TestIntegration_Modern_RealBackend_Ping verifies ping returns a bare
// {"jsonrpc":"2.0","id":..,"result":{}} -- no resultType, no _meta -- per
// dispatchModern's documented deliberate bypass of the envelope builders for
// this method.
func TestIntegration_Modern_RealBackend_Ping(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealModernTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "ping", nil, 7, "")
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
	assert.Equal(t, map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(7),
		"result":  map[string]any{},
	}, decoded)
}

// TestIntegration_Modern_RealBackend_Notification verifies a Modern request
// with no "id" (a notification) is acknowledged with 202 and no body, per
// dispatchModern's ID-nil check -- which runs before any method dispatch.
func TestIntegration_Modern_RealBackend_Notification(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealModernTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "tools/list", nil, nil, "")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Nil(t, decoded)
	leftover, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Empty(t, leftover, "a notification response must carry no body")
}

// TestIntegration_Modern_RealBackend_UnknownMethod verifies a syntactically
// well-formed Modern request naming a method dispatchModern does not
// recognize is rejected with 404 + JSON-RPC -32601, per the draft spec's
// MUST-404-unimplemented-method rule (writeModernError).
func TestIntegration_Modern_RealBackend_UnknownMethod(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealModernTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "resources/subscribe", map[string]any{"uri": "file:///x"}, 1, "")
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode, "decoded: %+v", decoded)
	errObj, ok := decoded["error"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	assert.EqualValues(t, -32601, errObj["code"])
}

// TestIntegration_Modern_RealBackend_MalformedArguments verifies a
// syntactically valid tools/call whose "arguments" is present but not a JSON
// object is rejected with 400 + JSON-RPC -32602, per hasNonObjectArguments'
// pre-dispatch shape check.
func TestIntegration_Modern_RealBackend_MalformedArguments(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealModernTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "tools/call", map[string]any{
		"name":      "echo",
		"arguments": "not-an-object",
	}, 1, "echo")
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode, "decoded: %+v", decoded)
	errObj, ok := decoded["error"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	assert.EqualValues(t, -32602, errObj["code"])
}
