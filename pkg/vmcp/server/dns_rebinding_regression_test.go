// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

// TestRegression_DNSRebinding_RejectsForeignHostByDefault pins that the vMCP
// server does NOT pass go-sdk's WithDisableLocalhostProtection option, so a
// Streamable HTTP server bound to a loopback listener retains go-sdk's default
// DNS-rebinding protection: a POST whose Host header names a non-localhost
// value is rejected with 403 before the request reaches the MCP dispatcher.
//
// Only toolhive-core (mcpcompat/server) exercises this behaviour directly
// today; this test pins it at the vMCP integration point so a future change
// that starts threading WithDisableLocalhostProtection(true) into the Serve
// path's streamableOpts (server.go Handler) regresses loudly instead of
// silently reopening the DNS-rebinding hole.
//
// The positive control (httptest's own loopback Host, which the client sets
// automatically) must NOT be rejected, so the negative case cannot pass
// vacuously (e.g. if the whole POST path were broken).
func TestRegression_DNSRebinding_RejectsForeignHostByDefault(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	factory, _ := newToolSessionFactory(t, ctrl, nil)
	fc := &fakeCore{}

	srv, err := Serve(context.Background(), fc, &ServerConfig{
		SessionTTL:           time.Minute,
		SessionManagerConfig: &sessionmanager.FactoryConfig{Base: factory},
		BackendRegistry:      vmcp.NewImmutableRegistry([]vmcp.Backend{}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })

	handler, err := srv.Handler(context.Background())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	initBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "dns-rebinding-test", "version": "1.0"},
		},
	})
	require.NoError(t, err)

	tests := []struct {
		name       string
		host       string // empty means "leave the client-assigned default (loopback) Host"
		wantStatus int
	}{
		{
			name:       "foreign Host header is rejected (DNS-rebinding protection)",
			host:       "evil.example.com",
			wantStatus: http.StatusForbidden,
		},
		{
			name: "loopback Host header is accepted (positive control)",
			// Leave req.Host at its client-assigned default (the loopback
			// listener's own address) so this case cannot pass vacuously if the
			// POST path itself were broken.
			host:       "",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/mcp", bytes.NewReader(initBody))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if tt.host != "" {
				// Set the Host FIELD (not a header) — go-sdk's DNS-rebinding check
				// inspects http.Request.Host, which net/http populates from the
				// request line / Host header on the wire. Setting Header.Set("Host",
				// ...) would not exercise the same code path.
				req.Host = tt.host
			}

			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.wantStatus, resp.StatusCode,
				"POST with Host=%q must yield status %d", tt.host, tt.wantStatus)
		})
	}
}
