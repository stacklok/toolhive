// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// bareSession implements only server.ClientSession (not SessionWithTools),
// used to exercise setSessionToolsReplace's unsupported-session error branch.
type bareSession struct{ id string }

var _ server.ClientSession = (*bareSession)(nil)

func (b *bareSession) SessionID() string                                 { return b.id }
func (*bareSession) Initialize()                                         {}
func (*bareSession) Initialized() bool                                   { return true }
func (*bareSession) NotificationChannel() chan<- mcp.JSONRPCNotification { return nil }

// TestSetSessionToolsReplace covers the REPLACE (not merge) semantics that
// distinguish it from setSessionToolsDirect: a tool present in the session's
// existing overlay but absent from the new set must disappear, and a session
// that does not implement SessionWithTools is rejected.
func TestSetSessionToolsReplace(t *testing.T) {
	t.Parallel()

	t.Run("removes a tool no longer present", func(t *testing.T) {
		t.Parallel()
		sess := &fakeSDKSession{id: "s1", tools: map[string]server.ServerTool{
			"old": {Tool: mcp.Tool{Name: "old"}},
		}}
		err := setSessionToolsReplace(sess, []server.ServerTool{{Tool: mcp.Tool{Name: "new"}}})
		require.NoError(t, err)
		assert.NotContains(t, sess.tools, "old", "replace must remove a tool no longer present")
		assert.Contains(t, sess.tools, "new")
	})

	t.Run("empty replacement clears the session's tools", func(t *testing.T) {
		t.Parallel()
		sess := &fakeSDKSession{id: "s2", tools: map[string]server.ServerTool{
			"old": {Tool: mcp.Tool{Name: "old"}},
		}}
		err := setSessionToolsReplace(sess, nil)
		require.NoError(t, err)
		assert.Empty(t, sess.tools)
	})

	t.Run("rejects a session without SessionWithTools", func(t *testing.T) {
		t.Parallel()
		err := setSessionToolsReplace(&bareSession{id: "s3"}, nil)
		require.Error(t, err)
	})
}

// trackedBackendIDs replaces sessionID's coordinator entry's backendIDs, used
// to simulate the session having connected to specific backends (the mock
// session factories in this package register sessions with no real backend
// metadata).
func trackedBackendIDs(t *testing.T, srv *Server, sessionID string, backendIDs ...string) {
	t.Helper()
	ids := make(map[string]struct{}, len(backendIDs))
	for _, id := range backendIDs {
		ids[id] = struct{}{}
	}
	srv.listChanged.mu.Lock()
	entry, ok := srv.listChanged.tracked[sessionID]
	srv.listChanged.mu.Unlock()
	require.True(t, ok, "session must already be tracked by the coordinator")
	entry.backendIDs = ids
}

func isTracked(srv *Server, sessionID string) bool {
	srv.listChanged.mu.Lock()
	defer srv.listChanged.mu.Unlock()
	_, ok := srv.listChanged.tracked[sessionID]
	return ok
}

// fastDebounce shrinks the coordinator's coalescing window so tests do not sleep
// a real quarter-second per assertion (which flakes under -race CI load). The
// field is atomic and read by the worker on each wake, and callers set it before
// sending any notification, so there is no race with the running worker.
func fastDebounce(srv *Server) {
	srv.listChanged.debounceNanos.Store(int64(time.Millisecond))
}

// TestListChangedCoordinator_RegistersAndUntracksOnBindingFailure verifies
// track() is populated by a normal registration and untrack() is invoked by
// terminateOnBindingFailure.
func TestListChangedCoordinator_RegistersAndUntracksOnBindingFailure(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, _ := registerServeSession(t, fc)

	assert.True(t, isTracked(srv, sessionID), "a successfully registered session must be tracked")

	srv.terminateOnBindingFailure(sessionID, "t", errors.New("boom"))
	assert.False(t, isTracked(srv, sessionID), "terminateOnBindingFailure must untrack the session")
}

// TestListChangedCoordinator_UntracksOnTerminate verifies the normal
// session-termination path (Manager.Terminate, the SDK DELETE / internal path)
// promptly removes the session from the coordinator's registry via the
// SetOnTerminate hook — not only lazily on a later sweep — so entries do not
// accumulate for the lifetime of the process.
func TestListChangedCoordinator_UntracksOnTerminate(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, _ := registerServeSession(t, fc)
	require.True(t, isTracked(srv, sessionID), "a successfully registered session must be tracked")

	_, err := srv.vmcpSessionMgr.Terminate(sessionID)
	require.NoError(t, err)

	// Prompt (synchronous with Terminate via the onTerminate hook), not on a sweep.
	assert.False(t, isTracked(srv, sessionID),
		"a normally-terminated session must be untracked promptly, without waiting for a sweep")
}

