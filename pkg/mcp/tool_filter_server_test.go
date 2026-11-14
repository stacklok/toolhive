// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests serve the tools/list filter on a real HTTP server, whose status,
// connection, and method handling the recorder does not model, and in front
// of a reverse proxy to a backend, so that the transport between the proxy
// and the backend takes part.

const (
	listRequest = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	callRequest = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"allowed_tool","arguments":{}}}`
	callResult  = `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"done"}]}}`
)

// serveListFilterOverHTTP serves backend through a tools/list filter, allowing
// only allowed_tool, on a real HTTP server, and returns the server together
// with the log net/http writes about the responses it serves.
func serveListFilterOverHTTP(
	t *testing.T, backend http.Handler, opts ...ToolMiddlewareOption,
) (*httptest.Server, *syncBuffer) {
	t.Helper()

	middleware, err := NewListToolsMappingMiddleware(append([]ToolMiddlewareOption{WithToolsFilter("allowed_tool")}, opts...)...)
	require.NoError(t, err)

	errorLog := &syncBuffer{}
	server := httptest.NewUnstartedServer(middleware(backend))
	server.Config.ErrorLog = log.New(errorLog, "", 0)
	server.Start()
	t.Cleanup(server.Close)
	return server, errorLog
}

// identityClient returns a client that neither asks for nor decodes a content
// coding, so that a Content-Encoding the server sends reaches the test.
func identityClient(t *testing.T) *http.Client {
	t.Helper()

	transport := &http.Transport{DisableCompression: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

// syncBuffer is a buffer that the goroutine serving a connection may write to
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestListToolsFilterForwardsACodedResponseToHEAD checks that the answer to a
// HEAD request that declares a content coding, which carries no body, is
// forwarded with its status and Content-Encoding.
func TestListToolsFilterForwardsACodedResponseToHEAD(t *testing.T) {
	t.Parallel()

	server, errorLog := serveListFilterOverHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
	}))

	request, err := http.NewRequest(http.MethodHead, server.URL+"/mcp", nil)
	require.NoError(t, err)
	response, err := identityClient(t).Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	server.Close()

	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, "gzip", response.Header.Get("Content-Encoding"))
	assert.Empty(t, body)
	assert.Empty(t, errorLog.String())
}

// TestListToolsFilterReadsAFlushBeforeTheStatus checks that a flush before the
// wrapped handler has written a status writes the status first, as net/http
// does, so that the response is read and held like any other: Content-Length
// is dropped from a body the filter rewrites, the status and header of an
// event stream reach the client at that flush, a body withheld after the
// flush is answered with an error, and a coded body is withheld without its
// Content-Encoding. net/http logs no superfluous status.
func TestListToolsFilterReadsAFlushBeforeTheStatus(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`
	unfilterable := listPrefix + `{"tools":[` + allowedTool + `,1]}}`
	gzippedList := gzipBytes(t, list)

	tests := []struct {
		name string
		// handle writes the response, flushing before any status;
		// headersRead is closed once the client has received the status
		// and header.
		handle          func(w http.ResponseWriter, flush func(), headersRead <-chan struct{})
		status          int
		contentType     string
		contentEncoding string
		body            string
	}{
		{
			name: "Content-Length then a list",
			handle: func(w http.ResponseWriter, flush func(), _ <-chan struct{}) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", strconv.Itoa(len(list)))
				flush()
				_, _ = w.Write([]byte(list))
			},
			status:      http.StatusOK,
			contentType: "application/json",
			body:        filtered + "\n",
		},
		{
			name: "event stream header then an event",
			handle: func(w http.ResponseWriter, flush func(), headersRead <-chan struct{}) {
				w.Header().Set("Content-Type", "text/event-stream")
				flush()
				<-headersRead
				_, _ = w.Write([]byte(sseEvent(list)))
				flush()
			},
			status:      http.StatusOK,
			contentType: "text/event-stream",
			body:        sseEvent(filtered),
		},
		{
			name: "a list the filter withholds after the flush",
			handle: func(w http.ResponseWriter, flush func(), _ <-chan struct{}) {
				flush()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(unfilterable))
			},
			status:      http.StatusInternalServerError,
			contentType: "application/json",
			body:        withheldError,
		},
		{
			name: "a coded list after the flush",
			handle: func(w http.ResponseWriter, flush func(), _ <-chan struct{}) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Encoding", "gzip")
				flush()
				_, _ = w.Write(gzippedList)
			},
			status:      http.StatusInternalServerError,
			contentType: "application/json",
			body:        withheldError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			headersRead := make(chan struct{})
			server, errorLog := serveListFilterOverHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				flusher, ok := w.(http.Flusher)
				require.True(t, ok)
				tt.handle(w, flusher.Flush, headersRead)
			}))

			response, err := identityClient(t).Post(server.URL+"/mcp", "application/json",
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			require.NoError(t, err)
			close(headersRead)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			server.Close()

			assert.Equal(t, tt.status, response.StatusCode)
			assert.Equal(t, tt.contentType, response.Header.Get("Content-Type"))
			assert.Equal(t, tt.contentEncoding, response.Header.Get("Content-Encoding"))
			assert.Equal(t, tt.body, string(body))
			assert.Empty(t, errorLog.String())
		})
	}
}

