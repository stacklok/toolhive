// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// TestResponseFilteringWriter_ContentEncodedListFailsClosed reproduces
// GHSA-v869-pp2g-ff4q: a backend that compresses a list response (whether or
// not the client asked for it) must not slip past the filter. The writer
// cannot inspect a compressed body, so it must refuse to forward it rather than
// pass the unfiltered descriptors through.
func TestResponseFilteringWriter_ContentEncodedListFailsClosed(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	listResult, err := json.Marshal(mcp.ListToolsResult{Tools: []mcp.Tool{
		{Name: "weather", Description: "Get weather information"},
		{Name: "admin_tool", Description: "Administrative operations"},
	}})
	require.NoError(t, err)
	plainBody, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: listResult})
	require.NoError(t, err)

	var gzipped bytes.Buffer
	gw := gzip.NewWriter(&gzipped)
	_, err = gw.Write(plainBody)
	require.NoError(t, err)
	require.NoError(t, gw.Close())

	tests := []struct {
		name            string
		contentEncoding string
		body            []byte
		wantFiltered    bool // true: list is filtered and returned; false: fail closed
	}{
		{name: "gzip", contentEncoding: "gzip", body: gzipped.Bytes()},
		{name: "brotli label", contentEncoding: "br", body: []byte("opaque bytes a gzip reader cannot decode")},
		{name: "mixed case and stacked", contentEncoding: "Identity, GZIP", body: gzipped.Bytes()},
		{name: "explicit identity is filtered", contentEncoding: "identity", body: plainBody, wantFiltered: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Encoding", tt.contentEncoding)
				_, _ = w.Write(tt.body)
			}))
			t.Cleanup(backend.Close)
			backendURL, err := url.Parse(backend.URL)
			require.NoError(t, err)

			frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
					Subject: "user123", Name: "Test User",
					Claims: jwt.MapClaims{"sub": "user123", "name": "Test User"},
				}}
				ctx := auth.WithIdentity(r.Context(), identity)
				parsed := &mcpparser.ParsedMCPRequest{Method: string(mcp.MethodToolsList), ID: float64(1)}
				ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, parsed)
				r = r.WithContext(ctx)

				filteringWriter := NewResponseFilteringWriter(w, authorizer, r, string(mcp.MethodToolsList), nil, nil)
				proxy := httputil.NewSingleHostReverseProxy(backendURL)
				proxy.FlushInterval = -1 // production transparent proxy
				proxy.ServeHTTP(filteringWriter, r)
				assert.NoError(t, filteringWriter.FlushAndFilter())
			}))
			t.Cleanup(frontend.Close)

			// Ask for raw bytes so the test client does not decode on the wire.
			client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, frontend.URL+"/mcp", nil)
			require.NoError(t, err)
			req.Header.Set("Accept-Encoding", "gzip")
			resp, err := client.Do(req)
			require.NoError(t, err)
			t.Cleanup(func() { _ = resp.Body.Close() })
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			if tt.wantFiltered {
				assert.Equal(t, http.StatusOK, resp.StatusCode)
				assert.Contains(t, string(body), "weather")
				assert.NotContains(t, string(body), "admin_tool")
				return
			}

			assert.Empty(t, resp.Header.Get("Content-Encoding"),
				"the replacement error body is plain JSON and must not carry the backend's encoding")
			assert.NotContains(t, string(body), "admin_tool")
			assert.NotContains(t, string(body), "weather")
			assert.Contains(t, string(body), "internal error")
		})
	}
}

