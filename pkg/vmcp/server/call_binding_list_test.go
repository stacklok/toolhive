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
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/script"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
	"github.com/stacklok/toolhive/pkg/vmcp/codemode"
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
// Modern, only the items of its entry's server, for each list method, once:
// the list call uses the token up, so a second list call and a new session
// are refused with token_reused.
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
			resp := s.send(t, http.MethodPost, token, session, listBody(2, tc.method), nil)
			require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
			assert.Equal(t, []string{tc.want}, listedKeys(t, tc.method, resp.body))
			assert.JSONEq(t, "2", string(rpcID(t, resp.body)))
			requireRefused(t, s.send(t, http.MethodPost, token, session, listBody(3, tc.method), nil),
				callbinding.ReasonTokenReused, 3)
			requireRefused(t, s.initialize(t, token), callbinding.ReasonTokenReused, 1)

			modernToken := s.tokens.add("modern", claimsFor(cbClient, "jti-2", []any{listEntry(cbServer, tc.method)}))
			body, headers := cbModernList(4, tc.method)
			modern := s.send(t, http.MethodPost, modernToken, "", body, headers)
			require.Equal(t, http.StatusOK, modern.status, "body: %s", modern.body)
			assert.Equal(t, []string{tc.want}, listedKeys(t, tc.method, modern.body))
			assert.Contains(t, modern.body, `"resultType"`, "the rest of the Modern result is kept")
			body, headers = cbModernList(5, tc.method)
			requireRefused(t, s.send(t, http.MethodPost, modernToken, "", body, headers), callbinding.ReasonTokenReused, 5)
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
		callbinding.ReasonNoOpenCall, nil)
	requireRefused(t, s.send(t, http.MethodPost, token, session, `{"jsonrpc":"2.0","id":7,"result":{}}`, nil),
		callbinding.ReasonNoOpenCall, nil)

	// No refusal used the token up: its one list call still runs.
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

// TestCallBinding_ListCallServerChecks checks the entry's server before
// anything is recorded: a server that is unknown, offers the caller nothing,
// or contributes nothing to the view (its discovery fails) is refused with the
// same 403 server_not_found, so a token holder cannot tell which backends
// exist; the token stays unused. A backend that offers only resource
// templates is in the view, and one in the view with no items for the token's
// method is answered with an empty list.
func TestCallBinding_ListCallServerChecks(t *testing.T) {
	t.Parallel()
	for path, wrap := range listCallPaths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			testListCallServerChecks(t, wrap)
		})
	}
}

func testListCallServerChecks(t *testing.T, wrap func(core.VMCP) core.VMCP) {
	t.Helper()
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		fc.emptyBackends = []string{"empty"}
		fc.unavailableBackends = []string{"down"}
		fc.resourceTemplates = append(fc.resourceTemplates,
			vmcp.ResourceTemplate{URITemplate: "file:///tmpl/{path}", Name: "only", BackendID: "tmpl"})
		return wrap(fc)
	}})

	var refusals []cbResponse
	for _, server := range []string{"unknown", "empty", "down"} {
		token := s.tokens.add(server, claimsFor(cbClient, "jti-"+server, []any{listEntry(server, "tools/list")}))
		for range 2 {
			resp := s.send(t, http.MethodPost, token, "", listBody(2, "tools/list"), nil)
			requireRefused(t, resp, callbinding.ReasonServerNotFound, 2)
			refusals = append(refusals, resp)
		}
	}
	for _, resp := range refusals[1:] {
		assert.Equal(t, refusals[0].body, resp.body, "every server not in the view gets the same answer")
	}

	tmpl := s.tokens.add("tmpl", claimsFor(cbClient, "jti-tmpl", []any{listEntry("tmpl", "resources/templates/list")}))
	resp := s.send(t, http.MethodPost, tmpl, s.session(t, tmpl), listBody(3, "resources/templates/list"), nil)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.Equal(t, []string{"file:///tmpl/{path}"}, listedKeys(t, "resources/templates/list", resp.body))

	prompts := s.tokens.add("tmpl-prompts", claimsFor(cbClient, "jti-tmpl-prompts", []any{listEntry("tmpl", "prompts/list")}))
	resp = s.send(t, http.MethodPost, prompts, s.session(t, prompts), listBody(4, "prompts/list"), nil)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.Empty(t, listedKeys(t, "prompts/list", resp.body))
}

// listCallPaths are the two ways a list call is answered: from the core's
// lookup (core.BackendCapabilitiesLookup), and, for a core that does not
// implement it, from its lists. "lists" hides the lookup behind a decorator
// that only embeds core.VMCP, which does not promote it.
var listCallPaths = map[string]func(core.VMCP) core.VMCP{
	"lookup": func(v core.VMCP) core.VMCP { return v },
	"lists":  func(v core.VMCP) core.VMCP { return struct{ core.VMCP }{v} },
}

// narrowedPromptsCore is a decorator that embeds core.VMCP and lists no
// prompts, as a decorator that narrows a list does. It does not implement
// core.BackendCapabilitiesLookup.
type narrowedPromptsCore struct{ core.VMCP }

