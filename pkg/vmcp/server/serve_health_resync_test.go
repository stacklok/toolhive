// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// TestResyncSessionsOnBackendHealthChange_ResyncsLiveSession verifies (#5786)
// that a backend health change fans out to a registered session: its tool
// store is REPLACED with the freshly core-derived set (gaining a recovered
// backend's tool and dropping a failed backend's tool) and the capability
// cache is invalidated exactly once, by the listener — the per-run purge is
// skipped on the health path (the cache key already varies with the
// health-filtered backend-ID set), so sibling sessions can share the entry
// the first sweep repopulates.
func TestResyncSessionsOnBackendHealthChange_ResyncsLiveSession(t *testing.T) {
	t.Parallel()

	// The core now advertises the recovered backend's tool; the tool of the
	// backend that dropped out is gone.
	fc := &fakeCore{tools: []vmcp.Tool{{Name: "kept"}, {Name: "recovered"}}}
	srv := &Server{
		core:           fc,
		vmcpSessionMgr: &stubSessionManager{alive: true},
		resyncBaseCtx:  context.Background(),
	}

	sess := &fakeToolsSession{id: "sess-1", tools: map[string]server.ServerTool{
		"kept":   {Tool: mcp.Tool{Name: "kept"}},
		"failed": {Tool: mcp.Tool{Name: "failed"}}, // must disappear after resync
	}}
	_, toolsWorker := srv.buildListChangedSink("sess-1", sess, nil, nil)
	srv.healthResync.add("sess-1", toolsWorker)

	srv.resyncSessionsOnBackendHealthChange(1)

	require.Eventually(t, func() bool { return sess.setToolsCalls() > 0 },
		2*time.Second, 10*time.Millisecond, "health change must resync the registered session's tools")
	got := sess.GetSessionTools()
	assert.Contains(t, got, "kept")
	assert.Contains(t, got, "recovered", "the recovered backend's tool must be gained")
	assert.NotContains(t, got, "failed", "the failed backend's tool must be dropped")
	assert.Equal(t, int32(1), fc.invalidateCacheCalls.Load(),
		"the fan-out must purge the capability cache exactly once per delivery — in the listener, not per session run")
}

// TestResyncSessionsOnBackendHealthChange_PurgesOncePerDelivery verifies the
// purge economics of the fan-out: one delivery purges the shared capability
// cache once, no matter how many live sessions it triggers — N per-run purges
// would each evict what a sibling session's sweep just repopulated, forcing N
// full backend sweeps where later same-identity sessions could share one.
func TestResyncSessionsOnBackendHealthChange_PurgesOncePerDelivery(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv := &Server{
		core:           fc,
		vmcpSessionMgr: &stubSessionManager{alive: true},
		resyncBaseCtx:  context.Background(),
	}

	sessions := []*fakeToolsSession{{id: "sess-1"}, {id: "sess-2"}, {id: "sess-3"}}
	for _, sess := range sessions {
		_, toolsWorker := srv.buildListChangedSink(sess.id, sess, nil, nil)
		srv.healthResync.add(sess.id, toolsWorker)
	}

	srv.resyncSessionsOnBackendHealthChange(1)

	for _, sess := range sessions {
		sess := sess
		require.Eventually(t, func() bool { return sess.setToolsCalls() > 0 },
			2*time.Second, 10*time.Millisecond, "every registered session must be resynced")
	}
	assert.Equal(t, int32(1), fc.invalidateCacheCalls.Load(),
		"one delivery must purge once, not once per session")
}

// setToolsCalls returns the fake's SetSessionTools call count under its lock,
// for assertions that run concurrently with a live resync worker (a bare field
// read would race with the worker's SetSessionTools).
func (f *fakeToolsSession) setToolsCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setSessionToolsCalls
}

// gatedSessionManager is a stubSessionManager whose GetMultiSession blocks on
// gate (a closed channel unblocks all callers), letting the coalescing test
// deterministically hold the first resync in flight while further deliveries
// arrive.
type gatedSessionManager struct {
	stubSessionManager
	gate  chan struct{}
	calls atomic.Int32
}

func (m *gatedSessionManager) GetMultiSession(context.Context, string) (vmcpsession.MultiSession, bool) {
	m.calls.Add(1)
	<-m.gate
	return nil, true
}