// TestListToolsFilterKeepsAProxiedStreamOpenPastAnOversizedEvent checks that
// a client on an event stream the backend holds open, forwarded by a reverse
// proxy that flushes every write, receives the events after an event that
// would make the filter hold more than it may while the stream is still open,
// and nothing in that event's place: its start names no request, and neither
// does the GET request that opened the stream.
func TestListToolsFilterKeepsAProxiedStreamOpenPastAnOversizedEvent(t *testing.T) {
	t.Parallel()

	const endpointEvent = "event: endpoint\ndata: /messages\n\n"
	oversizedStart := "data: " + strings.Repeat("x", testEventBound)
	replyTwo := sseEvent(`{"jsonrpc":"2.0","id":2,"result":{"content":[]}}`)
	// The backend waits for endpointRead, closed once the client has read the
	// endpoint event, before it ends the oversized event and writes reply two,
	// and for release before it ends the stream.
	endpointRead, release := make(chan struct{}), make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		write := func(chunk string) {
			for off := 0; off < len(chunk); off += 32 << 10 {
				_, writeErr := w.Write([]byte(chunk[off:min(off+32<<10, len(chunk))]))
				require.NoError(t, writeErr)
				flusher.Flush()
			}
		}
		write(endpointEvent)
		write(oversizedStart)
		select {
		case <-endpointRead:
		case <-r.Context().Done():
			return
		}
		write("\n\n" + replyTwo)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(backend.Close)
	endpointReadOnce := sync.OnceFunc(func() { close(endpointRead) })
	releaseStream := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseStream)
	t.Cleanup(endpointReadOnce)

	target, err := url.Parse(backend.URL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	server, errorLog := serveListFilterOverHTTP(t, proxy, withHeldBounds(testEventBound, testBodyBound))

	response, err := identityClient(t).Get(server.URL + "/sse")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	reader := bufio.NewReader(response.Body)
	// readEvent returns the next event of the stream, up to and including the
	// blank line that ends it, while the backend holds the stream open.
	readEvent := func() string {
		var event strings.Builder
		for {
			line, readErr := reader.ReadString('\n')
			require.NoError(t, readErr)
			event.WriteString(line)
			if line == "\n" {
				return event.String()
			}
		}
	}
	assert.Equal(t, endpointEvent, readEvent())
	endpointReadOnce()
	assert.Equal(t, replyTwo, readEvent())

	releaseStream()
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Empty(t, rest)
	server.Close()
	assert.Empty(t, errorLog.String())
}

// serveListFilterOverProxy serves backend through a reverse proxy wrapped in a
// tools/list filter allowing only allowed_tool, on a real HTTP server, and
// returns the server together with a channel that receives, once per request,
// the Accept-Encoding values the request holds after the filter ran, as the
// middleware around the filter sees them.
func serveListFilterOverProxy(t *testing.T, backend *httptest.Server) (*httptest.Server, <-chan []string) {
	t.Helper()

	middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"))
	require.NoError(t, err)

	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	proxy.FlushInterval = -1

	callerHeader := make(chan []string, 1)
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		middleware(proxy).ServeHTTP(w, r)
		callerHeader <- r.Header.Values("Accept-Encoding")
	})
	server := httptest.NewServer(outer)
	t.Cleanup(server.Close)
	return server, callerHeader
}

// postThroughFilter sends a JSON-RPC request to server with the client's
// Accept-Encoding, none when it is empty, and returns the response and its
// body.
func postThroughFilter(t *testing.T, server *httptest.Server, request, acceptEncoding string) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/mcp", strings.NewReader(request))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	resp, err := identityClient(t).Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp, string(body)
}

