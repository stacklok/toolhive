// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestNewIncomingAuthMiddleware_AuthzEnforced tests that Cedar authorization policies
// configured in IncomingAuthConfig.Authz are properly enforced by the middleware.
//
// These tests assert the EXPECTED behavior:
//   - When authz is configured with a deny-all policy, requests should be rejected
//   - When authz is configured with role-based policies, unauthorized users should be rejected
//
// Currently these tests FAIL because authz is not wired up in vMCP.
// Once authz middleware is implemented, these tests should pass.
func TestNewIncomingAuthMiddleware_AuthzEnforced(t *testing.T) {
	t.Parallel()

	t.Run("deny_all_policy_blocks_tool_calls", func(t *testing.T) {
		t.Parallel()

		// Configure with anonymous auth + Cedar policy that denies all tool calls
		cfg := &config.IncomingAuthConfig{
			Type: "anonymous",
			Authz: &config.AuthzConfig{
				Type: "cedar",
				Policies: []string{
					// This policy should deny all tool call requests
					`forbid(principal, action == Action::"call_tool", resource);`,
					// But allow listing tools
					`permit(principal, action == Action::"list_tools", resource);`,
				},
			},
		}

		middleware, _, err := NewIncomingAuthMiddleware(t.Context(), cfg)
		require.NoError(t, err, "middleware creation should succeed")
		require.NotNil(t, middleware, "middleware should not be nil")

		// Track if the handler is called
		handlerCalled := false
		testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusOK)
		})

		wrapped := middleware(testHandler)

		// Simulate a tools/call request that should be DENIED by the Cedar policy
		mcpRequest := map[string]any{
			"jsonrpc": "2.0",
			"method":  "tools/call",
			"id":      1,
			"params": map[string]any{
				"name":      "dangerous_tool",
				"arguments": map[string]any{},
			},
		}
		body, err := json.Marshal(mcpRequest)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		wrapped.ServeHTTP(recorder, req)

		// EXPECTED: The handler should NOT be called because the Cedar policy denies it
		assert.False(t, handlerCalled,
			"handler should NOT be called - Cedar policy should deny tools/call requests")

		// EXPECTED: The response should be 403 Forbidden
		assert.Equal(t, http.StatusForbidden, recorder.Code,
			"response should be 403 Forbidden when Cedar policy denies the request")
	})

	t.Run("role_based_policy_blocks_non_admin", func(t *testing.T) {
		t.Parallel()

		// Configure with anonymous auth + Cedar policy requiring admin role
		cfg := &config.IncomingAuthConfig{
			Type: "anonymous",
			Authz: &config.AuthzConfig{
				Type: "cedar",
				Policies: []string{
					// Only admins can call tools
					`permit(principal, action == Action::"call_tool", resource) when { principal.claim_role == "admin" };`,
				},
			},
		}

		middleware, _, err := NewIncomingAuthMiddleware(t.Context(), cfg)
		require.NoError(t, err, "middleware creation should succeed")
		require.NotNil(t, middleware, "middleware should not be nil")

		handlerCalled := false
		testHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusOK)
		})

		wrapped := middleware(testHandler)

		// Anonymous user has no role, so should be denied
		mcpRequest := map[string]any{
			"jsonrpc": "2.0",
			"method":  "tools/call",
			"id":      1,
			"params": map[string]any{
				"name":      "admin_only_tool",
				"arguments": map[string]any{},
			},
		}
		body, err := json.Marshal(mcpRequest)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		wrapped.ServeHTTP(recorder, req)

		// EXPECTED: Anonymous user should be denied (no admin role)
		assert.False(t, handlerCalled,
			"handler should NOT be called - anonymous user lacks admin role")
		assert.Equal(t, http.StatusForbidden, recorder.Code,
			"response should be 403 Forbidden for non-admin user")
	})
}