// TestResyncSessionsOnBackendHealthChange_CoalescesBurst verifies a burst of
// health-change deliveries collapses into the in-flight resync plus exactly
// one follow-up run (the per-session worker's dirty-flag coalescing), not one
// re-derivation per delivery.
func TestResyncSessionsOnBackendHealthChange_CoalescesBurst(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	mgr := &gatedSessionManager{stubSessionManager: stubSessionManager{alive: true}, gate: make(chan struct{})}
	srv := &Server{
		core:           fc,
		vmcpSessionMgr: mgr,
		resyncBaseCtx:  context.Background(),
	}
	sess := &fakeToolsSession{id: "sess-1"}
	_, toolsWorker := srv.buildListChangedSink("sess-1", sess, nil, nil)
	srv.healthResync.add("sess-1", toolsWorker)

	// First delivery starts the worker; it blocks inside the liveness guard.
	srv.resyncSessionsOnBackendHealthChange(1)
	require.Eventually(t, func() bool { return mgr.calls.Load() == 1 },
		2*time.Second, 10*time.Millisecond, "first resync must be in flight")

	// Nine more deliveries arrive while the first resync is blocked: they must
	// fold into a single dirty flag.
	for gen := uint64(2); gen <= 10; gen++ {
		srv.resyncSessionsOnBackendHealthChange(gen)
	}
	close(mgr.gate)

	// The blocked run completes and exactly one follow-up run drains the
	// coalesced deliveries: two re-derivations total, never ten.
	require.Eventually(t, func() bool { return fc.listToolsCalls.Load() == 2 },
		2*time.Second, 10*time.Millisecond, "burst must coalesce into in-flight + one follow-up")
	// Give any (incorrect) extra runs a moment to surface before asserting.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(2), fc.listToolsCalls.Load(),
		"a burst of deliveries must coalesce instead of re-deriving once per delivery")
}

// TestResyncSessionsOnBackendHealthChange_OptimizerModeReindexesQuietly
// verifies the #5786 PR2 behavior that replaces PR1's optimizer-mode no-op:
// the fan-out DOES reach an optimizer-mode session (its index is rebuilt over
// the current health-filtered core set) but must NOT rewrite the session's
// advertised tool store — find_tool/call_tool are the only advertised names and
// they do not change on a health flip, so re-applying them would emit a
// downstream notifications/tools/list_changed carrying no news.
func TestResyncSessionsOnBackendHealthChange_OptimizerModeReindexesQuietly(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	factory := &recordingOptimizerFactory{}
	srv := &Server{
		core:             &healthEnabledCore{fakeCore: fc, reporter: newTestHealthReporter(t)},
		vmcpSessionMgr:   &stubSessionManager{alive: true},
		resyncBaseCtx:    context.Background(),
		optimizerFactory: factory.build,
	}

	// Give the session a registered optimizer handle, as registration does.
	sess := &fakeToolsSession{id: "sess-1"}
	_, err := srv.serveSessionTools(context.Background(), "sess-1", nil)
	require.NoError(t, err)
	buildsAfterRegistration := factory.calls.Load()
	_, toolsWorker := srv.buildListChangedSink("sess-1", sess, nil, nil)
	srv.healthResync.add("sess-1", toolsWorker)

	srv.resyncSessionsOnBackendHealthChange(1)

	require.Eventually(t, func() bool { return factory.calls.Load() > buildsAfterRegistration },
		2*time.Second, 10*time.Millisecond, "health change must rebuild the session's optimizer index")
	require.Eventually(t, func() bool { return fc.listToolsCalls.Load() >= 1 },
		2*time.Second, 10*time.Millisecond, "re-index must re-derive the core tool set")
	assert.Equal(t, 0, sess.setToolsCalls(),
		"optimizer mode must not rewrite the session tool store (no spurious tools/list_changed)")
}

// TestResyncSessionsOnBackendHealthChange_PrunesDeadSession verifies the lazy
// registry prune: a triggered resync that finds the session terminated skips
// the work and removes the session's registration, so the registry does not
// accumulate entries for sessions that ended without server involvement.
func TestResyncSessionsOnBackendHealthChange_PrunesDeadSession(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv := &Server{
		core:           fc,
		vmcpSessionMgr: &stubSessionManager{alive: false},
		resyncBaseCtx:  context.Background(),
	}
	sess := &fakeToolsSession{id: "sess-1"}
	_, toolsWorker := srv.buildListChangedSink("sess-1", sess, nil, nil)
	srv.healthResync.add("sess-1", toolsWorker)

	srv.resyncSessionsOnBackendHealthChange(1)

	require.Eventually(t, func() bool { return len(srv.healthResync.snapshot()) == 0 },
		2*time.Second, 10*time.Millisecond, "dead session must be pruned from the registry")
	assert.Equal(t, int32(0), fc.listToolsCalls.Load(), "no re-derivation for a dead session")
	assert.Equal(t, 0, sess.setToolsCalls())
}

// fakeSessionIDManager is a minimal server.SessionIdManager whose Terminate
// outcome is scripted, for testing pruneOnTerminateSessionIDManager.
type fakeSessionIDManager struct {
	terminateNotAllowed bool
	terminateErr        error
}

func (*fakeSessionIDManager) Generate() string              { return "" }
func (*fakeSessionIDManager) Validate(string) (bool, error) { return false, nil }
func (f *fakeSessionIDManager) Terminate(string) (bool, error) {
	return f.terminateNotAllowed, f.terminateErr
}

