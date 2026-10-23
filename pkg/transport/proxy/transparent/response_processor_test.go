// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

func TestNoOpResponseProcessorValidatesJSONRPCResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "valid result response passes through",
			body:       `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
			wantStatus: http.StatusOK,
			wantBody:   `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		},
		{
			name:       "valid error response passes through",
			body:       `{"jsonrpc":"2.0","id":"abc","error":{"code":-32601,"message":"Method not found"}}`,
			wantStatus: http.StatusOK,
			wantBody:   `{"jsonrpc":"2.0","id":"abc","error":{"code":-32601,"message":"Method not found"}}`,
		},
		{
			name:       "valid batch response passes through",
			body:       `[{"jsonrpc":"2.0","id":1,"result":{}},{"jsonrpc":"2.0","id":"two","result":{}}]`,
			wantStatus: http.StatusOK,
			wantBody:   `[{"jsonrpc":"2.0","id":1,"result":{}},{"jsonrpc":"2.0","id":"two","result":{}}]`,
		},
		{
			name:       "valid null result response passes through",
			body:       `{"jsonrpc":"2.0","id":1,"result":null}`,
			wantStatus: http.StatusOK,
			wantBody:   `{"jsonrpc":"2.0","id":1,"result":null}`,
		},
		{
			name:       "missing jsonrpc is rejected",
			body:       `{"id":1,"result":{"ok":true}}`,
			wantStatus: http.StatusBadGateway,
			wantBody:   `"Invalid upstream JSON-RPC response"`,
		},
		{
			name:       "invalid id type is rejected",
			body:       `{"jsonrpc":"2.0","id":{"nested":true},"result":{}}`,
			wantStatus: http.StatusBadGateway,
			wantBody:   `"JSON-RPC response id must be string, number, or null"`,
		},
		{
			name:       "non-object body is rejected",
			body:       `"not an object"`,
			wantStatus: http.StatusBadGateway,
			wantBody:   `"JSON-RPC response must be an object or array"`,
		},
		{
			name:       "result and error together are rejected",
			body:       `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32603,"message":"boom"}}`,
			wantStatus: http.StatusBadGateway,
			wantBody:   `"JSON-RPC response must include exactly one of result or error"`,
		},
		{
			name:       "trailing JSON value is rejected",
			body:       `{"jsonrpc":"2.0","id":1,"result":{}} {"jsonrpc":"2.0","id":2,"result":{}}`,
			wantStatus: http.StatusBadGateway,
			wantBody:   `"JSON-RPC response must contain a single JSON value"`,
		},
		{
			name:       "trailing delimiter is rejected",
			body:       `{"jsonrpc":"2.0","id":1,"result":{}}]`,
			wantStatus: http.StatusBadGateway,
			wantBody:   `"JSON-RPC response must contain a single JSON value"`,
		},
		{
			name:       "fractional error code is rejected",
			body:       `{"jsonrpc":"2.0","id":1,"error":{"code":1.5,"message":"nope"}}`,
			wantStatus: http.StatusBadGateway,
			wantBody:   `"JSON-RPC error response must include error.code and error.message"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := jsonResponse(tt.body)
			if tt.wantStatus == http.StatusBadGateway {
				// These sensitive headers must not survive a rewrite. Content-Encoding
				// is covered separately by TestNoOpResponseProcessorSkipsCompressedResponses;
				// setting it here would route through the pass-through gate instead.
				resp.Header.Set("Mcp-Session-Id", "upstream-session-leak")
				resp.Header.Set("Set-Cookie", "leak=1")
				resp.Header.Set("Etag", "\"upstream-etag\"")
				resp.Header.Set("Cache-Control", "private, max-age=60")
			}
			err := (&NoOpResponseProcessor{}).ProcessResponse(resp)
			require.NoError(t, err)

			gotBody, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Contains(t, string(gotBody), tt.wantBody)
			assert.Equal(t, int64(len(gotBody)), resp.ContentLength)
			assert.Equal(t, len(gotBody), int(resp.ContentLength))
			if tt.wantStatus == http.StatusBadGateway {
				// Wholesale header replacement: only Content-Type and Content-Length remain.
				assert.Empty(t, resp.Header.Get("Mcp-Session-Id"))
				assert.Empty(t, resp.Header.Get("Set-Cookie"))
				assert.Empty(t, resp.Header.Get("Etag"))
				assert.Empty(t, resp.Header.Get("Cache-Control"))
				assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
				assert.Nil(t, resp.Trailer)
			}
		})
	}
}

func TestNoOpResponseProcessorAcceptsJSONContentTypeParameters(t *testing.T) {
	t.Parallel()

	resp := jsonResponse(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	resp.Header.Set("Content-Type", "application/json; charset=utf-8")

	err := (&NoOpResponseProcessor{}).ProcessResponse(resp)
	require.NoError(t, err)

	gotBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, `{"jsonrpc":"2.0","id":1,"result":{}}`, string(gotBody))
}

func TestNoOpResponseProcessorSkipsNonJSONRPCResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		method      string
		status      int
		contentType string
		body        string
	}{
		{
			name:        "non-post response",
			method:      http.MethodGet,
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"resource":"https://example.com"}`,
		},
		{
			name:        "non-200 response",
			method:      http.MethodPost,
			status:      http.StatusAccepted,
			contentType: "application/json",
			body:        ``,
		},
		{
			name:        "non-json response",
			method:      http.MethodPost,
			status:      http.StatusOK,
			contentType: "text/plain",
			body:        `not json`,
		},
		{
			name:        "post response with event stream",
			method:      http.MethodPost,
			status:      http.StatusOK,
			contentType: "text/event-stream",
			body:        "event: message\ndata: {}\n\n",
		},
		{
			name:        "content type containing application/json is not enough",
			method:      http.MethodPost,
			status:      http.StatusOK,
			contentType: "application/jsonsomethingelse",
			body:        `not json`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := mcpRequest(tt.method)
			resp := &http.Response{
				StatusCode:    tt.status,
				Status:        http.StatusText(tt.status),
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(tt.body)),
				ContentLength: int64(len(tt.body)),
				Request:       req,
			}
			resp.Header.Set("Content-Type", tt.contentType)

			err := (&NoOpResponseProcessor{}).ProcessResponse(resp)
			require.NoError(t, err)

			gotBody, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Equal(t, tt.status, resp.StatusCode)
			assert.Equal(t, tt.body, string(gotBody))
		})
	}
}

