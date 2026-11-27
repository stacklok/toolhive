// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// TestResponseFilteringWriter_Non2xxStatusPreservedOnWire reproduces the
// production transparent-proxy wiring (real HTTP server + httputil.ReverseProxy
// with FlushInterval:-1 + ResponseFilteringWriter) and asserts that non-2xx
// result bodies are filtered without losing the backend status on the wire.
func TestResponseFilteringWriter_Non2xxStatusPreservedOnWire(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	backendResult := mcp.ListToolsResult{Tools: []mcp.Tool{
		{Name: "weather", Description: "Get weather information"},
		{Name: "calculator", Description: "Perform calculations"},
		{Name: "admin_tool", Description: "Administrative operations"},
	}}
	resultData, err := json.Marshal(backendResult)
	require.NoError(t, err)
	backendRPCResponse := &jsonrpc2.Response{ID: jsonrpc2.Int64ID(1), Result: json.RawMessage(resultData)}
	backendBody, err := jsonrpc2.EncodeMessage(backendRPCResponse)
	require.NoError(t, err)

	// Backend returns the same list body with a caller-chosen status code.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusOK
		if v := r.URL.Query().Get("code"); v != "" {
			fmt.Sscanf(v, "%d", &code)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(backendBody)
	}))
	t.Cleanup(backend.Close)
	backendURL, _ := url.Parse(backend.URL)

	// Frontend mirrors the authz-middleware + transparent-proxy wiring.
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
			Subject: "user123", Name: "Test User",
			Claims: jwt.MapClaims{"sub": "user123", "name": "Test User"},
		}}
		ctx := auth.WithIdentity(r.Context(), identity)
		parsed := &mcpparser.ParsedMCPRequest{Method: string(mcp.MethodToolsList), ID: float64(1)}
		ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, parsed)
		r = r.WithContext(ctx)

		filteringWriter := newTestFilteringWriter(w, authorizer, r, string(mcp.MethodToolsList), nil, nil)
		proxy := httputil.NewSingleHostReverseProxy(backendURL)
		proxy.FlushInterval = -1 // production transparent proxy
		proxy.ServeHTTP(filteringWriter, r)
		assert.NoError(t, filteringWriter.FlushAndFilter())
	}))
	t.Cleanup(frontend.Close)

	do := func(query string) (*http.Response, []byte) {
		resp, err := http.Get(frontend.URL + "/mcp" + query)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, body
	}

	// 2xx list responses must still be filtered (admin_tool removed).
	resp200, body200 := do("")
	assert.Equal(t, http.StatusOK, resp200.StatusCode)
	assert.Contains(t, string(body200), "weather")
	assert.NotContains(t, string(body200), "admin_tool")

	// HTTP status is not a confidentiality boundary: callers can inspect a
	// non-2xx response body, so those result bodies must be filtered too.
	for _, code := range []int{http.StatusInternalServerError, http.StatusNotFound} {
		resp, body := do(fmt.Sprintf("?code=%d", code))
		assert.Equalf(t, code, resp.StatusCode,
			"non-2xx backend status %d was rewritten on the wire", code)
		assert.Contains(t, string(body), "weather")
		assert.NotContains(t, string(body), "admin_tool")
	}
}

// newProxiedMiddleware serves the authz middleware chain (ParsingMiddleware and
// Middleware) in front of httputil.ReverseProxy with FlushInterval: -1, the
// transparent proxy's wiring, forwarding to backend. Every request carries a
// fixed identity, as the authentication middleware would set it.
func newProxiedMiddleware(t *testing.T, backend http.Handler) *httptest.Server {
	t.Helper()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	backendServer := httptest.NewServer(backend)
	t.Cleanup(backendServer.Close)
	backendURL, err := url.Parse(backendServer.URL)
	require.NoError(t, err)

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite:       func(pr *httputil.ProxyRequest) { pr.SetURL(backendURL) },
	}
	chain := mcpparser.ParsingMiddleware(Middleware(authorizer, proxy, nil))
	frontend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
			Subject: "user123", Claims: jwt.MapClaims{"sub": "user123"},
		}}
		chain.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
	}))
	t.Cleanup(frontend.Close)
	return frontend
}