// receiveWithin returns the values a channel delivers within timeout, or
// fails the test.
func receiveWithin(t *testing.T, ch <-chan []string, timeout time.Duration) []string {
	t.Helper()

	select {
	case values := <-ch:
		return values
	case <-time.After(timeout):
		t.Fatal("timeout waiting for a handler to report a header")
		return nil
	}
}

// TestListToolsFilterLetsTheTransportDecodeGzip covers a backend that gzips
// every response: the filter forwards the request without the client's
// Accept-Encoding, the transport asks the backend for gzip on its own and
// decodes the body, and the filter reads it in the clear, whatever the client
// asked for. The request the middleware around the filter holds keeps the
// client's header.
func TestListToolsFilterLetsTheTransportDecodeGzip(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`

	tests := []struct {
		name           string
		request        string
		acceptEncoding string
		backendBody    string
		expected       string
	}{
		{
			name:           "tools/list asking for gzip and deflate",
			request:        listRequest,
			acceptEncoding: "gzip, deflate",
			backendBody:    list,
			expected:       filtered,
		},
		{
			name:           "tools/call asking for gzip and deflate",
			request:        callRequest,
			acceptEncoding: "gzip, deflate",
			backendBody:    callResult,
			expected:       callResult,
		},
		{
			name:        "tools/list asking for nothing",
			request:     listRequest,
			backendBody: list,
			expected:    filtered,
		},
		{
			name:           "tools/call asking for identity",
			request:        callRequest,
			acceptEncoding: "identity",
			backendBody:    callResult,
			expected:       callResult,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gzipped := gzipBytes(t, tt.backendBody)
			backendSaw := make(chan []string, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backendSaw <- r.Header.Values("Accept-Encoding")
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Encoding", "gzip")
				w.WriteHeader(http.StatusOK)
				if _, err := w.Write(gzipped); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(backend.Close)
			server, callerHeader := serveListFilterOverProxy(t, backend)

			resp, body := postThroughFilter(t, server, tt.request, tt.acceptEncoding)

			assert.Equal(t, http.StatusOK, resp.StatusCode)
			_, coded := resp.Header["Content-Encoding"]
			assert.False(t, coded, "the client must receive the body the transport decoded")
			assert.JSONEq(t, tt.expected, body)
			assert.NotContains(t, body, "hidden_tool")
			assert.Equal(t, []string{"gzip"}, receiveWithin(t, backendSaw, 5*time.Second),
				"the backend must see the Accept-Encoding the transport adds")
			var clients []string
			if tt.acceptEncoding != "" {
				clients = []string{tt.acceptEncoding}
			}
			assert.Equal(t, clients, receiveWithin(t, callerHeader, 5*time.Second),
				"the request the middleware around the filter holds must keep the client's Accept-Encoding")
		})
	}
}

// TestListToolsFilterWithholdsACodingTheTransportCannotDecode covers a backend
// that answers with a content coding the transport does not decode: the body
// reaches the filter coded, so it is withheld whenever the filter would have
// had to read it, and the client receives the error that answers its request
// under HTTP 500, without the backend's Content-Encoding or any of its bytes.
func TestListToolsFilterWithholdsACodingTheTransportCannotDecode(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	const callError = `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"internal error"}}`

	tests := []struct {
		name        string
		request     string
		coding      string
		backendBody string
		expected    string
	}{
		{name: "br on a tools/list", request: listRequest, coding: "br", backendBody: list, expected: withheldError},
		{name: "zstd on a tools/list", request: listRequest, coding: "zstd", backendBody: list, expected: withheldError},
		{name: "br on a tools/call", request: callRequest, coding: "br", backendBody: callResult, expected: callError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Encoding", tt.coding)
				w.WriteHeader(http.StatusOK)
				if _, err := w.Write([]byte(tt.backendBody)); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(backend.Close)
			server, _ := serveListFilterOverProxy(t, backend)

			resp, body := postThroughFilter(t, server, tt.request, "gzip, br, zstd")

			assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
			assert.Empty(t, resp.Header.Values("Content-Encoding"), "the backend's content coding must not reach the client")
			assert.JSONEq(t, tt.expected, body)
			assert.NotContains(t, body, tt.backendBody)
			assert.NotContains(t, body, "hidden_tool")
		})
	}
}
