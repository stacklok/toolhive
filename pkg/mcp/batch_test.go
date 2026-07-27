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
)

func TestIsBatchRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "single request object", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, want: false},
		{name: "batch array", body: `[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`, want: true},
		{name: "empty array", body: `[]`, want: true},
		{name: "array with leading whitespace", body: "  \n\t [1]", want: true},
		// Vertical tab / form feed are Unicode whitespace that json ignores;
		// bytes.TrimSpace must strip them so detection matches decoding (#5745).
		{name: "array with leading vertical tab", body: "\v\f[1]", want: true},
		{name: "object with leading whitespace", body: "  \n\t {}", want: false},
		{name: "empty body", body: ``, want: false},
		{name: "whitespace only", body: "   \n\t", want: false},
		{name: "malformed unterminated array", body: `[{"a":1}`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, IsBatchRequest([]byte(tt.body)))
		})
	}
}

func TestBatchUnsupportedError(t *testing.T) {
	t.Parallel()
	err := &BatchUnsupportedError{}
	// Must satisfy CodedError so the shared JSON-RPC error writer picks up its code.
	var coded CodedError = err
	assert.Equal(t, CodeInvalidRequest, coded.Code())
	assert.Nil(t, coded.Data())
	assert.NotEmpty(t, coded.Error())
}

func TestWriteBatchUnsupportedError(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()

	WriteBatchUnsupportedError(w)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		Error   struct {
			Code    int64  `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "2.0", resp.JSONRPC)
	assert.Equal(t, CodeInvalidRequest, resp.Error.Code)
	assert.NotEmpty(t, resp.Error.Message)

	// A batch has no single request id to echo. A tagged-struct decode
	// (above) can't tell an absent "id" key from a present null -- both
	// decode to the zero value -- so the omission is checked via a map
	// decode instead.
	var asMap map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &asMap))
	_, hasID := asMap["id"]
	assert.False(t, hasID, `"id" key must be omitted, not present as null`)
}
