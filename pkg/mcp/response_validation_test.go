// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		reason string // empty means the body is accepted
	}{
		// Accepted: the response envelopes DecodeMessage admits.
		{name: "result with int id", body: `{"jsonrpc":"2.0","id":1,"result":{}}`},
		{name: "result with string id", body: `{"jsonrpc":"2.0","id":"abc","result":{"tools":[]}}`},
		{name: "result with max int64 id", body: `{"jsonrpc":"2.0","id":9223372036854775807,"result":null}`},
		{name: "non-object result", body: `{"jsonrpc":"2.0","id":1,"result":[1,"two"]}`},
		{name: "error with id", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"boom"}}`},
		{name: "error without id", body: `{"jsonrpc":"2.0","error":{"code":-32700,"message":"Parse error"}}`},
		{name: "leading BOM", body: "\xEF\xBB\xBF" + `{"jsonrpc":"2.0","id":1,"result":{}}`},
		{name: "case-distinct names inside result", body: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"t","inputSchema":{"type":"object","properties":{"ID":{},"id":{}}}}]}}`},
		{name: "repeated name inside result", body: `{"jsonrpc":"2.0","id":1,"result":{"a":1,"a":2}}`},
		{name: "case-distinct names inside error data", body: `{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"x","data":{"Path":1,"path":2}}}`},
		{name: "result with null error", body: `{"jsonrpc":"2.0","id":1,"result":{},"error":null}`},
		{name: "error with null result", body: `{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":-32603,"message":"boom"}}`},
		{name: "null result with null error", body: `{"jsonrpc":"2.0","id":1,"result":null,"error":null}`},
		{name: "surrounding whitespace", body: " \r\n\t" + `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n"},

		// Rejected with a fixed reason.
		{name: "empty body", body: ``, reason: "parse"},
		{name: "invalid JSON", body: `{"jsonrpc":"2.0","id":1,"result":`, reason: "parse"},
		{name: "trailing data", body: `{"jsonrpc":"2.0","id":1,"result":{}}{}`, reason: "parse"},
		{name: "batch", body: `[{"jsonrpc":"2.0","id":1,"result":{}}]`, reason: "batch"},
		{name: "request", body: `{"jsonrpc":"2.0","id":1,"method":"ping"}`, reason: "not_a_response"},
		{name: "notification", body: `{"jsonrpc":"2.0","method":"notifications/progress"}`, reason: "not_a_response"},
		{name: "request with undecodable params", body: `{"jsonrpc":"2.0","id":1,"method":"x","params":{"n":1e1000}}`, reason: "not_a_response"},
		{name: "result without id", body: `{"jsonrpc":"2.0","result":{}}`, reason: "invalid_response"},
		{name: "null id", body: `{"jsonrpc":"2.0","id":null,"result":{}}`, reason: "invalid_response"},
		{name: "fractional id", body: `{"jsonrpc":"2.0","id":1.5,"result":{}}`, reason: "invalid_response"},
		{name: "missing jsonrpc version", body: `{"id":1,"result":{}}`, reason: "invalid_response"},
		{name: "both result and error", body: `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"x"}}`, reason: "invalid_response"},
		{name: "null error without result", body: `{"jsonrpc":"2.0","id":1,"error":null}`, reason: "invalid_response"},
		{name: "result with null error and no id", body: `{"jsonrpc":"2.0","result":{},"error":null}`, reason: "invalid_response"},
		{name: "null result beside malformed error", body: `{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":-32603}}`, reason: "invalid_response"},
		{name: "neither result nor error", body: `{"jsonrpc":"2.0","id":1}`, reason: "invalid_response"},
		{name: "error without message", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32603}}`, reason: "invalid_response"},
		{name: "duplicate key", body: `{"jsonrpc":"2.0","id":1,"id":2,"result":{}}`, reason: "invalid_response"},
		{name: "duplicate key after nested value", body: `{"jsonrpc":"2.0","id":1,"result":{"id":3,"x":[{"id":4}]},"id":2}`, reason: "invalid_response"},
		{name: "case-folded key alias", body: `{"jsonrpc":"2.0","id":1,"Result":{},"result":{}}`, reason: "invalid_response"},
		{name: "case-folded id alias", body: `{"jsonrpc":"2.0","ID":1,"result":{}}`, reason: "invalid_response"},
		{name: "case-folded error member alias", body: `{"jsonrpc":"2.0","id":1,"error":{"code":1,"Code":2,"message":"x"}}`, reason: "invalid_response"},
		{name: "non-object", body: `"hello"`, reason: "invalid_response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateResponse([]byte(tt.body))
			if tt.reason == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.reason, err.Error())
		})
	}
}