// TestListChangedCoordinator_CoalescesBurst verifies a burst of
// NotifyBackendListChanged calls for the same backend, well within the
// debounce window, collapses into exactly one re-sweep.
func TestListChangedCoordinator_CoalescesBurst(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, _ := registerServeSession(t, fc)
	fastDebounce(srv)
	trackedBackendIDs(t, srv, sessionID, "backend-1")

	before := fc.listToolsCalls.Load()
	for i := 0; i < 5; i++ {
		srv.listChanged.NotifyBackendListChanged("backend-1", vmcp.ListChangedTools)
	}

	require.Eventually(t, func() bool {
		return fc.listToolsCalls.Load() > before
	}, 2*time.Second, 5*time.Millisecond, "a re-sweep should occur after the debounce window")

	// Give any incorrect extra resweep time to occur, then assert exactly one
	// happened for the whole burst.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, before+1, fc.listToolsCalls.Load(),
		"a burst of notifications for one backend must coalesce into exactly one re-sweep")
}

// TestListChangedCoordinator_KindRouting verifies that only the dirty kind's
// core re-list is invoked: a resources notification re-lists resources (and
// resource templates, which share the same capability gate) but not tools or
// prompts, and vice versa for a tools notification.
func TestListChangedCoordinator_KindRouting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		kind          vmcp.ListChangedKind
		wantTools     bool
		wantResources bool
		wantPrompts   bool
	}{
		{"tools", vmcp.ListChangedTools, true, false, false},
		{"resources", vmcp.ListChangedResources, false, true, false},
		{"prompts", vmcp.ListChangedPrompts, false, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
			srv, sessionID, _ := registerServeSession(t, fc)
			fastDebounce(srv)
			trackedBackendIDs(t, srv, sessionID, "backend-1")

			toolsBefore := fc.listToolsCalls.Load()
			resourcesBefore := fc.listResourcesCalls.Load()
			promptsBefore := fc.listPromptsCalls.Load()

			srv.listChanged.NotifyBackendListChanged("backend-1", tt.kind)

			// Wait for whichever counter this kind is expected to bump; then assert
			// none of the others moved.
			require.Eventually(t, func() bool {
				switch {
				case tt.wantTools:
					return fc.listToolsCalls.Load() > toolsBefore
				case tt.wantResources:
					return fc.listResourcesCalls.Load() > resourcesBefore
				default:
					return fc.listPromptsCalls.Load() > promptsBefore
				}
			}, 2*time.Second, 5*time.Millisecond, "expected re-sweep did not occur")

			// A short settle so a wrongly-routed call would have had time to land.
			time.Sleep(150 * time.Millisecond)
			assert.Equal(t, tt.wantTools, fc.listToolsCalls.Load() > toolsBefore, "tools re-list routing")
			assert.Equal(t, tt.wantResources, fc.listResourcesCalls.Load() > resourcesBefore, "resources re-list routing")
			assert.Equal(t, tt.wantPrompts, fc.listPromptsCalls.Load() > promptsBefore, "prompts re-list routing")
		})
	}
}

// TestListChangedCoordinator_UnaffectedSessionNotSwept verifies a session
// whose backendIDs do not intersect the dirty backend is left alone.
func TestListChangedCoordinator_UnaffectedSessionNotSwept(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, _ := registerServeSession(t, fc)
	fastDebounce(srv)
	trackedBackendIDs(t, srv, sessionID, "backend-unrelated")

	before := fc.listToolsCalls.Load()
	srv.listChanged.NotifyBackendListChanged("backend-1", vmcp.ListChangedTools)

	// There is nothing to wait ON for a negative assertion, so sleep well past the
	// (now ~1ms) debounce window and confirm no re-sweep happened.
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, before, fc.listToolsCalls.Load(),
		"a backend not in this session's backendIDs must not trigger a re-sweep")
}

