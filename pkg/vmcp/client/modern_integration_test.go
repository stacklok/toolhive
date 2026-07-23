// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// startEchoBackend stands up a real in-process MCP server over streamable-HTTP
// exposing a single "echo" tool, returning the URL of its /mcp endpoint. Mirrors
// the server package's startRealMCPBackend test utility (not importable across
// the package boundary).
func startEchoBackend(t *testing.T) string {
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
			return &mcpmcp.CallToolResult{Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)}}, nil
		},
	)

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewStreamableHTTPServer(mcpSrv))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// newModernVMCPServer stands up a real vMCP server with Modern (2026-07-28)
// dispatch enabled, aggregating the echo backend at backendURL. A well-formed
// Modern request routes through classifyingHandler -> dispatchModern (the Phase-2
// server side), so this is the real target modernCall must satisfy end-to-end.
func newModernVMCPServer(t *testing.T, backendURL string) *httptest.Server {
	t.Helper()

	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	backend := vmcp.Backend{
		ID:            "real-backend",
		Name:          "real-backend",
		BaseURL:       backendURL,
		TransportType: "streamable-http",
	}
	mockRegistry := mocks.NewMockBackendRegistry(ctrl)
	mockRegistry.EXPECT().List(gomock.Any()).Return([]vmcp.Backend{backend}).AnyTimes()
	mockRegistry.EXPECT().Get(gomock.Any(), gomock.Any()).Return(&backend).AnyTimes()

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated, strategies.NewUnauthenticatedStrategy(),
	))

	backendClient, err := NewHTTPBackendClient(authReg)
	require.NoError(t, err)
	resolver, err := aggregator.NewPriorityConflictResolver([]string{backend.Name})
	require.NoError(t, err)
	agg := aggregator.NewDefaultAggregator(backendClient, resolver, nil, nil)

	srv, err := server.New(
		context.Background(),
		&server.Config{
			Host:           "127.0.0.1",
			Port:           0,
			SessionTTL:     5 * time.Minute,
			SessionFactory: vmcpsession.NewSessionFactory(authReg),
			Aggregator:     agg,
			// main re-added this kill-switch (default off, #5959); the harness
			// must opt in so a well-formed Modern request reaches dispatchModern
			// rather than falling through to the Legacy SDK path.
			ModernDispatchEnabled: true,
		},
		router.NewSessionRouter(&vmcp.RoutingTable{}),
		backendClient,
		mockRegistry,
		nil,
	)
	require.NoError(t, err)

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// TestIntegration_ModernCall_Discover proves the shim end-to-end: modernCall's
// hand-rolled Modern request satisfies the real Phase-2 dispatchModern server
// (the headers and _meta it validates), and the server's discover envelope
// decodes back through the shim with capability flags reflecting the echo
// backend's actual tool set.
func TestIntegration_ModernCall_Discover(t *testing.T) {
	t.Parallel()

	backendURL := startEchoBackend(t)
	vmcpSrv := newModernVMCPServer(t, backendURL)

	// Capture the exact headers the shim sent by recording them in the client's
	// transport, then delegating to the server's real client transport.
	var gotHeaders http.Header
	hc := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gotHeaders = req.Header.Clone()
		return http.DefaultTransport.RoundTrip(req)
	})}

	var out struct {
		ResultType   string `json:"resultType"`
		Capabilities struct {
			Tools       json.RawMessage `json:"tools"`
			Completions json.RawMessage `json:"completions"`
			Resources   json.RawMessage `json:"resources"`
			Prompts     json.RawMessage `json:"prompts"`
		} `json:"capabilities"`
		SupportedVersions []string `json:"supportedVersions"`
	}
	err := modernCall(context.Background(), hc, vmcpSrv.URL+"/mcp", "server/discover", nil, "", &out)
	require.NoError(t, err)

	// Request shaping reached the real server intact.
	assert.Equal(t, "server/discover", gotHeaders.Get("Mcp-Method"))
	assert.Equal(t, "2026-07-28", gotHeaders.Get("MCP-Protocol-Version"))
	assert.Contains(t, gotHeaders.Get("Accept"), "text/event-stream")
	assert.Empty(t, gotHeaders.Get("Mcp-Session-Id"), "Modern responses/requests carry no session id")

	// Decoded discover envelope reflects the echo backend: a tool present,
	// completions unconditional, resources/prompts absent.
	assert.Equal(t, "complete", out.ResultType)
	assert.Contains(t, out.SupportedVersions, "2026-07-28")
	assert.NotEmpty(t, out.Capabilities.Tools, "echo backend has a tool")
	assert.NotEmpty(t, out.Capabilities.Completions, "completions is advertised unconditionally")
	assert.Empty(t, out.Capabilities.Resources, "echo backend exposes no resources")
	assert.Empty(t, out.Capabilities.Prompts, "echo backend exposes no prompts")
}

// TestIntegration_ModernListCapabilities proves the full Modern enumeration path
// end-to-end: ListCapabilities probes the Phase-2 dispatchModern server as
// Modern, then enumerates its real capabilities (tools/list) via the shim — no
// initialize handshake, no Mcp-Session-Id — and returns the echo backend's tool.
func TestIntegration_ModernListCapabilities(t *testing.T) {
	t.Parallel()

	backendURL := startEchoBackend(t)
	vmcpSrv := newModernVMCPServer(t, backendURL)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{
		WorkloadID:    "vmcp-modern",
		WorkloadName:  "vMCP Modern",
		BaseURL:       vmcpSrv.URL + "/mcp",
		TransportType: "streamable-http",
	}

	caps, err := h.ListCapabilities(context.Background(), target)
	require.NoError(t, err)

	// Classified Modern (not Legacy initialize) and the echo tool enumerated.
	rev, ok := h.cachedRevision(target.WorkloadID)
	require.True(t, ok)
	assert.Equal(t, mcpparser.RevisionModern, rev)

	require.Len(t, caps.Tools, 1, "the echo backend's tool must enumerate through Modern")
	assert.Equal(t, "echo", caps.Tools[0].Name)
	assert.Equal(t, target.WorkloadID, caps.Tools[0].BackendID)
	assert.Empty(t, caps.Resources, "echo backend exposes no resources")
	assert.Empty(t, caps.Prompts, "echo backend exposes no prompts")
}