// TestPruneOnTerminateSessionIDManager verifies the SDK-facing wrapper
// deregisters a session's health-resync worker only when the underlying
// Terminate actually terminated it: a disallowed or failed termination keeps
// the (still live) session registered.
func TestPruneOnTerminateSessionIDManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		notAllowed bool
		err        error
		wantPruned bool
	}{
		{name: "successful termination prunes", wantPruned: true},
		{name: "disallowed termination keeps registration", notAllowed: true},
		{name: "failed termination keeps registration", err: assert.AnError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var r healthResyncRegistry
			r.add("sess-1", &listChangedResyncWorker{})
			m := &pruneOnTerminateSessionIDManager{
				SessionIdManager: &fakeSessionIDManager{terminateNotAllowed: tt.notAllowed, terminateErr: tt.err},
				registry:         &r,
			}

			gotNotAllowed, gotErr := m.Terminate("sess-1")

			assert.Equal(t, tt.notAllowed, gotNotAllowed)
			assert.Equal(t, tt.err, gotErr)
			if tt.wantPruned {
				assert.Empty(t, r.snapshot())
			} else {
				assert.Len(t, r.snapshot(), 1)
			}
		})
	}
}

// registrationStubSessionManager extends stubSessionManager with a working
// CreateSession so handleSessionRegistrationImpl can run against it.
type registrationStubSessionManager struct {
	stubSessionManager
}

func (*registrationStubSessionManager) CreateSession(
	context.Context, string, vmcpsession.ListChangedSink,
) (vmcpsession.MultiSession, error) {
	return nil, nil
}

// healthEnabledCore wraps fakeCore (whose BackendHealth returns nil) with a
// non-nil reporter, so tests can exercise the health-monitoring-enabled side
// of the registration gate.
type healthEnabledCore struct {
	*fakeCore
	reporter health.Reporter
}

func (c *healthEnabledCore) BackendHealth() health.Reporter { return c.reporter }

// TestHandleSessionRegistration_HealthResyncRegistrationGate verifies that a
// passthrough session's tools resync worker joins the health fan-out registry
// only when health monitoring is enabled. With no monitor there is no OnChange
// subscription (see Serve), so no fan-out would ever run the registry's lazy
// prune — a session ending without a server-observed Terminate (TTL expiry,
// node-local cache eviction) would retain its worker closure forever.
func TestHandleSessionRegistration_HealthResyncRegistrationGate(t *testing.T) {
	t.Parallel()

	t.Run("health monitoring enabled registers the session", func(t *testing.T) {
		t.Parallel()

		srv := &Server{
			core:           &healthEnabledCore{fakeCore: &fakeCore{}, reporter: newTestHealthReporter(t)},
			vmcpSessionMgr: &registrationStubSessionManager{},
			resyncBaseCtx:  context.Background(),
		}

		require.NoError(t, srv.handleSessionRegistrationImpl(context.Background(), &fakeToolsSession{id: "sess-1"}))

		assert.Len(t, srv.healthResync.snapshot(), 1,
			"passthrough session must join the health fan-out registry when a monitor exists")
	})

	t.Run("health monitoring disabled skips registration", func(t *testing.T) {
		t.Parallel()

		srv := &Server{
			core:           &fakeCore{}, // BackendHealth() returns nil: monitoring disabled
			vmcpSessionMgr: &registrationStubSessionManager{},
			resyncBaseCtx:  context.Background(),
		}

		require.NoError(t, srv.handleSessionRegistrationImpl(context.Background(), &fakeToolsSession{id: "sess-1"}))

		assert.Empty(t, srv.healthResync.snapshot(),
			"with no monitor nothing ever triggers the fan-out or its lazy prune, so the entry would leak")
	})
}

// TestHealthResyncRegistry_AddRemoveSnapshot covers the registry's zero-value
// usability and add/remove/snapshot semantics.
func TestHealthResyncRegistry_AddRemoveSnapshot(t *testing.T) {
	t.Parallel()

	var r healthResyncRegistry
	assert.Empty(t, r.snapshot(), "zero-value registry must be usable")

	w1 := &listChangedResyncWorker{}
	w2 := &listChangedResyncWorker{}
	r.add("a", w1)
	r.add("b", w2)
	assert.Len(t, r.snapshot(), 2)

	r.remove("a")
	assert.Len(t, r.snapshot(), 1)

	r.remove("missing") // no-op
	assert.Len(t, r.snapshot(), 1)
}

// newTestHealthReporter returns a real (unstarted) Monitor to stand in as an
// enabled health reporter: the code under test only checks that
// core.BackendHealth() is non-nil, never that the monitor is running.
func newTestHealthReporter(t *testing.T) health.Reporter {
	t.Helper()
	mon, err := health.NewMonitor(nil, nil, health.MonitorConfig{
		CheckInterval:      time.Minute,
		UnhealthyThreshold: 1,
		Timeout:            time.Second,
	})
	require.NoError(t, err)
	return mon
}
