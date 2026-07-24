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

// TestNewListChangedNotificationHandler_DispatchesByMethod verifies (#5748,
// #5969) newListChangedNotificationHandler's wire-method dispatch: each of
// the three list_changed methods fires sink with the matching ChangeKind, and
// neither notifications/message nor an unknown method fires sink at all.
func TestNewListChangedNotificationHandler_DispatchesByMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		method    string
		wantFired bool
		wantKind  ChangeKind
	}{
		{"tools/list_changed dispatches KindTools", vmcp.MethodToolsListChangedNotification, true, KindTools},
		{"resources/list_changed dispatches KindResources", vmcp.MethodResourcesListChangedNotification, true, KindResources},
		{"prompts/list_changed dispatches KindPrompts", vmcp.MethodPromptsListChangedNotification, true, KindPrompts},
		{"notifications/message does not dispatch", vmcp.MethodLogNotification, false, ""},
		{"unknown method does not dispatch", "notifications/resources/updated", false, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			type firedCall struct {
				workloadID string
				kind       ChangeKind
			}
			fired := make(chan firedCall, 1)
			sink := func(_ context.Context, workloadID string, kind ChangeKind) {
				fired <- firedCall{workloadID: workloadID, kind: kind}
			}

			handler := newListChangedNotificationHandler("backend-1", sink)
			handler(mcpmcp.JSONRPCNotification{
				JSONRPC:      "2.0",
				Notification: mcpmcp.Notification{Method: tc.method},
			})

			if !tc.wantFired {
				select {
				case got := <-fired:
					t.Fatalf("sink must not fire for method %q, but fired with kind %q", tc.method, got.kind)
				case <-time.After(50 * time.Millisecond):
				}
				return
			}

			select {
			case got := <-fired:
				assert.Equal(t, "backend-1", got.workloadID)
				assert.Equal(t, tc.wantKind, got.kind)
			case <-time.After(5 * time.Second):
				t.Fatalf("sink never fired for method %q", tc.method)
			}
		})
	}
}

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
// WithToolCapabilities(true), WithResourceCapabilities(true, true), and
// WithPromptCapabilities(true). It captures its one connected session (via
// OnRegisterSession) and exposes addTool/addResource/addPrompt, which append a
// per-session tool/resource/prompt via SessionWithTools.SetSessionTools /
// SessionWithResources.SetSessionResources / SessionWithPrompts.SetSessionPrompts
// — the same mechanism ToolHive's own vMCP Server uses
// (setSessionToolsDirect/resyncSessionTools and their resource/prompt
// counterparts) — which is what actually drives the underlying go-sdk
// server's real notifications/{tools,resources,prompts}/list_changed
// broadcast. (The mcpcompat-wrapper-level MCPServer.AddTool/AddResource/
// AddPrompt called AFTER the server starts serving does NOT do this: it only
// mutates the wrapper's pre-serve set, registered once at buildServer time, so
// it has no effect on an already-connected session.)
type listChangedTestBackend struct {
	url string

	// ready closes once OnRegisterSession has captured session, decoupling
	// addTool/addResource/addPrompt from the client-side race noted below.
	ready chan struct{}

	mu        sync.Mutex
	session   mcpserver.ClientSession
	tools     map[string]mcpserver.ServerTool
	resources map[string]mcpserver.ServerResource
	prompts   map[string]mcpserver.ServerPrompt
}

func newListChangedTool(name string) mcpserver.ServerTool {
	return mcpserver.ServerTool{
		Tool: mcpmcp.NewTool(name, mcpmcp.WithDescription("a list_changed test tool")),
		Handler: func(_ context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			return mcpmcp.NewToolResultText("ok"), nil
		},
	}
}

func newListChangedResource(uri string) mcpserver.ServerResource {
	return mcpserver.ServerResource{
		Resource: mcpmcp.Resource{URI: uri, Name: uri},
		Handler: func(context.Context, mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
			return []mcpmcp.ResourceContents{mcpmcp.TextResourceContents{URI: uri, Text: "ok"}}, nil
		},
	}
}

