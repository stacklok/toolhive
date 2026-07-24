// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
)

// Forwarding integration fixtures. These exercise the server->client forwarding
// cluster end-to-end: a real in-process backend, mid tools/call, drives
// elicitation/sampling/progress/logging back at its caller; vMCP must relay that
// traffic to the real downstream client on the same session.
const (
	fwdElicitTool   = "elicit_tool"
	fwdSampleTool   = "sample_tool"
	fwdProgressTool = "progress_tool"
	fwdLogTool      = "log_tool"

	fwdSampledSummary = "a short summary"
	fwdSampleModel    = "test-model"
	fwdProgressToken  = "tok-1"
	fwdLogData        = "hello-from-backend"
)

// forwardingRealBackendTimeout bounds each real-backend forwarding test. These
// exercise async, server-initiated traffic relayed backend -> vMCP -> downstream
// over real in-process HTTP clients; under the full-suite parallel `-race` load
// on CI a single relay can take many seconds. The timeout is deliberately
// generous so a slow-but-working relay is not mistaken for a hang: it is the
// single deadline the tests wait against (waitNotification derives its own
// deadline from the per-test context), so a genuine hang still fails with a
// clear error rather than blocking to the `go test` global timeout. See #5962.
const forwardingRealBackendTimeout = 60 * time.Second

// startForwardingBackend starts a real in-process MCP backend whose tools drive
// server->client traffic (elicitation, sampling, progress, logging) during a
// tools/call. Returns the backend's /mcp URL.
func startForwardingBackend(t *testing.T) string {
	t.Helper()

	srv := mcpserver.NewMCPServer("forwarding-backend", "1.0.0",
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithLogging(),
	)

	srv.AddTool(
		mcpmcp.NewTool(fwdElicitTool, mcpmcp.WithDescription("ask the client to confirm")),
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			res, err := srv.RequestElicitation(ctx, mcpmcp.ElicitationRequest{
				Params: mcpmcp.ElicitationParams{
					Message:         "Confirm?",
					RequestedSchema: map[string]any{"type": "object"},
				},
			})
			if err != nil {
				return nil, err
			}
			return mcpmcp.NewToolResultText("action=" + string(res.Action)), nil
		},
	)

	srv.AddTool(
		mcpmcp.NewTool(fwdSampleTool, mcpmcp.WithDescription("ask the client to sample")),
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			res, err := srv.RequestSampling(ctx, mcpmcp.CreateMessageRequest{
				CreateMessageParams: mcpmcp.CreateMessageParams{
					MaxTokens: 100,
					Messages: []mcpmcp.SamplingMessage{{
						Role:    mcpmcp.RoleUser,
						Content: mcpmcp.NewTextContent("summarize this"),
					}},
				},
			})
			if err != nil {
				return nil, err
			}
			text, _ := res.Content.(map[string]any)["text"].(string)
			return mcpmcp.NewToolResultText("sampled=" + text + " model=" + res.Model), nil
		},
	)

	srv.AddTool(
		mcpmcp.NewTool(fwdProgressTool, mcpmcp.WithDescription("emit progress")),
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			if err := srv.SendNotificationToClient(ctx, "notifications/progress", map[string]any{
				"progressToken": fwdProgressToken,
				"progress":      0.5,
				"total":         1.0,
				"message":       "halfway",
			}); err != nil {
				return nil, err
			}
			return mcpmcp.NewToolResultText("done"), nil
		},
	)

	srv.AddTool(
		mcpmcp.NewTool(fwdLogTool, mcpmcp.WithDescription("emit a log message")),
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			if err := srv.SendNotificationToClient(ctx, "notifications/message", map[string]any{
				"level": "info",
				"data":  fwdLogData,
			}); err != nil {
				return nil, err
			}
			return mcpmcp.NewToolResultText("logged"), nil
		},
	)

	streamableSrv := mcpserver.NewStreamableHTTPServer(srv)
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamableSrv)

	ts := httptest.NewServer(mux)
	// Force-close active connections before Close: the persistent session
	// connector holds a standalone SSE GET stream open for the whole session
	// (see cleanupBackendServer), which Close would otherwise block on.
	cleanupBackendServer(t, ts)
	return ts.URL + "/mcp"
}

// downstreamClient builds a real mcpcompat client against the vMCP endpoint,
// wired for server->client traffic (elicitation/sampling handlers, continuous
// listening) and collecting forwarded notifications on the returned channel.
type downstreamClient struct {
	c        *client.Client
	notifCh  chan mcpmcp.JSONRPCNotification
	elicited chan struct{}
}

