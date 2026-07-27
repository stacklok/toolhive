// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// Forwarding integration fixtures. These exercise the server->client forwarding
// cluster end-to-end: a real in-process backend, mid tools/call, drives
// elicitation/sampling/progress/logging back at its caller; vMCP must relay that
// traffic to the real downstream client on the same session. That session is
// necessarily Legacy (2025-11-25): the Modern revision removed server-initiated
// requests, so the downstream clients here are explicitly pinned to Legacy —
// see legacyPinningRoundTripper. The Modern-client behavior for the same
// eliciting backend is asserted separately in
// TestIntegration_Modern_RealBackend_ElicitingToolFailsCleanly.
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
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// legacyPinningRoundTripper pins a downstream mcpcompat client to the Legacy
// (2025-11-25) revision by answering its Modern-first server/discover probe
// with an HTTP 400 / JSON-RPC -32022 UnsupportedProtocolVersionError body
// (built with the production mcpparser.ClassificationErrorResponse so the
// fake cannot drift from the real error shape), which drives go-sdk's
// Connect down its documented fall-back-to-Legacy-initialize path. Every
// other request passes through to base untouched.
//
// MECHANISM, precisely: this is NOT what vMCP itself does. vMCP never rejects
// server/discover — the kill-switch (#5959) explicitly exempts it, so vMCP
// answers HTTP 200 with a discover RESULT whose supportedVersions omits
// 2026-07-28, and the go-sdk client negotiates down from that. The
// RoundTripper substitutes a -32022 rejection to force the same Legacy
// outcome deterministically, independent of what versions the server under
// test advertises.
//
// WHY PIN: these fixtures assert the Legacy-only server-initiated surface —
// mid-call elicitation/sampling requests and progress/logging notifications
// relayed over a live session. The 2026-07-28 revision removes server-
// initiated requests entirely (go-sdk's assertServerInitiatedRequestAllowed
// refuses them by protocol version; the replacement is client-polled
// multi-round retrieval, SEP-2322) and SEP-2577 deprecates sampling and
// logging outright. While the kill-switch is on, the version-omitting
// discover result pins these clients to Legacy incidentally; once it is
// removed (#6033) the go-sdk-based client would negotiate Modern and this
// surface would vanish mid-test — failing at connect on subscriptions/listen,
// not at the behavior under test. Pinning makes the tests' Legacy dependency
// explicit instead of incidental.
//
// The pin lives in the transport because mcpcompat documents it cannot set a
// protocol version (go-sdk's ClientSessionOptions.protocolVersion is
// unexported — see the LIMITATION note in mcpcompat/client and #5911).
// Replace this with a real client option if #5911 lands.
//
// LOAD-BEARING after #6033: once the kill-switch is gone, this RoundTripper
// is the ONLY thing keeping these downstream clients on Legacy. It is not
// leftover kill-switch scaffolding — deleting it silently flips every test
// in this file to Modern and voids what they assert. The intercepted counter
// (asserted after every connect) exists to catch exactly that: if go-sdk ever
// stops probing server/discover first, the pin becomes dead code and the
// counter assertion fails instead of the tests silently changing meaning.
type legacyPinningRoundTripper struct {
	base http.RoundTripper
	// intercepted counts server/discover probes answered with the -32022
	// rejection. Asserted > 0 after connect — proof the pin FIRED, not merely
	// that it exists.
	intercepted atomic.Int32
}

func (rt *legacyPinningRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.Body == nil {
		return rt.base.RoundTrip(req)
	}
	// Clone before touching anything: RoundTrippers must not modify the
	// caller's request beyond consuming the body (net/http contract; see also
	// the copy-before-mutating rule). The body is the one thing a routing
	// interceptor must read, so consume it from the original and give the
	// clone a fresh reader.
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Body = io.NopCloser(bytes.NewReader(body))
	var probe struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Method == "server/discover" {
		rt.intercepted.Add(1)
		return mcpparser.ClassificationErrorResponse(req, probe.ID, &mcpparser.UnsupportedVersionError{
			Requested: mcpparser.MCPVersionModern,
			Supported: []string{mcpparser.MCPVersionLegacy},
		}), nil
	}
	return rt.base.RoundTrip(req)
}

// newLegacyPinnedHTTPClient returns an *http.Client for
// transport.WithHTTPBasicClient that pins the mcpcompat client to Legacy,
// plus the RoundTripper itself so callers can assert the pin actually fired —
// see legacyPinningRoundTripper.
func newLegacyPinnedHTTPClient() (*http.Client, *legacyPinningRoundTripper) {
	rt := &legacyPinningRoundTripper{base: http.DefaultTransport}
	return &http.Client{Transport: rt}, rt
}

// requirePinFired asserts the Legacy pin intercepted at least one
// server/discover probe during connect. Without this, the pin is
// indistinguishable from dead code: on a kill-switch base the tests pass
// even with the RoundTripper deleted (the server's own version-omitting
// discover answer pins the client), and if go-sdk ever stopped probing
// server/discover first the tests would silently stop testing what they
// claim.
func requirePinFired(t *testing.T, rt *legacyPinningRoundTripper) {
	t.Helper()
	require.Positive(t, rt.intercepted.Load(),
		"Legacy pin never fired: the client did not probe server/discover; the pin may be dead code")
}

