// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcptransport "github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

func newTestRegistry(t *testing.T) vmcpauth.OutgoingAuthRegistry {
	t.Helper()
	reg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, reg.RegisterStrategy(
		authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy(),
	))
	return reg
}

// mergeForwardedHeaders is now a one-line delegation to
// headerforward.MergeForwardedHeaders; full coverage lives in
// pkg/vmcp/headerforward/transport_test.go (TestMergeForwardedHeaders).

func TestCreateMCPClient_UnsupportedTransport(t *testing.T) {
	t.Parallel()

	unsupportedTypes := []string{"stdio", "grpc", "", "ws"}
	for _, transport := range unsupportedTypes {
		t.Run(transport, func(t *testing.T) {
			t.Parallel()

			target := &vmcp.BackendTarget{
				WorkloadID:    "test-backend",
				WorkloadName:  "test-backend",
				BaseURL:       "http://localhost:9999",
				TransportType: transport,
			}

			_, err := createMCPClient(context.Background(), target, nil, newTestRegistry(t), "", secrets.NewEnvironmentProvider(), nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, vmcp.ErrUnsupportedTransport,
				"transport %q should return ErrUnsupportedTransport", transport)
		})
	}
}

// listChangedTestBackend is a real mcpcompat MCP server (streamable-HTTP) with
// WithToolCapabilities(true). It captures its one connected session (via
// OnRegisterSession) and exposes addTool, which appends a per-session tool via
// SessionWithTools.SetSessionTools — the same mechanism ToolHive's own vMCP
// Server uses (setSessionToolsDirect/resyncSessionTools) — which is what
// actually drives the underlying go-sdk server's real
// notifications/tools/list_changed broadcast. (The mcpcompat-wrapper-level
// MCPServer.AddTool called AFTER the server starts serving does NOT do this:
// it only mutates the wrapper's pre-serve tool set, registered once at
// buildServer time, so it has no effect on an already-connected session.)
type listChangedTestBackend struct {
	url string

	// ready closes once OnRegisterSession has captured session, decoupling
	// addTool from the client-side race noted below.
	ready chan struct{}

	mu      sync.Mutex
	session mcpserver.ClientSession
	tools   map[string]mcpserver.ServerTool
}

func newListChangedTool(name string) mcpserver.ServerTool {
	return mcpserver.ServerTool{
		Tool: mcpmcp.NewTool(name, mcpmcp.WithDescription("a list_changed test tool")),
		Handler: func(_ context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			return mcpmcp.NewToolResultText("ok"), nil
		},
	}
}

// newListChangedTestBackend starts the backend; its OnRegisterSession hook
// captures the connecting session and closes ready.
func newListChangedTestBackend(t *testing.T) *listChangedTestBackend {
	t.Helper()
	b := &listChangedTestBackend{
		ready: make(chan struct{}),
		tools: map[string]mcpserver.ServerTool{
			"initial_tool": newListChangedTool("initial_tool"),
		},
	}

	hooks := &mcpserver.Hooks{}
	hooks.AddOnRegisterSession(func(_ context.Context, session mcpserver.ClientSession) {
		b.mu.Lock()
		b.session = session
		tools := make(map[string]mcpserver.ServerTool, len(b.tools))
		for k, v := range b.tools {
			tools[k] = v
		}
		b.mu.Unlock()
		sessionWithTools, ok := session.(mcpserver.SessionWithTools)
		if !ok {
			t.Errorf("listChangedTestBackend: session does not support per-session tools")
			return
		}
		sessionWithTools.SetSessionTools(tools)
		close(b.ready)
	})

	srv := mcpserver.NewMCPServer("list-changed-backend", "1.0.0",
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithHooks(hooks),
	)
	streamableSrv := mcpserver.NewStreamableHTTPServer(srv)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamableSrv)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	b.url = ts.URL + "/mcp"
	return b
}

// addTool adds name to the connected session's per-session tool set via
// SetSessionTools, reconciling it onto the live go-sdk server and (since
// WithToolCapabilities(true) is set) triggering a real, asynchronous
// notifications/tools/list_changed broadcast — independent of any in-flight
// call.
//
// It waits on b.ready first: the mcpcompat/go-sdk client's Initialize sends
// notifications/initialized and returns as soon as the SEND succeeds, without
// waiting for the SERVER to finish processing it (registerAndSync, which is
// where OnRegisterSession/b.session-capture runs) — so a caller cannot safely
// call addTool immediately after Initialize returns without this explicit
// synchronization.
func (b *listChangedTestBackend) addTool(t *testing.T, name string) {
	t.Helper()
	select {
	case <-b.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("listChangedTestBackend: timed out waiting for session registration")
	}

	b.mu.Lock()
	session := b.session
	b.tools[name] = newListChangedTool(name)
	tools := make(map[string]mcpserver.ServerTool, len(b.tools))
	for k, v := range b.tools {
		tools[k] = v
	}
	b.mu.Unlock()
	sessionWithTools, ok := session.(mcpserver.SessionWithTools)
	require.True(t, ok, "listChangedTestBackend: session does not support per-session tools")
	sessionWithTools.SetSessionTools(tools)
}