// newDownstreamClient connects a downstream client to vmcpURL. When
// withHandlers is true it advertises elicitation and sampling and answers those
// requests; when false it advertises neither (the negative path). It always
// registers an OnNotification collector.
func newDownstreamClient(ctx context.Context, t *testing.T, vmcpURL string, withHandlers bool) *downstreamClient {
	t.Helper()

	dc := &downstreamClient{
		notifCh:  make(chan mcpmcp.JSONRPCNotification, 8),
		elicited: make(chan struct{}, 1),
	}

	var clientOpts []client.ClientOption
	if withHandlers {
		clientOpts = append(clientOpts,
			client.WithElicitationHandler(client.ElicitationHandlerFunc(
				func(_ context.Context, _ mcpmcp.ElicitationRequest) (*mcpmcp.ElicitationResult, error) {
					select {
					case dc.elicited <- struct{}{}:
					default:
					}
					return &mcpmcp.ElicitationResult{
						ElicitationResponse: mcpmcp.ElicitationResponse{
							Action:  mcpmcp.ElicitationResponseActionAccept,
							Content: map[string]any{"confirmed": true},
						},
					}, nil
				},
			)),
			client.WithSamplingHandler(client.SamplingHandlerFunc(
				func(_ context.Context, _ mcpmcp.CreateMessageRequest) (*mcpmcp.CreateMessageResult, error) {
					return &mcpmcp.CreateMessageResult{
						SamplingMessage: mcpmcp.SamplingMessage{
							Role:    mcpmcp.RoleAssistant,
							Content: mcpmcp.NewTextContent(fwdSampledSummary),
						},
						Model:      fwdSampleModel,
						StopReason: "endTurn",
					}, nil
				},
			)),
		)
	}

	c, err := client.NewStreamableHttpClientWithOpts(
		vmcpURL,
		[]transport.StreamableHTTPCOption{transport.WithContinuousListening()},
		clientOpts,
	)
	require.NoError(t, err)
	dc.c = c

	c.OnNotification(func(n mcpmcp.JSONRPCNotification) {
		select {
		case dc.notifCh <- n:
		default:
		}
	})

	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Initialize(ctx, mcpmcp.InitializeRequest{
		Params: mcpmcp.InitializeParams{
			ProtocolVersion: mcpmcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcpmcp.Implementation{Name: "downstream", Version: "1.0"},
		},
	})
	require.NoError(t, err)

	return dc
}

// waitNotification blocks for a forwarded notification with the given method,
// or until ctx is done. It waits against the caller's context rather than an
// independent hardcoded timer so there is a single deadline for the test (see
// forwardingRealBackendTimeout): a fixed timer shorter than the context flaked
// under CI `-race` load — the async backend -> vMCP -> downstream relay can take
// well over a second — while a timer longer than the context would mask a hang.
func (dc *downstreamClient) waitNotification(ctx context.Context, t *testing.T, method string) mcpmcp.JSONRPCNotification {
	t.Helper()
	for {
		select {
		case n := <-dc.notifCh:
			if n.Method == method {
				return n
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s notification: %v", method, ctx.Err())
		}
	}
}

// TestForwarding_Elicitation_RealBackend verifies a backend's mid-call
// elicitation/create is relayed to the downstream client and its response
// carried back into the tool result.
func TestForwarding_Elicitation_RealBackend(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), forwardingRealBackendTimeout)
	defer cancel()

	backendURL := startForwardingBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", true)

	res, err := dc.c.CallTool(ctx, mcpmcp.CallToolRequest{
		Params: mcpmcp.CallToolParams{Name: fwdElicitTool},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "elicitation must round-trip to the downstream client")
	require.Len(t, res.Content, 1)
	txt, ok := mcpmcp.AsTextContent(res.Content[0])
	require.True(t, ok)
	assert.Equal(t, "action=accept", txt.Text)

	// The downstream elicitation handler must have actually fired.
	select {
	case <-dc.elicited:
	default:
		t.Fatal("downstream elicitation handler was not invoked")
	}
}

