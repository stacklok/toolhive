// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

func TestMiddlewarePassesValidatedClientResponsesAndRejectsOtherUnparsedBodies(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action == Action::"call_tool", resource == Tool::"allowed");`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	tests := []struct {
		name            string
		body            string
		contentType     string
		wantStatus      int
		wantHandlerCall bool
	}{
		{
			name:            "result response reaches transport",
			body:            `{"jsonrpc":"2.0","id":1,"result":{}}`,
			contentType:     "application/json",
			wantStatus:      http.StatusAccepted,
			wantHandlerCall: true,
		},
		{
			name:            "error response reaches transport",
			body:            `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`,
			contentType:     "application/json",
			wantStatus:      http.StatusAccepted,
			wantHandlerCall: true,
		},
		{
			name:        "protected request remains denied",
			body:        `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"protected","arguments":{}}}`,
			contentType: "application/json",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:            "response under text/plain reaches transport",
			body:            `{"jsonrpc":"2.0","id":4,"result":{}}`,
			contentType:     "text/plain",
			wantStatus:      http.StatusAccepted,
			wantHandlerCall: true,
		},
		{
			name:            "response without Content-Type reaches transport",
			body:            `{"jsonrpc":"2.0","id":5,"result":{}}`,
			wantStatus:      http.StatusAccepted,
			wantHandlerCall: true,
		},
		{
			name:        "response missing result and error is rejected",
			body:        `{"jsonrpc":"2.0","id":6}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "response containing result and error is rejected",
			body:        `{"jsonrpc":"2.0","id":7,"result":{},"error":{"code":-32601,"message":"Method not found"}}`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
		{
			name:        "malformed JSON is rejected",
			body:        `{"jsonrpc":"2.0","id":8,"method":`,
			contentType: "application/json",
			wantStatus:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handlerCalled := false
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusAccepted)
			})
			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, handler, nil))

			req, err := http.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tt.body))
			require.NoError(t, err)
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}

			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			assert.Equal(t, tt.wantStatus, rr.Code, "response body: %s", rr.Body.String())
			assert.Equal(t, tt.wantHandlerCall, handlerCalled)
		})
	}
}
