// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestProbePerformsOnlyInitialize(t *testing.T) {
	t.Parallel()

	mcpServer := server.NewMCPServer("probe-test", "1.2.3", server.WithToolCapabilities(true))
	streamableServer := server.NewStreamableHTTPServer(mcpServer, server.WithEndpointPath("/mcp"))

	var mu sync.Mutex
	var methods []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read MCP request body: %v", err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			var request struct {
				Method string `json:"method"`
			}
			if json.Unmarshal(body, &request) == nil && request.Method != "" {
				mu.Lock()
				methods = append(methods, request.Method)
				mu.Unlock()
			}
		}
		streamableServer.ServeHTTP(w, r)
	})
	testServer := httptest.NewServer(handler)
	t.Cleanup(testServer.Close)

	result, err := Probe(context.Background(), testServer.URL+"/mcp",
		string(types.TransportTypeStreamableHTTP), "probe-client")
	require.NoError(t, err)
	assert.Equal(t, "probe-test", result.ServerInfo.Name)
	assert.Equal(t, "1.2.3", result.ServerInfo.Version)
	assert.Equal(t, string(types.TransportTypeStreamableHTTP), result.Transport)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, methods, "initialize")
	assert.NotContains(t, methods, "tools/list")
	assert.NotContains(t, methods, "resources/list")
	assert.NotContains(t, methods, "prompts/list")
}

func TestProbeAutoReportsEachTransportFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: "missing bearer token"},
		{name: "backend not ready", status: http.StatusServiceUnavailable, body: "backend not ready"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, tt.body, tt.status)
			}))
			t.Cleanup(testServer.Close)

			_, err := Probe(context.Background(), testServer.URL+"/mcp", TransportAuto, "probe-client")
			require.Error(t, err)
			assert.Contains(t, err.Error(), string(types.TransportTypeStreamableHTTP)+":")
			assert.Contains(t, err.Error(), string(types.TransportTypeSSE)+":")
			assert.Contains(t, err.Error(), strconv.Itoa(tt.status))
		})
	}
}

func TestProbeSucceedsWhenSessionTeardownFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		transport string
	}{
		{name: "explicit streamable-http", transport: string(types.TransportTypeStreamableHTTP)},
		{name: "auto", transport: TransportAuto},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mcpServer := server.NewMCPServer("probe-test", "1.2.3")
			streamableServer := server.NewStreamableHTTPServer(mcpServer, server.WithEndpointPath("/mcp"))
			// Reset the connection on the session-teardown DELETE, as some
			// ingresses and WAFs do.
			testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					conn, _, err := http.NewResponseController(w).Hijack()
					if err != nil {
						t.Errorf("hijack DELETE connection: %v", err)
						return
					}
					_ = conn.Close()
					return
				}
				streamableServer.ServeHTTP(w, r)
			}))
			t.Cleanup(testServer.Close)

			result, err := Probe(context.Background(), testServer.URL+"/mcp", tt.transport, "probe-client")
			require.NoError(t, err)
			assert.Equal(t, "probe-test", result.ServerInfo.Name)
			assert.Equal(t, string(types.TransportTypeStreamableHTTP), result.Transport)
		})
	}
}
