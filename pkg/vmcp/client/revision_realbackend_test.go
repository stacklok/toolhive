// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// newRealEchoServer stands up a real go-sdk v1.7-backed streamable-HTTP MCP
// server (via mcpcompat, the same SDK backends run in production) exposing a
// single "echo" tool that echoes back its "input" argument. stateless controls
// whether the server is built with mcpserver.WithStateless(true) — the same
// knob that determines whether the backend answers server/discover with
// 2026-07-28 in supportedVersions (Modern) or negotiates it away (Legacy); see
// TestProbeRevision_RealBackends.
func newRealEchoServer(
	t *testing.T, stateless bool, onCallTool func(mcpmcp.CallToolRequest), srvOpts ...mcpserver.ServerOption,
) *httptest.Server {
	t.Helper()

	mcpSrv := mcpserver.NewMCPServer("real-backend", "1.0.0", srvOpts...)
	mcpSrv.AddTool(
		mcpmcp.NewTool("echo",
			mcpmcp.WithDescription("Echoes the input back"),
			mcpmcp.WithString("input", mcpmcp.Required()),
		),
		func(_ context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			if onCallTool != nil {
				onCallTool(req)
			}
			args, _ := req.Params.Arguments.(map[string]any)
			input, _ := args["input"].(string)
			return &mcpmcp.CallToolResult{Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)}}, nil
		},
	)

	var opts []mcpserver.StreamableHTTPOption
	if stateless {
		opts = append(opts, mcpserver.WithStateless(true))
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewStreamableHTTPServer(mcpSrv, opts...))
	// httptest.NewServer binds an OS-assigned (random) port.
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// TestProbeRevision_RealBackends is the anti-regression pin for Fix 1: it
// exercises probeRevision against a REAL go-sdk v1.7 backend (not a hand-rolled
// httptest fake), stateful and stateless, so a future go-sdk change to
// server/discover's supportedVersions semantics fails HERE with a clear signal
// instead of surfacing as dozens of mystery failures elsewhere.
//
// A stateful streamable-HTTP server negotiates DOWN on server/discover (its
// supportedVersions excludes 2026-07-28 — see go-sdk's discover(), which
// requires WithStateless(true) to advertise the Modern revision), so it must
// classify Legacy. A stateless server advertises 2026-07-28 and must classify
// Modern.
func TestProbeRevision_RealBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		stateless bool
		wantRev   mcpparser.Revision
	}{
		{name: "stateful streamable-HTTP backend negotiates down -> Legacy", stateless: false, wantRev: mcpparser.RevisionLegacy},
		{name: "stateless streamable-HTTP backend advertises 2026-07-28 -> Modern", stateless: true, wantRev: mcpparser.RevisionModern},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := newRealEchoServer(t, tt.stateless, nil)
			h := newProbeClient(t)
			target := &vmcp.BackendTarget{
				WorkloadID:    "real-backend",
				WorkloadName:  "Real Backend",
				BaseURL:       srv.URL + "/mcp",
				TransportType: "streamable-http",
			}

			rev, err := h.probeRevision(context.Background(), target)
			require.NoError(t, err)
			assert.Equal(t, tt.wantRev, rev)

			cached, ok := h.cachedRevision(target.WorkloadID)
			require.True(t, ok)
			assert.Equal(t, tt.wantRev, cached)
		})
	}
}

// TestLegacyCallTool_StripsReservedMeta_RealBackend pins Fix 5 end-to-end
// against a real go-sdk v1.7 stateful (Legacy) backend: a legacy CallTool
// carrying both reserved io.modelcontextprotocol/* _meta keys (as a downstream
// Modern caller's request would) and a custom caller key must succeed — the
// reserved keys would otherwise make the real backend reject the request
// outright (HTTP 400: "protocol version ... is only supported on stateless
// HTTP servers") — and the backend must actually receive the custom key but
// NOT the reserved ones.
func TestLegacyCallTool_StripsReservedMeta_RealBackend(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		gotMeta map[string]any
		sawCall bool
	)
	srv := newRealEchoServer(t, false, func(req mcpmcp.CallToolRequest) {
		mu.Lock()
		defer mu.Unlock()
		sawCall = true
		if req.Params.Meta != nil {
			gotMeta = req.Params.Meta.AdditionalFields
		}
	})

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{
		WorkloadID:    "real-backend",
		WorkloadName:  "Real Backend",
		BaseURL:       srv.URL + "/mcp",
		TransportType: "streamable-http",
	}
	h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy)

	callerMeta := map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "downstream-modern-client", "version": "1.0.0"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		"io.modelcontextprotocol/logLevel":           "debug",
		"custom-caller-key":                          "custom-value",
	}

	res, err := h.CallTool(context.Background(), target, "echo", map[string]any{"input": "hello legacy"}, callerMeta, nil)
	require.NoError(t, err, "reserved Modern _meta must not leak onto the Legacy backend hop")
	require.Len(t, res.Content, 1)
	assert.Equal(t, "hello legacy", res.Content[0].Text)

	mu.Lock()
	defer mu.Unlock()
	require.True(t, sawCall, "the backend's tool handler must have been invoked")
	// Assert on the namespace, not a fixed list: this catches any reserved key,
	// including ones added to the fixture later. No passthroughMetaKeys entry is
	// in play here, so a blanket prefix check is exact.
	for k := range gotMeta {
		assert.False(t, strings.HasPrefix(k, mcpparser.ReservedMetaPrefix),
			"reserved _meta key %q must be stripped before the Legacy hop", k)
	}
	assert.Equal(t, "custom-value", gotMeta["custom-caller-key"], "non-reserved caller _meta must survive")
}

