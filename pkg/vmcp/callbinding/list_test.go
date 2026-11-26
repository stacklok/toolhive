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

// TestListItemsMembersCoverListMethods pins the items members to the list
// methods a list-call entry may name, so a method added there cannot reach
// ReadListResponse without its member.
func TestListItemsMembersCoverListMethods(t *testing.T) {
	t.Parallel()
	for _, method := range []string{
		"tools/list", "prompts/list", "resources/list", "resources/templates/list",
		"tools/call", "Tools/list", "completion/complete", "",
	} {
		_, known := listItemsMembers[method]
		assert.Equal(t, toolcalldetails.IsListMethod(method), known, "%q", method)
	}
}

// TestListResponseEncode checks that a list response is answered with the
// given items, every other member kept as written and in order, nextCursor
// dropped and the cache hints made private and stale.
func TestListResponseEncode(t *testing.T) {
	t.Parallel()
	answer := []json.RawMessage{json.RawMessage(`{"name":"github_read","description":"a <b> & c"}`)}
	tests := []struct {
		name     string
		method   string
		response string
		want     string
	}{
		{
			name:   "tools",
			method: "tools/list",
			response: `{"jsonrpc":"2.0","id":12345678901234567890,"result":{"tools":[{"name":"other_read"}],` +
				`"nextCursor":"other_read","_meta":{"n":1.50},"resultType":"complete"}}`,
			want: `{"jsonrpc":"2.0","id":12345678901234567890,"result":{"tools":[{"name":"github_read",` +
				`"description":"a <b> & c"}],"_meta":{"n":1.50},"resultType":"complete"}}`,
		},
		{
			name:     "prompts with null items",
			method:   "prompts/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"prompts":null}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"prompts":[{"name":"github_read","description":"a <b> & c"}]}}`,
		},
		{
			name:     "resources",
			method:   "resources/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"resources":[]}}`,
			want:     `{"jsonrpc":"2.0","id":1,"result":{"resources":[{"name":"github_read","description":"a <b> & c"}]}}`,
		},
		{
			name:     "templates, cache hints private and stale",
			method:   "resources/templates/list",
			response: `{"jsonrpc":"2.0","id":1,"result":{"ttlMs":60000,"cacheScope":"public","resourceTemplates":[]}}`,
			want: `{"jsonrpc":"2.0","id":1,"result":{"ttlMs":0,"cacheScope":"private",` +
				`"resourceTemplates":[{"name":"github_read","description":"a <b> & c"}]}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp, isError, err := ReadListResponse(tc.method, []byte(tc.response))
			require.NoError(t, err)
			require.False(t, isError)
			got, err := resp.Encode(answer)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(got))
		})
	}

	resp, _, err := ReadListResponse("tools/list", []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
	require.NoError(t, err)
	got, err := resp.Encode(nil)
	require.NoError(t, err)
	assert.Equal(t, `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`, string(got), "no items is an empty array")
}

func TestReadListResponseRefusals(t *testing.T) {
	t.Parallel()
	_, isError, err := ReadListResponse("tools/list",
		[]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"x"}}`))
	require.NoError(t, err)
	assert.True(t, isError, "a JSON-RPC error")

	for name, tc := range map[string]struct{ method, response, cause string }{
		"not a list method": {"tools/call", `{"jsonrpc":"2.0","id":1,"result":{}}`, "not a list method"},
		"not JSON":          {"tools/list", `data: {"result":{}}`, "not a JSON object"},
		"null":              {"tools/list", `null`, "not a JSON object"},
		"trailing data": {"tools/list", `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}{}`,
			"not a JSON object"},
		"neither result nor error": {"tools/list", `{"jsonrpc":"2.0","id":1}`, "neither a result nor an error"},
		"result twice": {"tools/list", `{"jsonrpc":"2.0","id":1,"result":{"tools":[]},"result":{"tools":[]}}`,
			"ambiguous result"},
		"result not an object": {"tools/list", `{"jsonrpc":"2.0","id":1,"result":[]}`, "result is not an object"},
		"null result":          {"tools/list", `{"jsonrpc":"2.0","id":1,"result":null}`, "result is not an object"},
		"no items member":      {"prompts/list", `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`, "no items member"},
		"items in other case": {"tools/list", `{"jsonrpc":"2.0","id":1,"result":{"tools":[],"Tools":[]}}`,
			"ambiguous items"},
		"items not an array": {"tools/list", `{"jsonrpc":"2.0","id":1,"result":{"tools":{"name":"a"}}}`,
			"items member is not an array"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resp, isError, err := ReadListResponse(tc.method, []byte(tc.response))
			require.ErrorIs(t, err, ErrListResult)
			assert.Contains(t, err.Error(), tc.cause)
			assert.Nil(t, resp)
			assert.False(t, isError)
		})
	}
}

func TestListParamsCarryCursor(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		params  string
		want    bool
		refused bool
	}{
		"absent params":        {params: ``},
		"null params":          {params: `null`},
		"no cursor":            {params: `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}`},
		"null cursor":          {params: `{"cursor":null}`},
		"cursor":               {params: `{"cursor":"abc"}`, want: true},
		"empty cursor":         {params: `{"cursor":""}`, want: true},
		"cursor in other case": {params: `{"Cursor":"abc"}`, refused: true},
		"cursor twice":         {params: `{"cursor":null,"cursor":"abc"}`, refused: true},
		"params not an object": {params: `["cursor"]`, refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := ListParamsCarryCursor(json.RawMessage(tc.params))
			if tc.refused {
				require.ErrorIs(t, err, ErrListResult)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