// TestNoOpResponseProcessorSkipsCompressedResponses verifies that responses
// carrying a non-identity Content-Encoding are passed through unchanged.
// Decoding here would either reject legitimate compressed JSON-RPC frames or
// open a decompression-bomb amplification path.
func TestNoOpResponseProcessorSkipsCompressedResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		contentEncoding string
		body            string
	}{
		{
			name:            "gzip valid json is left alone",
			contentEncoding: "gzip",
			body:            gzipBytes(t, `{"jsonrpc":"2.0","id":1,"result":{}}`),
		},
		{
			name:            "gzip malformed body is left alone (no false reject)",
			contentEncoding: "gzip",
			body:            "not really gzip, but encoding header is set",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := jsonResponse(tt.body)
			resp.Header.Set("Content-Encoding", tt.contentEncoding)

			err := (&NoOpResponseProcessor{}).ProcessResponse(resp)
			require.NoError(t, err)

			gotBody, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.Equal(t, tt.body, string(gotBody))
		})
	}
}

// TestNoOpResponseProcessorValidatesUnderIdentityEncoding proves that an
// explicit Content-Encoding: identity does not bypass validation: a malformed
// JSON-RPC body must still produce a 502 rewrite.
func TestNoOpResponseProcessorValidatesUnderIdentityEncoding(t *testing.T) {
	t.Parallel()

	resp := jsonResponse(`{"id":1,"result":{"ok":true}}`) // missing jsonrpc → invalid
	resp.Header.Set("Content-Encoding", "identity")

	require.NoError(t, (&NoOpResponseProcessor{}).ProcessResponse(resp))

	gotBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Contains(t, string(gotBody), `"Invalid upstream JSON-RPC response"`)
}