// stubClientNotifier is a minimal ClientNotifier for BindForwarders: the
// tools/call path only reads the bound requesters to decide WHICH forwarding
// handlers to install — the real backend here never notifies mid-call, so it is
// never invoked. (stubElicitationRequester/stubSamplingRequester live in
// client_test.go.)
type stubClientNotifier struct{}

func (stubClientNotifier) NotifyProgress(context.Context, vmcp.ProgressNotification) error {
	return nil
}
func (stubClientNotifier) NotifyLog(context.Context, vmcp.LogMessage) error { return nil }

// TestCallTool_MisCachedLegacy_ForwardingAgainstStatelessBackend pins the
// forwarding tools/call arm of the "stale Legacy cache is harmless" property
// (issue #5992), previously covered only for ListCapabilities
// (forwarding=false). A backend mis-cached Legacy that is genuinely
// stateless-Modern is queried through the FORWARDING path: forwarders are
// bound, so newStreamableHTTPClient enables transport.WithContinuousListening
// — the standalone server->client GET stream that, opened against a session-less
// backend, is exactly what made the POOLED session-factory connect fail in
// #6006 (subscriptions/listen -> "session not found").
//
// The per-call path survives because of WHERE the skip lives: go-sdk's
// streamableClientConn.sessionUpdated opens the standalone stream ONLY when the
// negotiated protocol version is < 2026-07-28 (mcp/streamable.go). On the
// mis-cached-Legacy hop the SDK's Modern-first client negotiates Modern off the
// backend, so the version gate suppresses the standalone stream entirely — no
// subscriptions/listen is ever attempted, Connect succeeds, and the call
// round-trips. legacyInit then flips the cache to Modern in-band, so the next
// call skips the legacy handshake.
//
// This is the behavior a regression in the version gate (or a shim change that
// opens the stream unconditionally) would silently break: the call would start
// failing only on the forwarding path, invisible to the ListCapabilities pin.
func TestCallTool_MisCachedLegacy_ForwardingAgainstStatelessBackend(t *testing.T) {
	t.Parallel()

	srv := newRealEchoServer(t, true, nil) // stateless => Modern

	h := newProbeClient(t)
	h.BindForwarders(&stubElicitationRequester{}, &stubSamplingRequester{}, stubClientNotifier{})

	target := &vmcp.BackendTarget{
		WorkloadID:    "real-backend",
		WorkloadName:  "Real Backend",
		BaseURL:       srv.URL + "/mcp",
		TransportType: "streamable-http",
	}
	h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy) // mis-cached

	res, err := h.CallTool(context.Background(), target, "echo", map[string]any{"input": "hello forwarding"}, nil, nil)
	require.NoError(t, err,
		"the negotiated-Modern version gate must suppress the standalone stream so the per-call forwarding path survives")
	require.Len(t, res.Content, 1)
	assert.Equal(t, "hello forwarding", res.Content[0].Text)

	// legacyInit flips the cache off the genuinely-negotiated Initialize result.
	rev, ok := h.cachedRevision(target.WorkloadID)
	require.True(t, ok)
	assert.Equal(t, mcpparser.RevisionModern, rev,
		"a successful mis-cached-Legacy call self-heals the cache to Modern")
}

// newRecordingEchoServer is a real go-sdk backend that advertises the logging
// capability and reports every JSON-RPC method it receives.
func newRecordingEchoServer(t *testing.T, onMethod func(method string)) *httptest.Server {
	t.Helper()

	backend := newRealEchoServer(t, false, nil, mcpserver.WithLogging())
	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)
	rp := httputil.NewSingleHostReverseProxy(backendURL)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr)
		r.Body = io.NopCloser(bytes.NewReader(body))

		var probe struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &probe)
		if probe.Method != "" && onMethod != nil {
			onMethod(probe.Method)
		}
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// newHintLyingServer emulates github-mcp-server v1.6.0's dual-era behaviour: a
// backend that negotiates 2026-07-28 on the Legacy initialize handshake while
// answering server/discover with a body that is NOT a valid Modern envelope
// (no resultType), which modernCall rejects as Legacy-shaped.
//
// It delegates everything except server/discover to a real stateless go-sdk
// backend -- which is what makes the handshake genuinely negotiate 2026-07-28
// rather than a hand-rolled approximation -- and substitutes the resultType-less
// discover body that produces the mismatch.
func newHintLyingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newHintLyingServerRecording(t, nil)
}

