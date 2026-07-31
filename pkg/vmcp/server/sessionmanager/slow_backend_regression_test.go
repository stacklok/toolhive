// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// slowBackendDelay is the per-request delay of the simulated slow-but-working
// backend. It stands in for the real 10-25s latency in #5861, scaled down so
// the suite stays fast while remaining orders of magnitude above the healthy
// backends' sub-millisecond responses.
const slowBackendDelay = 2 * time.Second

// slowBackendLatencyBudget is the ceiling on total CreateSession latency for a
// tenant containing one known-bad slow backend. It backs the secondary
// (latency) assertion; the primary assertion is the request counter, which is
// deterministic. Set well below slowBackendDelay: if session creation blocks on
// the slow backend at all, elapsed time lands at or above slowBackendDelay.
const slowBackendLatencyBudget = slowBackendDelay / 2

// startSlowMCPBackend starts an in-process MCP backend that delays every
// request by delay before responding. It is deliberately WORKING, not broken:
// #5861 is about a backend that is merely slow, which is why simulating it with
// a connection refusal or a 5xx would not reproduce the failure.
//
// The returned counter reports how many HTTP requests the backend received. It
// is the deterministic signal that the backend was (or was not) contacted:
// asserting it is zero proves the skip directly, where a latency assertion only
// infers it and is vulnerable to CI noise.
func startSlowMCPBackend(t *testing.T, backendID string, delay time.Duration) (*vmcp.Backend, *atomic.Int64) {
	t.Helper()
	mcpSrv := mcpserver.NewMCPServer(backendID, "1.0.0")
	mcpSrv.AddTool(
		mcpmcp.NewTool("slow_tool",
			mcpmcp.WithDescription("A tool on a slow-but-working backend"),
			mcpmcp.WithString("input", mcpmcp.Required()),
		),
		func(_ context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, _ := req.Params.Arguments.(map[string]any)
			input, _ := args["input"].(string)
			return &mcpmcp.CallToolResult{
				Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)},
			}, nil
		},
	)
	streamableSrv := mcpserver.NewStreamableHTTPServer(mcpSrv)
	var requests atomic.Int64
	mux := http.NewServeMux()
	// Count and delay every request, including the initialize handshake — the
	// delay is what makes this backend slow rather than broken.
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(delay)
		streamableSrv.ServeHTTP(w, r)
	}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return &vmcp.Backend{
		ID:            backendID,
		Name:          backendID,
		BaseURL:       ts.URL + "/mcp",
		TransportType: "streamable-http",
	}, &requests
}

// staticHealth is a health.StatusProvider stub returning fixed per-backend
// statuses, standing in for a live health.Monitor that has already classified
// the slow backend. #5861's premise is that the monitor ALREADY knows the
// backend is bad; the bug is that session creation never asks.
type staticHealth map[string]vmcp.BackendHealthStatus

func (s staticHealth) QueryBackendStatus(backendID string) (vmcp.BackendHealthStatus, bool) {
	status, ok := s[backendID]
	return status, ok
}

// newTestManagerWithHealth creates a Manager over backends with the given
// health.StatusProvider (nil = health monitoring disabled). Uses in-memory
// session storage; the health-gating tests do not exercise Redis.
func newTestManagerWithHealth(
	t *testing.T, backends []*vmcp.Backend, backendHealth health.StatusProvider,
) *Manager {
	t.Helper()
	backendList := make([]vmcp.Backend, len(backends))
	for i, b := range backends {
		backendList[i] = *b
	}
	registry := vmcp.NewImmutableRegistry(backendList)
	factory := vmcpsession.NewSessionFactory(newUnauthenticatedAuthRegistry(t))

	storage, err := transportsession.NewLocalSessionDataStorage(time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })

	sm, cleanup, err := New(
		storage,
		&FactoryConfig{Base: factory, BackendHealth: backendHealth},
		registry,
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup(context.Background())) })
	return sm
}

// ---------------------------------------------------------------------------
// Regression tests: #5861 — a slow backend must not inflate tenant session
// latency once the health monitor has classified it
// ---------------------------------------------------------------------------

// TestRegression_CreateSession_SkipsKnownBadSlowBackend pins the fix for #5861:
// a backend the health monitor has already classified as bad must not be
// re-attempted during per-session backend init.
//
// Before the fix, Manager.listAllBackends returned the registry unfiltered and
// session.makeBaseSession blocked on wg.Wait() for every backend, so a single
// slow backend set the floor for the whole tenant's initialize latency — a
// 10-25s backend against a ~30s client timeout is the reported coin flip.
// Health status gated capability aggregation (core.filterHealthyBackends) but
// never connection establishment, and establishment runs first.
//
// The primary assertion is the slow backend's request counter: zero requests
// proves the skip directly and deterministically. Elapsed time is asserted as a
// secondary signal — it is what the user actually experiences, but on its own it
// is vulnerable to CI noise (slow runners, GC pauses).
//
// The degraded case is the reason the fix cannot simply reuse
// core.filterHealthyBackends unchanged. The reported backend oscillated
// unhealthy -> degraded -> unhealthy continuously, because its 10-25s latency
// straddles the compiled-in 5s degraded threshold and 10s probe timeout
// (health/monitor.go) with no hysteresis. filterHealthyBackends INCLUDES
// degraded, so reusing that predicate verbatim would leave every degraded-phase
// session paying the full latency and the coin flip would survive the fix. See
// health.ShouldOpenSession.
func TestRegression_CreateSession_SkipsKnownBadSlowBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status vmcp.BackendHealthStatus
	}{
		{
			// The monitor has classified the backend unhealthy because its real
			// latency exceeds the probe timeout — the state the reporter observed.
			name:   "unhealthy",
			status: vmcp.BackendUnhealthy,
		},
		{
			// The other half of the flap: slow enough to be degraded, not slow
			// enough to be unhealthy.
			name:   "degraded",
			status: vmcp.BackendDegraded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fast := startMCPBackend(t, "backend-fast", "echo")
			slow, slowRequests := startSlowMCPBackend(t, "backend-slow", slowBackendDelay)

			sm := newTestManagerWithHealth(t, []*vmcp.Backend{fast, slow}, staticHealth{
				fast.ID: vmcp.BackendHealthy,
				slow.ID: tt.status,
			})

			start := time.Now()
			sessionID := createSession(t, sm, nil)
			elapsed := time.Since(start)

			require.NotEmpty(t, sessionID)
			assert.Zero(t, slowRequests.Load(),
				"session creation must not contact a backend already known %s "+
					"(#5861); it received %d request(s)", tt.status, slowRequests.Load())
			assert.Less(t, elapsed, slowBackendLatencyBudget,
				"session creation must not block on a backend already known %s; "+
					"took %v, budget %v — the slow backend's %v per-request delay is "+
					"on the critical path", tt.status, elapsed, slowBackendLatencyBudget,
				slowBackendDelay)
		})
	}
}