// TestResponseFilteringWriter_ContentEncodedWithoutReverseProxy covers writers
// that reach the filter by paths httputil.ReverseProxy never takes: a Write with
// no WriteHeader, and a Flush that commits headers before FlushAndFilter runs.
func TestResponseFilteringWriter_ContentEncodedWithoutReverseProxy(t *testing.T) {
	t.Parallel()

	opaque := []byte("opaque-encoded-bytes")
	backendHeaders := func(h http.Header, contentType string) {
		h.Set("Content-Type", contentType)
		h.Set("Content-Encoding", "gzip")
		h.Set("Content-Length", "20")
		h.Set("ETag", `"abc"`)
		h.Set("Content-MD5", "deadbeef")
		h.Set("Content-Range", "bytes 0-19/20")
	}

	tests := []struct {
		name string
		// method the filter was built for.
		method string
		drive  func(rfw *ResponseFilteringWriter)
		// wantRejected expects the 500 JSON-RPC error in place of the body.
		wantRejected bool
		// wantStatus is the forwarded status when the response is not rejected.
		// Zero means 200.
		wantStatus int
	}{
		{
			name:   "write without WriteHeader",
			method: string(mcp.MethodToolsList),
			drive: func(rfw *ResponseFilteringWriter) {
				backendHeaders(rfw.Header(), "application/json")
				_, _ = rfw.Write(opaque)
			},
			wantRejected: true,
		},
		{
			name:   "write after WriteHeader",
			method: string(mcp.MethodToolsList),
			drive: func(rfw *ResponseFilteringWriter) {
				backendHeaders(rfw.Header(), "application/json")
				rfw.WriteHeader(http.StatusOK)
				_, _ = rfw.Write(opaque)
			},
			wantRejected: true,
		},
		{
			name:   "flushed event stream commits the rejection head",
			method: string(mcp.MethodToolsList),
			drive: func(rfw *ResponseFilteringWriter) {
				backendHeaders(rfw.Header(), "text/event-stream")
				rfw.WriteHeader(http.StatusOK)
				_, _ = rfw.Write(opaque)
				rfw.Flush()
			},
			wantRejected: true,
		},
		{
			name:   "flush before the body arrives",
			method: string(mcp.MethodToolsList),
			drive: func(rfw *ResponseFilteringWriter) {
				backendHeaders(rfw.Header(), "text/event-stream")
				rfw.WriteHeader(http.StatusOK)
				rfw.Flush()
				_, _ = rfw.Write(opaque)
			},
			wantRejected: true,
		},
		{
			name:   "unfiltered method keeps the backend response",
			method: string(mcp.MethodToolsCall),
			drive: func(rfw *ResponseFilteringWriter) {
				backendHeaders(rfw.Header(), "application/json")
				rfw.WriteHeader(http.StatusOK)
				_, _ = rfw.Write(opaque)
			},
		},
		{
			// Only a 2xx can carry a list, so an encoded error status is
			// forwarded intact rather than replaced by an opaque 500 -- a client
			// whose session expired still sees the 404 it needs to act on.
			name:   "encoded non-2xx keeps the backend status",
			method: string(mcp.MethodToolsList),
			drive: func(rfw *ResponseFilteringWriter) {
				backendHeaders(rfw.Header(), "application/json")
				rfw.WriteHeader(http.StatusNotFound)
				_, _ = rfw.Write(opaque)
			},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			rfw := NewResponseFilteringWriter(rec, nil, req, tt.method, nil, nil)

			tt.drive(rfw)
			require.NoError(t, rfw.FlushAndFilter())

			resp := rec.Result()
			t.Cleanup(func() { _ = resp.Body.Close() })
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			if !tt.wantRejected {
				wantStatus := cmp.Or(tt.wantStatus, http.StatusOK)
				assert.Equal(t, wantStatus, resp.StatusCode)
				assert.Equal(t, "gzip", resp.Header.Get("Content-Encoding"))
				assert.Equal(t, opaque, body)
				return
			}

			assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
			for _, h := range []string{"Content-Encoding", "Content-Length", "ETag", "Content-MD5", "Content-Range"} {
				assert.Empty(t, resp.Header.Get(h), "%s describes the backend body, not the error", h)
			}
			assert.NotContains(t, string(body), string(opaque))
			assert.Contains(t, string(body), "internal error")
		})
	}
}