func (narrowedPromptsCore) ListPrompts(context.Context, *auth.Identity) ([]vmcp.Prompt, error) {
	return []vmcp.Prompt{}, nil
}

// TestCallBinding_ListCallDecoratorLists checks that a list call on a
// decorator that embeds core.VMCP, and so does not offer the inner core's
// lookup, is answered from the decorator's own lists: the prompt it drops is
// not listed, though the inner core's lookup would list it, and its other
// lists are answered as usual.
func TestCallBinding_ListCallDecoratorLists(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		return narrowedPromptsCore{fc}
	}})
	prompts := s.tokens.add("prompts", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "prompts/list")}))
	body, headers := cbModernList(2, "prompts/list")
	resp := s.send(t, http.MethodPost, prompts, "", body, headers)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.Empty(t, listedKeys(t, "prompts/list", resp.body), "the decorator's list, not the inner core's lookup")

	tools := s.tokens.add("tools", claimsFor(cbClient, "jti-2", []any{listEntry(cbServer, "tools/list")}))
	body, headers = cbModernList(3, "tools/list")
	resp = s.send(t, http.MethodPost, tools, "", body, headers)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.Equal(t, []string{cbTool}, listedKeys(t, "tools/list", resp.body))
}

// TestCallBinding_ListCallCodeModeReservedName checks that with code mode on,
// a backend that advertises a tool named execute_tool_script gets 503 for a
// tools/list list call, as code mode refuses its tools/list, while a
// prompts/list list call on the same backend is answered with its prompts.
func TestCallBinding_ListCallCodeModeReservedName(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
		fc.tools = append(fc.tools, vmcp.Tool{Name: script.ExecuteToolScriptName, BackendID: cbServer})
		return codemode.NewDecorator(fc, nil)
	}})
	tools := s.tokens.add("tools", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
	body, headers := cbModernList(2, "tools/list")
	resp := s.send(t, http.MethodPost, tools, "", body, headers)
	require.Equal(t, http.StatusServiceUnavailable, resp.status, "body: %s", resp.body)
	assert.NotContains(t, resp.body, script.ExecuteToolScriptName)

	prompts := s.tokens.add("prompts", claimsFor(cbClient, "jti-2", []any{listEntry(cbServer, "prompts/list")}))
	body, headers = cbModernList(3, "prompts/list")
	resp = s.send(t, http.MethodPost, prompts, "", body, headers)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.Equal(t, []string{cbPrompt}, listedKeys(t, "prompts/list", resp.body))
}

// TestCallBinding_ListCallUnavailableList checks that a list call whose
// method's list failed transiently on its server gets 503 on Legacy and
// Modern without using the token up, and lists once the list is available:
// the empty list the failure was reported as is not the server's answer.
func TestCallBinding_ListCallUnavailableList(t *testing.T) {
	t.Parallel()
	var fc *fakeCore
	s := newCallBindingTestServer(t, cbServerOptions{wrap: func(c *fakeCore) core.VMCP {
		c.setUnavailableLists(cbServer, "prompts/list")
		fc = c
		return c
	}})
	token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "prompts/list")}))
	modernBody, modernHeaders := cbModernList(2, "prompts/list")
	for _, send := range []func() cbResponse{
		func() cbResponse {
			return s.send(t, http.MethodPost, token, s.session(t, token), listBody(2, "prompts/list"), nil)
		},
		func() cbResponse { return s.send(t, http.MethodPost, token, "", modernBody, modernHeaders) },
	} {
		resp := send()
		require.Equal(t, http.StatusServiceUnavailable, resp.status, "the token stays unused: %s", resp.body)
		assert.NotContains(t, resp.body, cbPrompt)
	}

	other := s.tokens.add("other", claimsFor(cbClient, "jti-2", []any{listEntry(cbServer, "tools/list")}))
	otherBody, otherHeaders := cbModernList(3, "tools/list")
	before := fc.listToolsCalls.Load()
	resp := s.send(t, http.MethodPost, other, "", otherBody, otherHeaders)
	require.Equal(t, http.StatusOK, resp.status, "the server's other lists are answered: %s", resp.body)
	assert.Equal(t, int32(1), fc.listToolsCalls.Load()-before,
		"the answer comes from the server's own lookup: only the usual handler lists every tool")

	fc.setUnavailableLists(cbServer)
	resp = s.send(t, http.MethodPost, token, "", modernBody, modernHeaders)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.Equal(t, []string{cbPrompt}, listedKeys(t, "prompts/list", resp.body))
}

// pagedTools is more than one page of tools, all from another server, with
// the bound server's tools sorted after them.
func pagedTools() []vmcp.Tool {
	var tools []vmcp.Tool
	for i := range modernPageSize + 5 {
		tools = append(tools, vmcp.Tool{Name: fmt.Sprintf("other_%05d", i), BackendID: "other"})
	}
	return append(tools,
		vmcp.Tool{Name: "zz_" + cbTool, BackendID: cbServer}, vmcp.Tool{Name: "zz_" + cbTool + "_2", BackendID: cbServer})
}