// TestRegression_CreateSession_HealthyBackendStillConnected guards against the
// fix over-filtering. Without it, the latency tests above would pass
// vacuously if the fix skipped every backend.
func TestRegression_CreateSession_HealthyBackendStillConnected(t *testing.T) {
	t.Parallel()

	fast := startMCPBackend(t, "backend-fast", "echo")
	sm := newTestManagerWithHealth(t, []*vmcp.Backend{fast}, staticHealth{
		fast.ID: vmcp.BackendHealthy,
	})

	sessionID := createSession(t, sm, nil)

	sess, ok := sm.GetMultiSession(context.Background(), sessionID)
	require.True(t, ok)
	require.NotNil(t, sess)
	assert.NotEmpty(t, sess.BackendSessions(),
		"a healthy backend must still hold a session — the health filter must "+
			"not drop everything")
}

// TestRegression_CreateSession_UnknownStatusIsAttempted pins that "not yet
// classified" never fails closed. #5861's fix must skip backends known to be
// bad, not backends nothing is known about yet.
//
// Two distinct paths produce Unknown, and both must still be attempted:
//
//   - Untracked (exists=false): the monitor has not created state for the
//     backend yet. The registry status is consulted, and the k8s workload
//     mapper returns BackendUnknown for a Pending phase
//     (workloads/k8s.go mapK8SWorkloadPhaseToHealth).
//   - Tracked as Unknown (exists=true): the monitor recorded a first failure
//     BELOW the unhealthy threshold, which stores status Unknown with a
//     non-zero failure count (health/status.go RecordFailure). Nothing is
//     confirmed yet — the default threshold needs 3 consecutive failures.
//
// Serving is not gated on the first health check completing (only the status
// reporter calls WaitForInitialHealthChecks), so sessions are created while
// backends are still Unknown. Failing closed there would connect a session to
// zero backends during the startup window — a regression against pre-#5861
// behaviour and a worse outcome than the bug being fixed.
func TestRegression_CreateSession_UnknownStatusIsAttempted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// monitored is the status the health monitor reports, or "" for a
		// backend the monitor does not track at all (exists=false).
		monitored vmcp.BackendHealthStatus
		tracked   bool
		// registryStatus is the status the backend carries in the registry.
		registryStatus vmcp.BackendHealthStatus
	}{
		{
			name:           "untracked by monitor, registry reports unknown (k8s Pending)",
			tracked:        false,
			registryStatus: vmcp.BackendUnknown,
		},
		{
			name:           "untracked by monitor, registry status unset",
			tracked:        false,
			registryStatus: "",
		},
		{
			name:           "tracked as unknown (first failure below unhealthy threshold)",
			monitored:      vmcp.BackendUnknown,
			tracked:        true,
			registryStatus: vmcp.BackendHealthy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			backend := startMCPBackend(t, "backend-pending", "echo")
			backend.HealthStatus = tt.registryStatus

			health := staticHealth{}
			if tt.tracked {
				health[backend.ID] = tt.monitored
			}

			sm := newTestManagerWithHealth(t, []*vmcp.Backend{backend}, health)
			sessionID := createSession(t, sm, nil)

			sess, ok := sm.GetMultiSession(context.Background(), sessionID)
			require.True(t, ok)
			require.NotNil(t, sess)
			assert.NotEmpty(t, sess.BackendSessions(),
				"a backend whose health is not yet established must still be "+
					"attempted — session establishment must not fail closed before "+
					"the health monitor has confirmed anything")
		})
	}
}

// TestRegression_CreateSession_NilHealthProviderConnectsAll pins that disabling
// health monitoring preserves the pre-fix behaviour exactly: every backend is
// attempted. A nil provider must not be read as "everything is unhealthy".
func TestRegression_CreateSession_NilHealthProviderConnectsAll(t *testing.T) {
	t.Parallel()

	fast := startMCPBackend(t, "backend-fast", "echo")
	sm := newTestManagerWithHealth(t, []*vmcp.Backend{fast}, nil)

	sessionID := createSession(t, sm, nil)

	sess, ok := sm.GetMultiSession(context.Background(), sessionID)
	require.True(t, ok)
	assert.NotEmpty(t, sess.BackendSessions(),
		"with health monitoring disabled every backend must be attempted")
}
