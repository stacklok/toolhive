// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

// FuzzDecodeMessage checks that DecodeMessage never panics and that any
// message it admits survives an encode/decode round trip unchanged.
func FuzzDecodeMessage(f *testing.F) {
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"echo"}}`,
		`{"jsonrpc":"2.0","id":"request-1","method":"ping"}`,
		`{"jsonrpc":"2.0","id":0,"method":"ping","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":null}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","_meta":{"x":1e1000}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"x":1.7976931348623157e308}}}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"result":{"x":1e1000}}`,
		`{"jsonrpc":"2.0","error":{"code":-32603,"message":"backend error"}}`,
		`{"jsonrpc":"2.0","id":"a","error":{"code":-32603,"message":"e","data":{"k":[1,2]}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","Method":"x"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":{"a":1,"a":2}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
		"\xEF\xBB\xBF" + `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		msg, err := DecodeMessage(body)
		if err != nil {
			return
		}
		require.NotNil(t, msg)

		encoded, err := jsonrpc2.EncodeMessage(msg)
		require.NoError(t, err)
		again, err := DecodeMessage(encoded)
		require.NoError(t, err, "re-encoded message rejected: %s", encoded)
		require.Equal(t, messageIdentity(msg), messageIdentity(again))

		reencoded, err := jsonrpc2.EncodeMessage(again)
		require.NoError(t, err)
		require.True(t, bytes.Equal(encoded, reencoded),
			"encoding is not a fixed point:\nfirst:  %s\nsecond: %s", encoded, reencoded)
	})
}

// messageIdentity returns the kind, ID and method of msg for comparison.
func messageIdentity(msg jsonrpc2.Message) [3]any {
	switch m := msg.(type) {
	case *jsonrpc2.Request:
		return [3]any{"request", m.ID.Raw(), m.Method}
	case *jsonrpc2.Response:
		return [3]any{"response", m.ID.Raw(), m.Error != nil}
	default:
		return [3]any{"unknown", nil, nil}
	}
}