func newListChangedPrompt(name string) mcpserver.ServerPrompt {
	return mcpserver.ServerPrompt{
		Prompt: mcpmcp.NewPrompt(name),
		Handler: func(context.Context, mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
			return &mcpmcp.GetPromptResult{Messages: []mcpmcp.PromptMessage{
				mcpmcp.NewPromptMessage(mcpmcp.RoleUser, mcpmcp.NewTextContent("ok")),
			}}, nil
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
		resources: map[string]mcpserver.ServerResource{
			"initial_resource": newListChangedResource("initial_resource"),
		},
		prompts: map[string]mcpserver.ServerPrompt{
			"initial_prompt": newListChangedPrompt("initial_prompt"),
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
		resources := make(map[string]mcpserver.ServerResource, len(b.resources))
		for k, v := range b.resources {
			resources[k] = v
		}
		prompts := make(map[string]mcpserver.ServerPrompt, len(b.prompts))
		for k, v := range b.prompts {
			prompts[k] = v
		}
		b.mu.Unlock()

		sessionWithTools, ok := session.(mcpserver.SessionWithTools)
		if !ok {
			t.Errorf("listChangedTestBackend: session does not support per-session tools")
			return
		}
		sessionWithTools.SetSessionTools(tools)

		sessionWithResources, ok := session.(mcpserver.SessionWithResources)
		if !ok {
			t.Errorf("listChangedTestBackend: session does not support per-session resources")
			return
		}
		sessionWithResources.SetSessionResources(resources)

		sessionWithPrompts, ok := session.(mcpserver.SessionWithPrompts)
		if !ok {
			t.Errorf("listChangedTestBackend: session does not support per-session prompts")
			return
		}
		sessionWithPrompts.SetSessionPrompts(prompts)

		close(b.ready)
	})

	srv := mcpserver.NewMCPServer("list-changed-backend", "1.0.0",
		mcpserver.WithToolCapabilities(true),
		mcpserver.WithResourceCapabilities(true, true),
		mcpserver.WithPromptCapabilities(true),
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

// addResource adds uri to the connected session's per-session resource set via
// SetSessionResources, triggering a real, asynchronous
// notifications/resources/list_changed broadcast. See addTool for the b.ready
// synchronization rationale.
func (b *listChangedTestBackend) addResource(t *testing.T, uri string) {
	t.Helper()
	select {
	case <-b.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("listChangedTestBackend: timed out waiting for session registration")
	}

	b.mu.Lock()
	session := b.session
	b.resources[uri] = newListChangedResource(uri)
	resources := make(map[string]mcpserver.ServerResource, len(b.resources))
	for k, v := range b.resources {
		resources[k] = v
	}
	b.mu.Unlock()
	sessionWithResources, ok := session.(mcpserver.SessionWithResources)
	require.True(t, ok, "listChangedTestBackend: session does not support per-session resources")
	sessionWithResources.SetSessionResources(resources)
}

// addPrompt adds name to the connected session's per-session prompt set via
// SetSessionPrompts, triggering a real, asynchronous
// notifications/prompts/list_changed broadcast. See addTool for the b.ready
// synchronization rationale.
func (b *listChangedTestBackend) addPrompt(t *testing.T, name string) {
	t.Helper()
	select {
	case <-b.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("listChangedTestBackend: timed out waiting for session registration")
	}

	b.mu.Lock()
	session := b.session
	b.prompts[name] = newListChangedPrompt(name)
	prompts := make(map[string]mcpserver.ServerPrompt, len(b.prompts))
	for k, v := range b.prompts {
		prompts[k] = v
	}
	b.mu.Unlock()
	sessionWithPrompts, ok := session.(mcpserver.SessionWithPrompts)
	require.True(t, ok, "listChangedTestBackend: session does not support per-session prompts")
	sessionWithPrompts.SetSessionPrompts(prompts)
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
// end-to-end regression for #5748 (tools) and #5969 (resources, prompts): a
// non-nil sink fires with the backend's WorkloadID and the expected kind when
// the real backend emits notifications/{tools,resources,prompts}/list_changed
// asynchronously (outside any in-flight call), over the same
// continuous-listening connection, and the nil-sink path never panics or
// opens the standalone stream.
func TestCreateMCPClient_ListChangedSink_FiresOnBackendNotification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		trigger  func(t *testing.T, b *listChangedTestBackend)
		wantKind ChangeKind
	}{
		{"tools", func(t *testing.T, b *listChangedTestBackend) {
			t.Helper()
			b.addTool(t, "added_tool")
		}, KindTools},
		{"resources", func(t *testing.T, b *listChangedTestBackend) {
			t.Helper()
			b.addResource(t, "added_resource")
		}, KindResources},
		{"prompts", func(t *testing.T, b *listChangedTestBackend) {
			t.Helper()
			b.addPrompt(t, "added_prompt")
		}, KindPrompts},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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
			// Buffered generously: registration itself sets all three per-session
			// stores (tools, resources, prompts), each of which fires its own
			// registration-time list_changed broadcast (see serve.go's R1 note) in
			// addition to the one this test explicitly triggers below.
			fired := make(chan firedCall, 16)
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

			// Backend asynchronously changes its per-session set — a real
			// list_changed trigger, independent of any in-flight call from this
			// client. Drain fired notifications (which may also include
			// registration-time broadcasts of OTHER kinds, per the buffer comment
			// above) until the expected kind is observed.
			tc.trigger(t, b)

			deadline := time.After(10 * time.Second)
			for {
				select {
				case got := <-fired:
					assert.Equal(t, "list-changed-backend", got.backendWorkloadID)
					if got.kind == tc.wantKind {
						return
					}
				case <-deadline:
					t.Fatalf("timed out waiting for a %q list_changed notification", tc.wantKind)
				}
			}
		})
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
