// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package wireerr

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestURLElicitationRequired(t *testing.T) {
	t.Parallel()

	data := json.RawMessage(`{"elicitations":[]}`)
	err := URLElicitationRequired(data)

	var wireErr *jsonrpc.Error
	require.ErrorAs(t, err, &wireErr)
	assert.Same(t, wireErr, err, "must be a bare *jsonrpc.Error, or go-sdk drops its data")
	assert.EqualValues(t, -32042, wireErr.Code)
	assert.Equal(t, "URL elicitation required", wireErr.Message)
	assert.JSONEq(t, string(data), string(wireErr.Data))
}

func TestFindURLElicitationRequired(t *testing.T) {
	t.Parallel()

	bare := URLElicitationRequired(json.RawMessage(`{"elicitations":[]}`))
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "bare", err: bare, want: bare},
		{name: "wrapped", err: fmt.Errorf("call_tool: %w", bare), want: bare},
		{name: "other JSON-RPC code", err: &jsonrpc.Error{Code: jsonrpc.CodeInternalError}},
		{name: "plain error", err: errors.New("backend unavailable")},
		{name: "nil", err: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FindURLElicitationRequired(tc.err)
			if tc.want == nil {
				assert.NoError(t, got)
				return
			}
			assert.Same(t, tc.want, got)
		})
	}
}