// TestCreateMCPClient_ContinuousListeningGatedOnSink verifies createMCPClient
// only enables the streamable-HTTP standalone GET stream (WithContinuousListening)
// when a non-nil sink is supplied. Some backends hang when this stream is opened
// against them (#5748 R3), so it must stay strictly opt-in.
func TestCreateMCPClient_ContinuousListeningGatedOnSink(t *testing.T) {
	t.Parallel()

	b := newListChangedTestBackend(t)
	target := &vmcp.BackendTarget{
		WorkloadID:    "list-changed-backend",
		WorkloadName:  "list-changed-backend",
		BaseURL:       b.url,
		TransportType: "streamable-http",
	}

	tests := []struct {
		name           string
		sink           ListChangedSink
		wantContinuous bool
	}{
		{name: "nil sink disables continuous listening", sink: nil, wantContinuous: false},
		{
			name:           "non-nil sink enables continuous listening",
			sink:           func(context.Context, string, ChangeKind) {},
			wantContinuous: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c, err := createMCPClient(
				context.Background(), target, nil, newTestRegistry(t), "", secrets.NewEnvironmentProvider(), tc.sink,
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })

			sh, ok := c.GetTransport().(*mcptransport.StreamableHTTP)
			require.True(t, ok, "expected a streamable-HTTP transport")
			assert.Equal(t, tc.wantContinuous, sh.ContinuousListening())
		})
	}
}

// TestCreateMCPClient_ListChangedSink_FiresOnBackendNotification is the
// end-to-end regression for #5748: a non-nil sink fires with the backend's
// WorkloadID and kind="tools" when the real backend emits
// notifications/tools/list_changed asynchronously (outside any in-flight
// call), and the nil-sink path never panics or opens the standalone stream.
func TestCreateMCPClient_ListChangedSink_FiresOnBackendNotification(t *testing.T) {
	t.Parallel()

	b := newListChangedTestBackend(t)
	target := &vmcp.BackendTarget{
		WorkloadID:    "list-changed-backend",
		WorkloadName:  "list-changed-backend",
		BaseURL:       b.url,
		TransportType: "streamable-http",
	}

	type firedCall struct {
		backendWorkloadID string
		kind              ChangeKind
	}
	fired := make(chan firedCall, 4)
	sink := func(_ context.Context, backendWorkloadID string, kind ChangeKind) {
		fired <- firedCall{backendWorkloadID: backendWorkloadID, kind: kind}
	}

	c, err := createMCPClient(
		context.Background(), target, nil, newTestRegistry(t), "", secrets.NewEnvironmentProvider(), sink,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Initialize(context.Background(), mcpmcp.InitializeRequest{
		Params: mcpmcp.InitializeParams{
			ProtocolVersion: mcpmcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcpmcp.Implementation{Name: "test-client", Version: "1.0"},
		},
	})
	require.NoError(t, err)

	// Backend asynchronously changes its (per-session) tool set — a real
	// tools/list_changed trigger, independent of any in-flight call from this
	// client.
	b.addTool(t, "added_tool")

	select {
	case got := <-fired:
		assert.Equal(t, "list-changed-backend", got.backendWorkloadID)
		assert.Equal(t, KindTools, got.kind)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the list_changed sink to fire")
	}
}

// TestCreateMCPClient_ListChangedSink_DoesNotStallInFlightCall is the F9
// corroborated-concern regression: firing a real backend tools/list_changed —
// whose sink is (deliberately, for the test) slow — must NOT stall an in-flight
// call on the same backend session. The sink runs on the client's
// notification-dispatch goroutine, which is independent of the request path, so
// a CallTool issued while the sink is still working completes promptly. This
// validates the no-deadlock conclusion behind moving the real resync off the
// receive loop (#5748 B-HIGH-2).
func TestCreateMCPClient_ListChangedSink_DoesNotStallInFlightCall(t *testing.T) {
	t.Parallel()

	b := newListChangedTestBackend(t)
	target := &vmcp.BackendTarget{
		WorkloadID:    "list-changed-backend",
		WorkloadName:  "list-changed-backend",
		BaseURL:       b.url,
		TransportType: "streamable-http",
	}

	sinkEntered := make(chan struct{}, 1)
	releaseSink := make(chan struct{})
	sink := func(_ context.Context, _ string, kind ChangeKind) {
		if kind != KindTools {
			return
		}
		select {
		case sinkEntered <- struct{}{}:
		default:
		}
		// Simulate a slow resync body running on the dispatch goroutine to prove
		// it does not hold anything the request path needs.
		<-releaseSink
	}

	c, err := createMCPClient(
		context.Background(), target, nil, newTestRegistry(t), "", secrets.NewEnvironmentProvider(), sink,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Initialize(context.Background(), mcpmcp.InitializeRequest{
		Params: mcpmcp.InitializeParams{
			ProtocolVersion: mcpmcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcpmcp.Implementation{Name: "test-client", Version: "1.0"},
		},
	})
	require.NoError(t, err)

	// Trigger a real tools/list_changed and wait until the (slow) sink is running.
	b.addTool(t, "added_tool")
	select {
	case <-sinkEntered:
	case <-time.After(10 * time.Second):
		close(releaseSink)
		t.Fatal("sink never fired")
	}

	// With the sink still blocked, an in-flight call on the same session must
	// complete promptly (independent dispatch vs request paths).
	callDone := make(chan error, 1)
	go func() {
		_, callErr := c.CallTool(context.Background(), mcpmcp.CallToolRequest{
			Params: mcpmcp.CallToolParams{Name: "initial_tool"},
		})
		callDone <- callErr
	}()

	select {
	case callErr := <-callDone:
		require.NoError(t, callErr, "in-flight call must succeed while a resync sink is running")
	case <-time.After(10 * time.Second):
		close(releaseSink)
		t.Fatal("in-flight call stalled while the list_changed sink was running")
	}

	close(releaseSink)
}
