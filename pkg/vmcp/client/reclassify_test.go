// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// TestIsRevisionMismatch is the narrow-predicate truth table.
func TestIsRevisionMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rev  mcpparser.Revision
		err  error
		want bool
	}{
		{"nil err", mcpparser.RevisionLegacy, nil, false},
		{"legacy INITIALIZE-step method-not-found -> mismatch", mcpparser.RevisionLegacy,
			fmt.Errorf("%w: %w", errLegacyInitFailed, mcp.ErrMethodNotFound), true},
		{"legacy INITIALIZE-step 4xx (ErrLegacySSEServer) -> mismatch", mcpparser.RevisionLegacy,
			fmt.Errorf("%w: %w", errLegacyInitFailed, transport.ErrLegacySSEServer), true},
		{"legacy DATA-PLANE method-not-found (no init marker) -> NOT mismatch", mcpparser.RevisionLegacy,
			fmt.Errorf("completion failed: %w", mcp.ErrMethodNotFound), false},
		{"modern wrong-era -> mismatch", mcpparser.RevisionModern,
			fmt.Errorf("wrap: %w", errWrongEra), true},
		{"modern legacy-response-body -> mismatch", mcpparser.RevisionModern,
			fmt.Errorf("wrap: %w", errLegacyResponseBody), true},
		{"modern data-plane method-not-found -> NOT mismatch", mcpparser.RevisionModern,
			fmt.Errorf("wrap: %w", mcp.ErrMethodNotFound), false},
		{"modern errLegacySSE (not a modern signal) -> NOT mismatch", mcpparser.RevisionModern,
			fmt.Errorf("wrap: %w", transport.ErrLegacySSEServer), false},
		{"auth: ErrUnauthorized -> NOT mismatch", mcpparser.RevisionLegacy, transport.ErrUnauthorized, false},
		{"auth: ErrAuthorizationRequired -> NOT mismatch", mcpparser.RevisionLegacy, transport.ErrAuthorizationRequired, false},
		{"auth: ErrUpstreamTokenNotFound -> NOT mismatch", mcpparser.RevisionLegacy, authtypes.ErrUpstreamTokenNotFound, false},
		{"auth: ErrAuthenticationFailed -> NOT mismatch", mcpparser.RevisionLegacy, vmcp.ErrAuthenticationFailed, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isRevisionMismatch(tt.rev, tt.err))
		})
	}
}

// modernDiscoverServer serves Modern server/discover (advertising tools) so a
// re-probe classifies Modern, and rejects Legacy requests (no Mcp-Method header)
// with 404 so a Legacy initialize surfaces transport.ErrLegacySSEServer.
func modernDiscoverServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.Header.Get("Mcp-Method")
		if method == "" {
			w.WriteHeader(http.StatusNotFound) // Legacy initialize -> ErrLegacySSEServer
			return
		}
		id, _ := modernReq(t, r)
		switch method {
		case "server/discover":
			writeModernResult(t, w, id, map[string]any{"capabilities": map[string]any{"tools": map[string]any{}}})
		case "tools/list":
			writeModernResult(t, w, id, map[string]any{
				"tools": []any{map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}}},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDispatch_ReclassifyAndRetry: a backend mis-cached Legacy but actually
// Modern flips after one re-probe and the retry succeeds.
func TestDispatch_ReclassifyAndRetry(t *testing.T) {
	t.Parallel()

	srv := modernDiscoverServer(t)
	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}
	h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy) // mis-cached

	var attempts []mcpparser.Revision
	err := h.dispatch(context.Background(), target, func(_ context.Context, rev mcpparser.Revision) error {
		attempts = append(attempts, rev)
		if rev == mcpparser.RevisionLegacy {
			// Legacy initialize-step rejection (carries the init marker).
			return fmt.Errorf("%w: %w", errLegacyInitFailed,
				wrapBackendError(transport.ErrLegacySSEServer, target.WorkloadID, "initialize client"))
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []mcpparser.Revision{mcpparser.RevisionLegacy, mcpparser.RevisionModern}, attempts,
		"exactly one retry, under the flipped revision")

	rev, _ := h.cachedRevision(target.WorkloadID)
	assert.Equal(t, mcpparser.RevisionModern, rev, "cache updated to the corrected revision")
}

// TestDispatch_NoRetryOnDataPlaneMethodNotFound: a genuine method-not-found on a
// correctly-classified Modern backend must not trigger a re-probe.
func TestDispatch_NoRetryOnDataPlaneMethodNotFound(t *testing.T) {
	t.Parallel()

	srv := modernDiscoverServer(t)
	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}
	h.setRevision(target.WorkloadID, mcpparser.RevisionModern)

	attempts := 0
	err := h.dispatch(context.Background(), target, func(_ context.Context, _ mcpparser.Revision) error {
		attempts++
		return fmt.Errorf("tool missing: %w", mcp.ErrMethodNotFound)
	})
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "data-plane method-not-found must not retry")
}

// TestDispatch_NoInfiniteLoop: even if fn always fails with a mismatch and the
// re-probe flips the era, fn runs at most twice.
func TestDispatch_NoInfiniteLoop(t *testing.T) {
	t.Parallel()

	// This server rejects everything (no Modern discover), so a re-probe from a
	// Modern cache classifies Legacy — a flip that would loop without the guard.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}
	h.setRevision(target.WorkloadID, mcpparser.RevisionModern)

	attempts := 0
	err := h.dispatch(context.Background(), target, func(_ context.Context, _ mcpparser.Revision) error {
		attempts++
		return fmt.Errorf("always wrong era: %w", errWrongEra)
	})
	require.Error(t, err)
	assert.Equal(t, 2, attempts, "one original + at most one retry, never a loop")
}

