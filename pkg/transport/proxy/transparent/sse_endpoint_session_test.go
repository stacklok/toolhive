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

	"github.com/stacklok/toolhive/pkg/auth"
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

func TestSSEInitializePreservesOwnedEndpointSession(t *testing.T) {
	t.Parallel()
	for _, key := range sessionQueryKeys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			const sid = "owned-sse-session"
			const body = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`
			endpoint := "/custom-message-endpoint?" + key + "=" + sid
			var posts atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "event: endpoint\ndata: "+endpoint+"\n\n")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				posts.Add(1)
				data, err := io.ReadAll(r.Body)
				if err != nil || string(data) != body || r.URL.Query().Get(key) != sid {
					http.Error(w, "wrong initialize or session", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			t.Cleanup(backend.Close)
			authenticate := types.NamedMiddleware{Name: "test-auth", Function: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
						Claims: map[string]any{"iss": "issuer", "sub": r.Header.Get("Authorization")},
					}}
					next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
				})
			}}
			proxy := NewTransparentProxy("127.0.0.1", 0, backend.URL, nil, nil, nil, false, false, "sse", nil, nil, "", false, authenticate)
			require.NoError(t, proxy.Start(t.Context()))
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, proxy.Stop(ctx))
			})
			base := "http://" + proxy.listener.Addr().String()
			client := &http.Client{Timeout: 5 * time.Second}
			get, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/custom-stream", nil)
			require.NoError(t, err)
			get.Header.Set("Authorization", "owner")
			stream, err := client.Do(get)
			require.NoError(t, err)
			t.Cleanup(func() { _ = stream.Body.Close() })
			scanner := bufio.NewScanner(stream.Body)
			require.True(t, scanner.Scan())
			require.Equal(t, "event: endpoint", scanner.Text())
			require.True(t, scanner.Scan())
			require.Equal(t, "data: "+endpoint, scanner.Text())
			for _, contentType := range []string{"application/json", "text/plain", ""} {
				for _, principal := range []string{"foreign", "owner"} {
					before := posts.Load()
					req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+endpoint, strings.NewReader(body))
					require.NoError(t, err)
					req.Header.Set("Authorization", principal)
					if contentType != "" {
						req.Header.Set("Content-Type", contentType)
					}
					resp, err := client.Do(req)
					require.NoError(t, err)
					drainAndClose(t, resp)
					if principal == "owner" {
						require.Equal(t, http.StatusAccepted, resp.StatusCode)
						require.Equal(t, before+1, posts.Load())
					} else {
						require.Equal(t, http.StatusNotFound, resp.StatusCode)
						require.Equal(t, before, posts.Load())
					}
				}
			}
			// Initialization must also use routing metadata from the owned session,
			// rather than sending a new-session request to the default backend.
			var routedPosts atomic.Int32
			routed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				routedPosts.Add(1)
				if r.URL.Query().Get(key) != "mapped-sid" || r.Header.Get("Mcp-Session-Id") != "mapped-sid" {
					http.Error(w, "wrong mapped session", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusAccepted)
			}))
			t.Cleanup(routed.Close)
			binding, err := proxy.sessionManager.LookupOwner(t.Context(), normalizeSessionID(sid))
			require.NoError(t, err)
			mapped := session.NewProxySession(normalizeSessionID(sid))
			mapped.SetMetadata(session.MetadataKeyIdentityBinding, binding)
			mapped.SetMetadata(sessionMetadataBackendURL, routed.URL)
			mapped.SetMetadata(sessionMetadataBackendSID, "mapped-sid")
			require.NoError(t, proxy.sessionManager.UpsertSessionIfOwner(mapped, binding))
			for _, principal := range []string{"foreign", "owner"} {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, base+endpoint, strings.NewReader(body))
				require.NoError(t, err)
				req.Header.Set("Authorization", principal)
				resp, err := client.Do(req)
				require.NoError(t, err)
				drainAndClose(t, resp)
				if principal == "foreign" {
					require.Equal(t, http.StatusNotFound, resp.StatusCode)
					require.Zero(t, routedPosts.Load())
				} else {
					require.Equal(t, http.StatusAccepted, resp.StatusCode)
					require.EqualValues(t, 1, routedPosts.Load())
				}
				require.EqualValues(t, 3, posts.Load(), "initialize must not reach the default backend")
			}
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
