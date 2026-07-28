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
	"time"

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
	// clientCapabilities is required on every Modern request; default to the
	// empty declaration, but preserve a caller-supplied value so tests can
	// exercise declared-capability behavior (see
	// TestIntegration_Modern_RealBackend_MidCallCapabilityContract).
	if _, ok := meta["io.modelcontextprotocol/clientCapabilities"]; !ok {
		meta["io.modelcontextprotocol/clientCapabilities"] = map[string]any{}
	}
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

	// Bounded: several callers assert "resolves promptly, never hangs", and
	// without a deadline a hang would only be caught by go test's package
	// timeout. 30s is generous for an in-process round-trip even under CI
	// -race load.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mcp", bytes.NewReader(payload))
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
	ts := newRealTestServer(t, backendURL)

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

// TestIntegration_Modern_RealBackend_ToolsList verifies tools/list against the
// real backend's discovered tool set, with the Modern cacheability envelope.
func TestIntegration_Modern_RealBackend_ToolsList(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealTestServer(t, backendURL)

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
	ts := newRealTestServer(t, backendURL)

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
	ts := newRealTestServer(t, backendURL)

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
	ts := newRealTestServer(t, backendURL)

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
	ts := newRealTestServer(t, backendURL)

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
	ts := newRealTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "resources/subscribe", map[string]any{"uri": "file:///x"}, 1, "")
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode, "decoded: %+v", decoded)
	errObj, ok := decoded["error"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	assert.EqualValues(t, -32601, errObj["code"])
}

// TestIntegration_Modern_RealBackend_ElicitingToolFailsCleanly pins the
// Modern-client CONTRACT for a backend tool that issues a mid-call
// server-initiated request (elicitation/create, sampling/createMessage): the
// call MUST resolve promptly (postModern is deadline-bounded) to an explicit
// JSON-RPC error — never a hang, never a fabricated success, and never a
// resultType "input_required" envelope, which this dispatcher does not emit
// (client-polled multi-round retrieval, SEP-2322, is unimplemented;
// modernResultTypeComplete is the only resultType modern_envelope.go builds).
//
// Deliberately NOT pinned: the specific error code. Today it is -32603, but
// the spec MUSTs -32021 MissingRequiredClientCapability for the undeclared
// case (at HTTP 400, which go-sdk's client escalates to permanent session
// death — the follow-up therefore plans -32021 at HTTP 200 as a documented
// deviation; see the "unavailable to Modern clients" section of
// docs/arch/10-virtual-mcp-architecture.md). Pinning -32603 here would make
// that spec-correcting follow-up look like a regression. TODO(follow-up):
// once the two-path capability-error contract lands, assert -32021 +
// data.requiredCapabilities for the undeclared case.
//
// This is the honest-unsupported contract for the surface the 2026-07-28
// revision removed: server-initiated requests need a live session, Modern
// requests have none by design, and SEP-2577 additionally deprecates sampling
// outright. The Legacy-session behavior for the same backend tools is covered
// by the TestForwarding_* fixtures, whose downstream clients pin Legacy
// explicitly (see legacyPinningRoundTripper).
func TestIntegration_Modern_RealBackend_ElicitingToolFailsCleanly(t *testing.T) {
	t.Parallel()

	backendURL := startForwardingBackend(t)
	ts := newRealTestServer(t, backendURL)

	tests := []struct {
		name string
		tool string
	}{
		{name: "elicitation", tool: fwdElicitTool},
		{name: "sampling", tool: fwdSampleTool},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp, decoded := postModern(t, ts.URL, "tools/call", map[string]any{"name": tc.tool}, 1, tc.tool)
			defer resp.Body.Close()

			// Both today's -32603 and the follow-up's -32021 (documented HTTP
			// 200 deviation) ride HTTP 200, so this holds across the fix.
			require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
			require.NotContains(t, decoded, "result",
				"must not fabricate a success or an input_required envelope: %+v", decoded)
			_, ok := decoded["error"].(map[string]any)
			require.True(t, ok, "the failure must be an explicit JSON-RPC error: %+v", decoded)

			// Nothing about the MESSAGE is asserted, deliberately. The refused
			// request's name (elicitation/create etc.) reaches it only via
			// go-sdk's "calling %q" error wrapping — a dependency's string
			// (testing.md, test scope) — and the message also carries the
			// pre-existing backend-workload-ID leak, which the follow-up
			// capability-error contract fixes with vMCP-owned messages. Both
			// message properties (names our string, no backend ID) are asserted
			// THERE, where vMCP owns the text.
		})
	}
}