func modernListParams(cursor string) string {
	params := `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",` +
		`"io.modelcontextprotocol/clientCapabilities":{}}`
	if cursor != "" {
		params += `,"cursor":` + cbJSON(cursor)
	}
	return params + "}"
}

var modernListHeaders = http.Header{"Mcp-Protocol-Version": {"2026-07-28"}, "Mcp-Method": {"tools/list"}}

// TestCallBinding_ListCallAnswersWhole checks that a list-call token's list
// longer than one page comes back whole, in one response without
// nextCursor, on Legacy and Modern: the first page the handler cuts holds
// only another server's tools, so its cursor would name one of them. No other
// server's tool appears anywhere in the response. A list call that carries a
// cursor of its own is refused with invalid_params, without using the token
// up.
func TestCallBinding_ListCallAnswersWhole(t *testing.T) {
	t.Parallel()
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "Legacy", true: "Modern"}[modern], func(t *testing.T) {
			t.Parallel()
			s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
				fc.tools = pagedTools()
				return fc
			}})
			token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
			session := ""
			if !modern {
				session = s.session(t, token)
			}
			send := func(id int, cursor string) cbResponse {
				if modern {
					return s.send(t, http.MethodPost, token, "", `{"jsonrpc":"2.0","id":`+cbJSON(id)+
						`,"method":"tools/list","params":`+modernListParams(cursor)+`}`, modernListHeaders)
				}
				params := ""
				if cursor != "" {
					params = `,"params":{"cursor":` + cbJSON(cursor) + `}`
				}
				return s.send(t, http.MethodPost, token, session,
					`{"jsonrpc":"2.0","id":`+cbJSON(id)+`,"method":"tools/list"`+params+`}`, nil)
			}

			requireRefused(t, send(2, "other_00999"), callbinding.ReasonInvalidParams, 2)

			resp := send(3, "")
			require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
			assert.Equal(t, []string{"zz_" + cbTool, "zz_" + cbTool + "_2"}, listedKeys(t, "tools/list", resp.body))
			assert.NotContains(t, resp.body, "nextCursor")
			assert.NotContains(t, resp.body, "other_", "no other server's tool, in an item or a cursor")
			assert.JSONEq(t, "3", string(rpcID(t, resp.body)))

			requireRefused(t, send(4, ""), callbinding.ReasonTokenReused, 4)
		})
	}
}

// TestCallBinding_ListCallSharedKey checks that items are attributed by their
// own backend, not by their key: another backend's resource with the bound
// server's resource URI is never answered, on Legacy or Modern.
func TestCallBinding_ListCallSharedKey(t *testing.T) {
	t.Parallel()
	for _, modern := range []bool{false, true} {
		t.Run(map[bool]string{false: "Legacy", true: "Modern"}[modern], func(t *testing.T) {
			t.Parallel()
			s := newCallBindingTestServer(t, cbServerOptions{wrap: func(fc *fakeCore) core.VMCP {
				fc.resources = []vmcp.Resource{
					{URI: cbResource, Name: "other's", Description: "from the other backend", BackendID: "other"},
					{URI: cbResource, Name: "bound", Description: "from the bound backend", BackendID: cbServer},
				}
				return fc
			}})
			token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "resources/list")}))
			var resp cbResponse
			if modern {
				body, headers := cbModernList(2, "resources/list")
				resp = s.send(t, http.MethodPost, token, "", body, headers)
			} else {
				resp = s.send(t, http.MethodPost, token, s.session(t, token), listBody(2, "resources/list"), nil)
			}
			require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
			assert.Equal(t, []string{cbResource}, listedKeys(t, "resources/list", resp.body))
			assert.Contains(t, resp.body, "from the bound backend")
			assert.NotContains(t, resp.body, "from the other backend")
		})
	}
}

