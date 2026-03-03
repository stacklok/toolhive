// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// ---------------------------------------------------------------------------
// Fakes (redeclared for package server_test; cannot import internal fakes
// from the internal server package tests since those are in package server).
// ---------------------------------------------------------------------------

// v2FakeMultiSession is a minimal MultiSession implementation that records
// whether CallTool was invoked and returns a preconfigured result.
type v2FakeMultiSession struct {
	transportsession.Session // embedded to satisfy ID/Type/timestamp/metadata methods

	tools          []vmcp.Tool
	callToolCalled atomic.Bool
	callToolResult *vmcp.ToolCallResult
	callToolErr    error
	closed         atomic.Bool
}

func newV2FakeMultiSession(sess transportsession.Session, tools []vmcp.Tool) *v2FakeMultiSession {
	return &v2FakeMultiSession{
		Session: sess,
		tools:   tools,
		callToolResult: &vmcp.ToolCallResult{
			Content: []vmcp.Content{{Type: "text", Text: "fake result"}},
		},
	}
}

func (f *v2FakeMultiSession) Tools() []vmcp.Tool {
	result := make([]vmcp.Tool, len(f.tools))
	copy(result, f.tools)
	return result
}

func (*v2FakeMultiSession) Resources() []vmcp.Resource { return nil }
func (*v2FakeMultiSession) Prompts() []vmcp.Prompt     { return nil }
func (*v2FakeMultiSession) BackendSessions() map[string]string {
	return nil
}

func (f *v2FakeMultiSession) CallTool(
	_ context.Context, _ string, _ map[string]any, _ map[string]any,
) (*vmcp.ToolCallResult, error) {
	f.callToolCalled.Store(true)
	return f.callToolResult, f.callToolErr
}

func (*v2FakeMultiSession) ReadResource(_ context.Context, _ string) (*vmcp.ResourceReadResult, error) {
	return nil, errors.New("not implemented")
}

func (*v2FakeMultiSession) GetPrompt(
	_ context.Context, _ string, _ map[string]any,
) (*vmcp.PromptGetResult, error) {
	return nil, errors.New("not implemented")
}

func (f *v2FakeMultiSession) Close() error {
	f.closed.Store(true)
	return nil
}

// v2FakeMultiSessionFactory tracks whether MakeSessionWithID was called
// and returns a v2FakeMultiSession with a preconfigured tool.
type v2FakeMultiSessionFactory struct {
	tools              []vmcp.Tool
	makeWithIDCalled   atomic.Bool
	lastCreatedSession *v2FakeMultiSession
	err                error
}

func newV2FakeFactory(tools []vmcp.Tool) *v2FakeMultiSessionFactory {
	return &v2FakeMultiSessionFactory{tools: tools}
}

func (f *v2FakeMultiSessionFactory) MakeSession(
	_ context.Context, _ *auth.Identity, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	if f.err != nil {
		return nil, f.err
	}
	sess := newV2FakeMultiSession(transportsession.NewStreamableSession("auto-id"), f.tools)
	f.lastCreatedSession = sess
	return sess, nil
}

func (f *v2FakeMultiSessionFactory) MakeSessionWithID(
	_ context.Context, id string, _ *auth.Identity, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	f.makeWithIDCalled.Store(true)
	if f.err != nil {
		return nil, f.err
	}
	sess := newV2FakeMultiSession(transportsession.NewStreamableSession(id), f.tools)
	f.lastCreatedSession = sess
	return sess, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// buildV2Server constructs a vMCP server with SessionManagementV2 enabled,
// backed by mock discovery infrastructure, and returns the httptest.Server
// and the session factory so tests can inspect state.
//
// The returned httptest.Server is closed automatically via t.Cleanup.
func buildV2Server(
	t *testing.T,
	factory vmcpsession.MultiSessionFactory,
) *httptest.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	// The discovery middleware calls List() + Discover() for every initialize request.
	// Return an empty (non-nil) AggregatedCapabilities so the middleware does not
	// dereference a nil pointer when logging tool/resource counts.
	emptyAggCaps := &aggregator.AggregatedCapabilities{}
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).Return(emptyAggCaps, nil).AnyTimes()
	// Stop is called when the server is stopped (not via httptest but via session manager cleanup).
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

