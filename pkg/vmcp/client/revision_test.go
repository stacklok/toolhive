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

	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
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
// request id.
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
			"resultType":   "complete",
			"capabilities": map[string]any{"tools": map[string]any{}, "completions": map[string]any{}},
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
			name: "clean 2xx discover -> Modern",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(discoverEnvelope(t, r))
			},
			wantRev: mcpparser.RevisionModern,
		},
		{
			name: "recognized Modern protocol error (-32022) -> Modern",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"unsupported version"}}`))
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

			rev, _, err := h.probeRevision(context.Background(), target)
			require.NoError(t, err)
			assert.Equal(t, tt.wantRev, rev)

			// The result is cached under the workload id.
			cached, ok := h.cachedRevision("b1")
			require.True(t, ok)
			assert.Equal(t, tt.wantRev, cached)
		})
	}
}

// TestProbeRevision_TimeoutFallsBackToLegacy verifies a dead backend (connection
// refused) classifies Legacy rather than erroring.
func TestProbeRevision_TimeoutFallsBackToLegacy(t *testing.T) {
	t.Parallel()

	// A server we immediately close: connections are refused.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "dead", BaseURL: url, TransportType: "streamable-http"}

	rev, caps, err := h.probeRevision(context.Background(), target)
	require.NoError(t, err)
	assert.Equal(t, mcpparser.RevisionLegacy, rev)
	assert.Nil(t, caps)
}

// TestListCapabilities_ModernServedFromCache verifies the cache: a Modern
// backend is probed once, and a second ListCapabilities is served from the
// cached revision (one discover round-trip, no re-probe fallback ladder).
func TestListCapabilities_ModernServedFromCache(t *testing.T) {
	t.Parallel()

	var discoverCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		discoverCalls.Add(1)
		assert.Equal(t, "server/discover", r.Header.Get("Mcp-Method"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(discoverEnvelope(t, r))
	}))
	t.Cleanup(srv.Close)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "modern", BaseURL: srv.URL, TransportType: "streamable-http"}

	caps1, err := h.ListCapabilities(context.Background(), target)
	require.NoError(t, err)
	require.NotNil(t, caps1)

	rev, ok := h.cachedRevision("modern")
	require.True(t, ok)
	assert.Equal(t, mcpparser.RevisionModern, rev)

	caps2, err := h.ListCapabilities(context.Background(), target)
	require.NoError(t, err)
	require.NotNil(t, caps2)

	// Step 2a is discover-only: enumerations are empty (Step 2b fills them).
	assert.Empty(t, caps2.Tools)

	// Two ListCapabilities calls => exactly two discover round-trips (one probe,
	// one cache-hit discover). If the cache were ignored, a re-probe would still
	// be two, so the real signal is that no OTHER method was ever called and the
	// backend never received an initialize handshake.
	assert.EqualValues(t, 2, discoverCalls.Load())
}