// postMCP sends one JSON-RPC request with id 7 for method to the server at baseURL.
func postMCP(t *testing.T, baseURL, method string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"`+method+`"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestMiddlewareBehindReverseProxyCommitsFilteredHead pins what a client sees for
// a filtered method behind the transparent proxy. The proxy flushes after every
// body write. If that flush committed the head, the client would get a 200 with
// the backend's Content-Type and ETag even when the filter then failed closed, and
// the 500 it wrote would be dropped as a superfluous WriteHeader. Whatever the
// filter writes, the backend's entity headers describe the backend's bytes, so
// none of them reaches the client.
func TestMiddlewareBehindReverseProxyCommitsFilteredHead(t *testing.T) {
	t.Parallel()

	const tools = `{"tools":[` +
		`{"name":"weather","inputSchema":{"type":"object"}},` +
		`{"name":"admin_tool","inputSchema":{"type":"object"}}]}`
	const list = `{"jsonrpc":"2.0","id":7,"result":` + tools + `}`
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	deepResult := `{"jsonrpc":"2.0","id":7,"result":{"tools":[{"name":"admin_tool","inputSchema":{"x":` + deep + `}}]}}`
	gzippedList := gzipString(t, list)

	tests := []struct {
		name        string
		contentType string
		// contentEncoding labels the body's coding. Go's transport decodes a body
		// only when it is labeled exactly "gzip", so a backend that labels gzip
		// bytes with the "x-gzip" alias, which clients decode (RFC 9110 section
		// 8.4.1.3), reaches the filter still encoded.
		contentEncoding string
		// backendStatus is the backend's status. Zero means 200.
		backendStatus   int
		body            string
		wantStatus      int
		wantContentType string
	}{
		{
			name:            "mislabeled result past the depth cap fails closed",
			contentType:     "text/plain",
			body:            deepResult,
			wantStatus:      http.StatusInternalServerError,
			wantContentType: "application/json",
		},
		{
			name:            "mislabeled clean list is filtered and relabeled",
			contentType:     "text/plain",
			body:            list,
			wantStatus:      http.StatusOK,
			wantContentType: "application/json",
		},
		{
			name:            "labeled clean list is filtered",
			contentType:     "application/json",
			body:            list,
			wantStatus:      http.StatusOK,
			wantContentType: "application/json",
		},
		{
			name:            "labeled result past the depth cap fails closed",
			contentType:     "application/json",
			body:            deepResult,
			wantStatus:      http.StatusInternalServerError,
			wantContentType: "application/json",
		},
		{
			name:            "labeled notification smuggling a result fails closed",
			contentType:     "application/json",
			body:            `{"jsonrpc":"2.0","method":"notifications/message","result":` + tools + `}`,
			wantStatus:      http.StatusInternalServerError,
			wantContentType: "application/json",
		},
		{
			name:            "event stream list is filtered",
			contentType:     "text/event-stream",
			body:            "data: " + list + "\n\n",
			wantStatus:      http.StatusOK,
			wantContentType: "text/event-stream",
		},
		{
			name:            "encoded 2xx list fails closed",
			contentType:     "application/json",
			contentEncoding: "x-gzip",
			body:            gzippedList,
			wantStatus:      http.StatusInternalServerError,
			wantContentType: "application/json",
		},
		{
			name:            "encoded 500 list keeps the status and loses the body",
			contentType:     "application/json",
			contentEncoding: "x-gzip",
			backendStatus:   http.StatusInternalServerError,
			body:            gzippedList,
			wantStatus:      http.StatusInternalServerError,
			wantContentType: "application/json",
		},
		{
			// A client whose session expired still sees the 404 it needs to act on.
			name:            "encoded 404 list keeps the status and loses the body",
			contentType:     "application/json",
			contentEncoding: "x-gzip",
			backendStatus:   http.StatusNotFound,
			body:            gzippedList,
			wantStatus:      http.StatusNotFound,
			wantContentType: "application/json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			frontend := newProxiedMiddleware(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				if tt.contentEncoding != "" {
					w.Header().Set("Content-Encoding", tt.contentEncoding)
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(tt.body)))
				w.Header().Set("ETag", `"backend"`)
				w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
				w.Header().Set("Repr-Digest", "sha-256=:YmFja2VuZA==:")
				w.WriteHeader(cmp.Or(tt.backendStatus, http.StatusOK))
				_, _ = io.WriteString(w, tt.body)
			}))

			resp := postMCP(t, frontend.URL, string(mcp.MethodToolsList))
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err, "the body must match the head's framing")

			assert.Equal(t, tt.wantStatus, resp.StatusCode)
			assert.Equal(t, tt.wantContentType, resp.Header.Get("Content-Type"))
			assert.Equal(t, "private, no-store", resp.Header.Get("Cache-Control"))
			for _, name := range []string{"Content-Encoding", "ETag", "Last-Modified", "Repr-Digest"} {
				assert.Emptyf(t, resp.Header.Get(name), "the backend's %s must not describe the body the filter wrote", name)
			}
			// A client decodes the x-gzip alias, so check what it would read.
			readable := body
			if zr, zerr := gzip.NewReader(bytes.NewReader(body)); zerr == nil {
				readable, err = io.ReadAll(zr)
				require.NoError(t, err)
			}
			assert.NotContains(t, string(readable), "admin_tool")

			payload := body
			if tt.contentType == "text/event-stream" {
				events := parseSSEStream(t, body)
				require.Len(t, events, 1)
				payload = []byte(events[0].data)
			}
			message, err := jsonrpc2.DecodeMessage(payload)
			require.NoError(t, err)
			response, ok := message.(*jsonrpc2.Response)
			require.True(t, ok)
			assert.Equal(t, jsonrpc2.Int64ID(7), response.ID)
			if tt.wantStatus == http.StatusOK {
				require.Nil(t, response.Error)
				assert.Contains(t, string(response.Result), "weather")
			} else {
				require.NotNil(t, response.Error)
			}
		})
	}
}

