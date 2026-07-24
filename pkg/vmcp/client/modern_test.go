// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

// completeEnvelope is a minimal valid Modern success body for a given method's
// result payload merged with the resultType envelope key.
func completeEnvelope(t *testing.T, id any, payload map[string]any) []byte {
	t.Helper()
	result := map[string]any{"resultType": "complete"}
	for k, v := range payload {
		result[k] = v
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	require.NoError(t, err)
	return body
}

// TestModernCall_RequestShaping verifies the wire shape the shim produces:
// mandatory headers, conditional Mcp-Name, the reserved _meta keys, and the
// absence of any session header.
func TestModernCall_RequestShaping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      string
		mcpName     string
		wantNameHdr string // expected Mcp-Name header value ("" = must be absent)
		callerMeta  map[string]any
	}{
		{
			name:        "name-required method sets Mcp-Name",
			method:      "tools/call",
			mcpName:     "echo",
			wantNameHdr: "echo",
		},
		{
			name:        "non-name-required method omits Mcp-Name even when provided",
			method:      "tools/list",
			mcpName:     "ignored",
			wantNameHdr: "",
		},
		{
			name:        "empty name omits Mcp-Name",
			method:      "tools/call",
			mcpName:     "",
			wantNameHdr: "",
		},
		{
			name:        "caller reserved _meta keys are overridden by vMCP's",
			method:      "tools/list",
			mcpName:     "",
			wantNameHdr: "",
			callerMeta: map[string]any{
				"io.modelcontextprotocol/protocolVersion":    "1999-01-01",
				"io.modelcontextprotocol/clientCapabilities": "not-an-object",
				"userKey": "preserved",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotReq *http.Request
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotReq = r
				gotBody, _ = readAll(t, r)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(completeEnvelope(t, 1, map[string]any{}))
			}))
			t.Cleanup(srv.Close)

			params := map[string]any{}
			if tt.callerMeta != nil {
				params["_meta"] = tt.callerMeta
			}
			err := modernCall(context.Background(), srv.Client(), srv.URL, tt.method, params, tt.mcpName, nil)
			require.NoError(t, err)

			assert.Equal(t, "application/json", gotReq.Header.Get("Content-Type"))
			assert.Equal(t, "application/json, text/event-stream", gotReq.Header.Get("Accept"))
			assert.Equal(t, "2026-07-28", gotReq.Header.Get("MCP-Protocol-Version"))
			assert.Equal(t, tt.method, gotReq.Header.Get("Mcp-Method"))
			assert.Equal(t, tt.wantNameHdr, gotReq.Header.Get("Mcp-Name"))
			assert.Empty(t, gotReq.Header.Get("Mcp-Session-Id"), "Modern is stateless: never send a session id")

			// Verify the reserved _meta keys are present and authoritative.
			var decoded struct {
				Params struct {
					Meta map[string]any `json:"_meta"`
				} `json:"params"`
			}
			require.NoError(t, json.Unmarshal(gotBody, &decoded))
			meta := decoded.Params.Meta
			assert.Equal(t, "2026-07-28", meta["io.modelcontextprotocol/protocolVersion"])
			assert.IsType(t, map[string]any{}, meta["io.modelcontextprotocol/clientCapabilities"])
			assert.Contains(t, meta, "io.modelcontextprotocol/clientInfo")
			if tt.callerMeta != nil {
				assert.Equal(t, "preserved", meta["userKey"], "non-reserved caller _meta keys survive")
			}
		})
	}
}

// TestModernCall_CallerMetaNotMutated verifies the shim never writes through the
// caller's params or _meta map (go-style copy-before-mutate rule).
func TestModernCall_CallerMetaNotMutated(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(completeEnvelope(t, 1, map[string]any{}))
	}))
	t.Cleanup(srv.Close)

	callerMeta := map[string]any{"userKey": "v"}
	params := map[string]any{"_meta": callerMeta, "name": "x"}
	require.NoError(t, modernCall(context.Background(), srv.Client(), srv.URL, "tools/list", params, "", nil))

	assert.Equal(t, map[string]any{"userKey": "v"}, callerMeta, "caller _meta must be untouched")
	assert.NotContains(t, params, "does-not-add-keys")
	_, addedMeta := params["_meta"].(map[string]any)
	assert.True(t, addedMeta)
	assert.Len(t, callerMeta, 1, "no reserved keys leaked into caller's _meta")
}

// TestModernCall_Decode verifies a complete envelope decodes into out.
func TestModernCall_Decode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(completeEnvelope(t, 1, map[string]any{
			"supportedVersions": []string{"2026-07-28"},
		}))
	}))
	t.Cleanup(srv.Close)

	var out struct {
		ResultType        string   `json:"resultType"`
		SupportedVersions []string `json:"supportedVersions"`
	}
	require.NoError(t, modernCall(context.Background(), srv.Client(), srv.URL, "server/discover", nil, "", &out))
	assert.Equal(t, "complete", out.ResultType)
	assert.Equal(t, []string{"2026-07-28"}, out.SupportedVersions)
}

