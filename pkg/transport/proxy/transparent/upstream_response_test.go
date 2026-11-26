// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/mcp"
)

const (
	toolsListIntID    = `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`
	toolsListStringID = `{"jsonrpc":"2.0","id":"req-7","method":"tools/list"}`
	notification      = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	validResult       = `{"jsonrpc":"2.0","id":7,"result":{}}`
)

// upstreamBackend describes one canned backend response.
type upstreamBackend struct {
	status      int
	contentType string
	// encoding is sent as Content-Encoding unprompted, with body as-is.
	encoding string
	// gzipIfAsked gzips body when the proxy's request accepts gzip.
	gzipIfAsked bool
	body        []byte
}

// serveUpstream starts a backend that answers every request with b. It
// always sets Mcp-Session-Id and Set-Cookie so tests can check that a
// rejection does not leak them. The returned value records the
// Accept-Encoding the backend last saw.
func serveUpstream(t *testing.T, b upstreamBackend) (*httptest.Server, *atomic.Value) {
	t.Helper()
	var sawAcceptEncoding atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAcceptEncoding.Store(r.Header.Get("Accept-Encoding"))
		w.Header().Set("Mcp-Session-Id", "upstream-session")
		w.Header().Set("Set-Cookie", "upstream=secret")
		if b.contentType != "" {
			w.Header().Set("Content-Type", b.contentType)
		}
		body := b.body
		switch {
		case b.encoding != "":
			w.Header().Set("Content-Encoding", b.encoding)
		case b.gzipIfAsked && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip"):
			var buf bytes.Buffer
			zw := gzip.NewWriter(&buf)
			_, _ = zw.Write(body)
			_ = zw.Close()
			body = buf.Bytes()
			w.Header().Set("Content-Encoding", "gzip")
		}
		w.WriteHeader(b.status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(backend.Close)
	return backend, &sawAcceptEncoding
}

// newValidatingProxy returns a streamable-http proxy and a reverse proxy using
// its tracingTransport against backendURL. The backend leg uses a dedicated
// transport rather than http.DefaultTransport: httptest.Server.Close in the
// package's parallel tests calls CloseIdleConnections on the shared transport,
// which could break a pooled connection and surface as a spurious 502.
func newValidatingProxy(t *testing.T, backendURL string) (*TransparentProxy, http.Handler) {
	t.Helper()
	p := NewTransparentProxy("127.0.0.1", 0, backendURL, nil, nil, nil,
		false, false, "streamable-http", nil, nil, "", false)
	t.Cleanup(func() { _ = p.sessionManager.Stop() })
	target, err := url.Parse(backendURL)
	require.NoError(t, err)
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	return p, &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
		},
		FlushInterval:  -1,
		Transport:      newTracingTransport(transport, p),
		ModifyResponse: p.modifyResponse,
	}
}

