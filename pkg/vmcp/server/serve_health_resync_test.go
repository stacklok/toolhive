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
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// TestResyncSessionsOnBackendHealthChange_ResyncsLiveSession verifies (#5786)
// that a backend health change fans out to a registered session: its tool
// store is REPLACED with the freshly core-derived set (gaining a recovered
// backend's tool and dropping a failed backend's tool) and the capability
// cache is invalidated so the re-derivation sweeps the new healthy set.
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
	assert.GreaterOrEqual(t, fc.invalidateCacheCalls.Load(), int32(1),
		"resync must invalidate the capability cache so the re-derivation sweeps the new healthy set")
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

// TestResyncSessionsOnBackendHealthChange_OptimizerModeIsNoOp verifies the
// #5786 PR1 passthrough-only gate: with the optimizer enabled the fan-out does
// nothing (rebuilding the optimizer's backing index is deferred to the
// optimizer-mode follow-up).
func TestResyncSessionsOnBackendHealthChange_OptimizerModeIsNoOp(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv := &Server{
		core:           fc,
		vmcpSessionMgr: &stubSessionManager{alive: true},
		resyncBaseCtx:  context.Background(),
		optimizerFactory: func(context.Context, []server.ServerTool) (optimizer.Optimizer, error) {
			panic("optimizer factory must not be invoked by the health-change fan-out")
		},
	}
	sess := &fakeToolsSession{id: "sess-1"}
	_, toolsWorker := srv.buildListChangedSink("sess-1", sess, nil, nil)
	srv.healthResync.add("sess-1", toolsWorker)

	srv.resyncSessionsOnBackendHealthChange(1)

	// Synchronous no-op: nothing was triggered, so no async work to wait out.
	assert.Equal(t, int32(0), fc.listToolsCalls.Load())
	assert.Equal(t, 0, sess.setToolsCalls())
	assert.Equal(t, int32(0), fc.invalidateCacheCalls.Load())
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
