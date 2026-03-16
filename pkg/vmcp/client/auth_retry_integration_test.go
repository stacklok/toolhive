// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
)

// TestAuthRetry_Transient401_ListCapabilities verifies the end-to-end retry path when a
// backend MCP server returns HTTP 401 on the first request it receives.
//
// NewHTTPBackendClient wraps httpBackendClient with retryingBackendClient.
// ListCapabilities creates a fresh MCP client per call (Start + Initialize + List*).
// httpStatusRoundTripper intercepts the 401 response before mcp-go processes it,
// converting it to vmcp.ErrAuthenticationFailed, which retryingBackendClient detects
// via errors.Is and retries until success.
func TestAuthRetry_Transient401_ListCapabilities(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int32
	backend, cleanup := startTransient401Server(t, &requestCount)
	defer cleanup()

	registry := auth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{}))

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       backend,
		TransportType: "streamable-http",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// ListCapabilities should succeed despite the initial 401 — the retry wrapper
	// must recreate the MCP client and successfully complete the capability query.
	caps, err := backendClient.ListCapabilities(ctx, target)

	require.NoError(t, err, "ListCapabilities should succeed after auth retry")
	require.NotNil(t, caps)
	assert.Len(t, caps.Tools, 1, "should discover the echo tool after retry")

	// Confirm the retry was exercised: the backend received more than one batch of
	// requests (the 401 attempt + the successful retry).
	assert.Greater(t, int(requestCount.Load()), 1,
		"backend must have received >1 request, confirming retry was exercised")
}

// startTransient401Server starts an httptest.Server backed by a real mcp-go MCP server.
// It returns 401 for the first request, then passes through to the real handler.
// The returned cleanup function must be deferred by the caller.
func startTransient401Server(tb testing.TB, requestCount *atomic.Int32) (baseURL string, cleanup func()) {
	tb.Helper()

	mcpSrv := server.NewMCPServer("test-backend", "1.0.0",
		server.WithToolCapabilities(true),
	)
	mcpSrv.AddTool(
		mcp.Tool{Name: "echo", Description: "Echo the input"},
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{mcp.NewTextContent("ok")},
			}, nil
		},
	)

	streamable := server.NewStreamableHTTPServer(mcpSrv, server.WithEndpointPath("/mcp"))

	httpSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			// Allow session-close DELETE to pass through without counting.
			streamable.ServeHTTP(w, r)
			return
		}
		n := requestCount.Add(1)
		if n <= 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		streamable.ServeHTTP(w, r)
	}))

	// Bind to a free port on loopback.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(tb, err)
	httpSrv.Listener = ln
	httpSrv.Start()

	tb.Logf("started transient-401 backend at %s/mcp (will fail first non-DELETE request)", httpSrv.URL)

	return httpSrv.URL + "/mcp", httpSrv.Close
}
