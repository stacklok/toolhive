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

func TestMiddlewareAcceptsClientResponse(t *testing.T) {
	t.Parallel()

	authorizer, err := cedar.NewCedarAuthorizer(cedar.ConfigOptions{
		Policies:     []string{`permit(principal, action, resource);`},
		EntitiesJSON: `[]`,
	}, "")
	require.NoError(t, err)

	for _, tt := range []struct {
		name string
		body string
	}{
		{"ping result", `{"jsonrpc":"2.0","id":1,"result":{}}`},
		{"error response", `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method not found"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var handlerCalled bool
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusAccepted)
			})
			middleware := mcpparser.ParsingMiddleware(Middleware(authorizer, handler, nil))

			req, err := http.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tt.body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")

			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			assert.True(t, handlerCalled, "a client response must reach the transport")
			assert.Equal(t, http.StatusAccepted, rr.Code)
		})
	}
}