// TestIntegration_Modern_RealBackend_ProgressDropped pins today's Modern
// behavior for a backend tool that emits notifications/progress mid-call: the
// notification is silently dropped and the call still completes promptly with
// its result. There is nowhere for it to go — a Modern response is a single
// JSON body (writeModernResult), and request-scoped notifications would ride
// a per-request SSE response stream (SEP-2260) that the single-shot
// dispatcher does not produce. That is a vMCP streaming-dispatch gap, NOT a
// spec absence — this test converts that prose claim (see
// TestForwarding_Progress_RealBackend's pin comment and the arch doc) into an
// executable one, and MUST be revisited when streaming Modern dispatch is
// implemented: at that point the progress notification becomes deliverable
// and this test's premise changes.
//
// Promptness matters here: on a Legacy session the same fixture relays the
// notification; on Modern, a client waiting for it would hang to its own
// deadline. postModern's bounded context is the hang guard.
func TestIntegration_Modern_RealBackend_ProgressDropped(t *testing.T) {
	t.Parallel()

	backendURL := startForwardingBackend(t)
	ts := newRealTestServer(t, backendURL)

	resp, decoded := postModern(t, ts.URL, "tools/call", map[string]any{
		"name": fwdProgressTool,
		// A progressToken is exactly what would solicit progress delivery if a
		// channel existed; its presence makes "dropped" the strongest claim.
		"_meta": map[string]any{"progressToken": fwdProgressToken},
	}, 1, fwdProgressTool)
	defer resp.Body.Close()

	// The call completes with the tool's result; the mid-call progress
	// notification had no channel and was dropped. A single JSON body can
	// carry nothing else, so completing at all IS the drop assertion.
	require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
	result, ok := decoded["result"].(map[string]any)
	require.True(t, ok, "decoded: %+v", decoded)
	assert.Equal(t, "complete", result["resultType"])
	content, ok := result["content"].([]any)
	require.True(t, ok && len(content) == 1, "decoded: %+v", decoded)
	assert.Equal(t, "done", content[0].(map[string]any)["text"])
}

// TestIntegration_Modern_RealBackend_LoggingContract pins today's Modern
// contract for logging/setLevel, which the 2026-07-28 revision removed (the
// per-request logLevel _meta key replaces it):
//
//   - A WELL-FORMED Modern logging/setLevel is an unknown method to
//     dispatchModern: 404 + -32601, matching go-sdk's own server ("method
//     removed in the new protocol").
//   - The request go-sdk v1.7.0-pre.3 ACTUALLY sends is malformed — its
//     SetLoggingLevel omits the per-request _meta injection (upstream bug),
//     producing a Modern header with no _meta protocolVersion — and vMCP's
//     classifier CORRECTLY rejects that shape with 400 + -32020
//     (HeaderMismatch). Loosening this to accept the malformed request would
//     be worse than the failing upstream call.
//
// See TestForwarding_Logging_RealBackend's pin comment for the full
// three-cause disposition (upstream bug, method removal/streaming gap,
// SEP-2577 deprecation).
func TestIntegration_Modern_RealBackend_LoggingContract(t *testing.T) {
	t.Parallel()

	backendURL := startForwardingBackend(t)
	ts := newRealTestServer(t, backendURL)

	t.Run("well-formed setLevel is method-not-found", func(t *testing.T) {
		t.Parallel()
		resp, decoded := postModern(t, ts.URL, "logging/setLevel", map[string]any{"level": "debug"}, 1, "")
		defer resp.Body.Close()

		require.Equal(t, http.StatusNotFound, resp.StatusCode, "decoded: %+v", decoded)
		errObj, ok := decoded["error"].(map[string]any)
		require.True(t, ok, "decoded: %+v", decoded)
		assert.EqualValues(t, -32601, errObj["code"])
	})

	t.Run("go-sdk's malformed setLevel is rejected -32020", func(t *testing.T) {
		t.Parallel()
		// Replicate the upstream bug's wire shape by hand: Modern header, but
		// params carrying NO _meta protocolVersion (postModern would inject it,
		// so build the request raw).
		payload, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "logging/setLevel",
			"params":  map[string]any{"level": "debug"},
		})
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/mcp", bytes.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "logging/setLevel")

		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode, "body: %s", body)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded), "body: %s", body)
		errObj, ok := decoded["error"].(map[string]any)
		require.True(t, ok, "body: %s", body)
		assert.EqualValues(t, -32020, errObj["code"],
			"the malformed request must be rejected as a header/_meta mismatch")
	})
}

