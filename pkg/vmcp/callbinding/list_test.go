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

// TestIsListMethod checks that the list methods are exactly the methods a
// list-call entry may name.
func TestIsListMethod(t *testing.T) {
	t.Parallel()
	for _, method := range []string{
		"tools/list", "prompts/list", "resources/list", "resources/templates/list",
		"tools/call", "Tools/list", "tools/list ", "resources/templates/list/", "completion/complete", "",
	} {
		raw, err := json.Marshal(map[string]string{"type": toolcalldetails.Type, "server": "s", "method": method})
		require.NoError(t, err)
		_, err = toolcalldetails.ParseEntry(raw)
		assert.Equal(t, err == nil, IsListMethod(method), "%q", method)
	}
}

func TestFilterListResult(t *testing.T) {
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
			name:   "tools keep only kept names and every other member",
			method: "tools/list",
			response: `{"jsonrpc":"2.0","id":7,"result":{"tools":[` +
				`{"name":"github_read","description":"a <b> & c","inputSchema":{"type":"object"}},` +
				`{"name":"other_read","description":"other"}],` +
				`"nextCursor":"abc","_meta":{"k":"v"},"resultType":"complete"}}`,
			want: `{"jsonrpc":"2.0","id":7,"result":{"tools":[` +
				`{"name":"github_read","description":"a <b> & c","inputSchema":{"type":"object"}}],` +
				`"nextCursor":"abc","_meta":{"k":"v"},"resultType":"complete"}}`,
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
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"other_read"}],"nextCursor":"n"}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"tools":[],"nextCursor":"n"}}`,
		},
		{
			// encoding/json would match "NAME" to a name field; the key is
			// read by its exact name.
			name:     "key in other letter case is dropped",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"NAME":"github_read"},{"Name":"github_read"}]}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
		},
		{
			name:     "item without a string key is dropped",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":1},{"description":"github_read"},{"name":null}]}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
		},
		{
			name:     "method other than the result's member lists nothing",
			method:   "prompts/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"other_read"}]}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"other_read"}]}}`,
		},
		{
			name:     "JSON-RPC error passes unchanged",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"x"}}`,
			want:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"x"}}`,
		},
		{
			name:     "null items pass unchanged",
			method:   "tools/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"tools":null}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"tools":null}}`,
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
			got, err := FilterListResult(tc.method, []byte(tc.response), keep)
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

// TestFilterListResultKeepsCharacters checks that kept items keep the
// characters the handler wrote: <, > and & are not escaped.
func TestFilterListResultKeepsCharacters(t *testing.T) {
	t.Parallel()
	got, err := FilterListResult("tools/list",
		[]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"a","description":"<b> & c"}]}}`),
		map[string]struct{}{"a": {}})
	require.NoError(t, err)
	assert.Contains(t, string(got), `"description":"<b> & c"`)
}
