// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

func listEntry(server, method string) map[string]any {
	return map[string]any{"type": toolcalldetails.Type, "server": server, "method": method}
}

func listBody(id int, method string) string {
	return `{"jsonrpc":"2.0","id":` + cbJSON(id) + `,"method":"` + method + `"}`
}

// cbModernList is a Modern (2026-07-28) request for a list method.
func cbModernList(id int, method string) (string, http.Header) {
	body := `{"jsonrpc":"2.0","id":` + cbJSON(id) + `,"method":"` + method + `","params":{"_meta":` +
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	return body, http.Header{"Mcp-Protocol-Version": {"2026-07-28"}, "Mcp-Method": {method}}
}

// listedKeys returns the keys of the items in a list response: names for
// tools and prompts, URIs for resources and URI templates for templates.
func listedKeys(t *testing.T, method, body string) []string {
	t.Helper()
	var resp struct {
		Result map[string]json.RawMessage `json:"result"`
		Error  json.RawMessage            `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp), "body: %s", body)
	require.Empty(t, resp.Error, "body: %s", body)
	members := map[string][2]string{
		"tools/list":               {"tools", "name"},
		"prompts/list":             {"prompts", "name"},
		"resources/list":           {"resources", "uri"},
		"resources/templates/list": {"resourceTemplates", "uriTemplate"},
	}[method]
	member, key := members[0], members[1]
	var items []map[string]any
	require.NoError(t, json.Unmarshal(resp.Result[member], &items), "body: %s", body)
	keys := make([]string, 0, len(items))
	for _, item := range items {
		k, _ := item[key].(string)
		keys = append(keys, k)
	}
	return keys
}

// TestCallBinding_ListCall checks that a list-call token lists, on Legacy and
// Modern, only the items of its entry's server, for each list method, and
// that listing does not use the token up.
func TestCallBinding_ListCall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method string
		want   string
		other  string
	}{
		{"tools/list", cbTool, cbOtherTool},
		{"prompts/list", cbPrompt, cbOtherPrompt},
		{"resources/list", cbResource, cbOtherResource},
		{"resources/templates/list", cbTemplate, cbOtherTemplate},
	}
	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()
			s := newCallBindingTestServer(t, cbServerOptions{})

			// The unfiltered list, for an unbound token, holds both servers'
			// items.
			plain := s.tokens.add("plain", claimsFor("other", "plain", nil))
			all := s.send(t, http.MethodPost, plain, s.session(t, plain), listBody(2, tc.method), nil)
			require.Equal(t, http.StatusOK, all.status, "body: %s", all.body)
			assert.Contains(t, listedKeys(t, tc.method, all.body), tc.other)

			token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, tc.method)}))
			session := s.session(t, token)
			for id := 2; id <= 3; id++ {
				resp := s.send(t, http.MethodPost, token, session, listBody(id, tc.method), nil)
				require.Equal(t, http.StatusOK, resp.status, "a list call does not use the token up: %s", resp.body)
				assert.Equal(t, []string{tc.want}, listedKeys(t, tc.method, resp.body))
				assert.JSONEq(t, cbJSON(id), string(rpcID(t, resp.body)))
			}

			body, headers := cbModernList(4, tc.method)
			modern := s.send(t, http.MethodPost, token, "", body, headers)
			require.Equal(t, http.StatusOK, modern.status, "body: %s", modern.body)
			assert.Equal(t, []string{tc.want}, listedKeys(t, tc.method, modern.body))
			assert.Contains(t, modern.body, `"resultType"`, "the rest of the Modern result is kept")

			// Listing started no call, so initialize still passes.
			require.Equal(t, http.StatusOK, s.initialize(t, token).status)
		})
	}
}

func rpcID(t *testing.T, body string) json.RawMessage {
	t.Helper()
	var resp struct {
		ID json.RawMessage `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp), "body: %s", body)
	return resp.ID
}

