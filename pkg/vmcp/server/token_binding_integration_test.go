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
	"strings"
	"sync"
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
// Factory that can inspect the stored token hash of the created session.
// ---------------------------------------------------------------------------

// tokenBindingFactory is a MultiSessionFactory that records the created session
// so tests can inspect the stored token hash.
type tokenBindingFactory struct {
	inner       vmcpsession.MultiSessionFactory
	mu          sync.RWMutex
	lastSession vmcpsession.MultiSession
}

func (f *tokenBindingFactory) MakeSession(
	ctx context.Context, id *auth.Identity, backends []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	sess, err := f.inner.MakeSession(ctx, id, backends)
	if err == nil {
		f.mu.Lock()
		f.lastSession = sess
		f.mu.Unlock()
	}
	return sess, err
}

func (f *tokenBindingFactory) MakeSessionWithID(
	ctx context.Context, sessID string, id *auth.Identity, allowAnonymous bool, backends []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	sess, err := f.inner.MakeSessionWithID(ctx, sessID, id, allowAnonymous, backends)
	if err == nil {
		f.mu.Lock()
		f.lastSession = sess
		f.mu.Unlock()
	}
	return sess, err
}

// getLastSession returns the last created session, thread-safe.
func (f *tokenBindingFactory) getLastSession() vmcpsession.MultiSession {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.lastSession
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

// TestIntegration_TokenBinding_SessionTerminatedOnCredentialMismatch tests the
// mid-flow mismatch scenario: after initialization with one token, a follow-up
// request with a different token must receive HTTP 401 and the session must be
// terminated (subsequent requests to the same session ID also fail).
func TestIntegration_TokenBinding_SessionTerminatedOnCredentialMismatch(t *testing.T) {
	t.Parallel()

	const originalToken = "original-bearer-token"
	const differentToken = "attacker-different-token"

	tools := []vmcp.Tool{{Name: "secret", Description: "secret tool"}}
	factory := newV2FakeFactory(tools)
	ts := buildTokenBindingServer(t, factory, tokenPassthroughMiddleware)

	// Step 1: Initialize with the original token.
	sessionID := mcpInitialize(t, ts, originalToken)

	require.Eventually(t, func() bool {
		return factory.makeWithIDCalled.Load()
	}, 2*time.Second, 10*time.Millisecond)

	// Step 2: Send a request with a DIFFERENT token → must receive 401.
	resp := mcpRequestWithToken(t, ts, sessionID, differentToken)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"mismatched token must receive 401; body: %s", body)
	assert.True(t, strings.Contains(string(body), "session authentication mismatch"),
		"response body must contain the mismatch error; got: %s", body)

	// Step 3: Session should be terminated — subsequent requests with the
	// original token should also fail (session no longer exists).
	resp2 := mcpRequestWithToken(t, ts, sessionID, originalToken)
	defer resp2.Body.Close()
	// After termination the SDK returns 404 (session not found).
	assert.NotEqual(t, http.StatusOK, resp2.StatusCode,
		"session should be terminated; subsequent requests must not succeed")
}

// TestIntegration_TokenBinding_AnonymousSession_RejectedWhenTokenPresented
// verifies that a session created without a bearer token rejects any follow-up
// request that suddenly presents a token.
func TestIntegration_TokenBinding_AnonymousSession_RejectedWhenTokenPresented(t *testing.T) {
	t.Parallel()

	tools := []vmcp.Tool{{Name: "anon-tool", Description: "anonymous tool"}}
	factory := newV2FakeFactory(tools)
	// No auth middleware → all sessions are anonymous.
	ts := buildTokenBindingServer(t, factory, nil)

	// Step 1: Initialize WITHOUT a bearer token (anonymous session).
	sessionID := mcpInitialize(t, ts, "")

	require.Eventually(t, func() bool {
		return factory.makeWithIDCalled.Load()
	}, 2*time.Second, 10*time.Millisecond)

	// Step 2: Follow-up request WITH a token → must receive 401.
	resp := mcpRequestWithToken(t, ts, sessionID, "unexpected-token")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"anonymous session must reject a request with a token; body: %s", body)
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

// TestIntegration_TokenBinding_StoredHashIsNotRawToken verifies that the raw
// bearer token is never stored in session metadata — only the hash.
func TestIntegration_TokenBinding_StoredHashIsNotRawToken(t *testing.T) {
	t.Parallel()

	const token = "raw-token-must-not-be-stored"
	tools := []vmcp.Tool{{Name: "t", Description: "tool"}}
	inner := newV2FakeFactory(tools)
	factory := &tokenBindingFactory{inner: inner}
	ts := buildTokenBindingServer(t, factory, tokenPassthroughMiddleware)

	sessionID := mcpInitialize(t, ts, token)

	// Wait for the session to be created and available.
	// Use factory.getLastSession() to safely check from another goroutine.
	var lastSession vmcpsession.MultiSession
	require.Eventually(t, func() bool {
		lastSession = factory.getLastSession()
		return lastSession != nil
	}, 2*time.Second, 10*time.Millisecond, "session must have been created")

	// The created session's metadata must contain the token hash, not the raw token.
	meta := lastSession.GetMetadata()
	storedHash, present := meta[vmcpsession.MetadataKeyTokenHash]

	require.True(t, present, "MetadataKeyTokenHash must be present in session metadata")
	assert.NotEqual(t, token, storedHash, "raw token must not be stored in metadata")
	assert.NotEmpty(t, storedHash, "hash must not be empty for an authenticated session")
	// The session ID is not a token.
	_ = sessionID
}