// TestCallBinding_SpentTokenGetsTokenReused checks that once a token has
// started its call, a tools/call or a list method it could otherwise send is
// refused with 401 token_reused, telling the client it needs a new token,
// rather than with a refusal about the request: for a list-call token and a
// tool-call token alike. A tool-call token still lists tools, and a method no
// token may send keeps method_not_allowed.
func TestCallBinding_SpentTokenGetsTokenReused(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})

	list := s.tokens.add("list", claimsFor(cbClient, "jti-list", []any{listEntry(cbServer, "tools/list")}))
	listSession := s.session(t, list)
	resp := s.send(t, http.MethodPost, list, listSession, listBody(2, "tools/list"), nil)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	requireRefused(t, s.send(t, http.MethodPost, list, listSession, toolCallBody(3, cbTool, "{}"), nil),
		callbinding.ReasonTokenReused, 3)
	requireRefused(t, s.send(t, http.MethodPost, list, listSession, listBody(4, "prompts/list"), nil),
		callbinding.ReasonTokenReused, 4)
	requireRefused(t, s.send(t, http.MethodPost, list, "", cbModernCall(5, "{}"), cbModernHeaders()),
		callbinding.ReasonTokenReused, 5)
	// A method no list-call token may send keeps its refusal about the request.
	requireRefused(t, s.send(t, http.MethodPost, list, listSession,
		`{"jsonrpc":"2.0","id":13,"method":"resources/read","params":{"uri":"x"}}`, nil),
		callbinding.ReasonMethodNotAllowed, 13)

	call := s.tokens.add("call", claimsFor(cbClient, "jti-call", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	callSession := s.session(t, call)
	requireCallRan(t, s.send(t, http.MethodPost, call, callSession, toolCallBody(6, cbTool, "{}"), nil))
	requireRefused(t, s.send(t, http.MethodPost, call, callSession, toolCallBody(7, cbOtherTool, `{"x":1}`), nil),
		callbinding.ReasonTokenReused, 7)
	requireRefused(t, s.send(t, http.MethodPost, call, callSession, listBody(8, "prompts/list"), nil),
		callbinding.ReasonTokenReused, 8)
	tools := s.send(t, http.MethodPost, call, callSession, listBody(9, "tools/list"), nil)
	require.Equal(t, http.StatusOK, tools.status, "a tool-call token still lists tools: %s", tools.body)
	requireRefused(t, s.send(t, http.MethodPost, call, callSession,
		`{"jsonrpc":"2.0","id":10,"method":"resources/read","params":{"uri":"x"}}`, nil),
		callbinding.ReasonMethodNotAllowed, 10)

	// Before its call, a tool-call token's mismatch is about the request.
	fresh := s.tokens.add("fresh", claimsFor(cbClient, "jti-fresh", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))
	freshSession := s.session(t, fresh)
	requireRefused(t, s.send(t, http.MethodPost, fresh, freshSession, toolCallBody(11, cbOtherTool, "{}"), nil),
		callbinding.ReasonToolMismatch, 11)
	requireRefused(t, s.send(t, http.MethodPost, fresh, freshSession, listBody(12, "prompts/list"), nil),
		callbinding.ReasonMethodNotAllowed, 12)
	assert.Equal(t, int32(1), s.fc.callToolCalls.Load())
}

// TestCallBinding_ListCallReleasedWithoutAnswer checks that a list call
// answered without a list, here the 404 for a session the server does not
// know, does not use the token up: nothing of the call reached the client.
func TestCallBinding_ListCallReleasedWithoutAnswer(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))

	gone := s.send(t, http.MethodPost, token, "no-such-session", listBody(2, "tools/list"), nil)
	require.Equal(t, http.StatusNotFound, gone.status, "body: %s", gone.body)

	resp := s.send(t, http.MethodPost, token, s.session(t, token), listBody(3, "tools/list"), nil)
	require.Equal(t, http.StatusOK, resp.status, "the token was released: %s", resp.body)
	assert.Equal(t, []string{cbTool}, listedKeys(t, "tools/list", resp.body))
}

// TestCallBinding_ListAndToolCallShareOneUse checks that the single-use record
// is per token, whatever its entry: a token that started a tool call cannot
// start a list call under the same issuer and jti, and the other way round.
func TestCallBinding_ListAndToolCallShareOneUse(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	callEntryFor := []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}
	listEntryFor := []any{listEntry(cbServer, "tools/list")}

	call := s.tokens.add("call-first", claimsFor(cbClient, "shared-1", callEntryFor))
	list := s.tokens.add("list-second", claimsFor(cbClient, "shared-1", listEntryFor))
	requireCallRan(t, s.send(t, http.MethodPost, call, s.session(t, call), toolCallBody(2, cbTool, "{}"), nil))
	requireRefused(t, s.send(t, http.MethodPost, list, "", listBody(3, "tools/list"), nil),
		callbinding.ReasonTokenReused, 3)

	list = s.tokens.add("list-first", claimsFor(cbClient, "shared-2", listEntryFor))
	call = s.tokens.add("call-second", claimsFor(cbClient, "shared-2", callEntryFor))
	listSession := s.session(t, list)
	resp := s.send(t, http.MethodPost, list, listSession, listBody(4, "tools/list"), nil)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	requireRefused(t, s.send(t, http.MethodPost, call, listSession, toolCallBody(5, cbTool, "{}"), nil),
		callbinding.ReasonTokenReused, 5)
	assert.Equal(t, int32(1), s.fc.callToolCalls.Load())
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

// listErrCore fails the list call's core lookup until cleared.
type listErrCore struct {
	*fakeCore
	mu  sync.Mutex
	err error
}

func (l *listErrCore) LookupBackendCapabilities(
	ctx context.Context, identity *auth.Identity, id string,
) (*core.AdmittedBackendCapabilities, error) {
	l.mu.Lock()
	err := l.err
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return l.fakeCore.LookupBackendCapabilities(ctx, identity, id)
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
	items, err := s.callBindingListItems(context.Background(), nil, "tools/list", cbServer)
	require.NoError(t, err)
	assert.Empty(t, items)
	items, err = s.callBindingListItems(context.Background(), nil, "prompts/list", cbServer)
	require.NoError(t, err)
	require.Len(t, items, 1, "prompts are not the optimizer's")
	assert.Contains(t, string(items[0]), cbPrompt)
	_, err = s.callBindingListItems(context.Background(), nil, "tools/call", cbServer)
	require.Error(t, err)
}

