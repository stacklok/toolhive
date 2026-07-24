// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// fakeToolsSession is a minimal server.ClientSession + server.SessionWithTools
// fake used to test resyncSessionTools/runListChangedResync without a live SDK
// session. It records every SetSessionTools call so tests can assert
// replacement semantics.
type fakeToolsSession struct {
	id string

	mu                   sync.Mutex
	tools                map[string]server.ServerTool
	setSessionToolsCalls int
}

func (*fakeToolsSession) Initialize()                                         {}
func (*fakeToolsSession) Initialized() bool                                   { return true }
func (*fakeToolsSession) NotificationChannel() chan<- mcp.JSONRPCNotification { return nil }
func (f *fakeToolsSession) SessionID() string                                 { return f.id }

func (f *fakeToolsSession) GetSessionTools() map[string]server.ServerTool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]server.ServerTool, len(f.tools))
	for k, v := range f.tools {
		out[k] = v
	}
	return out
}

func (f *fakeToolsSession) SetSessionTools(tools map[string]server.ServerTool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tools = tools
	f.setSessionToolsCalls++
}

var _ server.ClientSession = (*fakeToolsSession)(nil)
var _ server.SessionWithTools = (*fakeToolsSession)(nil)

// stubSessionManager is a minimal SessionManager for the #5748 resync tests.
// Only GetMultiSession is meaningful (its returned liveness bool drives the
// resync liveness guard); the rest satisfy the interface and fail loudly if a
// test reaches them unexpectedly.
type stubSessionManager struct {
	alive bool
}

func (m *stubSessionManager) GetMultiSession(context.Context, string) (vmcpsession.MultiSession, bool) {
	return nil, m.alive
}
func (*stubSessionManager) Generate() string { panic("stubSessionManager: Generate unexpected") }
func (*stubSessionManager) Validate(string) (bool, error) {
	panic("stubSessionManager: Validate unexpected")
}
func (*stubSessionManager) Terminate(string) (bool, error) { return false, nil }
func (*stubSessionManager) CreateSession(
	context.Context, string, vmcpsession.ListChangedSink,
) (vmcpsession.MultiSession, error) {
	panic("stubSessionManager: CreateSession unexpected")
}
func (*stubSessionManager) DecorateSession(string, func(vmcpsession.MultiSession) vmcpsession.MultiSession) error {
	panic("stubSessionManager: DecorateSession unexpected")
}
func (*stubSessionManager) NotifyBackendExpired(string, string, map[string]string) {}

var _ SessionManager = (*stubSessionManager)(nil)

// TestResyncSessionTools_ReplacesRatherThanMerges verifies (#5748) that
// resyncSessionTools REPLACES the session's tool store with the freshly
// core-derived set — unlike setSessionToolsDirect's registration-time MERGE —
// so a tool the backend removed (and the core therefore no longer advertises)
// disappears rather than lingering.
func TestResyncSessionTools_ReplacesRatherThanMerges(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "kept"}, {Name: "added"}}}
	srv := &Server{core: fc}

	sess := &fakeToolsSession{id: "sess-1", tools: map[string]server.ServerTool{
		"kept":    {Tool: mcp.Tool{Name: "kept"}},
		"removed": {Tool: mcp.Tool{Name: "removed"}}, // must disappear after resync
	}}

	err := srv.resyncSessionTools(context.Background(), sess, "sess-1", nil)
	require.NoError(t, err)

	got := sess.GetSessionTools()
	assert.Len(t, got, 2)
	assert.Contains(t, got, "kept")
	assert.Contains(t, got, "added")
	assert.NotContains(t, got, "removed", "resync must REPLACE the tool store, dropping a no-longer-advertised tool")
	assert.Equal(t, 1, sess.setSessionToolsCalls)
}

// TestResyncSessionTools_SessionWithoutToolSupport verifies resyncSessionTools
// fails loudly (rather than silently no-opping) when the session does not
// implement server.SessionWithTools.
func TestResyncSessionTools_SessionWithoutToolSupport(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv := &Server{core: fc}

	// plainSession implements only server.ClientSession, not SessionWithTools.
	type plainSession struct{ server.ClientSession }
	sess := plainSession{&fakeToolsSession{id: "sess-1"}}

	err := srv.resyncSessionTools(context.Background(), sess, "sess-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support per-session tools")
}

