// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
)

// TestListResultMembersCoverListMethods pins the result members to the list
// methods a list-call entry may name, so a method added there cannot reach
// the filter without its members.
func TestListResultMembersCoverListMethods(t *testing.T) {
	t.Parallel()
	for _, method := range []string{
		"tools/list", "prompts/list", "resources/list", "resources/templates/list",
		"tools/call", "Tools/list", "completion/complete", "",
	} {
		_, filtered := listResultMembers[method]
		assert.Equal(t, toolcalldetails.IsListMethod(method), filtered, "%q", method)
	}
}

// filterListResult reads one page and encodes it alone: the response a list
// with that one page gets.
func filterListResult(method string, response []byte, keep map[string]struct{}) ([]byte, error) {
	page, isError, err := ReadListPage(method, response, keep)
	if err != nil || isError {
		if isError {
			return response, nil
		}
		return nil, err
	}
	return page.Encode(page.Items)
}

func TestReadListPage(t *testing.T) {
	t.Parallel()
	keep := map[string]struct{}{"github_read": {}, "file:///github/readme": {}, "file:///github/{path}": {}}

	tests := []struct {
		name     string
		method   string
		response string
		want     string
		refused  bool
	}{
		{
			name:   "tools keep only kept names and every member but nextCursor",
			method: "tools/list",
			response: `{"jsonrpc":"2.0","id":7,"result":{"tools":[` +
				`{"name":"github_read","description":"a <b> & c","inputSchema":{"type":"object"}},` +
				`{"name":"other_read","description":"other"}],` +
				`"nextCursor":"abc","_meta":{"k":"v"},"resultType":"complete"}}`,
			want: `{"jsonrpc":"2.0","id":7,"result":{"tools":[` +
				`{"name":"github_read","description":"a <b> & c","inputSchema":{"type":"object"}}],` +
				`"_meta":{"k":"v"},"resultType":"complete"}}`,
		},
		{
			name:     "prompts by name",
			method:   "prompts/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"prompts":[{"name":"github_read"},{"name":"x"}]}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"prompts":[{"name":"github_read"}]}}`,
		},
		{
			name:   "resources by URI, not by name",
			method: "resources/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"resources":[` +
				`{"uri":"file:///github/readme","name":"x"},{"uri":"file:///other","name":"github_read"}]}}`,
			want: `{"jsonrpc":"2.0","id":1,"result":{"resources":[{"uri":"file:///github/readme","name":"x"}]}}`,
		},
		{
			name:   "resource templates by URI template",
			method: "resources/templates/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"resourceTemplates":[` +
				`{"uriTemplate":"file:///github/{path}"},{"uriTemplate":"file:///other/{path}"}]}}`,
			want: `{"jsonrpc":"2.0","id":1,"result":{"resourceTemplates":[{"uriTemplate":"file:///github/{path}"}]}}`,
		},
		{
			name:     "nothing kept",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"other_read"}],"nextCursor":"other_read"}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
		},
		{
			// encoding/json would match "NAME" to a name field, so an item
			// whose key could be read two ways is refused.
			name:     "key in other letter case is refused",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"NAME":"github_read"}]}}`,
			refused:  true,
		},
		{
			name:     "item without a string key is dropped",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":1},{"description":"github_read"},{"name":null}]}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
		},
		{
			name:     "result without the method's items member",
			method:   "prompts/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"other_read"}]}}`,
			refused:  true,
		},
		{
			name:     "items member in other letter case",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[],"Tools":[{"name":"other_read"}]}}`,
			refused:  true,
		},
		{
			name:     "items member twice",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[],"tools":[{"name":"other_read"}]}}`,
			refused:  true,
		},
		{
			name:     "result member twice",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[]},"result":{"tools":[{"name":"other_read"}]}}`,
			refused:  true,
		},
		{
			name:     "item key twice",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"github_read","name":"other_read"}]}}`,
			refused:  true,
		},
		{
			name:     "cache hints marked private and stale",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"ttlMs":60000,"cacheScope":"public","tools":[]}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"ttlMs":0,"cacheScope":"private","tools":[]}}`,
		},
		{
			name:     "JSON-RPC error passes unchanged",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"x"}}`,
			want:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"x"}}`,
		},
		{
			name:     "null items hold none",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":null}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
		},
		{
			name:     "nextCursor not a string",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[],"nextCursor":1}}`,
			refused:  true,
		},
		{
			name:     "nextCursor twice",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[],"nextCursor":"a","nextCursor":"b"}}`,
			refused:  true,
		},
		{name: "not a list method", method: "tools/call", response: `{"jsonrpc":"2.0","id":1,"result":{}}`, refused: true},
		{name: "not JSON", method: "tools/list", response: `data: {"result":{}}`, refused: true},
		{name: "null", method: "tools/list", response: `null`, refused: true},
		{name: "neither result nor error", method: "tools/list", response: `{"jsonrpc":"2.0","id":1}`, refused: true},
		{name: "result not an object", method: "tools/list", response: `{"jsonrpc":"2.0","id":1,"result":[]}`, refused: true},
		{name: "null result", method: "tools/list", response: `{"jsonrpc":"2.0","id":1,"result":null}`, refused: true},
		{
			name:     "items not an array",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":{"name":"github_read"}}}`,
			refused:  true,
		},
		{
			name:     "item not an object",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":["github_read"]}}`,
			refused:  true,
		},
		{
			name:     "trailing data",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}{}`,
			refused:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := filterListResult(tc.method, []byte(tc.response), keep)
			if tc.refused {
				require.ErrorIs(t, err, ErrListResult)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}

// TestReadListPageKeepsBytes checks that the response keeps the bytes and
// member order the handler wrote, apart from the filtered items: <, > and &
// stay unescaped and numbers keep their spelling.
func TestReadListPageKeepsBytes(t *testing.T) {
	t.Parallel()
	got, err := filterListResult("tools/list", []byte(`{"jsonrpc":"2.0","id":12345678901234567890,`+
		`"result":{"nextCursor":"c","tools":[{"name":"a","description":"<b> & c"},{"name":"b"}],"_meta":{"n":1.50}}}`),
		map[string]struct{}{"a": {}})
	require.NoError(t, err)
	assert.Equal(t, `{"jsonrpc":"2.0","id":12345678901234567890,`+
		`"result":{"tools":[{"name":"a","description":"<b> & c"}],"_meta":{"n":1.50}}}`, string(got))
}

func TestReadListPageNamesTheCause(t *testing.T) {
	t.Parallel()
	_, _, err := ReadListPage("tools/list", []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), nil)
	require.ErrorIs(t, err, ErrListResult)
	assert.Contains(t, err.Error(), "no items member")
}

func TestReadListPageCursor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		result   string
		wantNext bool
		cursor   string
	}{
		{name: "no cursor", result: `{"tools":[]}`},
		{name: "null cursor", result: `{"tools":[],"nextCursor":null}`},
		{name: "cursor", result: `{"tools":[],"nextCursor":"abc"}`, wantNext: true, cursor: "abc"},
		// An empty string is a cursor, not the end of the list.
		{name: "empty cursor", result: `{"tools":[],"nextCursor":""}`, wantNext: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			page, isError, err := ReadListPage("tools/list", []byte(`{"jsonrpc":"2.0","id":1,"result":`+tc.result+`}`), nil)
			require.NoError(t, err)
			require.False(t, isError)
			assert.Equal(t, tc.wantNext, page.HasNext)
			assert.Equal(t, tc.cursor, page.NextCursor)
		})
	}
}

// TestListPageEncodeMergesPages checks that Encode writes the first page's
// response with the given items and no nextCursor.
func TestListPageEncodeMergesPages(t *testing.T) {
	t.Parallel()
	keep := map[string]struct{}{"a": {}, "b": {}}
	first, _, err := ReadListPage("tools/list",
		[]byte(`{"jsonrpc":"2.0","id":3,"result":{"tools":[{"name":"a"},{"name":"x"}],"nextCursor":"x"}}`), keep)
	require.NoError(t, err)
	second, _, err := ReadListPage("tools/list",
		[]byte(`{"jsonrpc":"2.0","id":3,"result":{"tools":[{"name":"y"},{"name":"b"}]}}`), keep)
	require.NoError(t, err)
	got, err := first.Encode(append(first.Items, second.Items...))
	require.NoError(t, err)
	assert.Equal(t, `{"jsonrpc":"2.0","id":3,"result":{"tools":[{"name":"a"},{"name":"b"}]}}`, string(got))
}

func TestWithCursor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      any
		params  string
		want    string
		refused bool
	}{
		{
			name: "no params", id: float64(2),
			want: `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"cursor":"c<1>"}}`,
		},
		{
			name: "null params", id: "s", params: `null`,
			want: `{"jsonrpc":"2.0","id":"s","method":"tools/list","params":{"cursor":"c<1>"}}`,
		},
		{
			name: "cursor replaced, other members kept", id: float64(2),
			params: `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"},"cursor":"old"}`,
			want: `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":` +
				`{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"},"cursor":"c<1>"}}`,
		},
		{name: "cursor in other letter case", id: float64(2), params: `{"Cursor":"old"}`, refused: true},
		{name: "params not an object", id: float64(2), params: `[]`, refused: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := WithCursor(tc.id, "tools/list", json.RawMessage(tc.params), "c<1>")
			if tc.refused {
				require.ErrorIs(t, err, ErrListResult)
				return
			}
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(got))
		})
	}
}
