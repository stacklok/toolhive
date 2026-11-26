// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
)

const (
	cancelWaitTool = "wait"
	// cancelRounds repeats each cancellation so a lost cancellation is caught
	// even when it is lost only some of the time.
	cancelRounds = 5
	// cancelReachTimeout bounds how long a cancellation may take to reach the
	// backend tool.
	cancelReachTimeout = 10 * time.Second
)

// waitBackend is a real Legacy backend whose "wait" tool runs until its
// context is cancelled. Each call reports on started when it begins and on
// cancelled when its context ends.
type waitBackend struct {
	url       string
	started   chan struct{}
	cancelled chan struct{}
}

func startWaitBackend(t *testing.T) *waitBackend {
	t.Helper()

	b := &waitBackend{
		started:   make(chan struct{}, cancelRounds),
		cancelled: make(chan struct{}, cancelRounds),
	}
	mcpSrv := mcpserver.NewMCPServer("real-backend", "1.0.0")
	mcpSrv.AddTool(
		mcpmcp.NewTool(cancelWaitTool, mcpmcp.WithDescription("Runs until cancelled")),
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			b.started <- struct{}{}
			select {
			case <-ctx.Done():
				b.cancelled <- struct{}{}
				return nil, ctx.Err()
			case <-time.After(time.Minute):
				return &mcpmcp.CallToolResult{
					Content: []mcpmcp.Content{mcpmcp.NewTextContent("never cancelled")},
				}, nil
			}
		},
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewStreamableHTTPServer(mcpSrv))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	b.url = ts.URL + "/mcp"
	return b
}

// requireReached waits for one event on ch, failing with what did not happen.
func requireReached(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(cancelReachTimeout):
		t.Fatalf("%s within %s", what, cancelReachTimeout)
	}
}

// TestIntegration_RealBackend_LegacyCancellationReachesBackend verifies that a
// Legacy client's notifications/cancelled for an open tools/call cancels the
// backend tool, not only the vMCP's own handler.
func TestIntegration_RealBackend_LegacyCancellationReachesBackend(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), forwardingRealBackendTimeout)
	defer cancel()

	backend := startWaitBackend(t)
	vmcpTS := newRealTestServer(t, backend.url)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", false)

	for round := range cancelRounds {
		callCtx, cancelCall := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			_, err := dc.c.CallTool(callCtx, mcpmcp.CallToolRequest{
				Params: mcpmcp.CallToolParams{Name: cancelWaitTool},
			})
			done <- err
		}()

		requireReached(t, backend.started, "backend tool did not start")
		// Cancelling the call's context makes the client send
		// notifications/cancelled for it.
		cancelCall()
		requireReached(t, backend.cancelled,
			fmt.Sprintf("round %d: the client's cancellation did not reach the backend tool", round+1))
		<-done
	}
}

// TestIntegration_RealBackend_ModernCancellationReachesBackend verifies that a
// Modern client closing an open tools/call request cancels the backend tool.
// Modern has no session to carry notifications/cancelled; closing the request
// is the cancellation.
func TestIntegration_RealBackend_ModernCancellationReachesBackend(t *testing.T) {
	t.Parallel()

	backend := startWaitBackend(t)
	vmcpTS := newRealTestServer(t, backend.url)

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      cancelWaitTool,
			"arguments": map[string]any{},
			"_meta": map[string]any{
				"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
				"io.modelcontextprotocol/clientCapabilities": map[string]any{},
			},
		},
	})
	require.NoError(t, err)

	for round := range cancelRounds {
		callCtx, cancelCall := context.WithCancel(t.Context())
		req, err := http.NewRequestWithContext(callCtx, http.MethodPost, vmcpTS.URL+"/mcp", bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", cancelWaitTool)

		done := make(chan struct{})
		go func() {
			defer close(done)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		}()

		requireReached(t, backend.started, "backend tool did not start")
		cancelCall()
		requireReached(t, backend.cancelled,
			fmt.Sprintf("round %d: closing the Modern request did not reach the backend tool", round+1))
		<-done
	}
}
