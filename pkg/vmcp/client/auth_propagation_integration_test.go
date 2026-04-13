// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	healthcontext "github.com/stacklok/toolhive/pkg/vmcp/health/context"
)

// TestListCapabilities_AuthContextPropagatedThroughClose is a regression test
// for the bug fixed in #4613, where mcp-go's Close() emits a DELETE request
// with context.Background(), discarding the health-check marker and identity
// from the original ListCapabilities context.
//
// Without the fix, UpstreamInjectStrategy fails with "no identity found in
// context" for the DELETE request. When auth fails, authRoundTripper returns
// an error before sending the request, so the DELETE never reaches the server.
//
// This test would fail if identityPropagatingRoundTripper stopped capturing
// isHealthCheck at transport creation time and re-injecting it into every
// outgoing request.
func TestListCapabilities_AuthContextPropagatedThroughClose(t *testing.T) {
	t.Parallel()

	// Track whether the server received the DELETE that mcp-go sends on Close().
	// If auth fails in the transport, the request is dropped before reaching the
	// server and this stays false.
	var deleteReceived atomic.Bool

	mcpServer := server.NewMCPServer("test-backend", "1.0.0")
	streamServer := server.NewStreamableHTTPServer(mcpServer)

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleteReceived.Store(true)
		}
		streamServer.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	registry := vmcpauth.NewDefaultOutgoingAuthRegistry()
	err := registry.RegisterStrategy(authtypes.StrategyTypeUpstreamInject, strategies.NewUpstreamInjectStrategy())
	require.NoError(t, err)

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "test-backend",
		WorkloadName:  "Test Backend",
		BaseURL:       ts.URL + "/mcp",
		TransportType: "streamable-http",
		AuthConfig: &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeUpstreamInject,
			UpstreamInject: &authtypes.UpstreamInjectConfig{
				ProviderName: "test-provider",
			},
		},
	}

	// Health-check context: UpstreamInjectStrategy skips auth when the
	// health-check marker is present, so no user identity is needed.
	ctx, cancel := context.WithTimeout(
		healthcontext.WithHealthCheckMarker(context.Background()),
		5*time.Second,
	)
	defer cancel()

	// Call ListCapabilities — this exercises the full path:
	//   defaultClientFactory (captures isHealthCheck=true in transport)
	//   → Initialize → ListTools/Resources/Prompts
	//   → deferred c.Close() (mcp-go emits DELETE with context.Background())
	capabilities, err := backendClient.ListCapabilities(ctx, target)
	require.NoError(t, err)
	require.NotNil(t, capabilities)

	// REGRESSION GUARD: the transport must re-inject the health-check marker
	// into the DELETE from Close(). If it doesn't, UpstreamInjectStrategy
	// fails with "no identity found in context", authRoundTripper drops the
	// request before sending, and the server never sees the DELETE.
	assert.True(t, deleteReceived.Load(),
		"server did not receive DELETE: auth context propagation regression (#4613) likely reintroduced")
}