// postThroughProxy sends body through the proxy and returns the recorded response.
func postThroughProxy(t *testing.T, handler http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// assertInvalidUpstreamRejection checks the 502 that replaces a rejected
// upstream response: a JSON-RPC error echoing wantID and no upstream headers.
func assertInvalidUpstreamRejection(t *testing.T, rec *httptest.ResponseRecorder, wantID any) {
	t.Helper()
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Header().Get("Mcp-Session-Id"), "upstream session id must not reach the client")
	assert.Empty(t, rec.Header().Values("Set-Cookie"), "upstream cookies must not reach the client")
	assert.Empty(t, rec.Header().Get("Content-Encoding"))

	msg, err := mcp.DecodeMessage(rec.Body.Bytes())
	require.NoError(t, err, "rejection must be a valid JSON-RPC message: %s", rec.Body.String())
	resp, ok := msg.(*jsonrpc2.Response)
	require.True(t, ok, "rejection must be a JSON-RPC response")
	assert.Equal(t, wantID, resp.ID.Raw())
	require.Error(t, resp.Error)
	var envelope struct {
		Error struct {
			Code    int64  `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, mcp.CodeInternalError, envelope.Error.Code)
	assert.Equal(t, invalidUpstreamResponseMessage, envelope.Error.Message)
}

func TestTransparentProxyRejectsInvalidUpstreamResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request string
		backend upstreamBackend
		wantID  any
	}{
		{
			name:    "not JSON, int id",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", body: []byte(`not json`)},
			wantID:  int64(7),
		},
		{
			name:    "result without id, string id",
			request: toolsListStringID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", body: []byte(`{"jsonrpc":"2.0","result":{}}`)},
			wantID:  "req-7",
		},
		{
			name:    "empty body",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json"},
			wantID:  int64(7),
		},
		{
			name:    "batch",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", body: []byte(`[` + validResult + `]`)},
			wantID:  int64(7),
		},
		{
			name:    "request instead of response",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", body: []byte(toolsListIntID)},
			wantID:  int64(7),
		},
		{
			name:    "null id",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", body: []byte(`{"jsonrpc":"2.0","id":null,"result":{}}`)},
			wantID:  int64(7),
		},
		{
			name:    "media type matched case-insensitively with parameters",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "Application/JSON ; charset=utf-8", body: []byte(`{`)},
			wantID:  int64(7),
		},
		{
			name:    "malformed body gzipped on request",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", gzipIfAsked: true, body: []byte(`{"broken"`)},
			wantID:  int64(7),
		},
		{
			name:    "unprompted content coding Go cannot decode",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", encoding: "br", body: []byte(validResult)},
			wantID:  int64(7),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backend, _ := serveUpstream(t, tt.backend)
			_, handler := newValidatingProxy(t, backend.URL)
			rec := postThroughProxy(t, handler, tt.request, map[string]string{"Accept-Encoding": "gzip, br"})
			assertInvalidUpstreamRejection(t, rec, tt.wantID)
		})
	}
}

func TestTransparentProxyForwardsValidOrUnvalidatedUpstreamResponses(t *testing.T) {
	t.Parallel()

	large := []byte(`{"jsonrpc":"2.0","id":7,"result":{"text":"` + strings.Repeat("x", 5<<20) + `"}}`)
	bom := append([]byte("\xEF\xBB\xBF"), validResult...)

	tests := []struct {
		name    string
		request string
		backend upstreamBackend
		// wantBody defaults to backend.body.
		wantBody []byte
	}{
		{
			name:    "valid result",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", body: []byte(validResult)},
		},
		{
			name:    "valid error with id",
			request: toolsListStringID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json",
				body: []byte(`{"jsonrpc":"2.0","id":"req-7","error":{"code":-32601,"message":"Method not found"}}`)},
		},
		{
			name:    "valid error without id",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json",
				body: []byte(`{"jsonrpc":"2.0","error":{"code":-32700,"message":"Parse error"}}`)},
		},
		{
			name:    "case-distinct member names inside result",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json",
				body: []byte(`{"jsonrpc":"2.0","id":7,"result":{"tools":[{"name":"t","inputSchema":{"properties":{"ID":{},"id":{}}}}]}}`)},
		},
		{
			name:    "BOM-prefixed valid result forwarded byte for byte",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", body: bom},
		},
		{
			name:    "5 MB valid result forwarded byte for byte",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", body: large},
		},
		{
			name:     "valid result gzipped on request is decoded",
			request:  toolsListIntID,
			backend:  upstreamBackend{status: http.StatusOK, contentType: "application/json", gzipIfAsked: true, body: []byte(validResult)},
			wantBody: []byte(validResult),
		},
		{
			name:    "identity content coding",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json", encoding: "identity", body: []byte(validResult)},
		},
		{
			name:    "notification answered 200 with empty body",
			request: notification,
			backend: upstreamBackend{status: http.StatusOK, contentType: "application/json"},
		},
		{
			name:    "notification answered 202",
			request: notification,
			backend: upstreamBackend{status: http.StatusAccepted},
		},
		{
			name:    "SSE response is not validated",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusOK, contentType: "text/event-stream", body: []byte("data: not json\n\n")},
		},
		{
			name:    "non-200 status is not validated",
			request: toolsListIntID,
			backend: upstreamBackend{status: http.StatusInternalServerError, contentType: "application/json", body: []byte(`not json`)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backend, sawAcceptEncoding := serveUpstream(t, tt.backend)
			_, handler := newValidatingProxy(t, backend.URL)
			rec := postThroughProxy(t, handler, tt.request, map[string]string{"Accept-Encoding": "gzip, br"})

			want := tt.wantBody
			if want == nil {
				want = tt.backend.body
			}
			require.Equal(t, tt.backend.status, rec.Code, "body: %.200s", rec.Body.String())
			assert.True(t, bytes.Equal(want, rec.Body.Bytes()), "forwarded body must be unchanged")
			// The client's Accept-Encoding is withheld only from id-bearing
			// requests; Go's transport then asks for gzip itself.
			wantAcceptEncoding := "gzip"
			if tt.request == notification {
				wantAcceptEncoding = "gzip, br"
			}
			assert.Equal(t, wantAcceptEncoding, sawAcceptEncoding.Load())
			if tt.backend.gzipIfAsked {
				assert.Empty(t, rec.Header().Get("Content-Encoding"), "transport must decode the gzip it asked for")
			}
		})
	}
}

func TestTransparentProxyInvalidInitializeResponseCreatesNoSession(t *testing.T) {
	t.Parallel()

	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	tests := []struct {
		name      string
		body      string
		wantValid bool
	}{
		{name: "invalid response", body: `{"jsonrpc":"2.0","result":{}}`},
		// Control: the same exchange with a valid response does both, so the
		// negative assertions above are not vacuous.
		{name: "valid response", body: `{"jsonrpc":"2.0","id":1,"result":{}}`, wantValid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			backend, _ := serveUpstream(t, upstreamBackend{
				status: http.StatusOK, contentType: "application/json", body: []byte(tt.body),
			})
			p, handler := newValidatingProxy(t, backend.URL)
			rec := postThroughProxy(t, handler, initialize, nil)

			_, stored := p.sessionManager.Get(normalizeSessionID("upstream-session"))
			assert.Equal(t, tt.wantValid, stored, "session registration")
			assert.Equal(t, tt.wantValid, p.serverInitialized(), "server initialized")
			if tt.wantValid {
				assert.Equal(t, http.StatusOK, rec.Code)
				assert.Equal(t, "upstream-session", rec.Header().Get("Mcp-Session-Id"))
				return
			}
			assertInvalidUpstreamRejection(t, rec, int64(1))
		})
	}
}

// trackingBody records whether it was closed.
type trackingBody struct {
	r      io.Reader
	closed bool
}

func (b *trackingBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *trackingBody) Close() error               { b.closed = true; return nil }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestBufferAndValidateResponse(t *testing.T) {
	t.Parallel()

	const limit = int64(len(validResult))
	tests := []struct {
		name       string
		body       io.Reader
		encoding   string
		wantReason string
	}{
		{name: "valid body at the limit", body: strings.NewReader(validResult)},
		{name: "body over the limit", body: strings.NewReader(validResult + " "), wantReason: "body_too_large"},
		{name: "read error", body: failingReader{}, wantReason: "read_error"},
		{name: "content coding", body: strings.NewReader(validResult), encoding: "gzip", wantReason: "content_encoding"},
		{name: "structurally invalid", body: strings.NewReader(`{"jsonrpc":"2.0"}`), wantReason: "invalid_response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			upstream := &trackingBody{r: tt.body}
			resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: upstream, ContentLength: -1}
			if tt.encoding != "" {
				resp.Header.Set("Content-Encoding", tt.encoding)
			}

			reason := bufferAndValidateResponse(resp, limit)

			assert.Equal(t, tt.wantReason, reason)
			assert.True(t, upstream.closed, "upstream body must be closed")
			if tt.wantReason == "" {
				got, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				assert.Equal(t, validResult, string(got))
				assert.Equal(t, limit, resp.ContentLength)
				assert.Equal(t, strconv.Itoa(len(validResult)), resp.Header.Get("Content-Length"))
			}
		})
	}
}
