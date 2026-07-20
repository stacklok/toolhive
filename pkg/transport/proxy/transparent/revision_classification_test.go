// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/mcp"
)

// roundTripFunc adapts a function to http.RoundTripper, letting tests spy on
// whether the backend transport was invoked without standing up a real server.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestParseRPCRequest(t *testing.T) {
	t.Parallel()

	tp := &tracingTransport{p: &TransparentProxy{targetURI: "http://backend"}}

	tests := []struct {
		name              string
		body              string
		wantMethod        string
		wantID            string // "" means nil/absent
		wantSingleRequest bool
		wantSawInitialize bool
	}{
		{
			name:              "single request with id",
			body:              `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			wantMethod:        "tools/list",
			wantID:            "1",
			wantSingleRequest: true,
		},
		{
			name:              "single initialize",
			body:              `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
			wantMethod:        "initialize",
			wantID:            "1",
			wantSingleRequest: true,
			wantSawInitialize: true,
		},
		{
			name:              "notification has no id",
			body:              `{"jsonrpc":"2.0","method":"notifications/progress"}`,
			wantMethod:        "notifications/progress",
			wantSingleRequest: false,
		},
		{
			name:              "explicit null id is not a valid request id",
			body:              `{"jsonrpc":"2.0","id":null,"method":"tools/list"}`,
			wantMethod:        "tools/list",
			wantID:            "null",
			wantSingleRequest: false,
		},
		{
			name:              "response-shaped body has no method",
			body:              `{"jsonrpc":"2.0","id":1,"result":{}}`,
			wantMethod:        "",
			wantID:            "1",
			wantSingleRequest: false,
		},
		{
			name:              "batch with initialize",
			body:              `[{"jsonrpc":"2.0","id":1,"method":"initialize"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`,
			wantSingleRequest: false,
			wantSawInitialize: true,
		},
		{
			name:              "batch without initialize",
			body:              `[{"jsonrpc":"2.0","id":1,"method":"tools/list"},{"jsonrpc":"2.0","id":2,"method":"tools/call"}]`,
			wantSingleRequest: false,
		},
		{
			name:              "malformed JSON",
			body:              `{not valid json`,
			wantSingleRequest: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			method, _, id, singleRequest, sawInitialize := tp.parseRPCRequest([]byte(tc.body))

			assert.Equal(t, tc.wantMethod, method)
			assert.Equal(t, tc.wantSingleRequest, singleRequest)
			assert.Equal(t, tc.wantSawInitialize, sawInitialize)
			if tc.wantID == "" {
				assert.Empty(t, string(id))
			} else {
				assert.Equal(t, tc.wantID, string(id))
			}
		})
	}
}

// TestRoundTripClassifiesModernRequests drives tracingTransport.RoundTrip
// directly (bypassing httputil.ReverseProxy) with a spy backend RoundTripper,
// so tests can assert both the returned response and whether the backend was
// ever contacted.
func TestRoundTripClassifiesModernRequests(t *testing.T) {
	t.Parallel()

	newProxy := func(spy http.RoundTripper) (*tracingTransport, *TransparentProxy) {
		p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false,
			"streamable-http", nil, nil, "", false)
		return newTracingTransport(spy, p), p
	}

	t.Run("malformed Modern single-request is rejected before the backend is called", func(t *testing.T) {
		t.Parallel()

		var backendCalled atomic.Bool
		spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
			backendCalled.Store(true)
			return httptest.NewRecorder().Result(), nil
		})
		tt, _ := newProxy(spy)

		// Header claims Modern but the body carries no _meta at all: a
		// HeaderMismatchError (bad/absent body version, non-empty header).
		req := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)

		resp, err := tt.RoundTrip(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.False(t, backendCalled.Load(), "backend must not be contacted for a rejected request")
	})

	t.Run("well-formed Modern single-request falls through to the backend", func(t *testing.T) {
		t.Parallel()

		var backendCalled atomic.Bool
		spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
			backendCalled.Store(true)
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		})
		tt, _ := newProxy(spy)

		// Not "initialize", has a valid id, and _meta carries a matching
		// protocolVersion plus clientCapabilities: ClassifyRevision returns
		// (RevisionModern, nil), so the request must reach the backend.
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{` +
			`"io.modelcontextprotocol/protocolVersion":"` + mcp.MCPVersionModern + `",` +
			`"io.modelcontextprotocol/clientCapabilities":{}}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)

		resp, err := tt.RoundTrip(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "a well-formed Modern request must not be rejected")
		assert.True(t, backendCalled.Load(), "backend must be contacted for a well-formed Modern request")
	})

	t.Run("batch with Modern header is forwarded, not rejected", func(t *testing.T) {
		t.Parallel()

		var backendCalled atomic.Bool
		spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
			backendCalled.Store(true)
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		})
		tt, _ := newProxy(spy)

		req := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`[{"jsonrpc":"2.0","id":1,"method":"tools/call"}]`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)

		resp, err := tt.RoundTrip(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "batches are never classified, so they must be forwarded")
		assert.True(t, backendCalled.Load(), "backend must be contacted for a forwarded batch")
	})

	t.Run("large-integer id is preserved verbatim in the 400 body", func(t *testing.T) {
		t.Parallel()

		const largeID = "9007199254740993" // 2^53 + 1: loses precision if round-tripped through float64
		spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("backend must not be contacted for a rejected request")
			return nil, nil
		})
		tt, _ := newProxy(spy)

		req := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":`+largeID+`,"method":"tools/call"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)

		resp, err := tt.RoundTrip(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var decoded struct {
			ID json.RawMessage `json:"id"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))
		assert.Equal(t, largeID, string(decoded.ID), "large integer id must not lose precision")
	})

	t.Run("Modern 200 response flips serverInitialized", func(t *testing.T) {
		t.Parallel()

		spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		})
		tt, p := newProxy(spy)
		require.False(t, p.serverInitialized(), "precondition: latch starts unset")

		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{` +
			`"io.modelcontextprotocol/protocolVersion":"` + mcp.MCPVersionModern + `",` +
			`"io.modelcontextprotocol/clientCapabilities":{}}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)

		resp, err := tt.RoundTrip(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.True(t, p.serverInitialized(), "a 200 to a Modern request must flip the readiness latch")
	})

	t.Run("Modern non-200 response does not flip serverInitialized", func(t *testing.T) {
		t.Parallel()

		spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: http.NoBody}, nil
		})
		tt, p := newProxy(spy)
		require.False(t, p.serverInitialized(), "precondition: latch starts unset")

		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{` +
			`"io.modelcontextprotocol/protocolVersion":"` + mcp.MCPVersionModern + `",` +
			`"io.modelcontextprotocol/clientCapabilities":{}}}}`
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)

		resp, err := tt.RoundTrip(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		assert.False(t, p.serverInitialized(), "a non-200 response must not flip the readiness latch")
	})

	t.Run("Legacy 200 response without session header or initialize does not flip serverInitialized", func(t *testing.T) {
		t.Parallel()

		spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		})
		tt, p := newProxy(spy)
		require.False(t, p.serverInitialized(), "precondition: latch starts unset")

		// Legacy request (no MCP-Protocol-Version header, not initialize) whose
		// 200 response carries no Mcp-Session-Id: neither existing branch fires,
		// and the new Modern branch must not broaden to cover this case either.
		req := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")

		resp, err := tt.RoundTrip(req)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.False(t, p.serverInitialized(), "a Legacy 200 with no session header must not flip the readiness latch")
	})
}
