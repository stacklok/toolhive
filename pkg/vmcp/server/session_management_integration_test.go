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

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpsdk "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// ---------------------------------------------------------------------------
// Fakes (redeclared for package server_test; cannot import internal fakes
// from the internal server package tests since those are in package server).
// ---------------------------------------------------------------------------

// fakeMultiSession is a minimal MultiSession implementation that records
// whether CallTool was invoked and returns a preconfigured result.
type fakeMultiSession struct {
	transportsession.Session // embedded to satisfy ID/Type/timestamp/metadata methods

	tools          []vmcp.Tool
	callToolCalled atomic.Bool
	callToolResult *vmcp.ToolCallResult
	callToolErr    error
	closed         atomic.Bool
}

func newFakeMultiSession(sess transportsession.Session, tools []vmcp.Tool) *fakeMultiSession {
	return &fakeMultiSession{
		Session: sess,
		tools:   tools,
		callToolResult: &vmcp.ToolCallResult{
			Content: []vmcp.Content{{Type: "text", Text: "fake result"}},
		},
	}
}

func (f *fakeMultiSession) Tools() []vmcp.Tool {
	result := make([]vmcp.Tool, len(f.tools))
	copy(result, f.tools)
	return result
}

func (*fakeMultiSession) Resources() []vmcp.Resource { return nil }
func (*fakeMultiSession) Prompts() []vmcp.Prompt     { return nil }
func (*fakeMultiSession) BackendSessions() map[string]string {
	return nil
}
func (*fakeMultiSession) GetRoutingTable() *vmcp.RoutingTable { return nil }

func (f *fakeMultiSession) CallTool(
	_ context.Context, _ *auth.Identity, _ string, _ map[string]any, _ map[string]any,
) (*vmcp.ToolCallResult, error) {
	f.callToolCalled.Store(true)
	return f.callToolResult, f.callToolErr
}

func (*fakeMultiSession) ReadResource(_ context.Context, _ *auth.Identity, _ string) (*vmcp.ResourceReadResult, error) {
	return nil, errors.New("not implemented")
}

func (*fakeMultiSession) GetPrompt(
	_ context.Context, _ *auth.Identity, _ string, _ map[string]any,
) (*vmcp.PromptGetResult, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeMultiSession) Close() error {
	f.closed.Store(true)
	return nil
}

// fakeMultiSessionFactory tracks whether MakeSessionWithID was called
// and returns a fakeMultiSession with a preconfigured tool.
type fakeMultiSessionFactory struct {
	tools              []vmcp.Tool
	makeWithIDCalled   atomic.Bool
	lastCreatedSession *fakeMultiSession
	err                error
}

func newFakeFactory(tools []vmcp.Tool) *fakeMultiSessionFactory {
	return &fakeMultiSessionFactory{tools: tools}
}