// TestListChangedResyncWorker_CoalescesAndNonBlocking verifies the B-HIGH-2
// hand-off: trigger() returns immediately, never spawns a goroutine per call,
// runs at most one resync at a time, and coalesces concurrent triggers into a
// single follow-up run (dirty flag).
func TestListChangedResyncWorker_CoalescesAndNonBlocking(t *testing.T) {
	t.Parallel()

	var (
		runs     atomic.Int32
		inFlight atomic.Int32
		maxSeen  atomic.Int32
		release  = make(chan struct{})
	)
	firstStarted := make(chan struct{}, 1)

	w := &listChangedResyncWorker{
		baseCtx: context.Background(),
		run: func(context.Context) {
			n := inFlight.Add(1)
			for {
				m := maxSeen.Load()
				if n <= m || maxSeen.CompareAndSwap(m, n) {
					break
				}
			}
			if runs.Add(1) == 1 {
				firstStarted <- struct{}{}
				<-release // hold the first run so subsequent triggers coalesce
			}
			inFlight.Add(-1)
		},
	}

	// First trigger starts the (blocked) worker goroutine.
	w.trigger()
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}

	// While the first run is blocked, fire many more triggers. Each must return
	// immediately (non-blocking) and collapse into a single dirty re-run.
	for range 50 {
		w.trigger()
	}

	close(release) // let the first run finish; one coalesced re-run should follow

	require.Eventually(t, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.running
	}, 5*time.Second, 5*time.Millisecond, "worker should become idle")

	assert.Equal(t, int32(1), maxSeen.Load(), "at most one resync may run at a time")
	assert.Equal(t, int32(2), runs.Load(),
		"50 triggers during one in-flight run must coalesce into exactly one follow-up run")
}

// TestRunListChangedResync verifies (#5748) the resync body run off the receive
// loop: it skips terminated sessions (liveness guard), and for a live session
// it invalidates the cache and reconstructs a context carrying the captured
// identity + forwarded headers (B-HIGH-1) before re-deriving/replacing the
// session's tools.
func TestRunListChangedResync(t *testing.T) {
	t.Parallel()

	const fwdKey = "X-Tenant"
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "alice"}}
	fwd := map[string]string{fwdKey: "acme"}

	t.Run("terminated session: skipped entirely", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{tools: []vmcp.Tool{{Name: "fresh"}}}
		srv := &Server{core: fc, vmcpSessionMgr: &stubSessionManager{alive: false}}
		sess := &fakeToolsSession{id: "sess-1", tools: map[string]server.ServerTool{
			"stale": {Tool: mcp.Tool{Name: "stale"}},
		}}

		srv.runListChangedResync(context.Background(), "sess-1", sess, identity, fwd)

		assert.Equal(t, int32(0), fc.invalidateCacheCalls.Load(), "terminated session must not invalidate the cache")
		assert.Equal(t, int32(0), fc.listToolsCalls.Load(), "terminated session must not re-aggregate")
		assert.Equal(t, 0, sess.setSessionToolsCalls)
	})

	t.Run("live session: invalidates, reconstructs ctx, replaces tools", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{tools: []vmcp.Tool{{Name: "fresh"}}}
		srv := &Server{core: fc, vmcpSessionMgr: &stubSessionManager{alive: true}}
		sess := &fakeToolsSession{id: "sess-1", tools: map[string]server.ServerTool{
			"stale": {Tool: mcp.Tool{Name: "stale"}},
		}}

		srv.runListChangedResync(context.Background(), "sess-1", sess, identity, fwd)

		assert.Equal(t, int32(1), fc.invalidateCacheCalls.Load(), "live session must invalidate the cache once")
		assert.Equal(t, 1, sess.setSessionToolsCalls)
		got := sess.GetSessionTools()
		require.Len(t, got, 1)
		assert.Contains(t, got, "fresh")
		assert.NotContains(t, got, "stale", "resync must replace the stale set")

		// B-HIGH-1: the resync must call the core with a context carrying the
		// captured identity AND forwarded headers, so the cache key and outbound
		// backend auth match a real request from this principal.
		box := fc.lastListToolsCtx.Load()
		require.NotNil(t, box, "ListTools must have been called")
		resyncCtx := box.ctx
		gotID, ok := auth.IdentityFromContext(resyncCtx)
		require.True(t, ok, "resync context must carry the captured identity")
		assert.Equal(t, "alice", gotID.Subject)
		assert.Equal(t, "acme", headerforward.ForwardedHeadersFromContext(resyncCtx)[fwdKey],
			"resync context must carry the captured forwarded headers")
	})
}

// TestBuildListChangedSink_IgnoresNonToolsKind verifies the sink's kind gate:
// a non-tools ChangeKind is dropped synchronously on the receive loop (no
// worker goroutine, no cache invalidation, no resync). The gate returns before
// worker.trigger, so the assertion is deterministic without waiting.
func TestBuildListChangedSink_IgnoresNonToolsKind(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "fresh"}}}
	srv := &Server{
		core:           fc,
		vmcpSessionMgr: &stubSessionManager{alive: true},
		resyncBaseCtx:  context.Background(),
	}
	sess := &fakeToolsSession{id: "sess-1"}

	sink := srv.buildListChangedSink("sess-1", sess, nil, nil)
	// A resources list_changed (out of scope) must be ignored synchronously.
	sink(context.Background(), "backend-1", vmcpsession.ChangeKind("resources"))

	assert.Equal(t, int32(0), fc.invalidateCacheCalls.Load(), "non-tools kind must not invalidate the cache")
	assert.Equal(t, 0, sess.setSessionToolsCalls, "non-tools kind must not resync tools")
}
