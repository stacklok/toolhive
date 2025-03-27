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

	"github.com/stacklok/vibetool/pkg/auth"
	"github.com/stacklok/vibetool/pkg/transport"
)

func TestMiddleware(t *testing.T) {

	// Create a Cedar authorizer
	authorizer, err := NewCedarAuthorizer(CedarAuthorizerConfig{
		Policies: []string{
			`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
			`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
			`permit(principal, action == Action::"read_resource", resource == Resource::"data");`,
		},
		EntitiesJSON: `[]`,
	})
	require.NoError(t, err, "Failed to create Cedar authorizer")

	// Test cases
	testCases := []struct {
		name             string
		method           string
		params           map[string]interface{}
		claims           map[string]interface{}
		expectStatus     int
		expectAuthorized bool
	}{
		{
			name:   "Authorized tool call",
			method: "tools/call",
			params: map[string]interface{}{
				"name": "weather",
				"arguments": map[string]interface{}{
					"location": "New York",
				},
			},
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Unauthorized tool call",
			method: "tools/call",
			params: map[string]interface{}{
				"name": "calculator",
				"arguments": map[string]interface{}{
					"operation": "add",
					"value1":    5,
					"value2":    10,
				},
			},
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Authorized prompt get",
			method: "prompts/get",
			params: map[string]interface{}{
				"name": "greeting",
			},
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Unauthorized prompt get",
			method: "prompts/get",
			params: map[string]interface{}{
				"name": "farewell",
			},
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Authorized resource read",
			method: "resources/read",
			params: map[string]interface{}{
				"uri": "data",
			},
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Unauthorized resource read",
			method: "resources/read",
			params: map[string]interface{}{
				"uri": "secret",
			},
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusForbidden,
			expectAuthorized: false,
		},
		{
			name:   "Ping is always allowed",
			method: "ping",
			params: map[string]interface{}{},
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
		{
			name:   "Initialize is always allowed",
			method: "initialize",
			params: map[string]interface{}{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]interface{}{
					"roots": map[string]interface{}{
						"listChanged": true,
					},
					"sampling": map[string]interface{}{},
				},
				"clientInfo": map[string]interface{}{
					"name":    "ExampleClient",
					"version": "1.0.0",
				},
			},
			claims: map[string]interface{}{
				"sub":  "user123",
				"name": "John Doe",
			},
			expectStatus:     http.StatusOK,
			expectAuthorized: true,
		},
	}

	// Run test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a JSON-RPC request
			request, err := transport.NewRequestMessage(tc.method, tc.params, 1)
			require.NoError(t, err, "Failed to create JSON-RPC request")

			// Marshal the request to JSON
			requestJSON, err := json.Marshal(request)
			require.NoError(t, err, "Failed to marshal JSON-RPC request")

			// Create an HTTP request
			req, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBuffer(requestJSON))
			require.NoError(t, err, "Failed to create HTTP request")
			req.Header.Set("Content-Type", "application/json")

			// Add claims to the request context
			req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsContextKey{}, tc.claims))

			// Create a response recorder
			rr := httptest.NewRecorder()

			// Create a handler that records if it was called
			var handlerCalled bool
			handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				handlerCalled = true
				w.WriteHeader(http.StatusOK)
			})

			// Apply the middleware
			middleware := authorizer.Middleware(handler)

			// Serve the request
			middleware.ServeHTTP(rr, req)

			// Check the response
			assert.Equal(t, tc.expectStatus, rr.Code, "Response status code does not match expected")
			assert.Equal(t, tc.expectAuthorized, handlerCalled, "Handler called status does not match expected")
		})
	}
}