// TestForwarding_Sampling_RealBackend verifies a backend's mid-call
// sampling/createMessage is relayed to the downstream client and the sampled
// message carried back into the tool result.
func TestForwarding_Sampling_RealBackend(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), forwardingRealBackendTimeout)
	defer cancel()

	backendURL := startForwardingBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", true)

	res, err := dc.c.CallTool(ctx, mcpmcp.CallToolRequest{
		Params: mcpmcp.CallToolParams{Name: fwdSampleTool},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "sampling must round-trip to the downstream client")
	require.Len(t, res.Content, 1)
	txt, ok := mcpmcp.AsTextContent(res.Content[0])
	require.True(t, ok)
	assert.Equal(t, "sampled="+fwdSampledSummary+" model="+fwdSampleModel, txt.Text)
}

// TestForwarding_Progress_RealBackend verifies a backend's mid-call
// notifications/progress is relayed to the downstream client, arriving before
// the tool result is read.
func TestForwarding_Progress_RealBackend(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), forwardingRealBackendTimeout)
	defer cancel()

	backendURL := startForwardingBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", true)

	res, err := dc.c.CallTool(ctx, mcpmcp.CallToolRequest{
		Params: mcpmcp.CallToolParams{Name: fwdProgressTool},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)

	n := dc.waitNotification(ctx, t, "notifications/progress")
	assert.Equal(t, fwdProgressToken, n.Params.AdditionalFields["progressToken"])
	assert.InDelta(t, 0.5, n.Params.AdditionalFields["progress"], 1e-9)
	assert.Equal(t, "halfway", n.Params.AdditionalFields["message"])
}

// TestForwarding_Logging_RealBackend verifies that vMCP requests debug logging
// from the backend (so it emits) and relays the backend's notifications/message
// to the downstream client, which has itself set a logging level.
func TestForwarding_Logging_RealBackend(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), forwardingRealBackendTimeout)
	defer cancel()

	backendURL := startForwardingBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", true)

	// notifications/message is delivered downstream only once the downstream
	// client has set a logging level.
	require.NoError(t, dc.c.SetLoggingLevel(ctx, mcpmcp.LoggingLevelDebug))

	res, err := dc.c.CallTool(ctx, mcpmcp.CallToolRequest{
		Params: mcpmcp.CallToolParams{Name: fwdLogTool},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)

	n := dc.waitNotification(ctx, t, "notifications/message")
	assert.Equal(t, "info", n.Params.AdditionalFields["level"])
	assert.Equal(t, fwdLogData, n.Params.AdditionalFields["data"])
}

// TestForwarding_Sampling_NoDownstreamCapability verifies the negative path:
// when the downstream client did not advertise sampling, the backend's
// sampling request fails cleanly (a failed tools/call) rather than hanging until
// the deadline.
func TestForwarding_Sampling_NoDownstreamCapability(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), forwardingRealBackendTimeout)
	defer cancel()

	backendURL := startForwardingBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", false)

	res, err := dc.c.CallTool(ctx, mcpmcp.CallToolRequest{
		Params: mcpmcp.CallToolParams{Name: fwdSampleTool},
	})

	// The call must resolve (error or IsError), never hang until the deadline.
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"sampling without a downstream handler must fail cleanly, not time out")
	if err == nil {
		assert.True(t, res.IsError, "backend sampling must fail when downstream lacks the capability")
	}
}

// TestForwarding_Elicitation_NoDownstreamCapability is the elicitation twin of
// TestForwarding_Sampling_NoDownstreamCapability: when the downstream client did
// not advertise elicitation, the backend's mid-call elicitation/create fails
// cleanly (a failed tools/call) rather than hanging until the deadline.
func TestForwarding_Elicitation_NoDownstreamCapability(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), forwardingRealBackendTimeout)
	defer cancel()

	backendURL := startForwardingBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)
	dc := newDownstreamClient(ctx, t, vmcpTS.URL+"/mcp", false)

	res, err := dc.c.CallTool(ctx, mcpmcp.CallToolRequest{
		Params: mcpmcp.CallToolParams{Name: fwdElicitTool},
	})

	// The call must resolve (error or IsError), never hang until the deadline.
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"elicitation without a downstream handler must fail cleanly, not time out")
	if err == nil {
		assert.True(t, res.IsError, "backend elicitation must fail when downstream lacks the capability")
	}
}

// samplingClient is a downstream client whose sampling handler returns a
// DISTINGUISHABLE summary+model and counts its own invocations, used to prove
// per-session isolation of forwarded server->client sampling.
type samplingClient struct {
	c           *client.Client
	sampleCalls atomic.Int32
}

