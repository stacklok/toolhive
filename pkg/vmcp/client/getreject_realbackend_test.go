// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// newGETRejectingEchoServer stands up the same real go-sdk streamable-HTTP
// backend as newRealEchoServer, stateful (Legacy), behind a handler that
// answers every HTTP GET with 400 and records the method of every request it
// receives.
//
// That is the Tableau MCP shape from issue #6497: a healthy backend that
// requires a session id issued by initialize, so a bare GET — which carries no
// session context — is rejected rather than upgraded to a standalone SSE
// stream.
func newGETRejectingEchoServer(t *testing.T, record func(method string)) *httptest.Server {
	t.Helper()

	mcpSrv := mcpserver.NewMCPServer("get-rejecting-backend", "1.0.0")
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

	inner := mcpserver.NewStreamableHTTPServer(mcpSrv)
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record(r.Method)
		if r.Method == http.MethodGet {
			// Tableau MCP's response to a session-less GET.
			http.Error(w, "Invalid or missing session ID", http.StatusBadRequest)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// TestListCapabilities_GETRejectingBackendIsHealthy pins the health signal
// reported in issue #6497: a backend that rejects a bare HTTP GET must not be
// treated as unavailable.
//
// vMCP's health check is BackendClient.ListCapabilities (see
// health.NewHealthChecker), which reaches the backend over POST. GET is only
// ever the standalone server->client SSE stream, and in Streamable HTTP that
// stream is optional — a server without one answers 405 per spec, and one that
// requires a session id (Tableau MCP) answers 400. Neither says anything about
// whether the backend can serve MCP.
//
// The assertion is therefore two-sided: ListCapabilities must succeed against
// such a backend, AND it must not issue a GET at all. Only the tools/call
// forwarding path enables transport.WithContinuousListening (see
// newStreamableHTTPClient); a change that opened the standalone stream on the
// non-forwarding path would put every session-requiring backend back to
// "unavailable" and drop its tools from tools/list.
func TestListCapabilities_GETRejectingBackendIsHealthy(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var methods []string
	srv := newGETRejectingEchoServer(t, func(method string) {
		mu.Lock()
		defer mu.Unlock()
		methods = append(methods, method)
	})

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{
		WorkloadID:    "get-rejecting-backend",
		WorkloadName:  "GET Rejecting Backend",
		BaseURL:       srv.URL + "/mcp",
		TransportType: "streamable-http",
	}

	caps, err := h.ListCapabilities(context.Background(), target)
	require.NoError(t, err,
		"a backend that rejects a bare GET is still a healthy MCP backend: the health check speaks MCP over POST")
	require.NotNil(t, caps)
	assert.Len(t, caps.Tools, 1)
	assert.Equal(t, "echo", caps.Tools[0].Name)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, methods, "the backend must have been reached")
	assert.NotContains(t, methods, http.MethodGet,
		"the health path must not open a standalone SSE GET stream; only the tools/call forwarding path may")
}
