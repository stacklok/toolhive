// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// toolNamesOnServer returns the names of the tools currently registered on the
// given local MCP server, by issuing a synthetic tools/list request through the
// shim's HandleMessage dispatch path. The bridge's forwardAll registers upstream
// tools here, so this is the assertion seam for "what does the bridge advertise
// downstream". Call only after the bridge has fully stopped (run() returned),
// since StdioBridge exposes no synchronization for its srv field while running.
func toolNamesOnServer(t *testing.T, srv *server.MCPServer) []string {
	t.Helper()
	resp := srv.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","method":"tools/list","id":1}`))
	jr, ok := resp.(mcp.JSONRPCResponse)
	require.True(t, ok, "tools/list should return a JSONRPCResponse, got %T", resp)
	buf, err := json.Marshal(jr.Result)
	require.NoError(t, err)
	var lt mcp.ListToolsResult
	require.NoError(t, json.Unmarshal(buf, &lt))
	names := make([]string, 0, len(lt.Tools))
	for _, tl := range lt.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func containsTool(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// noopToolHandler is a stand-in tool handler; the bridge re-fetch tests never
// invoke tools, they only assert the advertised set.
func noopToolHandler(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return nil, nil
}

// listToolsCounter is a server hook that atomically counts tools/list requests
// received by the backend. The bridge's forwardAll issues a tools/list upstream
// on startup and again whenever its OnNotification handler re-syncs on a
// *_list_changed notification, so the counter is the race-free readiness and
// re-fetch signal observed entirely on the backend side.
type listToolsCounter struct {
	count atomic.Int64
}

func (c *listToolsCounter) hook() server.OnBeforeListToolsFunc {
	return func(context.Context, any, *mcp.ListToolsRequest) { c.count.Add(1) }
}

// sessionHolder captures the single client session that connects to the backend,
// guarded by a mutex because the hook fires on the backend's session goroutine.
type sessionHolder struct {
	mu       sync.Mutex
	_session server.ClientSession
}

func (h *sessionHolder) set(s server.ClientSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h._session = s
}

func (h *sessionHolder) get() server.ClientSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h._session
}

// TestBridge_ToolsListChanged_TriggersReSync verifies the bridge's
// notifications/tools/list_changed re-fetch fix: when the upstream backend's
// tool set changes after the bridge has connected, the upstream emits a
// tools/list_changed notification, the bridge's OnNotification handler re-runs
// forwardAll, and the newly added tool appears on the bridge's local stdio
// server.
//
// The backend is a real mcpcompat streamable-HTTP server. A tool-set change
// visible to an already-connected client is driven through the per-session
// overlay (SessionWithTools.SetSessionTools): the shim syncs the overlay onto the
// live go-sdk server, which emits notifications/tools/list_changed over the
// standalone SSE stream to the bridge's upstream client (the bridge connects
// with WithContinuousListening, so the SSE stream is established). The bridge's
// OnNotification closure is the fix under test.
//
// ServeStdio blocks reading os.Stdin; this test swaps os.Stdin for a pipe so
// ServeStdio returns cleanly on stdin EOF at teardown. The bridge exposes no
// synchronization for its srv field, so readiness/re-fetch is observed via the
// backend's tools/list counter (race-free) and bridge.srv is read only after
// Shutdown returns (b.wg.Wait() provides the happens-before edge).
//
//nolint:paralleltest // Swaps process-global os.Stdin; cannot run in parallel.
func TestBridge_ToolsListChanged_TriggersReSync(t *testing.T) {
	// This test swaps process-global os.Stdin, so it cannot run in parallel
	// with any other test touching stdio.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- upstream backend with an initial tool "alpha" ---
	counter := &listToolsCounter{}
	holder := &sessionHolder{}
	hooks := &server.Hooks{}
	hooks.AddOnRegisterSession(func(_ context.Context, s server.ClientSession) { holder.set(s) })
	hooks.AddBeforeListTools(counter.hook())

	backend := server.NewMCPServer(
		"backend", "1.0",
		server.WithToolCapabilities(true),
		server.WithHooks(hooks),
	)
	backend.AddTool(mcp.NewTool("alpha"), noopToolHandler)

	httpSrv := server.NewStreamableHTTPServer(backend)
	ts := httptest.NewServer(httpSrv)
	t.Cleanup(ts.Close)

	// --- bridge pointing at the backend (streamable-http) ---
	bridge, err := NewStdioBridge("test", ts.URL, types.TransportTypeStreamableHTTP)
	require.NoError(t, err)

	// Swap os.Stdin for a pipe so ServeStdio (which hardcodes os.Stdin) does not
	// touch the real process stdin and can be unblocked at teardown by closing
	// the write end. Leave os.Stdout real; the assertions don't read it.
	origIn := os.Stdin
	rIn, wIn, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		os.Stdin = origIn
		_ = rIn.Close()
		_ = wIn.Close()
	})
	os.Stdin = rIn

	bridge.Start(ctx)
	t.Cleanup(func() {
		// Close the stdin write end so the go-sdk stdio reader hits EOF and
		// ServeStdio returns, letting run() exit and the WaitGroup drain. Then
		// Shutdown closes the upstream client and waits for run() to finish.
		_ = wIn.Close()
		bridge.Shutdown()
	})

	// Step 1: wait for the bridge to connect and run its initial forwardAll,
	// which issues the first upstream tools/list (counter >= 1) after the
	// session is registered. Observed entirely on the backend side: race-free.
	require.Eventually(t, func() bool {
		return holder.get() != nil && counter.count.Load() >= 1
	}, 5*time.Second, 50*time.Millisecond,
		"bridge did not complete initial forwardAll (session=%v, listCalls=%d)",
		holder.get() != nil, counter.count.Load())

	// Step 2: mutate the upstream tool set — add "beta" via the per-session
	// overlay. SetSessionTools syncs onto the live go-sdk server bound to this
	// session, which emits notifications/tools/list_changed over the SSE stream
	// to the bridge's upstream client. The bridge's OnNotification handler then
	// re-runs forwardAll, issuing a SECOND upstream tools/list (counter >= 2).
	backendSession := holder.get()
	swt, ok := backendSession.(server.SessionWithTools)
	require.True(t, ok, "backend session must implement SessionWithTools")
	swt.SetSessionTools(map[string]server.ServerTool{
		"alpha": {Tool: mcp.NewTool("alpha"), Handler: noopToolHandler},
		"beta":  {Tool: mcp.NewTool("beta"), Handler: noopToolHandler},
	})

	// Step 3: wait for the re-fetch — the second tools/list proves the
	// OnNotification handler fired and re-ran forwardAll (the fix).
	require.Eventually(t, func() bool {
		return counter.count.Load() >= 2
	}, 5*time.Second, 50*time.Millisecond,
		"bridge did not re-run forwardAll after tools/list_changed (listCalls=%d)",
		counter.count.Load())

	// Step 4: stop the bridge, then read bridge.srv. Shutdown calls
	// b.wg.Wait(), which synchronizes with run()'s defer b.wg.Done() (which is
	// happens-after every field write run() made), so bridge.srv is safe to read
	// here without a data race.
	_ = wIn.Close() // unblock ServeStdio (stdin EOF)
	bridge.Shutdown()

	names := toolNamesOnServer(t, bridge.srv)
	assert.True(t, containsTool(names, "alpha"),
		"alpha must be present after the re-fetch (forwardAll is additive), got %v", names)
	assert.True(t, containsTool(names, "beta"),
		"beta must be present after the re-sync, got %v", names)
}

// TestBridge_ProgressAndLoggingNotifications_DroppedByShim is a regression
// anchor for a known limitation of the mcpcompat client shim: it does not
// install ProgressNotificationHandler or LoggingMessageHandler on the
// underlying go-sdk client, so upstream notifications/progress and
// notifications/message notifications are dropped before they ever reach the
// bridge's OnNotification handler (and thus before they could be forwarded
// downstream by SendNotificationToAllClients, which itself only forwards those
// two methods).
//
// When the shim is fixed to install those handlers, delete this Skip and
// implement a real end-to-end test asserting that progress and logging
// notifications are forwarded to the bridge's local server clients.
//
//nolint:paralleltest // Skipped regression anchor; documents a shim limitation.
func TestBridge_ProgressAndLoggingNotifications_DroppedByShim(t *testing.T) {
	t.Skip("known shim limitation: mcpcompat client does not install " +
		"ProgressNotificationHandler/LoggingMessageHandler, so upstream " +
		"progress/logging notifications are dropped before reaching the bridge " +
		"(see client.installNotificationHandlers); additionally the shim's " +
		"SendNotificationToAllClients only forwards notifications/progress and " +
		"notifications/message, dropping list_changed downstream")
}