// gzipString returns s compressed with gzip.
func gzipString(t *testing.T, s string) string {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	_, err := io.WriteString(gw, s)
	require.NoError(t, err)
	require.NoError(t, gw.Close())
	return buf.String()
}

// TestMiddlewareBehindReverseProxyFlushesUnfilteredHead pins the other side: a
// method whose response is not filtered (features/list reaches the same writer)
// still has its status and headers sent when the proxy flushes, before the
// backend finishes the body.
func TestMiddlewareBehindReverseProxyFlushesUnfilteredHead(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseBackend := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseBackend)

	frontend := newProxiedMiddleware(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend", "yes")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":7,`)
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, `"result":{}}`)
	}))

	got := make(chan *http.Response, 1)
	go func() {
		// The client returns once the head arrives, while the backend is still
		// holding the rest of the body.
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, frontend.URL+"/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"features/list"}`))
		if err != nil {
			close(got)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			close(got)
			return
		}
		got <- resp
	}()

	var resp *http.Response
	select {
	case r, ok := <-got:
		require.True(t, ok, "the request failed")
		resp = r
	case <-time.After(5 * time.Second):
		t.Fatal("the head of an unfiltered response was not sent before the backend finished")
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, "yes", resp.Header.Get("X-Backend"))

	releaseBackend()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"result":{}}`, string(body))
}

// TestMiddlewareBehindReverseProxyFilteredFraming pins the framing of a filtered
// method's response behind the transparent proxy, where FlushAndFilter commits
// the head after the backend has finished: a body stripped to nothing must not
// keep the backend's Content-Length, and backend trailers, which describe bytes
// the filter rewrote, must not be copied into the head or sent as trailers.
func TestMiddlewareBehindReverseProxyFilteredFraming(t *testing.T) {
	t.Parallel()

	const list = `{"jsonrpc":"2.0","id":7,"result":{"tools":[` +
		`{"name":"weather","inputSchema":{"type":"object"}},` +
		`{"name":"admin_tool","inputSchema":{"type":"object"}}]}}`

	tests := []struct {
		name    string
		backend http.HandlerFunc
		// wantFiltered expects the filtered list; otherwise an empty body.
		wantFiltered bool
		// wantAbsent names fields that must be neither in the head nor a trailer.
		wantAbsent []string
	}{
		{
			name: "BOM-only body drops the backend's Content-Length",
			backend: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", strconv.Itoa(len(mcpparser.UTF8BOM)))
				_, _ = w.Write(mcpparser.UTF8BOM)
			},
		},
		{
			// An empty body has nothing to filter, so it is not rejected for its
			// encoding.
			name: "empty gzip body passes through",
			backend: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Encoding", "gzip")
				w.WriteHeader(http.StatusOK)
			},
		},
		{
			name: "announced trailer is dropped",
			backend: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Trailer", "X-Body-Digest")
				_, _ = io.WriteString(w, list)
				w.Header().Set("X-Body-Digest", "sha-256=backend")
			},
			wantFiltered: true,
			wantAbsent:   []string{"X-Body-Digest", "Trailer"},
		},
		{
			// A trailer the backend did not announce reaches the proxy's writer
			// under http.TrailerPrefix, which net/http would send as a trailer.
			name: "unannounced trailer is dropped",
			backend: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Trailer", "X-Body-Digest")
				_, _ = io.WriteString(w, list)
				w.Header().Set("X-Body-Digest", "sha-256=backend")
				w.Header().Set(http.TrailerPrefix+"X-Late-Digest", "sha-256=late")
			},
			wantFiltered: true,
			wantAbsent:   []string{"X-Body-Digest", "X-Late-Digest", "Trailer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			frontend := newProxiedMiddleware(t, tt.backend)
			resp := postMCP(t, frontend.URL, string(mcp.MethodToolsList))
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err, "the body must match the head's framing")

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			for _, name := range tt.wantAbsent {
				assert.Emptyf(t, resp.Header.Values(name), "%s must not be in the response head", name)
				assert.Emptyf(t, resp.Trailer.Values(name), "%s must not be sent as a trailer", name)
			}
			if !tt.wantFiltered {
				assert.Empty(t, body)
				return
			}
			assert.Contains(t, string(body), "weather")
			assert.NotContains(t, string(body), "admin_tool")
		})
	}
}

// TestResponseFilteringWriter_TrailerNamingHeadFieldKeepsCachePolicy covers a
// backend announcing a head field as a trailer. Dropping the announced names
// must not remove the private cache policy the filter sets for a list.
func TestResponseFilteringWriter_TrailerNamingHeadFieldKeepsCachePolicy(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
	rr := httptest.NewRecorder()
	rfw := NewResponseFilteringWriter(rr, authorizer, req, string(mcp.MethodToolsList), nil, nil)
	rfw.Header().Set("Content-Type", "application/json")
	rfw.Header().Set("Trailer", "Cache-Control")
	rfw.WriteHeader(http.StatusOK)
	_, err = rfw.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{"tools":[]}}`))
	require.NoError(t, err)
	// httputil.ReverseProxy copies the trailer's value into the head map.
	rfw.Header().Set("Cache-Control", "public, max-age=60")
	require.NoError(t, rfw.FlushAndFilter())

	committed := rr.Result()
	t.Cleanup(func() { _ = committed.Body.Close() })
	assert.Equal(t, "private, no-store", committed.Header.Get("Cache-Control"))
	assert.Empty(t, committed.Header.Get("Trailer"))
}
