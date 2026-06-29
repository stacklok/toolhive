// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseMCPResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         string
		wantHasError bool
		wantCode     int
		wantMessage  string
	}{
		{
			name:         "empty body",
			body:         "",
			wantHasError: false,
		},
		{
			name:         "success response with result",
			body:         `{"jsonrpc":"2.0","id":"1","result":{"content":[{"type":"text","text":"hello"}]}}`,
			wantHasError: false,
		},
		{
			name:         "error response with internal error",
			body:         `{"jsonrpc":"2.0","id":"1","error":{"code":-32603,"message":"GitLab API error: 401 Unauthorized"}}`,
			wantHasError: true,
			wantCode:     -32603,
			wantMessage:  "GitLab API error: 401 Unauthorized",
		},
		{
			name:         "error response with method not found",
			body:         `{"jsonrpc":"2.0","id":"1","error":{"code":-32601,"message":"Method not found"}}`,
			wantHasError: true,
			wantCode:     -32601,
			wantMessage:  "Method not found",
		},
		{
			name:         "error response with expired token",
			body:         `{"jsonrpc":"2.0","id":"1","error":{"code":-32603,"message":"GitLab API error: 401 Unauthorized\n{\"error\":\"invalid_token\",\"error_description\":\"Token is expired.\"}"}}`,
			wantHasError: true,
			wantCode:     -32603,
		},
		{
			name:         "invalid JSON",
			body:         `not json at all`,
			wantHasError: false,
		},
		{
			name:         "valid JSON without error field",
			body:         `{"jsonrpc":"2.0","id":"1"}`,
			wantHasError: false,
		},
		{
			name:         "long error message is preserved in full",
			body:         `{"jsonrpc":"2.0","id":"1","error":{"code":-32603,"message":"` + strings.Repeat("a", 300) + `"}}`,
			wantHasError: true,
			wantCode:     -32603,
			wantMessage:  strings.Repeat("a", 300),
		},
		{
			name:         "error with zero code",
			body:         `{"jsonrpc":"2.0","id":"1","error":{"code":0,"message":"unknown error"}}`,
			wantHasError: true,
			wantCode:     0,
			wantMessage:  "unknown error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := ParseMCPResponse([]byte(tt.body))
			assert.Equal(t, tt.wantHasError, result.HasError)
			if tt.wantHasError {
				assert.Equal(t, tt.wantCode, result.ErrorCode)
				if tt.wantMessage != "" {
					assert.Equal(t, tt.wantMessage, result.ErrorMessage)
				}
			}
		})
	}
}

func TestValidateJSONRPCResponseBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "valid result response",
			body: `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`,
		},
		{
			name: "valid error response",
			body: `{"jsonrpc":"2.0","id":"abc","error":{"code":-32601,"message":"Method not found"}}`,
		},
		{
			name: "valid batch response",
			body: `[{"jsonrpc":"2.0","id":1,"result":{}},{"jsonrpc":"2.0","id":null,"result":null}]`,
		},
		{
			name:    "invalid JSON",
			body:    `not json`,
			wantErr: "invalid JSON body",
		},
		{
			name:    "missing jsonrpc",
			body:    `{"id":1,"result":{"ok":true}}`,
			wantErr: `JSON-RPC response must include "jsonrpc":"2.0"`,
		},
		{
			name:    "invalid id type",
			body:    `{"jsonrpc":"2.0","id":{"nested":true},"result":{}}`,
			wantErr: "JSON-RPC response id must be string, number, or null",
		},
		{
			name:    "empty batch",
			body:    `[]`,
			wantErr: "JSON-RPC batch response must not be empty",
		},
		{
			name:    "result and error together",
			body:    `{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32603,"message":"boom"}}`,
			wantErr: "JSON-RPC response must include exactly one of result or error",
		},
		{
			name:    "trailing JSON value",
			body:    `{"jsonrpc":"2.0","id":1,"result":{}} {"jsonrpc":"2.0","id":2,"result":{}}`,
			wantErr: "JSON-RPC response must contain a single JSON value",
		},
		{
			name:    "trailing delimiter",
			body:    `{"jsonrpc":"2.0","id":1,"result":{}}]`,
			wantErr: "JSON-RPC response must contain a single JSON value",
		},
		{
			name:    "fractional error code",
			body:    `{"jsonrpc":"2.0","id":1,"error":{"code":1.5,"message":"nope"}}`,
			wantErr: "JSON-RPC error response must include error.code and error.message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateJSONRPCResponseBody([]byte(tt.body))
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