// newSamplingClient connects a downstream client to vmcpURL whose sampling
// handler always answers with the given summary and model (so the tool result
// distinguishes which client's handler served the request) and increments a
// per-client counter.
func newSamplingClient(ctx context.Context, t *testing.T, vmcpURL, summary, model string) *samplingClient {
	t.Helper()

	sc := &samplingClient{}
	c, err := client.NewStreamableHttpClientWithOpts(
		vmcpURL,
		[]transport.StreamableHTTPCOption{transport.WithContinuousListening()},
		[]client.ClientOption{
			client.WithSamplingHandler(client.SamplingHandlerFunc(
				func(_ context.Context, _ mcpmcp.CreateMessageRequest) (*mcpmcp.CreateMessageResult, error) {
					sc.sampleCalls.Add(1)
					return &mcpmcp.CreateMessageResult{
						SamplingMessage: mcpmcp.SamplingMessage{
							Role:    mcpmcp.RoleAssistant,
							Content: mcpmcp.NewTextContent(summary),
						},
						Model:      model,
						StopReason: "endTurn",
					}, nil
				},
			)),
		},
	)
	require.NoError(t, err)
	sc.c = c

	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Initialize(ctx, mcpmcp.InitializeRequest{
		Params: mcpmcp.InitializeParams{
			ProtocolVersion: mcpmcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcpmcp.Implementation{Name: "downstream", Version: "1.0"},
		},
	})
	require.NoError(t, err)
	return sc
}

// TestForwarding_Sampling_RealBackend_SessionIsolation proves a backend's
// forwarded sampling request reaches the CALLING session, never another session
// on the same vMCP server. Two downstream clients A and B, each with a
// distinguishable sampling response, call the sampling tool on their OWN session;
// each tool result must reflect THAT client's own response, and each client's
// sampling handler must fire exactly once (for its own call).
//
// This test would FAIL if forwarding routed to the wrong session: if A's tool
// call's sampling request were relayed to B's session, A's tool result would show
// "summary-B" (mismatching the "summary-A" assertion) and the handler counters
// would be lopsided (A=0, B=2 instead of 1/1) — either check trips.
func TestForwarding_Sampling_RealBackend_SessionIsolation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), forwardingRealBackendTimeout)
	defer cancel()

	backendURL := startForwardingBackend(t)
	vmcpTS := newRealTestServer(t, backendURL)

	clientA := newSamplingClient(ctx, t, vmcpTS.URL+"/mcp", "summary-A", "model-A")
	clientB := newSamplingClient(ctx, t, vmcpTS.URL+"/mcp", "summary-B", "model-B")

	// Call concurrently so the two forwarded sampling round-trips are in flight at
	// the same time — the strongest check that each resolves to its own session.
	type callResult struct {
		res *mcpmcp.CallToolResult
		err error
	}
	resA := make(chan callResult, 1)
	resB := make(chan callResult, 1)
	go func() {
		r, err := clientA.c.CallTool(ctx, mcpmcp.CallToolRequest{
			Params: mcpmcp.CallToolParams{Name: fwdSampleTool},
		})
		resA <- callResult{r, err}
	}()
	go func() {
		r, err := clientB.c.CallTool(ctx, mcpmcp.CallToolRequest{
			Params: mcpmcp.CallToolParams{Name: fwdSampleTool},
		})
		resB <- callResult{r, err}
	}()

	outA := <-resA
	outB := <-resB
	require.NoError(t, outA.err)
	require.NoError(t, outB.err)
	require.False(t, outA.res.IsError)
	require.False(t, outB.res.IsError)

	txtA, ok := mcpmcp.AsTextContent(outA.res.Content[0])
	require.True(t, ok)
	txtB, ok := mcpmcp.AsTextContent(outB.res.Content[0])
	require.True(t, ok)

	// Each tool result must carry the CALLING client's own sampling response.
	assert.Equal(t, "sampled=summary-A model=model-A", txtA.Text,
		"client A's tool result must reflect A's own sampling response")
	assert.Equal(t, "sampled=summary-B model=model-B", txtB.Text,
		"client B's tool result must reflect B's own sampling response")

	// Each client's handler fired exactly once — for its own call only.
	assert.Equal(t, int32(1), clientA.sampleCalls.Load(),
		"client A's sampling handler must fire once, for A's own call")
	assert.Equal(t, int32(1), clientB.sampleCalls.Load(),
		"client B's sampling handler must fire once, for B's own call")
}