func (f *fakeMultiSessionFactory) MakeSessionWithID(
	_ context.Context, id string, identity *auth.Identity, allowAnonymous bool, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	f.makeWithIDCalled.Store(true)
	if f.err != nil {
		return nil, f.err
	}
	baseSession := transportsession.NewStreamableSession(id)

	// Set basic metadata to indicate whether this is an anonymous session.
	// Integration tests don't need to verify crypto implementation details.
	if identity != nil && identity.Token != "" && !allowAnonymous {
		// Authenticated session - set non-empty hash placeholder
		baseSession.SetMetadata(vmcpsession.MetadataKeyTokenHash, "fake-hash-for-testing")
	} else {
		// Anonymous session - set empty hash
		baseSession.SetMetadata(vmcpsession.MetadataKeyTokenHash, "")
	}

	sess := newFakeMultiSession(baseSession, f.tools)
	f.lastCreatedSession = sess
	return sess, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// serverOptions holds optional configuration extensions for buildTestServerWithOptions.
type serverOptions struct {
	workflowDefs     map[string]*composer.WorkflowDefinition
	optimizerFactory func(context.Context, []mcpsdk.ServerTool) (optimizer.Optimizer, error)
}

// buildTestServer constructs a vMCP server with session management enabled,
// backed by mock discovery infrastructure, and returns the httptest.Server
// and the session factory so tests can inspect state.
//
// The returned httptest.Server is closed automatically via t.Cleanup.
func buildTestServer(
	t *testing.T,
	factory vmcpsession.MultiSessionFactory,
) *httptest.Server {
	t.Helper()
	return buildTestServerWithOptions(t, factory, serverOptions{})
}

// buildTestServerWithOptions is like buildTestServer but accepts optional workflow
// definitions and an optimizer factory, enabling composite tool and optimizer
// integration tests.
func buildTestServerWithOptions(
	t *testing.T,
	factory vmcpsession.MultiSessionFactory,
	opts serverOptions,
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
			Host:             "127.0.0.1",
			Port:             0,
			SessionTTL:       5 * time.Minute,
			SessionFactory:   factory,
			OptimizerFactory: opts.optimizerFactory,
		},
		rt,
		mockBackendClient,
		mockDiscoveryMgr,
		mockBackendRegistry,
		opts.workflowDefs,
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

// TestIntegration_SessionManagement_Initialize verifies the session management path end-to-end:
//
//  1. An MCP initialize request triggers handleSessionRegistration.
//  2. The fake factory's MakeSessionWithID is called (session created).
//  3. A subsequent tool call routes through the fake session's CallTool.
func TestIntegration_SessionManagement_Initialize(t *testing.T) {
	t.Parallel()

	testTool := vmcp.Tool{Name: "test-tool", Description: "a test tool"}
	factory := newFakeFactory([]vmcp.Tool{testTool})

	ts := buildTestServer(t, factory)

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

// TestIntegration_SessionManagement_Termination verifies the termination path:
//
//  1. An initialize request creates a MultiSession.
//  2. A DELETE request calls Terminate(), which calls Close() on the MultiSession,
//     releasing backend connections.
//  3. Subsequent requests with the terminated session ID are rejected.
func TestIntegration_SessionManagement_Termination(t *testing.T) {
	t.Parallel()

	testTool := vmcp.Tool{Name: "test-tool", Description: "a test tool"}
	factory := newFakeFactory([]vmcp.Tool{testTool})

	ts := buildTestServer(t, factory)

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

// TestIntegration_SessionManagement_TokenBinding verifies end-to-end token binding security:
//
//  1. Initialize a session with bearer token "token-A"
//  2. Make a tool call with the same token → succeeds
//  3. Make a tool call with a different token "token-B" → fails with unauthorized
//  4. Verify the session is terminated after auth failure
//
// NOTE: This test is currently skipped because the fake factory (fakeMultiSessionFactory)
// doesn't implement real token binding - it uses placeholder metadata instead of real
// HMAC-SHA256 hashes. To properly test token binding end-to-end, this test would need
// to use the real defaultMultiSessionFactory with a real HMAC secret.
//
// Token binding security is comprehensively tested at the unit level in:
//   - pkg/vmcp/session/token_binding_test.go (factory behavior)
//   - pkg/vmcp/session/internal/security/*_test.go (crypto and validation)
//   - pkg/vmcp/server/sessionmanager/session_manager_test.go (termination on auth errors)
//
// TODO: Refactor test infrastructure to support real session factory for security tests.
func TestIntegration_SessionManagement_TokenBinding(t *testing.T) {
	t.Skip("Fake factory doesn't implement real token binding - see test comment for details")
	t.Parallel()

	testTool := vmcp.Tool{Name: "echo", Description: "echoes input"}
	factory := newFakeFactory([]vmcp.Tool{testTool})
	ts := buildTestServer(t, factory)

	tokenA := "bearer-token-A"
	tokenB := "bearer-token-B"

	// Step 1: Initialize with token A
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "test-client",
				"version": "1.0",
			},
		},
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp", nil)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenA) // Set token A

	reqBody, err := json.Marshal(initReq)
	require.NoError(t, err)
	req.Body = io.NopCloser(bytes.NewReader(reqBody))

	initResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer initResp.Body.Close()

	require.Equal(t, http.StatusOK, initResp.StatusCode)
	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID, "should receive session ID")

	// Wait for factory to be called
	require.Eventually(t,
		func() bool { return factory.makeWithIDCalled.Load() },
		1*time.Second,
		10*time.Millisecond,
		"factory should be called to create session",
	)

	// Step 2: Call tool with token A (same as initialization) → should succeed
	toolReqA := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"msg": "hello"},
		},
	}

	reqA, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp", nil)
	require.NoError(t, err)
	reqA.Header.Set("Content-Type", "application/json")
	reqA.Header.Set("Mcp-Session-Id", sessionID)
	reqA.Header.Set("Authorization", "Bearer "+tokenA) // Same token

	reqBodyA, err := json.Marshal(toolReqA)
	require.NoError(t, err)
	reqA.Body = io.NopCloser(bytes.NewReader(reqBodyA))

	respA, err := http.DefaultClient.Do(reqA)
	require.NoError(t, err)
	defer respA.Body.Close()

	assert.Equal(t, http.StatusOK, respA.StatusCode, "tool call with matching token should succeed")

	// Step 3: Call tool with token B (different from initialization) → should fail
	toolReqB := map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"msg": "hijack attempt"},
		},
	}

	reqB, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp", nil)
	require.NoError(t, err)
	reqB.Header.Set("Content-Type", "application/json")
	reqB.Header.Set("Mcp-Session-Id", sessionID)
	reqB.Header.Set("Authorization", "Bearer "+tokenB) // Different token!

	reqBodyB, err := json.Marshal(toolReqB)
	require.NoError(t, err)
	reqB.Body = io.NopCloser(bytes.NewReader(reqBodyB))

	respB, err := http.DefaultClient.Do(reqB)
	require.NoError(t, err)
	defer respB.Body.Close()

	// The request should succeed at HTTP level but return an error result
	require.Equal(t, http.StatusOK, respB.StatusCode, "HTTP request should succeed")

	var result map[string]any
	err = json.NewDecoder(respB.Body).Decode(&result)
	require.NoError(t, err)

	// Should contain an error about unauthorized
	resultMap, ok := result["result"].(map[string]any)
	require.True(t, ok, "result should be an object")

	isError, ok := resultMap["isError"].(bool)
	require.True(t, ok && isError, "result should indicate error")

	// Step 4: Verify session is terminated (subsequent requests should fail)
	toolReqC := map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"msg": "after termination"},
		},
	}

	reqC, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp", nil)
	require.NoError(t, err)
	reqC.Header.Set("Content-Type", "application/json")
	reqC.Header.Set("Mcp-Session-Id", sessionID)
	reqC.Header.Set("Authorization", "Bearer "+tokenA) // Even with original token

	reqBodyC, err := json.Marshal(toolReqC)
	require.NoError(t, err)
	reqC.Body = io.NopCloser(bytes.NewReader(reqBodyC))

	respC, err := http.DefaultClient.Do(reqC)
	require.NoError(t, err)
	defer respC.Body.Close()

	// Session should be terminated, so this should fail
	assert.Equal(t, http.StatusInternalServerError, respC.StatusCode,
		"request should fail after session termination due to auth failure")
}

