// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// ---------------------------------------------------------------------------
// Test auth middleware: propagates bearer token to context identity.
// ---------------------------------------------------------------------------

// tokenPassthroughMiddleware is a test auth middleware that accepts any bearer
// token and propagates it as the identity token. This allows integration tests
// to exercise token binding without a real OIDC provider.
func tokenPassthroughMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := auth.ExtractBearerToken(r)
		if err == nil && token != "" {
			identity := &auth.Identity{
				Subject: "test-user",
				Token:   token,
			}
			r = r.WithContext(auth.WithIdentity(r.Context(), identity))
		}
		// No token == anonymous — pass through without identity.
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildTokenBindingServer creates a vMCP server with SessionManagementV2 and
// token binding middleware enabled. The authMiddleware parameter is optional;
// pass nil for anonymous-mode tests.
func buildTokenBindingServer(
	t *testing.T,
	factory vmcpsession.MultiSessionFactory,
	authMiddleware func(http.Handler) http.Handler,
) *httptest.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	emptyAggCaps := &aggregator.AggregatedCapabilities{}
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).Return(emptyAggCaps, nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	rt := router.NewDefaultRouter()

	srv, err := server.New(
		context.Background(),
		&server.Config{
			Host:                "127.0.0.1",
			Port:                0,
			SessionTTL:          5 * time.Minute,
			SessionManagementV2: true,
			SessionFactory:      factory,
			AuthMiddleware:      authMiddleware,
		},
		rt,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		nil,
	)
	require.NoError(t, err)

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// mcpInitialize sends an MCP initialize request and returns the session ID from
// the response header. Fails the test if the request does not succeed.
func mcpInitialize(t *testing.T, ts *httptest.Server, bearerToken string) string {
	t.Helper()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "initialize must succeed")
	sessionID := resp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	return sessionID
}

// mcpRequestWithToken sends a tools/list JSON-RPC request with the given
// session ID and bearer token, and returns the response.
func mcpRequestWithToken(t *testing.T, ts *httptest.Server, sessionID, bearerToken string) *http.Response {
	t.Helper()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

// TestIntegration_TokenBinding_AuthenticatedSession_ValidToken verifies the full
// request lifecycle: initialize with a bearer token, then use the same token on
// subsequent requests → all requests should succeed.
func TestIntegration_TokenBinding_AuthenticatedSession_ValidToken(t *testing.T) {
	t.Parallel()

	const token = "integration-test-bearer-token"
	tools := []vmcp.Tool{{Name: "hello", Description: "says hello"}}
	factory := newV2FakeFactory(tools)
	ts := buildTokenBindingServer(t, factory, tokenPassthroughMiddleware)

	// Step 1: Initialize with a bearer token.
	sessionID := mcpInitialize(t, ts, token)

	// Step 2: Wait for the session to be fully created via the hook.
	require.Eventually(t, func() bool {
		return factory.makeWithIDCalled.Load()
	}, 2*time.Second, 10*time.Millisecond, "MakeSessionWithID should be called after initialize")

	// Step 3: Subsequent request with the SAME token → must succeed.
	resp := mcpRequestWithToken(t, ts, sessionID, token)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"request with same token should succeed; body: %s", body)
}

// TestIntegration_TokenBinding_AnonymousSession_AllowedWithNoToken verifies
// that an anonymous session allows follow-up requests that also carry no token.
func TestIntegration_TokenBinding_AnonymousSession_AllowedWithNoToken(t *testing.T) {
	t.Parallel()

	tools := []vmcp.Tool{{Name: "anon-tool", Description: "anonymous tool"}}
	factory := newV2FakeFactory(tools)
	ts := buildTokenBindingServer(t, factory, nil)

	// Step 1: Initialize WITHOUT a token.
	sessionID := mcpInitialize(t, ts, "")

	require.Eventually(t, func() bool {
		return factory.makeWithIDCalled.Load()
	}, 2*time.Second, 10*time.Millisecond)

	// Step 2: Follow-up request also WITHOUT a token → must succeed.
	resp := mcpRequestWithToken(t, ts, sessionID, "")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"anonymous session with no token must be allowed; body: %s", body)
}

