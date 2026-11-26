// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"encoding/json"
	"fmt"
	"io"
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

func TestRoundTripMessageKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		body            string
		protocolVersion string
		wantInitialized bool
		wantRejected    bool
	}{
		{
			name: "single request with id",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		},
		{
			name: "request preserves whitespace and exact id",
			body: " \n" + `{"jsonrpc":"2.0", "id":9007199254740993, "method":"tools/list", "params":{}}` + "\t\r\n",
		},
		{
			name:         "malformed JSON on arbitrary path",
			body:         `{"jsonrpc":`,
			wantRejected: true,
		},
		{
			name:         "non-MCP JSON on arbitrary path",
			body:         `{"action":"submit","value":"example"}`,
			wantRejected: true,
		},
		{
			name:         "form body on arbitrary path",
			body:         `action=submit&value=example`,
			wantRejected: true,
		},
		{
			name:            "single initialize",
			body:            `{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
			wantInitialized: true,
		},
		{
			name:            "initialize notification retains handshake behavior",
			body:            `{"jsonrpc":"2.0","method":"initialize"}`,
			protocolVersion: mcp.MCPVersionModern,
			wantInitialized: true,
		},
		{
			name:            "notification is not revision classified",
			body:            `{"jsonrpc":"2.0","method":"notifications/progress"}`,
			protocolVersion: mcp.MCPVersionModern,
		},
		{
			name:            "response is not revision classified",
			body:            `{"jsonrpc":"2.0","id":9007199254740993,"result":{}}`,
			protocolVersion: mcp.MCPVersionModern,
		},
		{
			name:            "error response without id is not revision classified",
			body:            `{"jsonrpc":"2.0","error":{"code":-32603,"message":"client error"}}`,
			protocolVersion: mcp.MCPVersionModern,
		},
		{
			name:         "null id is rejected",
			body:         `{"jsonrpc":"2.0","id":null,"method":"tools/list"}`,
			wantRejected: true,
		},
		{
			name:         "batch initialize does not start handshake",
			body:         `[{"jsonrpc":"2.0","id":1,"method":"initialize"}]`,
			wantRejected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			backendCalled := false
			spy := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				backendCalled = true
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				assert.Equal(t, tc.body, string(body), "forward the original envelope")
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
			})
			p := NewTransparentProxy("127.0.0.1", 0, "http://backend", nil, nil, nil, false, false,
				"", nil, nil, "", false)
			t.Cleanup(func() { require.NoError(t, p.sessionManager.Stop()) })
			req := httptest.NewRequest(http.MethodPost, "/custom", strings.NewReader(tc.body))
			req.Header.Set("MCP-Protocol-Version", tc.protocolVersion)

			resp, err := newTracingTransport(spy, p).RoundTrip(req)
			require.NoError(t, err)
			drainAndClose(t, resp)
			if tc.wantRejected {
				assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			} else {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
			}
			assert.Equal(t, !tc.wantRejected, backendCalled)
			assert.Equal(t, tc.wantInitialized, p.serverInitialized())
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

	t.Run("batch is rejected before classification, backend not contacted", func(t *testing.T) {
		t.Parallel()

		var backendCalled atomic.Bool
		spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
			backendCalled.Store(true)
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
		})
		tt, _ := newProxy(spy)

		// Batches are rejected outright (batching was removed in MCP 2025-06-18)
		// before revision classification or forwarding — see #5745.
		req := httptest.NewRequest(http.MethodPost, "/mcp",
			strings.NewReader(`[{"jsonrpc":"2.0","id":1,"method":"tools/call"}]`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)

		resp, err := tt.RoundTrip(req)
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "batches must be rejected, not forwarded")
		assert.False(t, backendCalled.Load(), "backend must not be contacted for a rejected batch")
	})

	for _, rawID := range []string{`9007199254740993`, `0`, `"request-1"`, `""`} {
		for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
			t.Run(fmt.Sprintf("local_error_%d_id_%s", status, rawID), func(t *testing.T) {
				t.Parallel()

				backendCalls := 0
				spy := roundTripFunc(func(*http.Request) (*http.Response, error) {
					backendCalls++
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
				})
				tt, p := newProxy(spy)
				t.Cleanup(func() { require.NoError(t, p.sessionManager.Stop()) })

				req := httptest.NewRequest(http.MethodPost, "/mcp",
					strings.NewReader(`{"jsonrpc":"2.0","id":`+rawID+`,"method":"tools/call"}`))
				if status == http.StatusBadRequest {
					req.Header.Set("MCP-Protocol-Version", mcp.MCPVersionModern)
				} else {
					req.Header.Set("Mcp-Session-Id", "unknown-session")
				}

				resp, err := tt.RoundTrip(req)
				require.NoError(t, err)
				require.NotNil(t, resp)
				t.Cleanup(func() { drainAndClose(t, resp) })
				require.Equal(t, status, resp.StatusCode)
				assert.Zero(t, backendCalls)

				var decoded struct {
					ID    json.RawMessage  `json:"id"`
					Error *json.RawMessage `json:"error"`
				}
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))
				assert.Equal(t, rawID, string(decoded.ID), "preserve the exact ID in local errors")
				assert.NotNil(t, decoded.Error)
			})
		}
	}

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
