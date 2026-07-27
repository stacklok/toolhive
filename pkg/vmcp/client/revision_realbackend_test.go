// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"net/http"
	"net/http/httptest"
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
func newRealEchoServer(t *testing.T, stateless bool, onCallTool func(mcpmcp.CallToolRequest)) *httptest.Server {
	t.Helper()

	mcpSrv := mcpserver.NewMCPServer("real-backend", "1.0.0")
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

	res, err := h.CallTool(context.Background(), target, "echo", map[string]any{"input": "hello legacy"}, callerMeta)
	require.NoError(t, err, "reserved Modern _meta must not leak onto the Legacy backend hop")
	require.Len(t, res.Content, 1)
	assert.Equal(t, "hello legacy", res.Content[0].Text)

	mu.Lock()
	defer mu.Unlock()
	require.True(t, sawCall, "the backend's tool handler must have been invoked")
	for _, k := range mcpparser.ReservedModernMetaKeys {
		assert.NotContains(t, gotMeta, k, "reserved Modern _meta key %q must be stripped before the Legacy hop", k)
	}
	assert.Equal(t, "custom-value", gotMeta["custom-caller-key"], "non-reserved caller _meta must survive")
}
