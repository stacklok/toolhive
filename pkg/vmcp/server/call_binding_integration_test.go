// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
)

// cbRealPrincipal is the subject every request of these tests authenticates
// as, so sessions opened with one token are owned by the others too.
const cbRealPrincipal = "user-cb"

// cbRealServer is a vMCP built through server.New over the real echo backend,
// whose authentication middleware maps a bearer token to registered claims.
// A request without a token authenticates as the same principal with no
// authorization_details.
type cbRealServer struct {
	url    string
	calls  *atomic.Int32
	mu     sync.Mutex
	claims map[string]map[string]any
}

func newCallBindingRealServer(t *testing.T, toolDelay time.Duration, policies ...string) *cbRealServer {
	t.Helper()
	s := &cbRealServer{calls: &atomic.Int32{}, claims: map[string]map[string]any{}}
	s.serve(t, startRealMCPBackendWithToolDelay(t, toolDelay, func() { s.calls.Add(1) }), policies...)
	return s
}

// serve builds the vMCP over the backend at backendURL.
func (s *cbRealServer) serve(t *testing.T, backendURL string, policies ...string) {
	t.Helper()
	authMw := func(next http.Handler) http.Handler {
		withParser := mcpparser.ParsingMiddleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			s.mu.Lock()
			claims, ok := s.claims[token]
			s.mu.Unlock()
			if !ok {
				claims = map[string]any{"iss": "https://issuer.example.com", "sub": cbRealPrincipal}
			}
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: cbRealPrincipal, Claims: claims}}
			withParser.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
		})
	}
	// No policies leaves authorization unconfigured.
	ts := buildCedarAuthzServerWithConfig(t, backendURL, nil, nil, authMw, nil, policies...)
	s.url = ts.URL
}

// token registers a token bound to one call of the backend's echo tool with
// args, and returns it.
func (s *cbRealServer) token(t *testing.T, jti, args string) string {
	t.Helper()
	hash, err := callbinding.ArgumentsSHA256(json.RawMessage(args))
	require.NoError(t, err)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims[jti] = map[string]any{
		"iss": "https://issuer.example.com", "sub": cbRealPrincipal, "jti": jti,
		"exp": float64(time.Now().Add(time.Hour).Unix()),
		"authorization_details": []any{map[string]any{
			"type": toolcalldetails.Type, "server": "real-backend", "tool": "echo",
			"arguments_sha256": hash, "operation_id": "op-" + jti,
		}},
	}
	return jti
}

// post sends body as the holder of token on session and returns the status
// and body.
func (s *cbRealServer) post(t *testing.T, token, session, body string) (int, string) {
	t.Helper()
	status, raw, _ := s.send(t, token, session, body)
	return status, raw
}

// send is post that also returns the response's Mcp-Session-Id.
func (s *cbRealServer) send(t *testing.T, token, session, body string) (int, string, string) {
	t.Helper()
	return cbPostMCP(t, s.url, token, session, body, nil)
}

// cbPostMCP posts a JSON-RPC body to the MCP endpoint at baseURL as the
// holder of token (none when empty), on session when set, with extra
// headers, and returns the status, body and Mcp-Session-Id.
func cbPostMCP(t *testing.T, baseURL, token, session, body string, extra http.Header) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(raw), resp.Header.Get("Mcp-Session-Id")
}