// standInItems is the answer the stand-in list lookup gives.
var standInItems = []json.RawMessage{json.RawMessage(`{"name":"` + cbTool + `"}`)}

// standInBinding is a callBinding whose list lookup answers standInItems.
func standInBinding(t *testing.T) *callBinding {
	t.Helper()
	cb, err := newCallBinding(callbinding.Config{}, nil,
		func(context.Context, *auth.Identity, string, string) ([]json.RawMessage, error) {
			return standInItems, nil
		})
	require.NoError(t, err)
	return cb
}

// standInToken is a list-call token for tools/list with its own jti.
func standInToken(t *testing.T, jti string) *callbinding.Token {
	t.Helper()
	token, reason := callbinding.FromClaims(claimsFor(cbClient, jti, []any{listEntry(cbServer, "tools/list")}), nil)
	require.Empty(t, reason)
	return token
}

// standInList is a Legacy tools/list request on session s1 and its parse.
func standInList(ctx context.Context) (*http.Request, *mcpparser.ParsedMCPRequest) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listBody(2, "tools/list"))).WithContext(ctx)
	req.Header.Set("Mcp-Session-Id", "s1")
	return req, &mcpparser.ParsedMCPRequest{Method: "tools/list", ID: int64(2), IsRequest: true}
}

// recorded reports whether the token's call is in cb's record.
func recorded(t *testing.T, cb *callBinding, token *callbinding.Token) bool {
	t.Helper()
	_, found, err := cb.record.Lookup(context.Background(), token.Key)
	require.NoError(t, err)
	return found
}

// TestCallBinding_ListCallClientGone checks that a list call whose client
// went away before the handler answered is released, and answered with the
// 503 that records it as an error in the audit log.
func TestCallBinding_ListCallClientGone(t *testing.T) {
	t.Parallel()
	cb := standInBinding(t)
	token := standInToken(t, "jti-1")
	ctx, cancel := context.WithCancel(context.Background())
	req, parsed := standInList(ctx)
	rec := httptest.NewRecorder()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { cancel() })
	cb.serveBound(rec, req, next, &auth.Identity{}, token, parsed)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), cbTool, "no list")
	assert.False(t, rec.Flushed)
	assert.False(t, recorded(t, cb, token), "the token is released")
}

// TestCallBinding_ListCallCancelledAfterAnswer checks that a list call whose
// client went away is released even when the handler then wrote its whole
// answer, as Modern dispatch does: the answer never reached the client.
func TestCallBinding_ListCallCancelledAfterAnswer(t *testing.T) {
	t.Parallel()
	cb := standInBinding(t)
	token := standInToken(t, "jti-1")
	ctx, cancel := context.WithCancel(context.Background())
	req, parsed := standInList(ctx)
	rec := httptest.NewRecorder()
	cb.serveBound(rec, req, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
	}), &auth.Identity{}, token, parsed)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), cbTool, "no list")
	assert.False(t, recorded(t, cb, token), "the token is released")
}

// await receives from ch, failing the test when nothing arrives in time.
func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		require.FailNow(t, "timed out waiting")
		var zero T
		return zero
	}
}

// TestCallBinding_ListCallOneDelivered checks that of two concurrent list
// calls with one token, both run the handler but only one answer is
// delivered: the call is recorded once its answer is ready, and the other is
// refused with token_reused.
func TestCallBinding_ListCallOneDelivered(t *testing.T) {
	t.Parallel()
	cb := standInBinding(t)
	token := standInToken(t, "jti-1")
	started, release := make(chan struct{}, 2), make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
	})
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			req, parsed := standInList(context.Background())
			rec := httptest.NewRecorder()
			cb.serveBound(rec, req, handler, &auth.Identity{}, token, parsed)
			done <- rec
		}()
	}
	await(t, started)
	await(t, started)
	close(release)

	codes := map[int]int{}
	for range 2 {
		rec := await(t, done)
		codes[rec.Code]++
		if rec.Code == http.StatusOK {
			assert.Contains(t, rec.Body.String(), cbTool)
		} else {
			assert.Contains(t, rec.Body.String(), "(token_reused)")
		}
	}
	assert.Equal(t, map[int]int{http.StatusOK: 1, http.StatusUnauthorized: 1}, codes)
	assert.True(t, recorded(t, cb, token))
}