// TestCallBinding_ListCallMismatches checks that a list-call token sends
// only its entry's list method: another list method or a tools/call is
// refused with method_mismatch, any other request with method_not_allowed,
// and none of them reaches the core.
func TestCallBinding_ListCallMismatches(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
	session := s.session(t, token)
	baseline := s.fc.listPromptsCalls.Load()

	for i, method := range []string{"prompts/list", "resources/list", "resources/templates/list"} {
		requireRefused(t, s.send(t, http.MethodPost, token, session, listBody(10+i, method), nil),
			callbinding.ReasonMethodMismatch, 10+i)
	}
	requireRefused(t, s.send(t, http.MethodPost, token, session, toolCallBody(20, cbTool, "{}"), nil),
		callbinding.ReasonMethodMismatch, 20)
	requireRefused(t, s.send(t, http.MethodPost, token, "", cbModernCall(21, "{}"), cbModernHeaders()),
		callbinding.ReasonMethodMismatch, 21)
	body, headers := cbModernList(22, "prompts/list")
	requireRefused(t, s.send(t, http.MethodPost, token, "", body, headers), callbinding.ReasonMethodMismatch, 22)

	for i, method := range []string{"resources/read", "prompts/get", "completion/complete"} {
		requireRefused(t, s.send(t, http.MethodPost, token, session,
			`{"jsonrpc":"2.0","id":`+cbJSON(30+i)+`,"method":"`+method+`","params":{}}`, nil),
			callbinding.ReasonMethodNotAllowed, 30+i)
	}
	requireRefused(t, s.send(t, http.MethodPost, token, session, cancelBody("2"), nil),
		callbinding.ReasonMethodNotAllowed, nil)
	requireRefused(t, s.send(t, http.MethodPost, token, session, `{"jsonrpc":"2.0","id":7,"result":{}}`, nil),
		callbinding.ReasonNoOpenCall, nil)

	// A refusal does not use the token up.
	again := s.send(t, http.MethodPost, token, session, listBody(35, "tools/list"), nil)
	require.Equal(t, http.StatusOK, again.status, "body: %s", again.body)
	assert.Equal(t, []string{cbTool}, listedKeys(t, "tools/list", again.body))

	assert.Zero(t, s.fc.callToolCalls.Load())
	assert.Equal(t, baseline, s.fc.listPromptsCalls.Load(), "a refused list call never reaches the core")
	assert.Zero(t, s.fc.readResourceCalls.Load())
	assert.Zero(t, s.fc.getPromptCalls.Load())

	// A tool-call token may list tools, unfiltered as before, but no other
	// list method.
	callToken := s.tokens.add("call", claimsFor(cbClient, "jti-2", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	callSession := s.session(t, callToken)
	tools := s.send(t, http.MethodPost, callToken, callSession, listBody(40, "tools/list"), nil)
	require.Equal(t, http.StatusOK, tools.status, "body: %s", tools.body)
	assert.ElementsMatch(t, []string{cbTool, cbOtherTool, cbComposite}, listedKeys(t, "tools/list", tools.body))
	requireRefused(t, s.send(t, http.MethodPost, callToken, callSession, listBody(41, "prompts/list"), nil),
		callbinding.ReasonMethodNotAllowed, 41)
}

// TestCallBinding_ListCallServerWithoutItems checks that a list-call token
// for a server the vMCP has no items from lists nothing.
func TestCallBinding_ListCallServerWithoutItems(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("unknown", claimsFor(cbClient, "jti-1", []any{listEntry("unknown", "tools/list")}))
	resp := s.send(t, http.MethodPost, token, s.session(t, token), listBody(2, "tools/list"), nil)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.Empty(t, listedKeys(t, "tools/list", resp.body))
}

// TestCallBinding_ListCallFollowsCursor walks a Modern tools/list larger than
// one page with one list-call token: pages hold only the bound server's
// tools, including an empty page that still carries a cursor, and the walk
// collects exactly that server's tools.
func TestCallBinding_ListCallFollowsCursor(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		fc.tools = nil
		for i := range modernPageSize + 5 {
			fc.tools = append(fc.tools, vmcp.Tool{Name: fmt.Sprintf("other_%05d", i), BackendID: "other"})
		}
		fc.tools = append(fc.tools,
			vmcp.Tool{Name: "zz_" + cbTool, BackendID: cbServer}, vmcp.Tool{Name: "zz_" + cbTool + "_2", BackendID: cbServer})
		return fc
	}})
	token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))

	var listed []string
	cursor := ""
	for page := 0; ; page++ {
		require.Less(t, page, 5, "the walk must end")
		params := `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
			`"io.modelcontextprotocol/clientCapabilities":{}}`
		if cursor != "" {
			params += `,"cursor":` + cbJSON(cursor)
		}
		resp := s.send(t, http.MethodPost, token, "",
			`{"jsonrpc":"2.0","id":`+cbJSON(page+2)+`,"method":"tools/list","params":`+params+`}}`,
			http.Header{"Mcp-Protocol-Version": {"2026-07-28"}, "Mcp-Method": {"tools/list"}})
		require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
		keys := listedKeys(t, "tools/list", resp.body)
		if page == 0 {
			assert.Empty(t, keys, "the first page holds only other servers' tools")
		}
		listed = append(listed, keys...)
		var result struct {
			Result struct {
				NextCursor string `json:"nextCursor"`
			} `json:"result"`
		}
		require.NoError(t, json.Unmarshal([]byte(resp.body), &result))
		if cursor = result.Result.NextCursor; cursor == "" {
			break
		}
	}
	assert.Equal(t, []string{"zz_" + cbTool, "zz_" + cbTool + "_2"}, listed)
}

// TestCallBinding_ListCallRefusalIsAudited checks that a list-call token's
// refusal is audited as a denial with its reason.
func TestCallBinding_ListCallRefusalIsAudited(t *testing.T) {
	t.Parallel()
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	s := newCallBindingTestServer(t, cbServerOptions{auditFile: auditPath})
	token := s.tokens.add("list-token-value", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
	requireRefused(t, s.send(t, http.MethodPost, token, s.session(t, token), listBody(2, "prompts/list"), nil),
		callbinding.ReasonMethodMismatch, 2)

	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(auditPath)
		if err != nil {
			return false
		}
		for _, line := range bytes.Split(raw, []byte("\n")) {
			var event map[string]any
			if json.Unmarshal(line, &event) == nil && event["outcome"] == audit.OutcomeDenied {
				encoded := cbJSON(event)
				return strings.Contains(encoded, `"denial_reason":"method_mismatch"`) &&
					!strings.Contains(encoded, "list-token-value")
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
}

// listErrCore fails ListTools until cleared.
type listErrCore struct {
	*fakeCore
	mu  sync.Mutex
	err error
}

func (l *listErrCore) ListTools(ctx context.Context, identity *auth.Identity) ([]vmcp.Tool, error) {
	l.mu.Lock()
	err := l.err
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return l.fakeCore.ListTools(ctx, identity)
}

// TestCallBinding_ListCallLookupFailure checks that a list call whose
// server items cannot be looked up gets 503 and is not dispatched.
func TestCallBinding_ListCallLookupFailure(t *testing.T) {
	t.Parallel()
	lc := &listErrCore{}
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		lc.fakeCore = fc
		return lc
	}})
	token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
	session := s.session(t, token)

	lc.mu.Lock()
	lc.err = errors.New("aggregation failed")
	lc.mu.Unlock()
	resp := s.send(t, http.MethodPost, token, session, listBody(2, "tools/list"), nil)
	require.Equal(t, http.StatusServiceUnavailable, resp.status, "body: %s", resp.body)
	assert.NotContains(t, resp.body, cbOtherTool)

	lc.mu.Lock()
	lc.err = nil
	lc.mu.Unlock()
	resp = s.send(t, http.MethodPost, token, session, listBody(3, "tools/list"), nil)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.Equal(t, []string{cbTool}, listedKeys(t, "tools/list", resp.body))
}

// TestCallBinding_ListCallOptimizerListsNothing checks that with the
// optimizer enabled no tool counts as the server's: a session's tools are
// then find_tool and call_tool, which have no backend.
func TestCallBinding_ListCallOptimizerListsNothing(t *testing.T) {
	t.Parallel()
	fc := &fakeCore{
		tools:   []vmcp.Tool{{Name: cbTool, BackendID: cbServer}},
		prompts: []vmcp.Prompt{{Name: cbPrompt, BackendID: cbServer}},
	}
	s := &Server{core: fc, optimizerFactory: stubOptimizerFactory}
	keys, err := s.callBindingListItems(context.Background(), nil, "tools/list", cbServer)
	require.NoError(t, err)
	assert.Empty(t, keys)
	keys, err = s.callBindingListItems(context.Background(), nil, "prompts/list", cbServer)
	require.NoError(t, err)
	assert.Equal(t, map[string]struct{}{cbPrompt: {}}, keys, "prompts are not the optimizer's")
	_, err = s.callBindingListItems(context.Background(), nil, "tools/call", cbServer)
	require.Error(t, err)
}

// TestCallBinding_ListCallClientGone checks that a list call whose client
// went away before the handler answered writes nothing.
func TestCallBinding_ListCallClientGone(t *testing.T) {
	t.Parallel()
	cb, err := newCallBinding(callbinding.Config{}, nil,
		func(context.Context, *auth.Identity, string, string) (map[string]struct{}, error) {
			return map[string]struct{}{}, nil
		})
	require.NoError(t, err)
	token, reason := callbinding.FromClaims(
		claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}), nil)
	require.Empty(t, reason)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listBody(2, "tools/list"))).WithContext(ctx)
	rec := httptest.NewRecorder()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { cancel() })
	cb.serveBound(rec, req, next, &auth.Identity{}, token,
		&mcpparser.ParsedMCPRequest{Method: "tools/list", ID: float64(2), IsRequest: true})
	assert.Empty(t, rec.Body.String())
	assert.False(t, rec.Flushed)
}

// TestCallBinding_ListCallResponseHandling drives serveBound with stand-in
// handlers, to check how the answer to a list call is passed on: a response
// that is not 200 passes unchanged, a JSON-RPC error passes unchanged, and a
// 200 that cannot be read is replaced by a 500 rather than passed unfiltered.
// Nothing reaches the client before the filter runs, even when the handler
// flushes.
func TestCallBinding_ListCallResponseHandling(t *testing.T) {
	t.Parallel()
	keep := map[string]struct{}{cbTool: {}}
	cb, err := newCallBinding(callbinding.Config{}, nil,
		func(context.Context, *auth.Identity, string, string) (map[string]struct{}, error) { return keep, nil })
	require.NoError(t, err)
	token, reason := callbinding.FromClaims(
		claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}), nil)
	require.Empty(t, reason)
	parsed := &mcpparser.ParsedMCPRequest{Method: "tools/list", ID: float64(2), IsRequest: true}

	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "filtered",
			status:     http.StatusOK,
			body:       `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"` + cbTool + `"},{"name":"` + cbOtherTool + `"}],"nextCursor":"c"}}`,
			wantStatus: http.StatusOK,
			wantBody:   `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"` + cbTool + `"}],"nextCursor":"c"}}`,
		},
		{
			name:       "plain error body passes unchanged",
			status:     http.StatusNotFound,
			body:       "session not found",
			wantStatus: http.StatusNotFound,
			wantBody:   "session not found",
		},
		{
			name:       "result on another status is filtered too",
			status:     http.StatusAccepted,
			body:       `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"` + cbTool + `"},{"name":"` + cbOtherTool + `"}]}}`,
			wantStatus: http.StatusAccepted,
			wantBody:   `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"` + cbTool + `"}]}}`,
		},
		{
			name:       "result without its items member fails closed",
			status:     http.StatusOK,
			body:       `{"jsonrpc":"2.0","id":2,"result":{"items":[{"name":"` + cbOtherTool + `"}]}}`,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "JSON-RPC error passes unchanged",
			status:     http.StatusOK,
			body:       `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"x"}}`,
			wantStatus: http.StatusOK,
			wantBody:   `{"jsonrpc":"2.0","id":2,"error":{"code":-32603,"message":"x"}}`,
		},
		{
			name:       "unreadable result fails closed",
			status:     http.StatusOK,
			body:       "data: " + `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"` + cbOtherTool + `"}]}}`,
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var flushedEarly bool
			rec := httptest.NewRecorder()
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", "1")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body[:len(tc.body)/2]))
				w.(http.Flusher).Flush()
				flushedEarly = rec.Body.Len() > 0 || rec.Code != http.StatusOK
				_, _ = w.Write([]byte(tc.body[len(tc.body)/2:]))
			})
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listBody(2, "tools/list")))
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-1"}}
			cb.serveBound(rec, req, next, identity, token, parsed)

			assert.False(t, flushedEarly, "nothing reaches the client before the filter runs")
			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Empty(t, rec.Header().Get("Content-Length"))
			switch {
			case tc.wantStatus == http.StatusNotFound:
				assert.Equal(t, tc.wantBody, rec.Body.String())
				return
			case tc.wantBody != "":
				assert.JSONEq(t, tc.wantBody, rec.Body.String())
				return
			}
			assert.NotContains(t, rec.Body.String(), cbOtherTool)
			var resp struct {
				ID    json.RawMessage `json:"id"`
				Error struct {
					Code int64 `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "body: %s", rec.Body.String())
			assert.Equal(t, mcpparser.CodeInternalError, resp.Error.Code)
			assert.JSONEq(t, "2", string(resp.ID))
		})
	}
}
