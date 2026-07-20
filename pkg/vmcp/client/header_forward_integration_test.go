// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
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

	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
)

// TestHeaderForward_EndToEnd_ThroughHTTPBackendClient drives the full
// NewHTTPBackendClient → defaultClientFactory wiring. It verifies that:
//
//  1. The plaintext header reaches the backend
//  2. A secret-backed header is resolved through the EnvironmentProvider
//     using the TOOLHIVE_SECRET_<identifier> convention and reaches the backend
//
// Anything below this is exercised by the round-tripper unit tests, but only
// an end-to-end test confirms the *factory wiring itself* (provider lookup,
// transport stacking, ctx threading) is correct in production code paths.
func TestHeaderForward_EndToEnd_ThroughHTTPBackendClient(t *testing.T) {
	// t.Setenv forbids t.Parallel — fine here, this test is fast.
	const (
		identifier = "HEADER_FORWARD_X_API_KEY_BACKEND_E2E"
		envVarName = secrets.EnvVarPrefix + identifier
		secretVal  = "super-secret-resolved-from-env"
	)
	t.Setenv(envVarName, secretVal)

	captured := newCapturingMCPServer(t)
	t.Cleanup(captured.server.Close)

	registry := auth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, registry.RegisterStrategy("unauthenticated", &strategies.UnauthenticatedStrategy{}))

	backendClient, err := vmcpclient.NewHTTPBackendClient(registry)
	require.NoError(t, err)

	target := &vmcp.BackendTarget{
		WorkloadID:    "backend-e2e",
		WorkloadName:  "Backend E2E",
		BaseURL:       captured.server.URL,
		TransportType: "streamable-http",
		HeaderForward: &vmcp.HeaderForwardConfig{
			AddPlaintextHeaders: map[string]string{
				"X-Tenant": "acme",
			},
			AddHeadersFromSecret: map[string]string{
				"X-API-Key": identifier,
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// CallTool drives the full Initialize → tools/list flow through the
	// streamable-HTTP transport. We don't care about the call result — only
	// that the request reached the test server with the configured headers.
	_, _ = backendClient.CallTool(ctx, target, "anything", map[string]any{}, nil)

	captured.mu.Lock()
	defer captured.mu.Unlock()
	require.NotEmpty(t, captured.headers, "test server received no requests")
	assert.Equal(t, "acme", captured.headers.Get("X-Tenant"),
		"plaintext header must be forwarded by the e2e factory wiring")
	assert.Equal(t, secretVal, captured.headers.Get("X-Api-Key"),
		"secret-backed header must be resolved via EnvironmentProvider and forwarded")
}

// capturingMCPServer is a minimal HTTP server that records the headers of
// every inbound request and returns a stub MCP initialize response. It does
// not implement the full MCP protocol — just enough for the streamable-HTTP
// client to send at least one request whose headers we can inspect.
type capturingMCPServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	headers http.Header
}

func newCapturingMCPServer(t *testing.T) *capturingMCPServer {
	t.Helper()
	c := &capturingMCPServer{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		// Only capture the FIRST request — that's the one carrying the headers
		// resolved at client-construction time. Subsequent requests on the same
		// connection would see the same headers but recording every one makes
		// the test read confusing.
		if c.headers == nil {
			c.headers = r.Header.Clone()
		}
		c.mu.Unlock()

		// Drain the body so the underlying transport can reuse the connection.
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()

		// Return a minimal JSON-RPC error response so the client unwinds
		// cleanly. We don't need a full Initialize because we already have
		// what we need (the headers) before the client gives up.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"error": map[string]any{
				"code":    -32601,
				"message": "method not implemented in test server",
			},
		})
	}))
	// Sanity check — the test server URL must be plain HTTP (no TLS) so the
	// transport doesn't try to verify a self-signed cert against system roots.
	require.True(t, strings.HasPrefix(c.server.URL, "http://"),
		"capturingMCPServer must serve plain HTTP")
	return c
}