// TestCallBinding_ListCallMessages checks that no message is admitted for a
// list call: while its handler runs, the call is not recorded, so a
// notifications/cancelled for its ID and a JSON-RPC response on its session
// both get no_open_call, and the call is then answered.
func TestCallBinding_ListCallMessages(t *testing.T) {
	t.Parallel()
	cb := standInBinding(t)
	token := standInToken(t, "jti-1")
	started, release := make(chan struct{}), make(chan struct{})
	list := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
	})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req, parsed := standInList(context.Background())
		rec := httptest.NewRecorder()
		cb.serveBound(rec, req, list, &auth.Identity{}, token, parsed)
		done <- rec
	}()
	await(t, started)

	response := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":9,"result":{}}`))
	response.Header.Set("Mcp-Session-Id", "s1")
	refused := httptest.NewRecorder()
	cb.serveBound(refused, response, http.NotFoundHandler(), &auth.Identity{}, token, nil)
	assert.Equal(t, http.StatusForbidden, refused.Code)
	assert.Contains(t, refused.Body.String(), "(no_open_call)")

	cancel := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(cancelBody("2")))
	cancel.Header.Set("Mcp-Session-Id", "s1")
	cancelled := httptest.NewRecorder()
	cb.serveBound(cancelled, cancel, http.NotFoundHandler(), &auth.Identity{}, token, &mcpparser.ParsedMCPRequest{
		Method: "notifications/cancelled", Params: json.RawMessage(`{"requestId":2}`),
	})
	assert.Equal(t, http.StatusForbidden, cancelled.Code)
	assert.Contains(t, cancelled.Body.String(), "(no_open_call)")

	close(release)
	first := await(t, done)
	require.Equal(t, http.StatusOK, first.Code, "body: %s", first.Body.String())
	assert.Contains(t, first.Body.String(), cbTool)
	assert.True(t, recorded(t, cb, token))
}

// TestCallBinding_ListCallNotification checks that a list request without an
// ID, which gets no answer, is refused with method_not_allowed and leaves the
// token unused.
func TestCallBinding_ListCallNotification(t *testing.T) {
	t.Parallel()
	s := newCallBindingTestServer(t, cbServerOptions{})
	token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
	session := s.session(t, token)
	requireRefused(t, s.send(t, http.MethodPost, token, session, `{"jsonrpc":"2.0","method":"tools/list"}`, nil),
		callbinding.ReasonMethodNotAllowed, nil)
	resp := s.send(t, http.MethodPost, token, session, listBody(2, "tools/list"), nil)
	require.Equal(t, http.StatusOK, resp.status, "the token stays unused: %s", resp.body)
}