// postMCP sends a JSON-RPC POST to /mcp and returns the response.
func postMCP(t *testing.T, baseURL string, body map[string]any, sessionID string) *http.Response {
	t.Helper()

	rawBody, err := json.Marshal(body)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/mcp", bytes.NewReader(rawBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

// TestIntegration_SessionManagementV2_Initialize verifies the v2 path end-to-end:
//
//  1. An MCP initialize request triggers handleSessionRegistrationV2.
//  2. The fake factory's MakeSessionWithID is called (session created).
//  3. A subsequent tool call routes through the fake session's CallTool.
func TestIntegration_SessionManagementV2_Initialize(t *testing.T) {
	t.Parallel()

	testTool := vmcp.Tool{Name: "test-tool", Description: "a test tool"}
	factory := newV2FakeFactory([]vmcp.Tool{testTool})

	ts := buildV2Server(t, factory)

	// Step 1: Send initialize request.
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "test",
				"version": "1.0",
			},
		},
	}

	initResp := postMCP(t, ts.URL, initReq, "")
	defer initResp.Body.Close()

	require.Equal(t, http.StatusOK, initResp.StatusCode, "initialize should succeed")

	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "session ID should be returned in Mcp-Session-Id header")

	// Give the OnRegisterSession hook time to run (it may execute asynchronously
	// after the response is sent, but before the next request).
	require.Eventually(t, func() bool {
		return factory.makeWithIDCalled.Load()
	}, 2*time.Second, 10*time.Millisecond,
		"MakeSessionWithID should have been called after initialize")

	// Step 2: Send a tool call and verify it routes through the fake session.
	toolCallReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "test-tool",
			"arguments": map[string]any{},
		},
	}

	toolResp := postMCP(t, ts.URL, toolCallReq, sessionID)
	defer toolResp.Body.Close()

	body, err := io.ReadAll(toolResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, toolResp.StatusCode,
		"tool call should succeed; body: %s", string(body))

	// The fake session's CallTool should have been invoked.
	fakeSess := factory.lastCreatedSession
	require.NotNil(t, fakeSess, "factory should have created a session")
	assert.True(t, fakeSess.callToolCalled.Load(),
		"CallTool on the fake session should have been invoked by the tool call request")
}

// deleteMCP sends a DELETE request to /mcp with the given session ID and
// returns the response. Used to exercise the session termination path.
func deleteMCP(t *testing.T, baseURL, sessionID string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodDelete, baseURL+"/mcp", nil,
	)
	require.NoError(t, err)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// TestIntegration_SessionManagementV2_Termination verifies the termination path:
//
//  1. An initialize request creates a MultiSession.
//  2. A DELETE request calls Terminate(), which calls Close() on the MultiSession,
//     releasing backend connections.
//  3. Subsequent requests with the terminated session ID are rejected.
func TestIntegration_SessionManagementV2_Termination(t *testing.T) {
	t.Parallel()

	testTool := vmcp.Tool{Name: "test-tool", Description: "a test tool"}
	factory := newV2FakeFactory([]vmcp.Tool{testTool})

	ts := buildV2Server(t, factory)

	// Step 1: Initialize and obtain a session ID.
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}
	initResp := postMCP(t, ts.URL, initReq, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)

	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	// Wait for the OnRegisterSession hook to complete so the MultiSession exists.
	require.Eventually(t, func() bool {
		return factory.makeWithIDCalled.Load()
	}, 2*time.Second, 10*time.Millisecond,
		"MakeSessionWithID should have been called after initialize")

	// Step 2: Terminate the session via DELETE.
	delResp := deleteMCP(t, ts.URL, sessionID)
	defer delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode, "DELETE should return 200 OK")

	// Close() must have been called on the fake MultiSession,
	// confirming backend connections are released.
	fakeSess := factory.lastCreatedSession
	require.NotNil(t, fakeSess, "factory should have created a session")
	assert.True(t, fakeSess.closed.Load(),
		"Close() should have been called on the MultiSession after termination")

	// Subsequent requests with the terminated session ID are rejected.
	// After Terminate() deletes the session from storage, the discovery middleware's
	// handleSubsequentRequest finds no session and returns HTTP 401 before the SDK
	// even calls Validate(). The 401 is consistent with the existing behaviour for
	// all expired/unknown sessions in the discovery middleware.
	toolCallReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "test-tool",
			"arguments": map[string]any{},
		},
	}
	postResp := postMCP(t, ts.URL, toolCallReq, sessionID)
	defer postResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, postResp.StatusCode,
		"request with terminated session ID should be rejected")
}

// TestIntegration_SessionManagementV2_OldPathUnused verifies that when
// SessionManagementV2 is false, the fake factory is NOT called.
func TestIntegration_SessionManagementV2_OldPathUnused(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	testTool := vmcp.Tool{Name: "test-tool", Description: "a test tool"}
	factory := newV2FakeFactory([]vmcp.Tool{testTool})

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	emptyAggCaps := &aggregator.AggregatedCapabilities{}
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return(nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).Return(emptyAggCaps, nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	rt := router.NewDefaultRouter()

	// SessionManagementV2 is false — factory should NOT be called.
	srv, err := server.New(
		context.Background(),
		&server.Config{
			Host:                "127.0.0.1",
			Port:                0,
			SessionManagementV2: false,
			SessionFactory:      factory, // provided but should be ignored
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

	// Send initialize — this exercises the old (Phase 1) path.
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "test",
				"version": "1.0",
			},
		},
	}

	initResp := postMCP(t, ts.URL, initReq, "")
	defer initResp.Body.Close()

	require.Equal(t, http.StatusOK, initResp.StatusCode, "initialize should succeed on old path")
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "session ID should still be returned")

	// Assert the factory is never called over a window, rather than sleeping
	// a fixed duration — this avoids false-positives on slow CI hosts.
	assert.Never(t,
		func() bool { return factory.makeWithIDCalled.Load() },
		200*time.Millisecond,
		10*time.Millisecond,
		"MakeSessionWithID should NOT be called when SessionManagementV2 is false",
	)
}
