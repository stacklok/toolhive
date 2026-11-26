// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

func rawMCPRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req.WithContext(auth.WithIdentity(context.Background(), &auth.Identity{PrincipalInfo: auth.PrincipalInfo{
		Subject: "user", Claims: map[string]interface{}{"sub": "user"},
	}}))
}

func TestParsingMiddlewareRejectsAmbiguousRequestsBeforeAuthorization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "tool name case variant",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_public","Name":"read_secret"}}`,
		},
		{
			name: "resource URI case variant",
			body: `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"public://readme","URI":"file:///restricted/credentials"}}`,
		},
		{
			name: "envelope method case variant",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"danger"},"Method":"tools/list"}`,
		},
		{
			name: "skills URI case variant",
			body: `{"jsonrpc":"2.0","id":1,"method":"skills/get","params":{"uri":"mcp://example/allowed","URI":"mcp://example/denied"}}`,
		},
		{
			name: "nested argument case variant",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"deploy","arguments":{"mode":"safe","Mode":"destroy"}}}`,
		},
		{
			name: "exact duplicate tool name",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"denied","name":"allowed"}}`,
		},
		{
			name: "numeric overflow before case variant",
			body: `{"padding":1e1000,"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_public","Name":"read_secret"}}`,
		},
		{
			name: "numeric overflow after case variant",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_public","Name":"read_secret"},"padding":1e1000}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			authorizer := &stubAuthorizer{allowed: true}
			backendCalled := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				backendCalled = true
			})
			recorder := httptest.NewRecorder()

			mcpparser.ParsingMiddleware(Middleware(authorizer, next, nil)).ServeHTTP(
				recorder, rawMCPRequest(t, tt.body))

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.False(t, backendCalled)
			assert.Zero(t, authorizer.calls)

			var response map[string]any
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			_, hasID := response["id"]
			assert.False(t, hasID)
			errorBody, ok := response["error"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, float64(mcpparser.CodeInvalidRequest), errorBody["code"])
			assert.Equal(t, "Invalid Request", errorBody["message"])
		})
	}
}