// TestAuthorizeListAndServe_WithholdsAcceptEncoding verifies the middleware owns
// the encoding negotiation for the responses it filters: the client's
// Accept-Encoding never reaches the backend, so a backend that compresses on
// request is still filtered, and the caller's request is left untouched.
func TestAuthorizeListAndServe_WithholdsAcceptEncoding(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	listResult, err := json.Marshal(mcp.ListToolsResult{Tools: []mcp.Tool{
		{Name: "weather", Description: "Get weather information"},
		{Name: "admin_tool", Description: "Administrative operations"},
	}})
	require.NoError(t, err)
	plainBody, err := jsonrpc2.EncodeMessage(&jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: listResult})
	require.NoError(t, err)

	const clientEncoding = "br, gzip"

	var backendSaw atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ae := r.Header.Get("Accept-Encoding")
		backendSaw.Store(ae)
		w.Header().Set("Content-Type", "application/json")
		// A backend that compresses whenever it is allowed to.
		if strings.Contains(ae, "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			gw := gzip.NewWriter(w)
			_, _ = gw.Write(plainBody)
			_ = gw.Close()
			return
		}
		_, _ = w.Write(plainBody)
	}))
	t.Cleanup(backend.Close)
	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)

	var callerEncoding atomic.Value
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
			Subject: "user123", Name: "Test User",
			Claims: jwt.MapClaims{"sub": "user123", "name": "Test User"},
		}}
		parsed := &mcpparser.ParsedMCPRequest{Method: string(mcp.MethodToolsList), ID: float64(1)}
		ctx := context.WithValue(auth.WithIdentity(r.Context(), identity), mcpparser.MCPRequestContextKey, parsed)
		r = r.WithContext(ctx)

		proxy := httputil.NewSingleHostReverseProxy(backendURL)
		proxy.FlushInterval = -1 // production transparent proxy
		authorizeListAndServe(w, r, authorizer, parsed, featureOperation{}, nil, nil, proxy)
		callerEncoding.Store(r.Header.Get("Accept-Encoding"))
	}))
	t.Cleanup(frontend.Close)

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, frontend.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", clientEncoding)
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Asserting the exact value, not just that it changed, proves Go's transport
	// took over the negotiation — which is what makes the body decodable here.
	assert.Equal(t, "gzip", backendSaw.Load(),
		"the backend should see the transport's own Accept-Encoding, not the client's")
	assert.Equal(t, clientEncoding, callerEncoding.Load(), "the caller's request was modified")
	assert.Empty(t, resp.Header.Get("Content-Encoding"))
	assert.Contains(t, string(body), "weather")
	assert.NotContains(t, string(body), "admin_tool")
}

// TestAuthorizeListAndServe_UnfilteredListKeepsCompression covers the other side
// of the negotiation. features/list reaches authorizeListAndServe but has no
// response filter registered, so nothing here needs to read its body and the
// client's Accept-Encoding is forwarded unchanged -- withholding it would cost
// the hop its compression for no benefit.
func TestAuthorizeListAndServe_UnfilteredListKeepsCompression(t *testing.T) {
	t.Parallel()

	const clientEncoding = "br, gzip"

	var backendSaw atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendSaw.Store(r.Header.Get("Accept-Encoding"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	t.Cleanup(backend.Close)
	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)

	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parsed := &mcpparser.ParsedMCPRequest{Method: "features/list", ID: float64(1)}
		r = r.WithContext(context.WithValue(r.Context(), mcpparser.MCPRequestContextKey, parsed))

		proxy := httputil.NewSingleHostReverseProxy(backendURL)
		proxy.FlushInterval = -1 // production transparent proxy
		// Feature is empty for a protocol-only list method, which is what lets it
		// past the fail-closed guard without a registered response filter.
		authorizeListAndServe(w, r, nil, parsed, featureOperation{}, nil, nil, proxy)
	}))
	t.Cleanup(frontend.Close)

	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, frontend.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Accept-Encoding", clientEncoding)
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, clientEncoding, backendSaw.Load(),
		"an unfiltered list method should keep end-to-end compression")
}
