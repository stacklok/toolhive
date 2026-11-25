// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/test/integration/vmcp/helpers"
)

// TestRegression_SlidingSessionTTL_TrafficKeepsSessionAlive proves the
// session TTL is sliding: a session that receives traffic at least once per
// TTL window stays alive indefinitely, while an idle session is evicted once
// its TTL elapses.
//
// The transport session storage (LocalSessionDataStorage.Load) refreshes the
// last-access timestamp on every read, and the SDK's SessionIdManager.Validate
// is called on every request — so active traffic keeps the session alive while
// a session that goes idle for longer than the TTL is rejected on its next
// request.
//
// This test is timing-sensitive and must NOT run in parallel.
//
//nolint:paralleltest // timing-sensitive: relies on real TTL expiry and background cleanup
func TestRegression_SlidingSessionTTL_TrafficKeepsSessionAlive(t *testing.T) {
	const sessionTTL = 2 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backend := helpers.CreateBackendServer(t, []helpers.BackendTool{
		helpers.NewBackendTool("ping", "ping tool", func(_ context.Context, _ map[string]any) string {
			return `{"pong":true}`
		}),
	}, helpers.WithBackendName("ping-backend"))
	defer backend.Close()

	backends := []vmcp.Backend{
		helpers.NewBackend("ping-backend", helpers.WithURL(backend.URL+"/mcp")),
	}

	vmcpServer := helpers.NewVMCPServer(ctx, t, backends,
		helpers.WithPrefixConflictResolution("{workload}_"),
		helpers.WithSessionTTL(sessionTTL),
	)
	vmcpURL := "http://" + vmcpServer.Address() + "/mcp"

	// ── Active session: traffic every 500ms for 4s (2x the TTL) ──────────────
	//
	// If the TTL were fixed (not sliding), the session would expire at the 2s
	// mark and the next tools/call would fail. A sliding TTL refreshes
	// last-access on each request, so all calls must succeed.
	//
	// We use tools/call (not tools/list) here because tools/list returns the
	// SDK's cached tool set even after the vMCP session storage has evicted the
	// session (the go-sdk transport map is not TTL-gated), so it would pass
	// whether the TTL slides or not. tools/call routes through
	// enforceSessionBinding → storage.Load, so it fails once the session is gone
	// — making it the assertion that actually distinguishes sliding from fixed TTL.
	activeClient := helpers.NewMCPClient(ctx, t, vmcpURL)
	defer activeClient.Close()

	const activeTool = "ping-backend_ping"

	const (
		tickInterval = 500 * time.Millisecond
		totalWindow  = 4 * time.Second
	)
	ticks := int(totalWindow / tickInterval)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		for i := 0; i < ticks; i++ {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			// tools/call routes through enforceSessionBinding → storage.Load,
			// which refreshes last-access in the transport session storage.
			_ = activeClient.CallTool(ctx, activeTool, map[string]any{})
		}
	}()
	wg.Wait()

	// One final call after the full window: the session must still be alive.
	// CallTool fails the test if the session was evicted (the SDK surfaces the
	// missing session as a transport error or an error result).
	final := activeClient.CallTool(ctx, activeTool, map[string]any{})
	assert.False(t, final.IsError,
		"active session must survive past its TTL while receiving traffic; got error result %v", final.Content)

	// ── Idle session: no traffic for 3s (> TTL) must be evicted ──────────────
	//
	// A second session that goes idle for longer than the TTL must be rejected
	// on its next tool call. tools/list is NOT a reliable eviction signal here:
	// the go-sdk StreamableHTTPHandler keeps its per-session transport in an
	// in-memory map that is not TTL-gated, so tools/list continues to return
	// the cached tool set even after the vMCP session storage has evicted the
	// session. The eviction IS observable on tools/call, whose handler runs
	// enforceSessionBinding → GetMultiSession → checkSession → storage.Load,
	// which returns ErrSessionNotFound once the TTL has elapsed and the
	// background cleanup sweep has removed the entry.
	idleClient := newRawClient(vmcpURL)
	idleClient.initialize(t, "")

	// Let the session go idle well past the TTL. The cleanup goroutine runs on
	// a ttl/2 ticker, so waiting 2x the TTL after the idle period guarantees the
	// background sweep has evicted the session.
	idleFor := 3 * sessionTTL
	time.Sleep(idleFor)

	// A tool call on the idle session must be rejected: enforceSessionBinding
	// fails because the session is gone from storage. The SDK surfaces this as
	// either an HTTP error (4xx) or a JSON-RPC error result — assert both.
	resp := idleClient.postMCP(t, "", map[string]any{
		"jsonrpc": "2.0",
		"id":      idleClient.nextID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "ping-backend_ping",
			"arguments": map[string]any{},
		},
	}, idleClient.sessionID)
	idleClient.nextID++
	defer resp.Body.Close()

	rejected := resp.StatusCode >= 400 ||
		isToolResultError(mustParseJSONRPC(t, resp))
	assert.True(t, rejected,
		"idle session must be evicted after exceeding the TTL (status %d)", resp.StatusCode)
}

// Compile-time check that this file stays in the vmcp_test package.
var _ vmcp.Backend
