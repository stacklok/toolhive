// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// newProbeClient builds a real httpBackendClient with an unauthenticated
// registry so probeRevision's buildBackendRoundTripper succeeds.
func newProbeClient(t *testing.T) *httpBackendClient {
	t.Helper()
	reg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, reg.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, &strategies.UnauthenticatedStrategy{}))
	c, err := NewHTTPBackendClient(reg)
	require.NoError(t, err)
	return c.(*httpBackendClient)
}

// discoverEnvelope is a valid Modern server/discover success body echoing the
// request id, advertising supportedVersions that include 2026-07-28 (the
// authoritative Modern signal — see errModernNegotiatedDown).
func discoverEnvelope(t *testing.T, r *http.Request) []byte {
	t.Helper()
	body, _ := readAll(t, r)
	var req struct {
		ID any `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &req))
	out, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result": map[string]any{
			"resultType":        "complete",
			"capabilities":      map[string]any{"tools": map[string]any{}, "completions": map[string]any{}},
			"supportedVersions": []string{"2026-07-28", "2025-11-25"},
		},
	})
	require.NoError(t, err)
	return out
}

// TestProbeRevision_TruthTable exercises the Modern-first probe's classification:
// only a clean discover or a Modern-specific protocol error (-3202x) yields
// Modern; every other backend response falls back to Legacy.
func TestProbeRevision_TruthTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler http.HandlerFunc
		wantRev mcpparser.Revision
	}{
		{
			name: "clean 2xx discover, supportedVersions includes 2026-07-28 -> Modern",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(discoverEnvelope(t, r))
			},
			wantRev: mcpparser.RevisionModern,
		},
		{
			// Empirical probe bytes from a stateful 2025-11-25 backend: the go-sdk
			// v1.7 shim answers server/discover even though the backend negotiated
			// down to Legacy. supportedVersions — not a clean response alone — is
			// the authoritative Modern signal (SEP-2575; errModernNegotiatedDown).
			name: "clean 2xx discover, supportedVersions WITHOUT 2026-07-28 (stateful negotiate-down) -> Legacy",
			handler: func(w http.ResponseWriter, r *http.Request) {
				t.Helper()
				body, _ := readAll(t, r)
				var req struct {
					ID any `json:"id"`
				}
				require.NoError(t, json.Unmarshal(body, &req))
				out, err := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"resultType":        "complete",
						"capabilities":      map[string]any{"tools": map[string]any{}},
						"supportedVersions": []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"},
					},
				})
				require.NoError(t, err)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(out)
			},
			wantRev: mcpparser.RevisionLegacy,
		},
		{
			name: "clean 2xx discover, supportedVersions absent -> Legacy",
			handler: func(w http.ResponseWriter, r *http.Request) {
				t.Helper()
				body, _ := readAll(t, r)
				var req struct {
					ID any `json:"id"`
				}
				require.NoError(t, json.Unmarshal(body, &req))
				out, err := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result": map[string]any{
						"resultType":   "complete",
						"capabilities": map[string]any{"tools": map[string]any{}},
					},
				})
				require.NoError(t, err)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(out)
			},
			wantRev: mcpparser.RevisionLegacy,
		},
		{
			// -32022 (CodeUnsupportedProtocolVersion) alone does NOT prove Modern —
			// it means "I don't support the version you asked for", which a backend
			// negotiating down to Legacy also returns. Only when `data.supported`
			// still lists 2026-07-28 does it prove Modern (see the next case); an
			// absent data payload must classify Legacy, mirroring go-sdk's own
			// reference client falling back to a Legacy initialize here.
			name: "-32022 with no data.supported -> Legacy",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported version"}}`))
			},
			wantRev: mcpparser.RevisionLegacy,
		},
		{
			// -32022 whose data.supported still lists 2026-07-28 proves the peer is
			// Modern (it validated our Modern _meta and is merely picky about the
			// exact requested version), so it must classify Modern despite the error.
			name: "-32022 with data.supported including 2026-07-28 -> Modern",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported version",` +
					`"data":{"supported":["2026-07-28"],"requested":"2099-01-01"}}}`))
			},
			wantRev: mcpparser.RevisionModern,
		},
		{
			// A valid Modern envelope with a non-"complete" resultType proves the peer
			// is Modern (it decoded), so it must NOT fall back to Legacy.
			name: "input_required envelope -> Modern",
			handler: func(w http.ResponseWriter, r *http.Request) {
				id, _ := modernReq(t, r)
				writeModernResult(t, w, id, map[string]any{"resultType": "input_required"})
			},
			wantRev: mcpparser.RevisionModern,
		},
		{
			name: "discover -32601 (method not found) -> Legacy",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"method not found"}}`))
			},
			wantRev: mcpparser.RevisionLegacy,
		},
		{
			name: "400 session required (-32600) -> Legacy",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"session required"}}`))
			},
			wantRev: mcpparser.RevisionLegacy,
		},
		{
			name: "405 method not allowed -> Legacy",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			},
			wantRev: mcpparser.RevisionLegacy,
		},
		{
			name: "empty body -> Legacy",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
			},
			wantRev: mcpparser.RevisionLegacy,
		},
		{
			name: "200 with Legacy-shaped result (no resultType) -> Legacy",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`))
			},
			wantRev: mcpparser.RevisionLegacy,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)

			h := newProbeClient(t)
			target := &vmcp.BackendTarget{WorkloadID: "b1", BaseURL: srv.URL, TransportType: "streamable-http"}

			rev, err := h.probeRevision(context.Background(), target)
			require.NoError(t, err)
			assert.Equal(t, tt.wantRev, rev)

			// The result is cached under the workload id.
			cached, ok := h.cachedRevision("b1")
			require.True(t, ok)
			assert.Equal(t, tt.wantRev, cached)
		})
	}
}

// TestProbeRevision_SSEGate verifies an "sse" TransportType target classifies
// Legacy without ever attempting the Modern server/discover probe: TransportType
// == "sse" names the deprecated 2024-11-05 two-endpoint transport, which has no
// Modern endpoint to discover at this BaseURL (see probeRevision's doc comment).
// The handler fails the test if it is ever hit, proving no network call is made.
func TestProbeRevision_SSEGate(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("probeRevision must not make a network call for an sse target")
	}))
	t.Cleanup(srv.Close)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "sse-backend", BaseURL: srv.URL, TransportType: "sse"}

	rev, err := h.probeRevision(context.Background(), target)
	require.NoError(t, err)
	assert.Equal(t, mcpparser.RevisionLegacy, rev)

	cached, ok := h.cachedRevision(target.WorkloadID)
	require.True(t, ok)
	assert.Equal(t, mcpparser.RevisionLegacy, cached)
}

// TestProbeRevision_TransientLeavesUnprobed verifies a dead backend (connection
// refused) is INCONCLUSIVE: probeRevision returns the error uncached rather than
// caching Legacy, so a transient outage cannot poison the revision cache and the
// next call re-probes.
func TestProbeRevision_TransientLeavesUnprobed(t *testing.T) {
	t.Parallel()

	// A server we immediately close: connections are refused.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "dead", BaseURL: url, TransportType: "streamable-http"}

	_, err := h.probeRevision(context.Background(), target)
	require.Error(t, err)

	_, ok := h.cachedRevision("dead")
	assert.False(t, ok, "a transient probe failure must leave the backend unprobed")
}

// TestListCapabilities_ModernServedFromCache verifies the cache: a Modern
// backend is probed once, and a second ListCapabilities is served from the
// cached revision (discover + enumerate, never a Legacy initialize handshake).
func TestListCapabilities_ModernServedFromCache(t *testing.T) {
	t.Parallel()

	var initializeCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := modernReq(t, r)
		switch r.Header.Get("Mcp-Method") {
		case "server/discover":
			writeModernResult(t, w, id, map[string]any{
				"capabilities":      map[string]any{"tools": map[string]any{}},
				"supportedVersions": []string{"2026-07-28"},
			})
		case "tools/list":
			writeModernResult(t, w, id, map[string]any{
				"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}},
			})
		default:
			// Any non-Modern method (e.g. initialize) is a regression.
			initializeCalls.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "modern", BaseURL: srv.URL, TransportType: "streamable-http"}

	caps1, err := h.ListCapabilities(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, caps1.Tools, 1)

	rev, ok := h.cachedRevision("modern")
	require.True(t, ok)
	assert.Equal(t, mcpparser.RevisionModern, rev)

	// Second call is served via the cached Modern revision (discover+enumerate),
	// not a re-probe ladder, and still returns the enumerated tool.
	caps2, err := h.ListCapabilities(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, caps2.Tools, 1)
	assert.Equal(t, "echo", caps2.Tools[0].Name)

	assert.Zero(t, initializeCalls.Load(), "a Modern backend must never receive a Legacy initialize")
}