// cbOpenSession opens a Legacy session at baseURL as the holder of token
// (none when empty) and returns its ID.
func cbOpenSession(t *testing.T, baseURL, token string) string {
	t.Helper()
	status, body, sessionID := cbPostMCP(t, baseURL, token, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+
		`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"cb","version":"1"}}}`, nil)
	require.Equal(t, http.StatusOK, status, "initialize: %s", body)
	require.NotEmpty(t, sessionID)
	status, body, _ = cbPostMCP(t, baseURL, token, sessionID, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	require.Equal(t, http.StatusAccepted, status, "notifications/initialized: %s", body)
	return sessionID
}

// session opens a Legacy session without a token, as the tests' principal.
func (s *cbRealServer) session(t *testing.T) string {
	t.Helper()
	return cbOpenSession(t, s.url, "")
}

func cbEchoCall(id int, args string) string {
	return cbToolCall(id, "echo", args)
}

// cbToolCall is a tools/call body for tool with args.
func cbToolCall(id int, tool, args string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`,
		id, tool, args)
}

// listToken registers a token bound to listing method on the real backend,
// and returns it.
func (s *cbRealServer) listToken(jti, method string) string {
	return s.listTokenFor(jti, "real-backend", method)
}

// listTokenFor registers a token bound to listing method on server, and
// returns it.
func (s *cbRealServer) listTokenFor(jti, server, method string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims[jti] = map[string]any{
		"iss": "https://issuer.example.com", "sub": cbRealPrincipal, "jti": jti,
		"exp": float64(time.Now().Add(time.Hour).Unix()),
		"authorization_details": []any{map[string]any{
			"type": toolcalldetails.Type, "server": server, "method": method,
		}},
	}
	return jti
}

// startTemplateOnlyBackend starts an MCP backend that offers one resource
// template and nothing else.
func startTemplateOnlyBackend(t *testing.T) string {
	t.Helper()
	srv := mcpserver.NewMCPServer("template-only", "1.0.0")
	srv.AddResourceTemplate(mcpmcp.ResourceTemplate{URITemplate: "file:///docs/{path}", Name: "docs"},
		func(context.Context, mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
			return nil, nil
		})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewStreamableHTTPServer(srv))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// TestCallBindingReal_ListCallServerInView checks the list call's server
// check against the real core. A backend that offers only resource templates
// is in the caller's view, so its templates are listed. A backend whose items
// Cedar hides from the caller gets the same 403 server_not_found as a server
// that does not exist, so a token holder cannot tell the two apart.
func TestCallBindingReal_ListCallServerInView(t *testing.T) {
	t.Parallel()

	templates := &cbRealServer{calls: &atomic.Int32{}, claims: map[string]map[string]any{}}
	templates.serve(t, startTemplateOnlyBackend(t))
	token := templates.listToken("templates", "resources/templates/list")
	status, body := templates.post(t, token, templates.session(t),
		`{"jsonrpc":"2.0","id":2,"method":"resources/templates/list"}`)
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Contains(t, body, "file:///docs/{path}")

	hidden := newCallBindingRealServer(t, 0,
		`permit(principal, action == Action::"call_tool", resource == Tool::"unrelated");`)
	session := hidden.session(t)
	list := `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`
	hiddenStatus, hiddenBody := hidden.post(t, hidden.listToken("hidden", "tools/list"), session, list)
	unknownStatus, unknownBody := hidden.post(t, hidden.listTokenFor("unknown", "no-such-backend", "tools/list"), session, list)
	assert.Equal(t, http.StatusForbidden, hiddenStatus, "body: %s", hiddenBody)
	assert.Contains(t, hiddenBody, "(server_not_found)")
	assert.Equal(t, unknownStatus, hiddenStatus)
	assert.Equal(t, unknownBody, hiddenBody, "a hidden backend is answered as an unknown one")
	assert.Zero(t, hidden.calls.Load())
}

// TestCallBindingReal_ListCallKeepsAdmission checks that a list-call token's
// list is still filtered by Cedar: a tool on the bound server that Cedar
// hides stays hidden, so the binding only ever narrows what admission lists.
func TestCallBindingReal_ListCallKeepsAdmission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		policies []string
		want     []string
	}{
		{name: "without authorization both tools are listed", want: []string{"echo", "ping"}},
		{name: "a tool Cedar hides stays hidden", policies: []string{admissionPermitEcho}, want: []string{"echo"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &cbRealServer{calls: &atomic.Int32{}, claims: map[string]map[string]any{}}
			s.serve(t, startEchoPingBackend(t), tc.policies...)
			session := s.session(t)
			waitForEchoTool(t, s.url, session)

			status, body := s.post(t, s.listToken("list", "tools/list"), session,
				`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
			require.Equal(t, http.StatusOK, status, "body: %s", body)
			var resp struct {
				Result struct {
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
				} `json:"result"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &resp))
			var names []string
			for _, tool := range resp.Result.Tools {
				names = append(names, tool.Name)
			}
			assert.ElementsMatch(t, tc.want, names)
		})
	}
}

// TestCallBindingReal_CedarHidesTool checks that a tool Cedar hides from the
// caller is refused as tool_not_found and never reaches the backend.
func TestCallBindingReal_CedarHidesTool(t *testing.T) {
	t.Parallel()
	s := newCallBindingRealServer(t, 0,
		`permit(principal, action == Action::"call_tool", resource == Tool::"unrelated");`)
	session := s.session(t)
	token := s.token(t, "hidden", `{"input":"x"}`)

	status, body := s.post(t, token, session, cbEchoCall(2, `{"input":"x"}`))
	assert.Equal(t, http.StatusForbidden, status, "body: %s", body)
	assert.Contains(t, body, "(tool_not_found)")
	assert.Zero(t, s.calls.Load())
}

// TestCallBindingReal_CedarDenialUsesToken checks that a call Cedar denies
// after call binding admitted it uses the token up.
func TestCallBindingReal_CedarDenialUsesToken(t *testing.T) {
	t.Parallel()
	s := newCallBindingRealServer(t, 0, admissionPermitEcho, admissionForbidBlocked)
	session := s.session(t)
	waitForEchoTool(t, s.url, session)
	token := s.token(t, "blocked", `{"input":"blocked"}`)

	status, body := s.post(t, token, session, cbEchoCall(2, `{"input":"blocked"}`))
	require.Equal(t, http.StatusForbidden, status, "body: %s", body)
	assert.Equal(t, "call denied by authorization policy", parseRPCError(t, []byte(body)).message)

	status, body = s.post(t, token, session, cbEchoCall(3, `{"input":"blocked"}`))
	assert.Equal(t, http.StatusUnauthorized, status, "body: %s", body)
	assert.Contains(t, body, "(token_reused)")
	assert.Zero(t, s.calls.Load())
}

// TestCallBindingReal_ParallelCallsRunOnce sends the same call-bound
// tools/call concurrently through the real handler: exactly one runs.
func TestCallBindingReal_ParallelCallsRunOnce(t *testing.T) {
	t.Parallel()
	s := newCallBindingRealServer(t, 200*time.Millisecond)
	session := s.session(t)
	waitForEchoTool(t, s.url, session)
	token := s.token(t, "parallel", `{"input":"once"}`)

	const n = 8
	statuses := make(chan int, n)
	var start sync.WaitGroup
	start.Add(1)
	for i := range n {
		go func() {
			start.Wait()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url+"/mcp",
				strings.NewReader(cbEchoCall(10+i, `{"input":"once"}`)))
			if err != nil {
				statuses <- 0
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Mcp-Session-Id", session)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				statuses <- 0
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	start.Done()

	counts := map[int]int{}
	for range n {
		select {
		case status := <-statuses:
			counts[status]++
		case <-time.After(10 * time.Second):
			require.FailNow(t, "a call did not return")
		}
	}
	assert.Equal(t, map[int]int{http.StatusOK: 1, http.StatusUnauthorized: n - 1}, counts)
	assert.Equal(t, int32(1), s.calls.Load(), "the backend ran the call once")
}

// TestCallBindingReal_DuplicateArguments sends a tools/call whose params
// carry arguments twice: it is refused before call binding hashes either, and
// never reaches the backend.
func TestCallBindingReal_DuplicateArguments(t *testing.T) {
	t.Parallel()
	s := newCallBindingRealServer(t, 0)
	session := s.session(t)
	waitForEchoTool(t, s.url, session)
	token := s.token(t, "dup", `{"input":"a"}`)

	status, body := s.post(t, token, session, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+
		`{"name":"echo","arguments":{"input":"a"},"arguments":{"input":"b"}}}`)
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", body)
	rpcErr := parseRPCError(t, []byte(body))
	require.True(t, rpcErr.present, "body: %s", body)
	assert.Equal(t, int(mcpparser.CodeInvalidRequest), rpcErr.code, "the parser refuses it: %s", body)
	assert.Zero(t, s.calls.Load())

	status, body = s.post(t, token, session, cbEchoCall(3, `{"input":"a"}`))
	assert.Equal(t, http.StatusOK, status, "the refusal does not use the token up: %s", body)
	assert.Equal(t, int32(1), s.calls.Load())
}