// newHintLyingServerRecording is newHintLyingServer with a hook invoked for
// every JSON-RPC method the backend receives.
func newHintLyingServerRecording(t *testing.T, onMethod func(method string)) *httptest.Server {
	t.Helper()

	// WithLogging so the backend advertises the logging capability: without it
	// enableBackendLogging returns before ever reaching the version check, and
	// a test asserting setLevel is not sent would pass vacuously.
	backend := newRealEchoServer(t, true, nil, mcpserver.WithLogging()) // stateless => negotiates 2026-07-28
	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)
	rp := httputil.NewSingleHostReverseProxy(backendURL)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr)
		r.Body = io.NopCloser(bytes.NewReader(body))

		if onMethod != nil {
			var probe struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &probe)
			if probe.Method != "" {
				onMethod(probe.Method)
			}
		}

		if !bytes.Contains(body, []byte(`"server/discover"`)) {
			rp.ServeHTTP(w, r)
			return
		}

		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(body, &req)
		id := req.ID
		if len(id) == 0 {
			id = json.RawMessage(`null`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{`+
			`"capabilities":{"tools":{}},`+
			`"supportedVersions":["2026-07-28","2025-11-25"],`+
			`"serverInfo":{"name":"hint-liar","version":"1.0.0"}}}`, id)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// TestLegacyInit_DoesNotPromoteOnHandshakeHintAlone pins #6154.
//
// The backend negotiates 2026-07-28 on the Legacy initialize -- the SDK client
// offers its own latest and the dual-era server accepts it -- while its
// server/discover answer is not a valid Modern envelope. github-mcp-server
// v1.6.0 behaves exactly this way in production.
//
// Promoting the cache to Modern on that handshake hint alone made the two
// self-corrections fight: probeRevision cached Legacy, legacyInit promoted back
// to Modern, the next Modern call failed on the non-Modern discover body and
// reclassified to Legacy, and so on. Every other health check failed, forever.
// The cached revision must therefore stay Legacy across repeated calls.
func TestLegacyInit_DoesNotPromoteOnHandshakeHintAlone(t *testing.T) {
	t.Parallel()

	srv := newHintLyingServer(t)
	h := newProbeClient(t)
	target := &vmcp.BackendTarget{
		WorkloadID:    "b",
		BaseURL:       srv.URL + "/mcp",
		TransportType: "streamable-http",
	}

	for i := range 3 {
		_, err := h.ListCapabilities(context.Background(), target)
		require.NoError(t, err, "ListCapabilities call %d", i)

		rev, ok := h.cachedRevision(target.WorkloadID)
		require.True(t, ok, "revision should be cached after call %d", i)
		assert.Equal(t, mcpparser.RevisionLegacy, rev,
			"cached revision must stay Legacy after call %d; flipping to Modern is the #6154 oscillation", i)
	}
}

// TestEnableBackendLogging_SkipsRemovedRPCOnModernSession pins the fix for
// tool calls dying against a dual-era backend.
//
// A backend that negotiates 2026-07-28 over the LEGACY initialize handshake
// leaves the session obliged to carry that version on every request. go-sdk
// still sends logging/setLevel as a Legacy-shaped call, so it arrives with a
// Modern protocol header and no Modern _meta -- a shape ToolHive's classifier
// rejects with -32020 by a deliberate, pinned contract (see
// TestIntegration_Modern_RealBackend_LoggingContract in pkg/vmcp/server).
// go-sdk treats that rejection as fatal, so the session closes and the tool
// call the logging was only meant to decorate dies with it.
//
// The RPC was removed in 2026-07-28, so issuing it on a session that
// negotiated that revision is the defect. Legacy sessions must still get it.
func TestEnableBackendLogging_SkipsRemovedRPCOnModernSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		negotiated       string
		wantSetLevelSent bool
	}{
		{
			name:             "modern negotiated: removed RPC is skipped",
			negotiated:       mcpparser.MCPVersionModern,
			wantSetLevelSent: false,
		},
		{
			name:             "legacy negotiated: RPC is still sent",
			negotiated:       mcpparser.MCPVersionLegacy,
			wantSetLevelSent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var mu sync.Mutex
			var sawSetLevel bool
			srv := newRecordingEchoServer(t, func(method string) {
				if method == "logging/setLevel" {
					mu.Lock()
					sawSetLevel = true
					mu.Unlock()
				}
			})

			h := newProbeClient(t)
			h.BindForwarders(&stubElicitationRequester{}, &stubSamplingRequester{}, stubClientNotifier{})
			target := &vmcp.BackendTarget{
				WorkloadID:    "b",
				BaseURL:       srv.URL + "/mcp",
				TransportType: "streamable-http",
			}

			c, err := h.clientFactory(context.Background(), target, true)
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })

			// The session must be live before SetLoggingLevel can reach the
			// wire; an unconnected client fails inside the SDK instead.
			_, _, err = h.legacyInit(context.Background(), c, target)
			require.NoError(t, err)

			caps := &mcpmcp.ServerCapabilities{Logging: &struct{}{}}
			h.enableBackendLogging(context.Background(), c, caps, tt.negotiated, target.WorkloadID)

			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tt.wantSetLevelSent, sawSetLevel)
		})
	}
}
