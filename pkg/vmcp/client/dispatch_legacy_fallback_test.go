// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestDispatch_LegacyFallbackOnInconclusiveProbe pins the fix for the
// dispatch abort regressed by merge 885e8b14f: a Modern server/discover probe
// that comes back transient (HTTP 503, -> errModernTransient) must fall back
// to the Legacy initialize/tools-list path uncached, not fail the whole call.
// Caching the fallback would pin a genuinely Modern backend to Legacy past a
// transient outage, so the revision cache must remain unset afterward.
func TestDispatch_LegacyFallbackOnInconclusiveProbe(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Mcp-Method") != "" {
			// Modern probe: transient failure, discovered before the body is read.
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		body, err := readAll(t, r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req jsonRPCRequest
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"protocolVersion": "2024-11-05",
					"capabilities": {"tools": {}},
					"serverInfo": {"name": "flaky-probe", "version": "1.0.0"}
				}`),
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "tools/list":
			resp := jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: json.RawMessage(`{
					"tools": [{"name": "echo", "description": "echoes input"}]
				}`),
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  json.RawMessage(`{}`),
			})
		}
	}))
	t.Cleanup(srv.Close)

	// newProbeClient, not setRevision: the probe must actually run so the
	// Modern 503 is hit and the fallback path exercised.
	h := newProbeClient(t)
	target := &vmcp.BackendTarget{
		WorkloadID:    "flaky-probe",
		BaseURL:       srv.URL,
		TransportType: "streamable-http",
	}

	caps, err := h.ListCapabilities(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, caps.Tools, 1)
	assert.Equal(t, "echo", caps.Tools[0].Name)

	_, ok := h.cachedRevision("flaky-probe")
	assert.False(t, ok, "an inconclusive probe must not pin a revision")
}