// ---------------------------------------------------------------------------
// Enhanced integration tests: Tool calls with token validation
//
// NOTE: These tests use v2FakeMultiSession which is a test stub that doesn't
// implement actual token validation logic. They verify the HTTP request routing
// and handler plumbing, but NOT the session-level validation itself.
//
// For comprehensive token validation tests (including validation logic,
// session termination, etc.), see:
//   - pkg/vmcp/session/token_binding_test.go (unit tests for validateCaller)
//   - pkg/vmcp/server/sessionmanager/session_manager_test.go (handler tests with real validation)
// ---------------------------------------------------------------------------

// mcpCallTool sends a tools/call JSON-RPC request with the given session ID
// and bearer token, invoking the specified tool. Returns the response.
func mcpCallTool(t *testing.T, ts *httptest.Server, sessionID, bearerToken, toolName string) *http.Response {
	t.Helper()

	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": map[string]any{},
		},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp", bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestIntegration_TokenBinding_ToolCall_MatchingToken verifies that actual tool
// calls with matching tokens succeed. This tests the full validation path through
// CallTool -> validateCaller -> backend execution.
func TestIntegration_TokenBinding_ToolCall_MatchingToken(t *testing.T) {
	t.Parallel()

	const token = "valid-tool-call-token"
	tools := []vmcp.Tool{{Name: "secure-tool", Description: "requires token validation"}}
	factory := newV2FakeFactory(tools)
	ts := buildTokenBindingServer(t, factory, tokenPassthroughMiddleware)

	// Step 1: Initialize with bearer token
	sessionID := mcpInitialize(t, ts, token)

	require.Eventually(t, func() bool {
		return factory.makeWithIDCalled.Load()
	}, 2*time.Second, 10*time.Millisecond)

	// Step 2: Call tool with SAME token → should succeed
	resp := mcpCallTool(t, ts, sessionID, token, "secure-tool")
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"tool call with matching token should succeed; body: %s", body)

	// Verify it's a successful JSON-RPC response
	var rpcResp map[string]any
	err = json.Unmarshal(body, &rpcResp)
	require.NoError(t, err)
	assert.NotContains(t, rpcResp, "error", "should not have JSON-RPC error")
}

// TestIntegration_TokenBinding_ToolCall_MismatchedToken is tested at the handler level
// in pkg/vmcp/server/sessionmanager/session_manager_test.go since it requires a real
// session implementation with actual token validation logic.

// TestIntegration_TokenBinding_ToolCall_AnonymousSession verifies that anonymous
// sessions (created without a token) can successfully make tool calls without tokens.
func TestIntegration_TokenBinding_ToolCall_AnonymousSession(t *testing.T) {
	t.Parallel()

	tools := []vmcp.Tool{{Name: "public-tool", Description: "available to anonymous users"}}
	factory := newV2FakeFactory(tools)
	ts := buildTokenBindingServer(t, factory, nil) // No auth middleware

	// Step 1: Initialize WITHOUT a token (anonymous)
	sessionID := mcpInitialize(t, ts, "")

	require.Eventually(t, func() bool {
		return factory.makeWithIDCalled.Load()
	}, 2*time.Second, 10*time.Millisecond)

	// Step 2: Call tool WITHOUT a token → should succeed
	resp := mcpCallTool(t, ts, sessionID, "", "public-tool")
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"anonymous tool call should succeed; body: %s", body)

	// Verify it's a successful JSON-RPC response
	var rpcResp map[string]any
	err = json.Unmarshal(body, &rpcResp)
	require.NoError(t, err)
	assert.NotContains(t, rpcResp, "error", "should not have JSON-RPC error")
}

// TestIntegration_TokenBinding_ToolCall_AnonymousSessionRejectsToken is tested at the
// session level in pkg/vmcp/session/token_binding_test.go which verifies that anonymous
// sessions reject token presentation (prevents session upgrade attacks).
