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

	"github.com/alicebob/miniredis/v2"
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

// healthStub is a health.StatusProvider returning fixed per-backend statuses,
// standing in for a live health.Monitor that has already classified the slow
// backend. #5861's premise is that the monitor ALREADY knows the backend is bad;
// the bug is that session creation never asks.
//
// Named to avoid shadowing the imported health package at call sites.
type healthStub map[string]vmcp.BackendHealthStatus

func (s healthStub) QueryBackendStatus(backendID string) (vmcp.BackendHealthStatus, bool) {
	status, ok := s[backendID]
	return status, ok
}

// newTestManagerWithSharedStorageAndHealth is newTestManagerWithSharedStorage
// with a health.StatusProvider wired, for tests that need the Redis-backed
// restore path (a fresh Manager over shared storage has an empty cache, so
// GetMultiSession restores rather than serving from cache).
func newTestManagerWithSharedStorageAndHealth(
	t *testing.T,
	storage transportsession.DataStorage,
	backends []*vmcp.Backend,
	backendHealth health.StatusProvider,
) *Manager {
	t.Helper()
	backendList := make([]vmcp.Backend, len(backends))
	for i, b := range backends {
		backendList[i] = *b
	}
	registry := vmcp.NewImmutableRegistry(backendList)
	factory := vmcpsession.NewSessionFactory(newUnauthenticatedAuthRegistry(t))

	sm, cleanup, err := New(
		storage,
		&FactoryConfig{Base: factory, BackendHealth: backendHealth},
		registry,
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cleanup(context.Background())) })
	return sm
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
// Before the fix, the new-session backend list was the registry unfiltered and
// session.makeBaseSession blocked on wg.Wait() for every backend, so a single
// slow backend set the floor for the whole tenant's initialize latency — a
// 10-25s backend against a ~30s client timeout is the reported coin flip.
// Health status gated capability aggregation (core.filterHealthyBackends) but
// never connection establishment, and establishment runs first.
//
// Both halves are asserted over the SAME Manager: the bad backend receives zero
// requests AND the healthy peer still holds a session. Asserting only the former
// would pass if the fix skipped everything, which is the failure mode a
// health-gating change is most likely to introduce.
//
// The request counter is the whole assertion — there is deliberately no
// wall-clock latency bound. Elapsed time only infers the skip, and a ceiling
// tight enough to be meaningful is also tight enough to flake under -race on a
// loaded CI runner.
//
// Degraded is NOT covered here because it is not skipped: see
// health.ShouldOpenSession for why (three producers, only one of which is
// latency) and TestRegression_CreateSession_DegradedIsAttempted.
func TestRegression_CreateSession_SkipsKnownBadSlowBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status vmcp.BackendHealthStatus
	}{
		{
			// The state the reporter observed: real latency exceeds the probe
			// timeout, so checks fail until the unhealthy threshold is reached.
			name:   "unhealthy",
			status: vmcp.BackendUnhealthy,
		},
		{
			// Terminal until an operator intervenes; connecting can only burn
			// session-establishment latency.
			name:   "unauthenticated",
			status: vmcp.BackendUnauthenticated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fast := startMCPBackend(t, "backend-fast", "echo")
			slow, slowRequests := startSlowMCPBackend(t, "backend-slow", slowBackendDelay)

			sm := newTestManagerWithHealth(t, []*vmcp.Backend{fast, slow}, healthStub{
				fast.ID: vmcp.BackendHealthy,
				slow.ID: tt.status,
			})

			sessionID := createSession(t, sm, nil)
			require.NotEmpty(t, sessionID)

			assert.Zero(t, slowRequests.Load(),
				"session creation must not contact a backend already known %s "+
					"(#5861); it received %d request(s)", tt.status, slowRequests.Load())

			sess, ok := sm.GetMultiSession(context.Background(), sessionID)
			require.True(t, ok)
			require.NotNil(t, sess)
			assert.Contains(t, sess.BackendSessions(), fast.ID,
				"the healthy backend must still hold a session — skipping the %s "+
					"backend must not drop its healthy peer", tt.status)
		})
	}
}

// TestRegression_CreateSession_DegradedIsAttempted pins that degraded backends
// are still connected, which is the deliberate limit on #5861's fix.
//
// vmcp.BackendDegraded has three producers and only one is about latency. The
// "recovering" producer (statusTracker.RecordSuccess) forces degraded on ANY
// success recorded while consecutiveFailures > 0, overriding the health check's
// own verdict — so a backend that just answered in microseconds carries degraded
// for up to one CheckInterval. Skipping degraded would exclude that fast, working
// backend from every session created in the window right after it recovers.
//
// See health.ShouldOpenSession. If a degradation reason is ever recorded
// alongside the status, the latency producer alone could be skipped and this test
// should be revisited.
func TestRegression_CreateSession_DegradedIsAttempted(t *testing.T) {
	t.Parallel()

	backend := startMCPBackend(t, "backend-degraded", "echo")
	sm := newTestManagerWithHealth(t, []*vmcp.Backend{backend}, healthStub{
		backend.ID: vmcp.BackendDegraded,
	})

	sessionID := createSession(t, sm, nil)
	sess, ok := sm.GetMultiSession(context.Background(), sessionID)
	require.True(t, ok)
	require.NotNil(t, sess)
	assert.Contains(t, sess.BackendSessions(), backend.ID,
		"a degraded backend must still be attempted: degraded also means "+
			"'recovering' and 'auth retrying', neither of which is slow")
}