// TestModernCall_SSEResponse verifies the dual-body reader handles a
// text/event-stream response, ignoring interleaved notifications and returning
// the final matching JSON-RPC response.
func TestModernCall_SSEResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Echo the request's JSON-RPC id: modernCall's SSE reader matches the
		// response frame by id (unlike the JSON path), and the id comes from a
		// shared counter, so it cannot be hardcoded.
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &req))

		w.Header().Set("Content-Type", "text/event-stream")
		// A progress notification (has "method", no id match) then the response.
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n"))
		_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":" + string(req.ID) +
			",\"result\":{\"resultType\":\"complete\",\"ok\":true}}\n\n"))
	}))
	t.Cleanup(srv.Close)

	var out map[string]any
	require.NoError(t, modernCall(context.Background(), srv.Client(), srv.URL, "server/discover", nil, "", &out))
	assert.Equal(t, "complete", out["resultType"])
	assert.Equal(t, true, out["ok"])
}

// TestModernCall_ErrorMapping verifies the era/error classification: a valid
// -32601 body is method-not-found (backend IS Modern), a non-"complete" envelope
// is input-required, and non-Modern responses are wrong-era.
func TestModernCall_ErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantErr     error  // errors.Is target; nil means "some error, but not a known sentinel"
		wantMsg     string // substring the error message must contain (checked when wantErr is nil)
	}{
		{
			name:        "valid -32601 error body is method-not-found",
			status:      http.StatusNotFound,
			contentType: "application/json",
			body:        `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`,
			wantErr:     mcp.ErrMethodNotFound,
		},
		{
			name:        "non-complete envelope is input-required",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"jsonrpc":"2.0","id":1,"result":{"resultType":"input_required"}}`,
			wantErr:     errModernInputRequired,
		},
		{
			name:        "bare 404 with no JSON-RPC body is wrong-era",
			status:      http.StatusNotFound,
			contentType: "text/plain",
			body:        "not found",
			wantErr:     errWrongEra,
		},
		{
			name:        "empty body is wrong-era",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        "",
			wantErr:     errWrongEra,
		},
		{
			name:        "200 with Legacy-shaped result (no resultType) is a legacy-response-body",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
			wantErr:     errLegacyResponseBody,
		},
		{
			name:        "other JSON-RPC error surfaces as a call error, not wrong-era",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"boom"}}`,
			wantMsg:     "boom",
		},
		{
			name:        "401 is auth, not wrong-era",
			status:      http.StatusUnauthorized,
			contentType: "text/plain",
			body:        "",
			wantErr:     errModernAuth,
		},
		{
			name:        "403 is auth, not wrong-era",
			status:      http.StatusForbidden,
			contentType: "application/json",
			body:        "",
			wantErr:     errModernAuth,
		},
		{
			name:        "407 proxy-auth is auth, not wrong-era",
			status:      http.StatusProxyAuthRequired,
			contentType: "text/plain",
			body:        "",
			wantErr:     errModernAuth,
		},
		{
			name:        "503 is transient, not wrong-era",
			status:      http.StatusServiceUnavailable,
			contentType: "text/plain",
			body:        "",
			wantErr:     errModernTransient,
		},
		{
			name:        "429 is transient, not wrong-era",
			status:      http.StatusTooManyRequests,
			contentType: "application/json",
			body:        "",
			wantErr:     errModernTransient,
		},
		{
			name:        "401 tagged text/event-stream is still auth, not wrong-era",
			status:      http.StatusUnauthorized,
			contentType: "text/event-stream",
			body:        "",
			wantErr:     errModernAuth,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(srv.Close)

			err := modernCall(context.Background(), srv.Client(), srv.URL, "server/discover", nil, "", nil)
			require.Error(t, err)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				// Auth/transient statuses must never masquerade as the not-Modern
				// signal (that is what would poison a cached-Modern backend).
				if tt.wantErr == errModernAuth || tt.wantErr == errModernTransient {
					assert.NotErrorIs(t, err, errWrongEra)
					assert.NotErrorIs(t, err, errLegacyResponseBody)
				}
				return
			}
			// A valid JSON-RPC error body means the backend IS Modern: the error
			// must surface (not be reclassified as wrong-era or method-not-found).
			assert.NotErrorIs(t, err, errWrongEra)
			assert.NotErrorIs(t, err, mcp.ErrMethodNotFound)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// TestModernCall_LargeSSEEvent verifies a single SSE data: event larger than the
// old 4 MiB scanner cap (but under maxResponseSize) decodes successfully.
func TestModernCall_LargeSSEEvent(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 5*1024*1024) // 5 MiB > old 4 MiB token cap
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := modernReq(t, r)
		w.Header().Set("Content-Type", "text/event-stream")
		out, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
			"result": map[string]any{"resultType": "complete", "blob": big}})
		require.NoError(t, err)
		_, _ = w.Write([]byte("data: " + string(out) + "\n\n"))
	}))
	t.Cleanup(srv.Close)

	var got struct {
		Blob string `json:"blob"`
	}
	require.NoError(t, modernCall(context.Background(), srv.Client(), srv.URL, "server/discover", nil, "", &got))
	assert.Len(t, got.Blob, len(big))
}

// readAll returns the request body bytes for assertions.
func readAll(t *testing.T, r *http.Request) ([]byte, error) {
	t.Helper()
	return io.ReadAll(r.Body)
}
