// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// sseEndpointFlow drives the full legacy-SSE handshake through the proxy: GET the
// stream, read the endpoint event, then POST to the advertised URL. It returns the
// endpoint the client received (empty if the stream died first) and the POST status.
func sseEndpointFlow(t *testing.T, endpointData string, mw ...types.NamedMiddleware) (string, int, int32) {
	t.Helper()

	var posts atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "event: endpoint\ndata: "+endpointData+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
			return
		}
		posts.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(backend.Close)

	p := NewTransparentProxy("127.0.0.1", 0, backend.URL, nil, nil, nil, false, false, "sse", nil, nil, "", false, mw...)
	require.NoError(t, p.Start(t.Context()))
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	time.Sleep(150 * time.Millisecond)
	base := "http://" + p.listener.Addr().String()

	resp, err := http.Get(base + "/sse") //nolint:gosec // test-only URL
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	scanner := bufio.NewScanner(resp.Body)
	var endpoint string
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data:") {
			endpoint = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
	}
	if endpoint == "" {
		return "", 0, posts.Load()
	}

	r2, err := http.Post(base+endpoint, "application/json", //nolint:gosec // test-only URL
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo"}}`))
	require.NoError(t, err)
	_ = r2.Body.Close()
	return endpoint, r2.StatusCode, posts.Load()
}

// TestSSEEndpointEventSurvivesEverySessionSpelling pins the regression that took
// operator E2E down. The proxy closes the SSE stream when it cannot find a session
// id in the endpoint event, so a backend that spells the carrier differently never
// reaches its client at all: the client sees "missing endpoint: unexpected EOF"
// rather than a tightened check.
//
// The yardstick server used by the operator suite emits
// `data: /sse?sessionid=...` -- lowercase -- which is why recognising only
// "sessionId" broke every SSE workload.
func TestSSEEndpointEventSurvivesEverySessionSpelling(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"/messages?sessionId=abc",  // TypeScript SDK, mcp-go
		"/messages?sessionid=abc",  // mark3labs/mcp-go, e.g. the yardstick test server
		"/messages?session_id=abc", // ToolHive's own SSE proxy
	} {
		t.Run(endpoint, func(t *testing.T) {
			t.Parallel()
			got, status, posts := sseEndpointFlow(t, endpoint)

			require.Equal(t, endpoint, got, "endpoint event must reach the client unchanged")
			require.Equal(t, http.StatusAccepted, status, "the follow-up POST must reach the backend")
			require.Equal(t, int32(1), posts)
		})
	}
}

// TestSSEEndpointSessionIsBoundForEverySpelling pins that recognising a spelling
// also binds it: the point of accepting the carrier is that ownership is then
// enforceable on the POSTs that follow, not merely that the stream survives.
func TestSSEEndpointSessionIsBoundForEverySpelling(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ endpoint, sid string }{
		{"/messages?sessionId=abc", "abc"},
		{"/messages?sessionid=abc", "abc"},
		{"/messages?session_id=abc", "abc"},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			t.Parallel()
			proxy := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, true, false, "sse", nil, nil, "", false)
			t.Cleanup(func() { require.NoError(t, proxy.sessionManager.Stop()) })
			processor := NewSSEResponseProcessor(proxy, "", false)
			resp := &http.Response{
				Header:  http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:    io.NopCloser(strings.NewReader("event: endpoint\ndata: " + tc.endpoint + "\n\n")),
				Request: httptest.NewRequest(http.MethodGet, "/sse", nil),
			}

			require.NoError(t, processor.ProcessResponse(resp))
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err, "the stream must not be closed with an error")
			require.Contains(t, string(body), "data: "+tc.endpoint)

			stored, ok := proxy.sessionManager.Get(normalizeSessionID(tc.sid))
			require.True(t, ok, "the session must be registered so POSTs can be ownership-checked")
			owner, exists := stored.GetMetadataValue(session.MetadataKeyIdentityBinding)
			require.True(t, exists, "endpoint must be bound before publication")
			require.Equal(t, "unauthenticated", owner)
		})
	}
}