// ---------------------------------------------------------------------------
// Helpers for composite tool and optimizer mode tests
// ---------------------------------------------------------------------------

// listToolNames sends a tools/list request and returns the tool names from the
// response. Returns nil when the request fails or the response cannot be parsed.
func listToolNames(t *testing.T, baseURL, sessionID string) []string {
	t.Helper()

	resp := postMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "tools/list",
		"params":  map[string]any{},
	}, sessionID)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var body struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}

	names := make([]string, 0, len(body.Result.Tools))
	for _, tool := range body.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

// fakeOptimizer is a minimal optimizer.Optimizer for testing optimizer mode.
// It returns empty results and does not require an embedding store.
type fakeOptimizer struct{}

func (*fakeOptimizer) FindTool(_ context.Context, _ optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
	return &optimizer.FindToolOutput{}, nil
}

func (*fakeOptimizer) CallTool(_ context.Context, _ optimizer.CallToolInput) (*mcpmcp.CallToolResult, error) {
	return &mcpmcp.CallToolResult{}, nil
}

// ---------------------------------------------------------------------------
// Composite tool and optimizer integration tests
// ---------------------------------------------------------------------------

// TestIntegration_SessionManagement_CompositeTools verifies that composite tools
// (workflow definitions) appear in tools/list alongside backend tools when
// session management is enabled.
func TestIntegration_SessionManagement_CompositeTools(t *testing.T) {
	t.Parallel()

	backendTool := vmcp.Tool{Name: "backend-tool", Description: "a backend tool"}
	factory := newFakeFactory([]vmcp.Tool{backendTool})

	workflowDef := &composer.WorkflowDefinition{
		Name:        "composite-tool",
		Description: "a composite workflow tool",
		Steps: []composer.WorkflowStep{
			{
				ID:   "step1",
				Type: composer.StepTypeTool,
				Tool: "backend-tool",
			},
		},
	}

	ts := buildTestServerWithOptions(t, factory, serverOptions{
		workflowDefs: map[string]*composer.WorkflowDefinition{
			"composite-tool": workflowDef,
		},
	})

	initResp := postMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)

	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	// Poll tools/list until the composite tool appears — confirms registration
	// and tool injection have both completed.
	require.Eventually(t, func() bool {
		for _, n := range listToolNames(t, ts.URL, sessionID) {
			if n == "composite-tool" {
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond,
		"composite-tool should appear in tools/list after session registration")

	toolNames := listToolNames(t, ts.URL, sessionID)
	assert.Contains(t, toolNames, "backend-tool", "backend tool should be in tools/list")
	assert.Contains(t, toolNames, "composite-tool", "composite tool should be in tools/list")
}

// TestIntegration_SessionManagement_CompositeToolConflict verifies that when a
// composite tool name collides with a backend tool name, the composite tool is
// silently skipped and the backend tool remains registered and callable.
func TestIntegration_SessionManagement_CompositeToolConflict(t *testing.T) {
	t.Parallel()

	// Both the backend and the workflow definition use the same name — a collision.
	const sharedName = "shared-tool"
	factory := newFakeFactory([]vmcp.Tool{{Name: sharedName, Description: "backend version"}})

	workflowDef := &composer.WorkflowDefinition{
		Name:        sharedName, // conflicts with the backend tool
		Description: "composite version — should be skipped due to name conflict",
		Steps: []composer.WorkflowStep{
			{
				ID:   "step1",
				Type: composer.StepTypeTool,
				Tool: "other-tool",
			},
		},
	}

	ts := buildTestServerWithOptions(t, factory, serverOptions{
		workflowDefs: map[string]*composer.WorkflowDefinition{
			sharedName: workflowDef,
		},
	})

	initResp := postMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)

	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	// Wait for the backend tool to appear in tools/list (confirms injection completed).
	require.Eventually(t, func() bool {
		for _, n := range listToolNames(t, ts.URL, sessionID) {
			if n == sharedName {
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond,
		"backend tool should appear in tools/list")

	toolNames := listToolNames(t, ts.URL, sessionID)
	assert.Contains(t, toolNames, sharedName,
		"backend tool should still be registered despite the name conflict")

	// Exactly one tool should have the shared name — the composite was skipped.
	count := 0
	for _, n := range toolNames {
		if n == sharedName {
			count++
		}
	}
	assert.Equal(t, 1, count,
		"only the backend tool should be registered; the conflicting composite tool must be skipped")

	// Backend tool must remain callable after conflict detection.
	toolResp := postMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      sharedName,
			"arguments": map[string]any{},
		},
	}, sessionID)
	defer toolResp.Body.Close()
	respBody, err := io.ReadAll(toolResp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, toolResp.StatusCode,
		"backend tool call should succeed after conflict detection; body: %s", string(respBody))
}

// TestIntegration_SessionManagement_OptimizerMode verifies that when an optimizer
// factory is configured with session management, tools/list exposes only
// find_tool and call_tool (the optimizer wraps all backend tools).
func TestIntegration_SessionManagement_OptimizerMode(t *testing.T) {
	t.Parallel()

	testTool := vmcp.Tool{Name: "test-tool", Description: "a test tool"}
	factory := newFakeFactory([]vmcp.Tool{testTool})

	ts := buildTestServerWithOptions(t, factory, serverOptions{
		optimizerFactory: func(_ context.Context, _ []mcpsdk.ServerTool) (optimizer.Optimizer, error) {
			return &fakeOptimizer{}, nil
		},
	})

	initResp := postMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	defer initResp.Body.Close()
	require.Equal(t, http.StatusOK, initResp.StatusCode)

	sessionID := initResp.Header.Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)

	// Poll until find_tool appears, confirming optimizer tools were injected.
	require.Eventually(t, func() bool {
		for _, n := range listToolNames(t, ts.URL, sessionID) {
			if n == "find_tool" {
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond,
		"find_tool should appear in tools/list when optimizer is configured")

	toolNames := listToolNames(t, ts.URL, sessionID)
	assert.Contains(t, toolNames, "find_tool", "find_tool must be exposed in optimizer mode")
	assert.Contains(t, toolNames, "call_tool", "call_tool must be exposed in optimizer mode")
	// The raw backend tool must not be directly visible — the optimizer wraps it.
	assert.NotContains(t, toolNames, "test-tool",
		"backend tools must not be directly exposed in optimizer mode")
	assert.Len(t, toolNames, 2,
		"only find_tool and call_tool should be exposed in optimizer mode")
}