// TestDispatch_NoDoubleExecOnLegacyBody: a lenient Legacy backend that EXECUTES a
// Modern tools/call and returns a Legacy-shaped 200 body must NOT be retried
// (no double-execution), but the cache is still flipped to the corrected era.
func TestDispatch_NoDoubleExecOnLegacyBody(t *testing.T) {
	t.Parallel()

	var toolCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := modernReq(t, r)
		if r.Header.Get("Mcp-Method") == "tools/call" {
			toolCalls.Add(1)
			// Legacy-shaped success: JSON-RPC result WITHOUT resultType (backend executed it).
			w.Header().Set("Content-Type", "application/json")
			out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id,
				"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}}})
			_, _ = w.Write(out)
			return
		}
		// server/discover during re-probe: reject so the backend classifies Legacy.
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}
	h.setRevision(target.WorkloadID, mcpparser.RevisionModern) // mis-cached Modern

	_, err := h.CallTool(context.Background(), target, "echo", map[string]any{"input": "x"}, nil)
	require.Error(t, err, "a Legacy-shaped body must surface as an error, not a blank success")
	assert.EqualValues(t, 1, toolCalls.Load(), "the side-effecting tool must run exactly once (no double-exec)")

	rev, _ := h.cachedRevision(target.WorkloadID)
	assert.Equal(t, mcpparser.RevisionLegacy, rev, "cache flipped for future calls despite no retry")
}

// TestDispatch_NoReprobeOnLegacyDataPlaneMethodNotFound: a data-plane -32601 from
// a genuine Legacy backend (a legitimately unimplemented method) must not
// reclassify or re-probe.
func TestDispatch_NoReprobeOnLegacyDataPlaneMethodNotFound(t *testing.T) {
	t.Parallel()

	// This server WOULD classify Modern if probed — so if a re-probe wrongly
	// fired, the cache would flip to Modern. It must stay Legacy.
	srv := modernDiscoverServer(t)
	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}
	h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy)

	attempts := 0
	err := h.dispatch(context.Background(), target, func(_ context.Context, _ mcpparser.Revision) error {
		attempts++
		// Data-plane -32601 (no errLegacyInitFailed marker), e.g. completion/complete.
		return fmt.Errorf("completion failed: %w", mcp.ErrMethodNotFound)
	})
	require.Error(t, err)
	assert.Equal(t, 1, attempts, "data-plane -32601 must not retry")

	rev, _ := h.cachedRevision(target.WorkloadID)
	assert.Equal(t, mcpparser.RevisionLegacy, rev, "data-plane -32601 must not re-probe/reclassify")
}

// TestDispatch_TransientDoesNotReclassify verifies an auth blip or transient
// outage on a cached-Modern backend neither retries nor flips the cache to Legacy
// (F1: status-blind errWrongEra used to poison the cache here).
func TestDispatch_TransientDoesNotReclassify(t *testing.T) {
	t.Parallel()

	for _, blip := range []error{errModernAuth, errModernTransient} {
		blip := blip
		t.Run(blip.Error(), func(t *testing.T) {
			t.Parallel()

			// This server WOULD classify Modern if re-probed — proving no re-probe fired.
			srv := modernDiscoverServer(t)
			h := newProbeClient(t)
			target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}
			h.setRevision(target.WorkloadID, mcpparser.RevisionModern)

			attempts := 0
			err := h.dispatch(context.Background(), target, func(_ context.Context, _ mcpparser.Revision) error {
				attempts++
				return fmt.Errorf("%w: HTTP blip", blip)
			})
			require.Error(t, err)
			assert.Equal(t, 1, attempts, "a transient/auth blip must not retry")

			rev, ok := h.cachedRevision(target.WorkloadID)
			require.True(t, ok)
			assert.Equal(t, mcpparser.RevisionModern, rev, "a blip must not flip a Modern backend to Legacy")
		})
	}
}

// TestReclassify_WarnsOnlyOnActualChange captures slog to confirm the WARN (which
// gates the reclassification counter in the same branch) fires only when the
// revision actually changes.
func TestReclassify_WarnsOnlyOnActualChange(t *testing.T) {
	// Not parallel: swaps the global slog default.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := modernDiscoverServer(t) // re-probe classifies Modern
	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}

	// prev=Legacy -> re-probe Modern: change, must WARN.
	got := h.reclassify(context.Background(), target, mcpparser.RevisionLegacy)
	assert.Equal(t, mcpparser.RevisionModern, got)
	assert.Contains(t, buf.String(), "reclassified", "a real change must WARN")

	// prev=Modern -> re-probe Modern: no change, must be silent.
	buf.Reset()
	got = h.reclassify(context.Background(), target, mcpparser.RevisionModern)
	assert.Equal(t, mcpparser.RevisionModern, got)
	assert.Empty(t, buf.String(), "a no-op re-probe must not WARN")
}

// TestListCapabilities_SelfCorrectsMisCachedBackend: a backend pinned Legacy by a
// stale cache recovers on the next ListCapabilities via one re-probe.
func TestListCapabilities_SelfCorrectsMisCachedBackend(t *testing.T) {
	t.Parallel()

	srv := modernDiscoverServer(t)
	h := newProbeClient(t)
	target := &vmcp.BackendTarget{WorkloadID: "b", BaseURL: srv.URL, TransportType: "streamable-http"}
	h.setRevision(target.WorkloadID, mcpparser.RevisionLegacy) // mis-cached

	caps, err := h.ListCapabilities(context.Background(), target)
	require.NoError(t, err)
	require.Len(t, caps.Tools, 1, "Modern enumeration recovered after re-probe")
	assert.Equal(t, "echo", caps.Tools[0].Name)

	rev, _ := h.cachedRevision(target.WorkloadID)
	assert.Equal(t, mcpparser.RevisionModern, rev)
}