// TestIntegration_Modern_RealBackend_MalformedArguments verifies a
// syntactically valid tools/call whose "arguments" is present but not a JSON
// object is rejected with 400 + JSON-RPC -32602, per hasNonObjectArguments'
// pre-dispatch shape check.
func TestIntegration_Modern_RealBackend_MalformedArguments(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealTestServer(t, backendURL)

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

// TestIntegration_Modern_RealBackend_MidCallCapabilityContract pins the
// two-path error contract for a backend tool that demands mid-call
// elicitation/sampling during a Modern client's tools/call
// (writeModernCallFailure):
//
//   - capability NOT declared in _meta clientCapabilities: the draft schema's
//     MissingRequiredClientCapabilityError — code -32021 with
//     data.requiredCapabilities typed as a ClientCapabilities object — served
//     at HTTP 200, a deliberate, documented deviation from the spec-mandated
//     400 (go-sdk treats a non-transient 4xx as permanent connection death;
//     see writeModernMissingCapability and go-sdk#1117).
//   - capability DECLARED: -32021 would wrongly blame the client, and the
//     2026-07-28 vocabulary has no "operation not supported" code, so it is
//     an explicit -32603 whose vMCP-owned message names the real cause:
//     honouring a declared capability mid-call is multi-round retrieval
//     (SEP-2322), which this server does not implement.
//
// Both paths emit vMCP-crafted messages, so — unlike the raw backend error
// chain they replace — they are asserted on ToolHive-owned strings, and the
// backend workload ID must NOT leak into them.
func TestIntegration_Modern_RealBackend_MidCallCapabilityContract(t *testing.T) {
	t.Parallel()

	backendURL := startForwardingBackend(t)
	ts := newRealTestServer(t, backendURL)

	tests := []struct {
		name       string
		tool       string
		capability string // the capability the backend's tool demands mid-call
		declared   bool   // whether the request's _meta declares it
	}{
		{name: "elicitation undeclared", tool: fwdElicitTool, capability: "elicitation", declared: false},
		{name: "sampling undeclared", tool: fwdSampleTool, capability: "sampling", declared: false},
		{name: "elicitation declared", tool: fwdElicitTool, capability: "elicitation", declared: true},
		{name: "sampling declared", tool: fwdSampleTool, capability: "sampling", declared: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			params := map[string]any{"name": tc.tool}
			if tc.declared {
				params["_meta"] = map[string]any{
					"io.modelcontextprotocol/clientCapabilities": map[string]any{
						tc.capability: map[string]any{},
					},
				}
			}
			resp, decoded := postModern(t, ts.URL, "tools/call", params, 1, tc.tool)
			defer resp.Body.Close()

			// Both paths ride HTTP 200: -32603 per writeModernError's mapping,
			// -32021 per writeModernMissingCapability's documented deviation.
			require.Equal(t, http.StatusOK, resp.StatusCode, "decoded: %+v", decoded)
			require.NotContains(t, decoded, "result",
				"must not fabricate a success or an input_required envelope: %+v", decoded)
			errObj, ok := decoded["error"].(map[string]any)
			require.True(t, ok, "decoded: %+v", decoded)
			msg, _ := errObj["message"].(string)

			// vMCP owns both messages: they must name the capability and the
			// gateway limitation (SEP-2322), and must not leak the backend
			// workload ID the raw error chain used to carry.
			assert.Contains(t, msg, tc.capability)
			assert.Contains(t, msg, "SEP-2322",
				"the message must name multi-round retrieval as the gateway limitation")
			assert.NotContains(t, msg, "real-backend",
				"the crafted message must not leak the backend workload ID")

			if !tc.declared {
				assert.EqualValues(t, -32021, errObj["code"])
				data, ok := errObj["data"].(map[string]any)
				require.True(t, ok, "decoded: %+v", decoded)
				required, ok := data["requiredCapabilities"].(map[string]any)
				require.True(t, ok, "decoded: %+v", decoded)
				assert.Contains(t, required, tc.capability,
					"requiredCapabilities must name the capability the server needed")
				return
			}
			assert.EqualValues(t, -32603, errObj["code"])
		})
	}
}
