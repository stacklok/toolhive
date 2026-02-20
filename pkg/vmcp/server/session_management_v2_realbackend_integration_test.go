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

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// startRealMCPBackend creates a real in-process MCP server over streamable-HTTP
// and returns its endpoint URL. The server is shut down via t.Cleanup.
//
// The server exposes a single "echo" tool that returns the "input" argument
// as a text content item.
func startRealMCPBackend(t *testing.T) string {
	t.Helper()

	mcpSrv := mcpserver.NewMCPServer("real-backend", "1.0.0")
	mcpSrv.AddTool(
		mcpmcp.NewTool("echo",
			mcpmcp.WithDescription("Echoes the input back"),
			mcpmcp.WithString("input", mcpmcp.Required()),
		),
		func(_ context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			input, _ := args["input"].(string)
			return &mcpmcp.CallToolResult{
				Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)},
			}, nil
		},
	)

	streamableSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamableSrv)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// newRealV2Server builds a vMCP server with SessionManagementV2 enabled and a
// real SessionFactory. The BackendRegistry mock returns the backend at backendURL
// so that CreateSession() opens a real HTTP connection to the MCP server.
func newRealV2Server(t *testing.T, backendURL string) *httptest.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockBackendClient := mocks.NewMockBackendClient(ctrl)
	mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
	mockBackendRegistry := mocks.NewMockBackendRegistry(ctrl)

	backend := vmcp.Backend{
		ID:            "real-backend",
		Name:          "real-backend",
		BaseURL:       backendURL,
		TransportType: "streamable-http",
	}

	// BackendRegistry.List() is called by CreateSession() to build the backend list.
	// Discover() is not called in the V2 path (WithSessionScopedRouting skips discovery).
	mockBackendRegistry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{backend}).AnyTimes()
	mockDiscoveryMgr.EXPECT().Discover(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).AnyTimes()
	mockDiscoveryMgr.EXPECT().Stop().AnyTimes()

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy(),
	))
	factory := vmcpsession.NewSessionFactory(authReg)

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

// waitForEchoTool polls tools/list until the "echo" tool appears or the
// deadline elapses. It relies on require.Eventually so the test fails
// immediately on timeout.
func waitForEchoTool(t *testing.T, baseURL, sessionID string) {
	t.Helper()
	listReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      99,
		"method":  "tools/list",
		"params":  map[string]any{},
	}
	require.Eventually(t, func() bool {
		resp := postMCP(t, baseURL, listReq, sessionID)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte(`"echo"`))
	}, 5*time.Second, 50*time.Millisecond,
		"tools/list should expose the 'echo' tool after session creation")
}

// ---------------------------------------------------------------------------
// Integration tests — real MCP backend, real SessionFactory
// ---------------------------------------------------------------------------

// TestIntegration_V2RealBackend_ToolDiscovery verifies that when a client
// initializes a V2 session, the vMCP server connects to the real backend and
// registers its tools. A subsequent tools/list request must return the "echo"
// tool discovered from the backend.
func TestIntegration_V2RealBackend_ToolDiscovery(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealV2Server(t, backendURL)

	// Initialize and obtain a session ID.
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

	// Wait for the OnRegisterSession hook to complete and the echo tool to appear.
	waitForEchoTool(t, ts.URL, sessionID)

	// Fetch tools/list one final time and parse the full response.
	resp := postMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}, sessionID)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var rpc struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(body, &rpc), "body: %s", string(body))

	require.Len(t, rpc.Result.Tools, 1, "expected exactly the 'echo' tool from the real backend")
	assert.Equal(t, "echo", rpc.Result.Tools[0].Name)
	assert.Equal(t, "Echoes the input back", rpc.Result.Tools[0].Description)
}

// TestIntegration_V2RealBackend_ToolCall verifies the full tool-call path:
// a tools/call request travels through the vMCP session manager to the real
// backend MCP server and the result is returned to the client.
func TestIntegration_V2RealBackend_ToolCall(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealV2Server(t, backendURL)

	// Initialize.
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

	// Wait for the session to be fully established before sending a tool call.
	waitForEchoTool(t, ts.URL, sessionID)

	// Call the echo tool and verify the result from the real backend.
	toolResp := postMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "hello from V2"},
		},
	}, sessionID)
	defer toolResp.Body.Close()

	body, err := io.ReadAll(toolResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, toolResp.StatusCode, "body: %s", string(body))

	var rpc struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(body, &rpc), "body: %s", string(body))
	require.Len(t, rpc.Result.Content, 1)
	assert.False(t, rpc.Result.IsError)
	assert.Equal(t, "text", rpc.Result.Content[0].Type)
	assert.Equal(t, "hello from V2", rpc.Result.Content[0].Text)
}

// TestIntegration_V2RealBackend_Termination verifies the session termination path
// against a real backend: a DELETE request closes the backend connection, and
// subsequent requests with the terminated session ID are rejected.
func TestIntegration_V2RealBackend_Termination(t *testing.T) {
	t.Parallel()

	backendURL := startRealMCPBackend(t)
	ts := newRealV2Server(t, backendURL)

	// Initialize.
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

	// Wait for session creation to complete before terminating.
	waitForEchoTool(t, ts.URL, sessionID)

	// Terminate the session.
	delResp := deleteMCP(t, ts.URL, sessionID)
	defer delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode, "DELETE should return 200 OK")

	// Subsequent requests with the terminated session ID are rejected.
	// After Terminate() deletes the session from storage, the discovery middleware
	// returns HTTP 401 ("session not found") before the SDK's Validate() is invoked.
	postResp := postMCP(t, ts.URL, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "echo",
			"arguments": map[string]any{"input": "should fail"},
		},
	}, sessionID)
	defer postResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, postResp.StatusCode,
		"request with terminated session ID should be rejected")
}