// TestListChangedCoordinator_PrunesTerminatedSession verifies that a sweep
// discovering (via Manager.Validate) that a tracked session was terminated by
// some other path removes it from the coordinator's registry.
func TestListChangedCoordinator_PrunesTerminatedSession(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, _ := registerServeSession(t, fc)
	fastDebounce(srv)
	trackedBackendIDs(t, srv, sessionID, "backend-1")
	require.True(t, isTracked(srv, sessionID))

	// Isolate the sweep's lazy Validate-based prune (distinct from the synchronous
	// Terminate untrack hook, which TestListChangedCoordinator_UntracksOnTerminate
	// covers): Terminate deletes the session from storage AND fires the hook that
	// removes the coordinator entry, so re-insert a stale entry afterwards to force
	// the next sweep to discover — via Manager.Validate — that the session is gone.
	_, err := srv.vmcpSessionMgr.Terminate(sessionID)
	require.NoError(t, err)
	srv.listChanged.track(sessionID, &trackedSession{
		sess:       &fakeSDKSession{id: sessionID, tools: map[string]server.ServerTool{}},
		backendIDs: map[string]struct{}{"backend-1": {}},
	})
	require.True(t, isTracked(srv, sessionID))

	srv.listChanged.NotifyBackendListChanged("backend-1", vmcp.ListChangedTools)

	require.Eventually(t, func() bool {
		return !isTracked(srv, sessionID)
	}, 2*time.Second, 5*time.Millisecond, "a terminated session must be pruned from the coordinator's registry on sweep")
}

// TestListChangedCoordinator_ToolsErrorRetainsPreviousSet verifies that a
// failed re-derivation of the tool set (core.ListTools erroring) leaves the
// session's previously-applied tools untouched rather than shrinking them.
func TestListChangedCoordinator_ToolsErrorRetainsPreviousSet(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, baseURL := registerServeSession(t, fc)
	fastDebounce(srv)
	trackedBackendIDs(t, srv, sessionID, "backend-1")

	listErr := errors.New("backend unavailable")
	fc.listToolsErr.Store(&listErr)

	before := fc.listToolsCalls.Load()
	srv.listChanged.NotifyBackendListChanged("backend-1", vmcp.ListChangedTools)
	require.Eventually(t, func() bool {
		return fc.listToolsCalls.Load() > before
	}, 2*time.Second, 5*time.Millisecond, "the failed re-sweep attempt must still run")

	// Give the (failed) sweep time to have applied any (incorrect) change.
	time.Sleep(150 * time.Millisecond)

	resp := postServeMCP(t, baseURL, map[string]any{
		"jsonrpc": "2.0", "id": 99, "method": "tools/list", "params": map[string]any{},
	}, sessionID)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), `"t"`,
		"a failed re-sweep must retain the previously-applied tool set, not shrink it")
}

// TestListChangedCoordinator_StartStop verifies the worker goroutine starts
// and stops cleanly, and that stop is safe to call from Server.shutdownFuncs
// (its actual signature).
func TestListChangedCoordinator_StartStop(t *testing.T) {
	t.Parallel()

	c := newListChangedCoordinator(&Server{})
	c.start()

	err := c.stop(context.Background())
	require.NoError(t, err)

	// A notification after stop must not panic (best-effort: the worker is gone,
	// so it is simply never processed).
	assert.NotPanics(t, func() {
		c.NotifyBackendListChanged("backend-1", vmcp.ListChangedTools)
	})
}

// TestListChangedCoordinator_SetInvalidatorInvokedOnSweep verifies that the
// installed CacheInvalidator is called with the dirty backend ID during a
// sweep, and is safely skipped (nil) when caching is disabled.
func TestListChangedCoordinator_SetInvalidatorInvokedOnSweep(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv, sessionID, _ := registerServeSession(t, fc)
	fastDebounce(srv)
	trackedBackendIDs(t, srv, sessionID, "backend-1")

	inv := &recordingInvalidator{done: make(chan string, 8)}
	srv.listChanged.setInvalidator(inv)

	srv.listChanged.NotifyBackendListChanged("backend-1", vmcp.ListChangedTools)

	select {
	case id := <-inv.done:
		assert.Equal(t, "backend-1", id)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for InvalidateBackend to be called")
	}
}

// recordingInvalidator is a minimal aggregator.CacheInvalidator that reports
// every InvalidateBackend call on a channel.
type recordingInvalidator struct {
	done chan string
}

func (r *recordingInvalidator) InvalidateBackend(backendID string) {
	select {
	case r.done <- backendID:
	default:
	}
}
