// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/proxy/httpsse"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TestSSEToolsListIsFilteredByAuthz reproduces #6926 on the httpsse proxy: a tools/list
// answer that arrives on the GET /sse stream must be filtered by tool:read, the same way
// a streamable-HTTP answer is. Only "weather" is permitted, so "secret" must not appear.
func TestSSEToolsListIsFilteredByAuthz(t *testing.T) {
	t.Parallel()

	authenticate := types.NamedMiddleware{Name: "authenticate", Function: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Subject: "owner", Claims: map[string]any{"iss": "issuer", "sub": "owner"}},
				Token:         "owner",
			}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), caller)))
		})
	}}

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)
	authzMW := types.NamedMiddleware{Name: "authz", Function: func(next http.Handler) http.Handler {
		return mcp.ParsingMiddleware(authz.Middleware(authorizer, next, nil))
	}}

	port := freePort(t)
	proxy := httpsse.NewHTTPSSEProxy("127.0.0.1", port, false, nil, []types.NamedMiddleware{authenticate, authzMW})
	require.NoError(t, proxy.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, proxy.Stop(context.Background())) })
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 10 * time.Second}

	// Open the SSE stream and learn the session id from the endpoint event.
	streamReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/sse", nil)
	require.NoError(t, err)
	streamReq.Header.Set("Authorization", "owner")
	stream, err := client.Do(streamReq)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Body.Close() })

	events := readSSEEvents(stream.Body)
	endpoint := <-events
	require.Equal(t, "endpoint", endpoint.name)
	sessionID := endpoint.data[strings.Index(endpoint.data, "session_id=")+len("session_id="):]

	// Send tools/list. The backend's answer is delivered on the stream, not here.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	postReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		base+"/messages?session_id="+sessionID, strings.NewReader(body))
	require.NoError(t, err)
	postReq.Header.Set("Authorization", "owner")
	postReq.Header.Set("Content-Type", "application/json")
	post, err := client.Do(postReq)
	require.NoError(t, err)
	_ = post.Body.Close()
	require.Equal(t, http.StatusAccepted, post.StatusCode)

	// Play the backend: answer the forwarded request with two tools.
	forwarded := <-proxy.GetMessageChannel()
	result, err := json.Marshal(map[string]any{"tools": []map[string]string{{"name": "weather"}, {"name": "secret"}}})
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(t.Context(), &jsonrpc2.Response{
		ID:     forwarded.(*jsonrpc2.Request).ID,
		Result: result,
	}))

	// The filtered list must be the only message on the stream that mentions tools.
	var listed sseEvent
	select {
	case listed = <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("no tools/list answer arrived on the SSE stream")
	}
	assert.Contains(t, listed.data, "weather")
	assert.NotContains(t, listed.data, "secret", "a tool the caller cannot read must not be listed")
}

// TestSSEPromptsListIsFilteredByAuthz is the prompts counterpart of
// TestSSEToolsListIsFilteredByAuthz. Prompt visibility is decided by get_prompt
// (see pkg/authz/response_filter.go), so the policy permits only that action on
// Prompt::"public_summary". "internal_audit" must not appear in the stream.
func TestSSEPromptsListIsFilteredByAuthz(t *testing.T) {
	t.Parallel()

	authenticate := types.NamedMiddleware{Name: "authenticate", Function: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := &auth.Identity{
				PrincipalInfo: auth.PrincipalInfo{Subject: "owner", Claims: map[string]any{"iss": "issuer", "sub": "owner"}},
				Token:         "owner",
			}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), caller)))
		})
	}}

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"get_prompt", resource == Prompt::"public_summary");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)
	authzMW := types.NamedMiddleware{Name: "authz", Function: func(next http.Handler) http.Handler {
		return mcp.ParsingMiddleware(authz.Middleware(authorizer, next, nil))
	}}

	port := freePort(t)
	proxy := httpsse.NewHTTPSSEProxy("127.0.0.1", port, false, nil, []types.NamedMiddleware{authenticate, authzMW})
	require.NoError(t, proxy.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, proxy.Stop(context.Background())) })
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 10 * time.Second}

	streamReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/sse", nil)
	require.NoError(t, err)
	streamReq.Header.Set("Authorization", "owner")
	stream, err := client.Do(streamReq)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.Body.Close() })

	events := readSSEEvents(stream.Body)
	endpoint := <-events
	require.Equal(t, "endpoint", endpoint.name)
	sessionID := endpoint.data[strings.Index(endpoint.data, "session_id=")+len("session_id="):]

	body := `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`
	postReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		base+"/messages?session_id="+sessionID, strings.NewReader(body))
	require.NoError(t, err)
	postReq.Header.Set("Authorization", "owner")
	postReq.Header.Set("Content-Type", "application/json")
	post, err := client.Do(postReq)
	require.NoError(t, err)
	_ = post.Body.Close()
	require.Equal(t, http.StatusAccepted, post.StatusCode)

	// Play the backend: answer the forwarded prompts/list with two prompts.
	forwarded := <-proxy.GetMessageChannel()
	result, err := json.Marshal(map[string]any{"prompts": []map[string]string{{"name": "public_summary"}, {"name": "internal_audit"}}})
	require.NoError(t, err)
	require.NoError(t, proxy.ForwardResponseToClients(t.Context(), &jsonrpc2.Response{
		ID:     forwarded.(*jsonrpc2.Request).ID,
		Result: result,
	}))

	var listed sseEvent
	select {
	case listed = <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("no prompts/list answer arrived on the SSE stream")
	}
	assert.Contains(t, listed.data, "public_summary")
	assert.NotContains(t, listed.data, "internal_audit", "a prompt the caller cannot get must not be listed")
}

type sseEvent struct {
	name string
	data string
}

// readSSEEvents parses an SSE stream into events. It stops when the stream ends.
func readSSEEvents(body io.Reader) <-chan sseEvent {
	out := make(chan sseEvent, 16)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(body)
		var ev sseEvent
		for scanner.Scan() {
			line := strings.TrimRight(scanner.Text(), "\r")
			switch {
			case line == "":
				if ev.name != "" || ev.data != "" {
					out <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "event:"):
				ev.name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				ev.data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
	}()
	return out
}

// freePort asks the OS for an unused TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}
