// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// TestEncodeJSONRPCError pins the wire properties the helper exists to
// guarantee: lowercase keys with the version tag stamped (the #5950 bug was
// reflection over the untagged jsonrpc2.Response), an absent id omitted
// rather than rendered null (#6038), and a legitimate id of 0 surviving —
// the omitempty on the library's wire struct tests IsNil, not zero-ness.
func TestEncodeJSONRPCError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   jsonrpc2.ID
		want string
	}{
		{
			name: "int64 id",
			id:   jsonrpc2.Int64ID(42),
			want: `{"jsonrpc":"2.0","id":42,"error":{"code":403,"message":"denied"}}`,
		},
		{
			name: "string id",
			id:   jsonrpc2.StringID("abc"),
			want: `{"jsonrpc":"2.0","id":"abc","error":{"code":403,"message":"denied"}}`,
		},
		{
			name: "zero id survives",
			id:   jsonrpc2.Int64ID(0),
			want: `{"jsonrpc":"2.0","id":0,"error":{"code":403,"message":"denied"}}`,
		},
		{
			name: "empty id omits the key",
			id:   jsonrpc2.ID{},
			want: `{"jsonrpc":"2.0","error":{"code":403,"message":"denied"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := EncodeJSONRPCError(&jsonrpc2.Response{
				ID:    tt.id,
				Error: jsonrpc2.NewError(JSONRPCCodeDenied, "denied"),
			})

			// assert.JSONEq is key-case-sensitive, so this single assertion
			// catches "Error"/"ID" substituted for "error"/"id" and a missing
			// "jsonrpc" tag; key presence is asserted separately because a
			// decoded nil is indistinguishable from a decoded null.
			assert.JSONEq(t, tt.want, string(body))

			var decoded map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body, &decoded))
			_, hasID := decoded["id"]
			assert.Equal(t, tt.id != jsonrpc2.ID{}, hasID, "id key presence mismatch")
		})
	}
}

func TestWriteJSONRPCError(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	err := WriteJSONRPCError(rr, http.StatusForbidden, &jsonrpc2.Response{
		ID:    jsonrpc2.Int64ID(7),
		Error: jsonrpc2.NewError(JSONRPCCodeDenied, "denied"),
	})
	require.NoError(t, err)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":7,"error":{"code":403,"message":"denied"}}`, rr.Body.String())
}
