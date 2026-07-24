// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
)

// startRealMCPBackend creates a real in-process MCP server over streamable-HTTP
// for integration testing. The server exposes a single "echo" tool that returns
// its input verbatim.
//
// This test utility is useful for integration tests that need a real backend
// MCP server instead of mocks, enabling end-to-end testing of the vMCP server's
// session management, routing, and protocol handling.
//
// Returns the full URL to the backend's /mcp endpoint.
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
	cleanupBackendServer(t, ts)
	return ts.URL + "/mcp"
}

// cleanupBackendServer tears down an in-process backend httptest.Server used as
// a vMCP backend. It force-closes active client connections BEFORE Close.
//
// vMCP's persistent session connector opens a standalone SSE GET stream and
// holds it open for the whole session (continuous listening, enabled whenever a
// list_changed sink is wired — which the real-backend test harness now does).
// That stream is a StateActive inbound request on this backend server, and
// httptest.Server.Close blocks until outstanding requests complete — nothing
// else closes that stream during a test, so a plain Close would hang until the
// package test timeout. CloseClientConnections force-drops the active
// connections first (the connector does not reconnect, matching the Phase-1
// no-reconnect behavior), so the subsequent Close returns promptly.
func cleanupBackendServer(t *testing.T, ts *httptest.Server) {
	t.Helper()
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
}

// MCPTestClient provides higher-level test utilities for MCP protocol interactions.
// This reduces boilerplate and improves test readability by providing semantic methods
// instead of manual JSON-RPC construction.
type MCPTestClient struct {
	baseURL   string
	sessionID string
	t         *testing.T
	nextID    int
}

// NewMCPTestClient creates a new test client for the given server URL.
func NewMCPTestClient(t *testing.T, baseURL string) *MCPTestClient {
	t.Helper()
	return &MCPTestClient{
		baseURL: baseURL,
		t:       t,
		nextID:  1,
	}
}

// InitializeSession sends an initialize request and returns the session ID.
func (c *MCPTestClient) InitializeSession() string {
	c.t.Helper()

	resp := c.postMCP(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1.0"},
		},
	}, "")
	c.nextID++
	defer resp.Body.Close()

	c.sessionID = resp.Header.Get("Mcp-Session-Id")
	if c.sessionID == "" {
		c.t.Fatal("initialize response missing Mcp-Session-Id header")
	}
	return c.sessionID
}

// ListTools calls tools/list and returns the raw response for parsing.
func (c *MCPTestClient) ListTools() *http.Response {
	c.t.Helper()

	resp := c.postMCP(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  "tools/list",
		"params":  map[string]any{},
	}, c.sessionID)
	c.nextID++
	return resp
}

// CallTool calls tools/call with the given tool name and arguments.
func (c *MCPTestClient) CallTool(toolName string, args map[string]any) *http.Response {
	c.t.Helper()

	resp := c.postMCP(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": args,
		},
	}, c.sessionID)
	c.nextID++
	return resp
}

// ReadResource calls resources/read for the given URI and returns the raw response.
func (c *MCPTestClient) ReadResource(uri string) *http.Response {
	c.t.Helper()

	resp := c.postMCP(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  "resources/read",
		"params":  map[string]any{"uri": uri},
	}, c.sessionID)
	c.nextID++
	return resp
}

// GetPrompt calls prompts/get for the given prompt name and returns the raw response.
func (c *MCPTestClient) GetPrompt(name string) *http.Response {
	c.t.Helper()

	resp := c.postMCP(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  "prompts/get",
		"params":  map[string]any{"name": name},
	}, c.sessionID)
	c.nextID++
	return resp
}

// SessionID returns the current session ID (available after InitializeSession).
func (c *MCPTestClient) SessionID() string {
	return c.sessionID
}

// Terminate sends a DELETE request to terminate the session.
func (c *MCPTestClient) Terminate() *http.Response {
	c.t.Helper()

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodDelete, c.baseURL+"/mcp", nil,
	)
	if err != nil {
		c.t.Fatalf("failed to create DELETE request: %v", err)
	}

	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("DELETE request failed: %v", err)
	}
	return resp
}

// postMCP is the low-level helper for sending JSON-RPC requests.
// It's kept private - tests should use the semantic methods above.
func (c *MCPTestClient) postMCP(body map[string]any, sessionID string) *http.Response {
	c.t.Helper()

	rawBody, err := json.Marshal(body)
	if err != nil {
		c.t.Fatalf("failed to marshal request: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.baseURL+"/mcp", bytes.NewReader(rawBody))
	if err != nil {
		c.t.Fatalf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("request failed: %v", err)
	}
	return resp
}
