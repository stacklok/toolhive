// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

const (
	passthroughKeyHeader    = "X-Api-Key"
	passthroughTenantHeader = "X-Tenant-Id"
	passthroughResourceURI  = "test://passthrough"
)

var passthroughTestHeaders = []string{passthroughKeyHeader, passthroughTenantHeader}

// recordedBackendCall is one tools/call or resources/read request as seen by the
// backend: its JSON-RPC method and the passthrough headers it carried.
type recordedBackendCall struct {
	method  string
	headers map[string]string
}

// passthroughCallRecorder records the passthrough headers of every tools/call and
// resources/read request a backend receives.
type passthroughCallRecorder struct {
	mu    sync.Mutex
	calls []recordedBackendCall
}

func (r *passthroughCallRecorder) add(c recordedBackendCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

func (r *passthroughCallRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *passthroughCallRecorder) last() recordedBackendCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[len(r.calls)-1]
}

// startHeaderRecordingBackend starts a streamable-HTTP MCP backend with an "echo"
// tool and a static text resource, recording the passthrough headers of every
// tools/call and resources/read request. Returns the backend's /mcp URL.
func startHeaderRecordingBackend(t *testing.T, rec *passthroughCallRecorder) string {
	t.Helper()

	mcpSrv := mcpserver.NewMCPServer("header-recording-backend", "1.0.0")
	mcpSrv.AddTool(
		mcpmcp.NewTool("echo",
			mcpmcp.WithDescription("Echoes the input back"),
			mcpmcp.WithString("input", mcpmcp.Required()),
		),
		func(_ context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			input, _ := args["input"].(string)
			return &mcpmcp.CallToolResult{
				Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)},
			}, nil
		},
	)
	mcpSrv.AddResource(
		mcpmcp.Resource{URI: passthroughResourceURI, Name: "passthrough", MIMEType: "text/plain"},
		func(_ context.Context, _ mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
			return []mcpmcp.ResourceContents{
				mcpmcp.TextResourceContents{URI: passthroughResourceURI, MIMEType: "text/plain", Text: "resource-body"},
			}, nil
		},
	)
	streamableSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)

	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// Errors are not fatal here: this runs off the test goroutine, and a
			// request the recorder cannot parse is simply not recorded, which the
			// test then reports as a missing backend call.
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			var msg struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &msg) == nil && (msg.Method == "tools/call" || msg.Method == "resources/read") {
				headers := map[string]string{}
				for _, name := range passthroughTestHeaders {
					if vals, ok := r.Header[name]; ok {
						headers[name] = vals[0]
					}
				}
				rec.add(recordedBackendCall{method: msg.Method, headers: headers})
			}
		}
		streamableSrv.ServeHTTP(w, r)
	}))

	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	return ts.URL + "/mcp"
}

// passthroughTestClient bounds every request so a hung server fails the test
// instead of stalling until the go test timeout.
var passthroughTestClient = &http.Client{Timeout: 10 * time.Second}

// postWithPassthroughHeaders posts a JSON-RPC message to the vMCP server with the
// given passthrough headers and returns the status code and body.
func postWithPassthroughHeaders(
	t *testing.T, baseURL string, body map[string]any, sessionID string, headers map[string]string,
) (*http.Response, []byte) {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/mcp", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := passthroughTestClient.Do(req)
	require.NoError(t, err)
	respBody, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	return resp, respBody
}

// TestPassthroughHeaders_AbsentHeaderIsNotForwarded verifies that passthrough
// headers are forwarded per request: each backend call carries exactly the
// allowlisted headers present on the request that triggered it, even when the
// request that created the session carried other values.
func TestPassthroughHeaders_AbsentHeaderIsNotForwarded(t *testing.T) {
	t.Parallel()

	rec := &passthroughCallRecorder{}
	backendURL := startHeaderRecordingBackend(t, rec)
	ts := httptest.NewServer(newRealTestHandler(t, backendURL, func(c *server.Config) {
		c.PassthroughHeaders = passthroughTestHeaders
	}))
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})

	creatorHeaders := map[string]string{passthroughKeyHeader: "creator-key", passthroughTenantHeader: "creator-tenant"}
	resp, body := postWithPassthroughHeaders(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "creator", "version": "1.0"},
		},
	}, "", creatorHeaders)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	resp, body = postWithPassthroughHeaders(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}, sessionID, creatorHeaders)
	require.Less(t, resp.StatusCode, http.StatusMultipleChoices, string(body))

	waitForEchoTool(t, ts.URL, sessionID)

	callTool := map[string]any{
		"name":      "echo",
		"arguments": map[string]any{"input": "echo-input"},
	}
	readResource := map[string]any{"uri": passthroughResourceURI}

	// The cases share one session and run in order, so the session creator's
	// values are always available to leak into a later request.
	cases := []struct {
		name   string
		method string
		params map[string]any
		sent   map[string]string
		want   map[string]string
	}{
		{
			name:   "tool call without headers forwards none",
			method: "tools/call",
			params: callTool,
			sent:   nil,
			want:   map[string]string{},
		},
		{
			name:   "tool call forwards the request's own values",
			method: "tools/call",
			params: callTool,
			sent:   map[string]string{passthroughKeyHeader: "other-key", passthroughTenantHeader: "other-tenant"},
			want:   map[string]string{passthroughKeyHeader: "other-key", passthroughTenantHeader: "other-tenant"},
		},
		{
			name:   "tool call with one of two headers forwards only that one",
			method: "tools/call",
			params: callTool,
			sent:   map[string]string{passthroughTenantHeader: "other-tenant"},
			want:   map[string]string{passthroughTenantHeader: "other-tenant"},
		},
		{
			name:   "tool call with the creator's values forwards them",
			method: "tools/call",
			params: callTool,
			sent:   creatorHeaders,
			want:   creatorHeaders,
		},
		{
			name:   "resource read without headers forwards none",
			method: "resources/read",
			params: readResource,
			sent:   nil,
			want:   map[string]string{},
		},
		{
			name:   "resource read forwards the request's own values",
			method: "resources/read",
			params: readResource,
			sent:   map[string]string{passthroughKeyHeader: "other-key"},
			want:   map[string]string{passthroughKeyHeader: "other-key"},
		},
	}

	for i, tc := range cases {
		before := rec.count()
		resp, body := postWithPassthroughHeaders(t, ts.URL, map[string]any{
			"jsonrpc": "2.0",
			"id":      100 + i,
			"method":  tc.method,
			"params":  tc.params,
		}, sessionID, tc.sent)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s: %s", tc.name, body)
		require.NotContains(t, string(body), `"error"`, "%s: %s", tc.name, body)

		require.Greater(t, rec.count(), before, "%s: backend did not receive the call", tc.name)
		got := rec.last()
		assert.Equal(t, tc.method, got.method, tc.name)
		assert.Equal(t, tc.want, got.headers, tc.name)
	}
}