// TestNoOpResponseProcessorRequiresMCPSignal narrows validation to traffic that
// carries an MCP streamable-HTTP signal on the request. application/json POST
// 200 responses from non-MCP traffic flowing through the catch-all proxy must
// not be rewritten.
func TestNoOpResponseProcessorRequiresMCPSignal(t *testing.T) {
	t.Parallel()

	body := `{"id":1,"result":{"ok":true}}` // missing jsonrpc — would be rejected if validated

	tests := []struct {
		name          string
		headers       map[string]string
		parsedContext bool
		validate      bool
	}{
		{
			name:     "no parsed context or MCP headers — pass through",
			validate: false,
		},
		{
			name:          "parsed MCP context without headers — validated",
			parsedContext: true,
			validate:      true,
		},
		{
			name:     "MCP-Protocol-Version header — validated",
			headers:  map[string]string{"MCP-Protocol-Version": "2025-06-18"},
			validate: true,
		},
		{
			name:     "Mcp-Session-Id header — validated",
			headers:  map[string]string{"Mcp-Session-Id": "session-abc"},
			validate: true,
		},
		{
			name:          "parsed MCP context and header — validated",
			headers:       map[string]string{"MCP-Protocol-Version": "2025-06-18"},
			parsedContext: true,
			validate:      true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequest(http.MethodPost, "http://example.com/mcp", nil)
			require.NoError(t, err)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			if tt.parsedContext {
				req = withParsedMCPRequest(req)
			}
			resp := &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				Header:        make(http.Header),
				Body:          io.NopCloser(strings.NewReader(body)),
				ContentLength: int64(len(body)),
				Request:       req,
			}
			resp.Header.Set("Content-Type", "application/json")

			require.NoError(t, (&NoOpResponseProcessor{}).ProcessResponse(resp))
			gotBody, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			if tt.validate {
				assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
				assert.Contains(t, string(gotBody), `"Invalid upstream JSON-RPC response"`)
			} else {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				assert.Equal(t, body, string(gotBody))
			}
		})
	}
}

// TestNoOpResponseProcessorRejectsOversizeResponse verifies the bounded read.
// The proxy is a security boundary; an unbounded io.ReadAll on attacker-
// controlled upstream bodies would amplify a malicious server into a memory
// DoS against the proxy.
func TestNoOpResponseProcessorRejectsOversizeResponse(t *testing.T) {
	t.Parallel()

	// Produce a body strictly larger than the cap. Content does not need to be
	// valid JSON-RPC — the size check fires before validation.
	oversize := strings.Repeat("a", maxJSONRPCResponseBytes+1)
	resp := jsonResponse(oversize)

	require.NoError(t, (&NoOpResponseProcessor{}).ProcessResponse(resp))

	gotBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Contains(t, string(gotBody), fmt.Sprintf("exceeds maximum allowed size of %d bytes", maxJSONRPCResponseBytes))
}

func jsonResponse(body string) *http.Response {
	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       mcpRequest(http.MethodPost),
	}
	resp.Header.Set("Content-Type", "application/json")
	return resp
}

func mcpRequest(method string) *http.Request {
	req, _ := http.NewRequest(method, "http://example.com/mcp", nil)
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	return req
}

func withParsedMCPRequest(req *http.Request) *http.Request {
	parsed := &mcpparser.ParsedMCPRequest{
		Method:    "tools/list",
		ID:        1,
		IsRequest: true,
	}
	ctx := context.WithValue(req.Context(), mcpparser.MCPRequestContextKey, parsed)
	return req.WithContext(ctx)
}

func gzipBytes(t *testing.T, payload string) string {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := gw.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	return buf.String()
}
