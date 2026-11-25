// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestSessionOwnershipHTTP(t *testing.T) {
	t.Parallel()
	for _, transport := range []string{"sse", "streamable-http"} {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()
			const sid = "backend-opaque-session"
			var backendCalls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backendCalls.Add(1)
				if r.URL.Path == "/sse" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: endpoint\ndata: /messages?sessionId="+sid+"\n\n")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), `"initialize"`) {
					w.Header().Set("Mcp-Session-Id", sid)
				}
				w.WriteHeader(200)
			}))
			t.Cleanup(backend.Close)
			authenticate := types.NamedMiddleware{Name: "test-auth", Function: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					token := r.Header.Get("Authorization")
					if token == "" {
						http.Error(w, "Unauthorized", http.StatusUnauthorized)
						return
					}
					iss, sub := "issuer", "owner"
					if token == "foreign" {
						sub = "foreign"
					}
					if token == "other-issuer" {
						iss = "other"
					}
					identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": iss, "sub": sub}}, Token: token}
					r = r.WithContext(auth.WithIdentity(r.Context(), identity))
					// Outbound rewriting must never be used as the source of ownership.
					r.Header.Set("Authorization", "backend-credential")
					next.ServeHTTP(w, r)
				})
			}}
			proxy := NewTransparentProxy("127.0.0.1", 0, backend.URL, nil, nil, nil, false, false, transport, nil, nil, "", false, authenticate)
			require.NoError(t, proxy.Start(t.Context()))
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, proxy.Stop(ctx))
			})
			base := "http://" + proxy.listener.Addr().String()
			client := &http.Client{Timeout: 5 * time.Second}
			send := func(method, path, token, header, body string) *http.Response {
				t.Helper()
				req, err := http.NewRequestWithContext(t.Context(), method, base+path, strings.NewReader(body))
				require.NoError(t, err)
				req.Header.Set("Authorization", token)
				req.Header.Set("Content-Type", "application/json")
				if header != "" {
					req.Header.Set("Mcp-Session-Id", header)
				}
				resp, err := client.Do(req)
				require.NoError(t, err)
				return resp
			}
			path, header := "/mcp", sid
			if transport == "sse" {
				resp := send(http.MethodGet, "/sse", "owner", "", "")
				require.Equal(t, 200, resp.StatusCode)
				t.Cleanup(func() { _ = resp.Body.Close() })
				scanner := bufio.NewScanner(resp.Body)
				require.True(t, scanner.Scan())
				require.Equal(t, "event: endpoint", scanner.Text())
				require.True(t, scanner.Scan())
				require.Equal(t, "data: /messages?sessionId="+sid, scanner.Text())
				path, header = "/messages?sessionId="+sid, ""
			} else {
				resp := send(http.MethodPost, "/mcp", "owner", "", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
				require.Equal(t, 200, resp.StatusCode)
				require.Equal(t, sid, resp.Header.Get("Mcp-Session-Id"))
				require.NoError(t, resp.Body.Close())
			}
			binding, err := proxy.sessionManager.LookupOwner(t.Context(), normalizeSessionID(sid))
			require.NoError(t, err)
			require.Equal(t, "issuer\x00owner", binding)
			before := backendCalls.Load()
			for _, token := range []string{"foreign", "other-issuer", ""} {
				for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPost} {
					for _, body := range []string{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":1,"result":{}}`, modernToolsCallBody} {
						resp := send(method, path, token, header, body)
						data, err := io.ReadAll(resp.Body)
						require.NoError(t, err)
						require.NoError(t, resp.Body.Close())
						want := 404
						if token == "" {
							want = 401
						}
						require.Equal(t, want, resp.StatusCode)
						require.NotContains(t, string(data), sid)
						require.NotContains(t, string(data), "issuer")
						require.Equal(t, before, backendCalls.Load(), "foreign request must never route, send, delete or recover")
					}
				}
			}
			// A conflicting backend SSE endpoint must never reach the second principal.
			if transport == "sse" {
				conflicting := send(http.MethodGet, "/sse", "foreign", "", "")
				data, _ := io.ReadAll(conflicting.Body) // The proxy deliberately terminates the stream.
				require.NoError(t, conflicting.Body.Close())
				require.NotContains(t, string(data), sid)
			}
			// Reusing a conflicting ID in a new backend response must not publish it.
			resp := send(http.MethodPost, "/mcp", "foreign", sid, `{"jsonrpc":"2.0","id":2,"method":"initialize"}`)
			require.Equal(t, 404, resp.StatusCode)
			require.Empty(t, resp.Header.Get("Mcp-Session-Id"))
			require.NoError(t, resp.Body.Close())
			stillOwner, err := proxy.sessionManager.LookupOwner(t.Context(), normalizeSessionID(sid))
			require.NoError(t, err)
			require.Equal(t, binding, stillOwner)
			resp = send(http.MethodPost, path, "refreshed", header, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
			require.Equal(t, 200, resp.StatusCode)
			require.NoError(t, resp.Body.Close())
			owner := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": "issuer", "sub": "owner"}}, Token: "refreshed"}
			require.NoError(t, sessionbinding.Validate(stillOwner, owner))
		})
	}
}