// TestCallBinding_ListCallResponseHandling drives serveBound with stand-in
// handlers, to check how the handler's answer becomes the list call's: a
// result, in JSON or in an event stream, is answered as JSON with the list
// lookup's items in place of the handler's own and without its cursor,
// whatever its 2xx status, and only then is the call recorded; any other
// status, an empty body and a JSON-RPC error pass unchanged; anything else
// that is not a list result gets a 500. The handler's headers go out only with
// its own response, and nothing reaches the client before the answer is
// complete, even when the handler flushes.
func TestCallBinding_ListCallResponseHandling(t *testing.T) {
	t.Parallel()
	cb := standInBinding(t)
	result := `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"` + cbOtherTool + `"}],"nextCursor":"` +
		cbOtherTool + `"}}`
	answer := `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"` + cbTool + `"}]}}`
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantStatus  int
		// wantBody is the exact answer; for a 500 it is empty.
		wantBody string
		// answered is a list answer, in JSON, and records the call.
		answered bool
	}{
		{name: "JSON result", status: http.StatusOK, body: result, wantStatus: http.StatusOK, wantBody: answer, answered: true},
		{name: "result on another 2xx status", status: http.StatusAccepted, body: result,
			wantStatus: http.StatusAccepted, wantBody: answer, answered: true},
		{
			name: "event stream result", status: http.StatusOK, contentType: "text/event-stream",
			body:       "event: message\nid: 1\ndata: " + result + "\n\n",
			wantStatus: http.StatusOK, wantBody: answer, answered: true,
		},
		{
			name: "event stream with a notification before the result", status: http.StatusOK,
			contentType: "text/event-stream; charset=utf-8",
			body: "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\r\n\r\n" +
				"data: " + result + "\r\n\r\n",
			wantStatus: http.StatusOK, wantBody: answer, answered: true,
		},
		{name: "plain error body", status: http.StatusNotFound, contentType: "text/plain", body: "session not found",
			wantStatus: http.StatusNotFound, wantBody: "session not found"},
		{name: "JSON error body on another status", status: http.StatusBadGateway, body: `{"error":"bad gateway"}`,
			wantStatus: http.StatusBadGateway, wantBody: `{"error":"bad gateway"}`},
		{name: "JSON-RPC error", status: http.StatusOK, body: `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"x"}}`,
			wantStatus: http.StatusOK, wantBody: `{"jsonrpc":"2.0","id":2,"error":{"code":-32602,"message":"x"}}`},
		{name: "result without its items member", status: http.StatusOK,
			body:       `{"jsonrpc":"2.0","id":2,"result":{"items":[{"name":"` + cbOtherTool + `"}]}}`,
			wantStatus: http.StatusInternalServerError},
		{name: "result and error", status: http.StatusOK,
			body:       `{"jsonrpc":"2.0","id":2,"result":{"tools":[]},"error":{"code":1,"message":"x"}}`,
			wantStatus: http.StatusInternalServerError},
		{name: "event stream without a response", status: http.StatusOK, contentType: "text/event-stream",
			body: "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"}\n\n", wantStatus: http.StatusInternalServerError},
		{name: "event stream labelled as JSON", status: http.StatusOK, body: "data: " + result + "\n\n",
			wantStatus: http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			contentType := tc.contentType
			if contentType == "" {
				contentType = "application/json"
			}
			var flushedEarly bool
			rec := httptest.NewRecorder()
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("Content-Length", "1")
				w.Header().Set("X-From-Handler", "yes")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body[:len(tc.body)/2]))
				w.(http.Flusher).Flush()
				flushedEarly = rec.Body.Len() > 0 || rec.Code != http.StatusOK || len(rec.Header()) > 0
				_, _ = w.Write([]byte(tc.body[len(tc.body)/2:]))
			})
			// Each case is its own token: a list call uses its token up.
			token := standInToken(t, tc.name)
			req, parsed := standInList(context.Background())
			cb.serveBound(rec, req, next, &auth.Identity{}, token, parsed)

			assert.False(t, flushedEarly, "nothing reaches the client before the answer is complete")
			require.Equal(t, tc.wantStatus, rec.Code, "body: %s", rec.Body.String())
			assert.Empty(t, rec.Header().Get("Content-Length"))
			assert.Equal(t, tc.answered, recorded(t, cb, token))
			assert.NotContains(t, rec.Body.String(), cbOtherTool)
			if tc.wantStatus == http.StatusInternalServerError {
				assert.Empty(t, rec.Header().Get("X-From-Handler"), "a 500 carries none of the handler's headers")
				var resp struct {
					ID    json.RawMessage `json:"id"`
					Error struct {
						Code int64 `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "body: %s", rec.Body.String())
				assert.Equal(t, mcpparser.CodeInternalError, resp.Error.Code)
				assert.JSONEq(t, "2", string(resp.ID))
				return
			}
			assert.Equal(t, "yes", rec.Header().Get("X-From-Handler"), "the handler's own response keeps its headers")
			assert.Equal(t, tc.wantBody, rec.Body.String())
			if tc.answered {
				assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
			} else {
				assert.Equal(t, contentType, rec.Header().Get("Content-Type"))
			}
		})
	}
}

// TestCallBinding_ListCallServerHealth checks that a list call on a configured
// backend that the core's lists leave out for its health status, though it
// may still answer, gets 503 without using the token up, and that the same
// token lists once the backend is healthy again. A degraded backend stays in
// the lists and is answered with its items.
func TestCallBinding_ListCallServerHealth(t *testing.T) {
	t.Parallel()
	for path, wrap := range listCallPaths {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			testListCallServerHealth(t, wrap)
		})
	}
}

func testListCallServerHealth(t *testing.T, wrap func(core.VMCP) core.VMCP) {
	t.Helper()
	opts := cbServerOptions{wrap: func(fc *fakeCore) core.VMCP { return wrap(fc) }}
	for _, status := range []vmcp.BackendHealthStatus{
		vmcp.BackendUnhealthy, vmcp.BackendUnknown, vmcp.BackendUnauthenticated,
	} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			s := newCallBindingTestServer(t, opts)
			s.fc.setBackendStatus(cbServer, status)
			token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
			body, headers := cbModernList(2, "tools/list")
			for range 2 {
				resp := s.send(t, http.MethodPost, token, "", body, headers)
				require.Equal(t, http.StatusServiceUnavailable, resp.status, "the token stays unused: %s", resp.body)
				assert.NotContains(t, resp.body, cbTool)
			}

			s.fc.setBackendStatus(cbServer, vmcp.BackendHealthy)
			resp := s.send(t, http.MethodPost, token, "", body, headers)
			require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
			assert.Equal(t, []string{cbTool}, listedKeys(t, "tools/list", resp.body))
		})
	}

	t.Run(string(vmcp.BackendDegraded), func(t *testing.T) {
		t.Parallel()
		s := newCallBindingTestServer(t, opts)
		s.fc.setBackendStatus(cbServer, vmcp.BackendDegraded)
		token := s.tokens.add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
		resp := s.send(t, http.MethodPost, token, s.session(t, token), listBody(2, "tools/list"), nil)
		require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
		assert.Equal(t, []string{cbTool}, listedKeys(t, "tools/list", resp.body))
	})
}

// cancelledListCore holds the Modern dispatch's core.ListTools until the
// client has gone, then answers it in full, as a backend slower than the
// client's timeout does. The list lookup before the call is recorded does not
// call ListTools (core.BackendCapabilitiesLookup), so dispatch makes the first.
type cancelledListCore struct {
	*fakeCore
	hold    atomic.Bool
	calls   atomic.Int32
	waiting chan struct{}
}

func (c *cancelledListCore) ListTools(ctx context.Context, identity *auth.Identity) ([]vmcp.Tool, error) {
	if c.hold.Load() && c.calls.Add(1) == 1 {
		close(c.waiting)
		<-ctx.Done()
	}
	return c.fakeCore.ListTools(ctx, identity)
}

// TestCallBinding_ModernListCallCancelled checks that a Modern list call whose
// client cancels while dispatch runs gives the token back, though dispatch
// then writes its whole answer: a retry with the same token gets the list.
// The cancelled call is audited as an error, not a success.
func TestCallBinding_ModernListCallCancelled(t *testing.T) {
	t.Parallel()
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	cc := &cancelledListCore{waiting: make(chan struct{})}
	cc.hold.Store(true)
	s := newCallBindingTestServer(t, cbServerOptions{auditFile: auditPath, wrap: func(fc *fakeCore) core.VMCP {
		cc.fakeCore = fc
		return cc
	}})
	token := s.tokens.add("list-token-value", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
	body, headers := cbModernList(2, "tools/list")

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, vs := range headers {
		req.Header[k] = vs
	}
	failed := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		failed <- err
	}()
	select {
	case <-cc.waiting:
	case <-time.After(10 * time.Second):
		require.FailNow(t, "dispatch did not run")
	}
	cancel()
	require.Error(t, await(t, failed), "the client cancelled")
	cc.hold.Store(false)

	// The release runs as the cancelled request returns; until then the
	// call is still in flight and a retry is refused without using the
	// token.
	var retry cbResponse
	require.Eventually(t, func() bool {
		retry = s.send(t, http.MethodPost, token, "", body, headers)
		return retry.status != http.StatusUnauthorized
	}, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, http.StatusOK, retry.status, "body: %s", retry.body)
	assert.Equal(t, []string{cbTool}, listedKeys(t, "tools/list", retry.body))

	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(auditPath)
		if err != nil {
			return false
		}
		outcomes := map[string]int{}
		for _, line := range bytes.Split(raw, []byte("\n")) {
			var event map[string]any
			if json.Unmarshal(line, &event) == nil && strings.Contains(string(line), "tools/list") {
				outcome, _ := event["outcome"].(string)
				outcomes[outcome]++
			}
		}
		return outcomes[audit.OutcomeError] == 1 && outcomes[audit.OutcomeSuccess] == 1
	}, 5*time.Second, 20*time.Millisecond, "the cancelled call is an error, the retry a success")
}

// TestCallBinding_FirstToolCallReadsNoRecord checks that a matching first
// tools/call needs no record read before it starts: with reads failing, it
// still runs, and Start alone finds a reuse. A mismatch reads the record to
// tell a spent token from a wrong request, so with reads failing it gets 503.
func TestCallBinding_FirstToolCallReadsNoRecord(t *testing.T) {
	t.Parallel()
	record := &spyRecord{Record: callbinding.NewMemoryRecord()}
	record.set(false, true)
	s := newCallBindingTestServer(t, cbServerOptions{binding: callbinding.Config{Record: record}})
	session := s.session(t, s.tokens.add("plain", claimsFor("other", "plain", nil)))
	token := s.tokens.add("call", claimsFor(cbClient, "jti-1", []any{callEntry(cbServer, cbTool, argsHash(t, `{}`))}))

	requireCallRan(t, s.send(t, http.MethodPost, token, session, toolCallBody(2, cbTool, "{}"), nil))
	requireRefused(t, s.send(t, http.MethodPost, token, session, toolCallBody(3, cbTool, "{}"), nil),
		callbinding.ReasonTokenReused, 3)
	mismatch := s.send(t, http.MethodPost, token, session, toolCallBody(4, cbOtherTool, "{}"), nil)
	assert.Equal(t, http.StatusServiceUnavailable, mismatch.status, "body: %s", mismatch.body)
	assert.Equal(t, int32(1), s.fc.callToolCalls.Load())
}

// TestCallBinding_ListCallOnRehydratedSession checks a list call on a Legacy
// session that another replica created: this replica's SDK rehydrates the
// session and answers in text/event-stream, and the list call is still
// answered, in JSON, with only the bound server's items.
func TestCallBinding_ListCallOnRehydratedSession(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	replicaA := newCallBindingTestServer(t, cbServerOptions{redisAddr: mr.Addr()})
	replicaB := newCallBindingTestServer(t, cbServerOptions{redisAddr: mr.Addr()})
	add := func(name string, claims map[string]any) string {
		replicaA.tokens.add(name, claims)
		return replicaB.tokens.add(name, claims)
	}

	// The fixture's premise: a session rehydrated on B answers in an event stream.
	plain := add("plain", claimsFor("other", "plain", nil))
	unbound := replicaB.send(t, http.MethodPost, plain, replicaA.session(t, plain), listBody(2, "tools/list"), nil)
	require.Equal(t, http.StatusOK, unbound.status, "body: %s", unbound.body)
	require.True(t, strings.HasPrefix(unbound.header.Get("Content-Type"), "text/event-stream"),
		"a rehydrated session answers in an event stream: %q", unbound.header.Get("Content-Type"))

	token := add("list", claimsFor(cbClient, "jti-1", []any{listEntry(cbServer, "tools/list")}))
	resp := replicaB.send(t, http.MethodPost, token, replicaA.session(t, token), listBody(3, "tools/list"), nil)
	require.Equal(t, http.StatusOK, resp.status, "body: %s", resp.body)
	assert.Equal(t, "application/json", resp.header.Get("Content-Type"))
	assert.Equal(t, []string{cbTool}, listedKeys(t, "tools/list", resp.body))
	assert.NotContains(t, resp.body, cbOtherTool)
}