// downstreamClient builds a real mcpcompat client against the vMCP endpoint,
// wired for server->client traffic (elicitation/sampling handlers, continuous
// listening) and collecting forwarded notifications on the returned channel.
// It is pinned to the Legacy revision (see legacyPinningRoundTripper): the
// forwarded traffic it collects exists only on a Legacy session.
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

	hc, pinRT := newLegacyPinnedHTTPClient()
	c, err := client.NewStreamableHttpClientWithOpts(
		vmcpURL,
		[]transport.StreamableHTTPCOption{
			transport.WithContinuousListening(),
			transport.WithHTTPBasicClient(hc),
		},
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
	requirePinFired(t, pinRT)

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
//
// Legacy-pinned: a server-initiated elicitation/create toward the client does
// not exist on Modern (2026-07-28) — SEP-2322 replaces it with client-polled
// inputRequests, which vMCP deliberately does not serve (see the "unavailable
// to Modern clients" limitation in docs/arch/10-virtual-mcp-architecture.md).
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
//
// Legacy-pinned: like elicitation, server-initiated sampling does not exist on
// Modern (SEP-2322) — and SEP-2577 additionally deprecates the sampling
// feature itself as of 2026-07-28 (sanctioned replacement: direct LLM-provider
// integration), so this surface is Legacy-only by spec, not by omission.
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
//
// Legacy-pinned — but NOT because Modern lacks a progress channel. It does
// not: on Modern, progress remains spec-legal as a request-scoped
// notification riding the POST-initiated SSE response stream (SEP-2260
// tightened exactly that channel; progressToken is unchanged in the draft
// schema). What is missing is on vMCP's side: dispatchModern is single-shot —
// writeModernResult emits one JSON body and cannot stream — so nothing riding
// the per-request stream is deliverable to a Modern client today. That is a
// vMCP "streaming Modern dispatch" gap (future work), distinct from both MRTR
// and subscriptions/listen (whose subscribable set is fixed at four
// list-changed/subscription types and excludes progress).
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
//
// Legacy-pinned, for three stacked reasons — record them so this is not
// re-litigated as a vMCP bug:
//  1. Upstream go-sdk defect: SetLoggingLevel omits injectRequestMeta
//     (client.go:1303 in v1.7.0-pre.3; every other Modern-aware method
//     injects), so a Modern logging/setLevel carries the MCP-Protocol-Version
//     header but no _meta.protocolVersion. vMCP's ClassifyRevision CORRECTLY
//     rejects that with -32020; accepting the malformed request to make this
//     test pass would be worse than the failure. Filed upstream separately.
//  2. Even with (1) fixed, the RPC no longer exists on Modern: the per-request
//     logLevel _meta key REPLACES logging/setLevel (draft schema; go-sdk's
//     server answers methodSetLevel with -32601 under 2026-07-28, server.go
//     "method removed in the new protocol"). Delivery of notifications/message
//     for a request that does carry logLevel rides the per-request SSE
//     response stream the single-shot dispatcher cannot produce — the same
//     streaming gap as TestForwarding_Progress_RealBackend.
//  3. SEP-2577 deprecates the logging feature itself as of 2026-07-28
//     (sanctioned replacement: stderr / OpenTelemetry).
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
//
// Legacy-pinned even though it happens to pass on Modern: capability-gated
// failure on a live session only exists on Legacy. On Modern the same call
// fails with the sessionless -32603 REGARDLESS of what the client advertises
// (withHandlers makes no difference), so the lenient assertion here would be
// satisfied for a reason unrelated to the gating this test exists to
// exercise. The pin keeps it testing what its name says. (Verified against a
// working subscriptions/listen build: unpinned, this test passes vacuously.)
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

	// The call must resolve (error or IsError), never hang until the deadline —
	// and the failure must NAME the refused request, so this cannot go vacuous
	// by passing on an unrelated failure (e.g. a connect error).
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"sampling without a downstream handler must fail cleanly, not time out")
	assert.Contains(t, callFailureText(t, res, err), "sampling/createMessage",
		"the failure must name the refused sampling request, not an unrelated error")
}

// callFailureText extracts the human-readable failure text from a failed
// CallTool: the error message when the call errored, otherwise the text
// content of an IsError result. Fails the test if the call did not fail at
// all — callers use it precisely to assert WHAT failed.
func callFailureText(t *testing.T, res *mcpmcp.CallToolResult, err error) string {
	t.Helper()
	if err != nil {
		return err.Error()
	}
	require.NotNil(t, res)
	require.True(t, res.IsError, "the call must fail (error or IsError result)")
	var text string
	for _, c := range res.Content {
		if tc, ok := mcpmcp.AsTextContent(c); ok {
			text += tc.Text
		}
	}
	return text
}

// TestForwarding_Elicitation_NoDownstreamCapability is the elicitation twin of
// TestForwarding_Sampling_NoDownstreamCapability: when the downstream client did
// not advertise elicitation, the backend's mid-call elicitation/create fails
// cleanly (a failed tools/call) rather than hanging until the deadline.
// Legacy-pinned for the same vacuous-pass reason as its sampling twin above.
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

	// Same shape as the sampling twin: resolve promptly AND name the refused
	// request, closing the vacuous-pass route.
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"elicitation without a downstream handler must fail cleanly, not time out")
	assert.Contains(t, callFailureText(t, res, err), "elicitation/create",
		"the failure must name the refused elicitation request, not an unrelated error")
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
// per-client counter. Pinned to Legacy like newDownstreamClient — see
// legacyPinningRoundTripper.
func newSamplingClient(ctx context.Context, t *testing.T, vmcpURL, summary, model string) *samplingClient {
	t.Helper()

	sc := &samplingClient{}
	hc, pinRT := newLegacyPinnedHTTPClient()
	c, err := client.NewStreamableHttpClientWithOpts(
		vmcpURL,
		[]transport.StreamableHTTPCOption{
			transport.WithContinuousListening(),
			transport.WithHTTPBasicClient(hc),
		},
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
	requirePinFired(t, pinRT)
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
//
// Legacy-pinned for the same reasons as TestForwarding_Sampling_RealBackend:
// per-session routing of a server-initiated request presupposes sessions,
// which Modern removed along with the requests themselves.
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