// TestRegression_CreateSession_MonitorMissHonoursRegistryStatus pins the
// resolution order in Manager.shouldOpenSession: when the monitor does not track
// a backend, the registry's own status decides.
//
// Both directions are covered, because a test suite that only exercises
// admitted statuses would still pass with the registry fallback deleted
// entirely — the zero value is admitted too.
//
//   - Untracked + registry says unhealthy: must SKIP. This is the case the
//     fallback exists for; a k8s workload already reported unhealthy is honoured
//     before the monitor has caught up.
//   - Untracked + registry says unknown / unset: must ATTEMPT. "Not yet
//     classified" fails open, or a cold monitor strands sessions with zero
//     backends during pod startup.
//   - Tracked as unknown: must ATTEMPT. The monitor stores Unknown with a
//     non-zero failure count for a first failure below the unhealthy threshold
//     (statusTracker.RecordFailure); nothing is confirmed until the threshold
//     (default 3) is reached. The registry value is deliberately different here
//     to prove the monitor's answer wins when it has one.
func TestRegression_CreateSession_MonitorMissHonoursRegistryStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// monitored is the status the health monitor reports; only consulted
		// when tracked is true.
		monitored vmcp.BackendHealthStatus
		tracked   bool
		// registryStatus is the status the backend carries in the registry.
		registryStatus vmcp.BackendHealthStatus
		wantConnected  bool
	}{
		{
			name:           "untracked by monitor, registry reports unhealthy",
			tracked:        false,
			registryStatus: vmcp.BackendUnhealthy,
			wantConnected:  false,
		},
		{
			name:           "untracked by monitor, registry reports unknown (k8s Pending)",
			tracked:        false,
			registryStatus: vmcp.BackendUnknown,
			wantConnected:  true,
		},
		{
			name:           "untracked by monitor, registry status unset",
			tracked:        false,
			registryStatus: "",
			wantConnected:  true,
		},
		{
			name:           "tracked as unknown overrides an unhealthy registry status",
			monitored:      vmcp.BackendUnknown,
			tracked:        true,
			registryStatus: vmcp.BackendUnhealthy,
			wantConnected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			backend := startMCPBackend(t, "backend-pending", "echo")
			backend.HealthStatus = tt.registryStatus

			stub := healthStub{}
			if tt.tracked {
				stub[backend.ID] = tt.monitored
			}

			sm := newTestManagerWithHealth(t, []*vmcp.Backend{backend}, stub)
			sessionID := createSession(t, sm, nil)

			sess, ok := sm.GetMultiSession(context.Background(), sessionID)
			require.True(t, ok)
			require.NotNil(t, sess)
			if tt.wantConnected {
				assert.Contains(t, sess.BackendSessions(), backend.ID,
					"backend must be attempted: monitor tracked=%v status=%q, "+
						"registry status %q", tt.tracked, tt.monitored, tt.registryStatus)
			} else {
				assert.NotContains(t, sess.BackendSessions(), backend.ID,
					"backend must be skipped: monitor tracked=%v, registry status %q "+
						"is confirmed-bad", tt.tracked, tt.registryStatus)
			}
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

// TestRegression_RestoreSession_IsNotHealthFiltered pins that health gating is
// confined to NEW sessions and never narrows a restored one.
//
// RestoreSession intersects the offered backend list with the session's stored
// backend IDs (session.RestoreSession), so filtering the list on the restore path
// would not defer a connection — it would DROP a backend the session already
// held. Because the routing table is rebuilt from whatever reconnects, that
// backend would stay gone for the rest of the session's life even after it
// recovered, since nothing reconnects an established session's backends.
//
// The backend is healthy when the session is created and unhealthy when it is
// restored, which is exactly the transition that would silently shrink a live
// session's capabilities.
func TestRegression_RestoreSession_IsNotHealthFiltered(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	storage := newSharedRedisStorage(t, mr)
	backend := startMCPBackend(t, "backend-alpha", "echo")

	// Healthy at creation: the session connects and stores the backend ID.
	smWriter := newTestManagerWithSharedStorageAndHealth(t, storage,
		[]*vmcp.Backend{backend}, healthStub{backend.ID: vmcp.BackendHealthy})
	sessionID := createSession(t, smWriter, nil)

	// Unhealthy at restore. A fresh manager has an empty cache, so
	// GetMultiSession takes the restore path.
	smReader := newTestManagerWithSharedStorageAndHealth(t, storage,
		[]*vmcp.Backend{backend}, healthStub{backend.ID: vmcp.BackendUnhealthy})

	sess, ok := smReader.GetMultiSession(t.Context(), sessionID)
	require.True(t, ok)
	require.NotNil(t, sess)
	assert.Contains(t, sess.BackendSessions(), backend.ID,
		"restore must not drop a backend the session already held, even when "+
			"health now reports it unhealthy — the drop would be permanent for "+
			"this session")
	assert.NotEmpty(t, sess.Tools(),
		"the restored session's routing table must still carry the backend's tools")
}
