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

// fakeCapsSession is a minimal server.ClientSession + SessionWithResources +
// SessionWithResourceTemplates + SessionWithPrompts fake used to test
// resyncSessionResources/resyncSessionPrompts/runListChangedResync without a
// live SDK session. It records every SetSession* call so tests can assert
// replacement semantics (#5969, mirroring fakeToolsSession for #5748).
type fakeCapsSession struct {
	id string

	mu                               sync.Mutex
	resources                        map[string]server.ServerResource
	resourceTemplates                map[string]server.ServerResourceTemplate
	prompts                          map[string]server.ServerPrompt
	setSessionResourcesCalls         int
	setSessionResourceTemplatesCalls int
	setSessionPromptsCalls           int
}

func (*fakeCapsSession) Initialize()                                         {}
func (*fakeCapsSession) Initialized() bool                                   { return true }
func (*fakeCapsSession) NotificationChannel() chan<- mcp.JSONRPCNotification { return nil }
func (f *fakeCapsSession) SessionID() string                                 { return f.id }

func (f *fakeCapsSession) GetSessionResources() map[string]server.ServerResource {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]server.ServerResource, len(f.resources))
	for k, v := range f.resources {
		out[k] = v
	}
	return out
}

func (f *fakeCapsSession) SetSessionResources(resources map[string]server.ServerResource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resources = resources
	f.setSessionResourcesCalls++
}

func (f *fakeCapsSession) GetSessionResourceTemplates() map[string]server.ServerResourceTemplate {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]server.ServerResourceTemplate, len(f.resourceTemplates))
	for k, v := range f.resourceTemplates {
		out[k] = v
	}
	return out
}

func (f *fakeCapsSession) SetSessionResourceTemplates(templates map[string]server.ServerResourceTemplate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resourceTemplates = templates
	f.setSessionResourceTemplatesCalls++
}

func (f *fakeCapsSession) GetSessionPrompts() map[string]server.ServerPrompt {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]server.ServerPrompt, len(f.prompts))
	for k, v := range f.prompts {
		out[k] = v
	}
	return out
}

func (f *fakeCapsSession) SetSessionPrompts(prompts map[string]server.ServerPrompt) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = prompts
	f.setSessionPromptsCalls++
}

var _ server.ClientSession = (*fakeCapsSession)(nil)
var _ server.SessionWithResources = (*fakeCapsSession)(nil)
var _ server.SessionWithResourceTemplates = (*fakeCapsSession)(nil)
var _ server.SessionWithPrompts = (*fakeCapsSession)(nil)

// resourcesOnlySession implements server.ClientSession + SessionWithResources
// but NOT SessionWithResourceTemplates, so a resyncSessionResources test can
// prove the templates-half type assertion fails loudly. It provides its own
// resource methods (rather than embedding fakeCapsSession, which would also
// promote the template/prompt methods and satisfy those interfaces).
type resourcesOnlySession struct {
	fake fakeCapsSession
}

func (*resourcesOnlySession) Initialize()                                         {}
func (*resourcesOnlySession) Initialized() bool                                   { return true }
func (*resourcesOnlySession) NotificationChannel() chan<- mcp.JSONRPCNotification { return nil }
func (s *resourcesOnlySession) SessionID() string                                 { return s.fake.id }
func (s *resourcesOnlySession) GetSessionResources() map[string]server.ServerResource {
	return s.fake.GetSessionResources()
}
func (s *resourcesOnlySession) SetSessionResources(resources map[string]server.ServerResource) {
	s.fake.SetSessionResources(resources)
}

var _ server.ClientSession = (*resourcesOnlySession)(nil)
var _ server.SessionWithResources = (*resourcesOnlySession)(nil)

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

// TestResyncSessionResources_SessionWithoutResourceSupport verifies (#5969)
// resyncSessionResources fails loudly (rather than silently no-opping) when
// the session does not implement server.SessionWithResources.
func TestResyncSessionResources_SessionWithoutResourceSupport(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{resources: []vmcp.Resource{{Name: "r", URI: "r"}}}
	srv := &Server{core: fc}

	// plainSession implements only server.ClientSession, not SessionWithResources.
	type plainSession struct{ server.ClientSession }
	sess := plainSession{&fakeCapsSession{id: "sess-1"}}

	err := srv.resyncSessionResources(context.Background(), sess, "sess-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support per-session resources")
}

// TestResyncSessionResources_SessionWithoutTemplateSupport verifies (#5969)
// resyncSessionResources fails loudly when the session supports per-session
// resources but not resource templates (SessionWithResourceTemplates). The
// resource store is set before the template assertion fails, so this pins the
// exact error surfaced for the templates half.
func TestResyncSessionResources_SessionWithoutTemplateSupport(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{resources: []vmcp.Resource{{Name: "r", URI: "r"}}}
	srv := &Server{core: fc}

	// resourcesOnlySession implements SessionWithResources but NOT
	// SessionWithResourceTemplates.
	sess := &resourcesOnlySession{fake: fakeCapsSession{id: "sess-1"}}

	err := srv.resyncSessionResources(context.Background(), sess, "sess-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support per-session resource templates")
}

// TestResyncSessionPrompts_SessionWithoutPromptSupport verifies (#5969)
// resyncSessionPrompts fails loudly (rather than silently no-opping) when the
// session does not implement server.SessionWithPrompts.
func TestResyncSessionPrompts_SessionWithoutPromptSupport(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{prompts: []vmcp.Prompt{{Name: "p"}}}
	srv := &Server{core: fc}

	// plainSession implements only server.ClientSession, not SessionWithPrompts.
	type plainSession struct{ server.ClientSession }
	sess := plainSession{&fakeCapsSession{id: "sess-1"}}

	err := srv.resyncSessionPrompts(context.Background(), sess, "sess-1", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support per-session prompts")
}

// TestResyncSessionResources_ReplacesRatherThanMerges verifies (#5969) that
// resyncSessionResources REPLACES both the session's resource store AND its
// resource-template store with the freshly core-derived sets — unlike
// setSessionResourcesDirect/setSessionResourceTemplatesDirect's
// registration-time MERGE — so a resource/template the backend removed (and
// the core therefore no longer advertises) disappears rather than lingering.
func TestResyncSessionResources_ReplacesRatherThanMerges(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{
		resources:         []vmcp.Resource{{Name: "kept", URI: "kept"}, {Name: "added", URI: "added"}},
		resourceTemplates: []vmcp.ResourceTemplate{{Name: "kept-tmpl", URITemplate: "kept-tmpl"}},
	}
	srv := &Server{core: fc}

	sess := &fakeCapsSession{
		id: "sess-1",
		resources: map[string]server.ServerResource{
			"kept":    {Resource: mcp.Resource{Name: "kept", URI: "kept"}},
			"removed": {Resource: mcp.Resource{Name: "removed", URI: "removed"}}, // must disappear
		},
		resourceTemplates: map[string]server.ServerResourceTemplate{
			"kept-tmpl":    {Template: mcp.ResourceTemplate{Name: "kept-tmpl", URITemplate: "kept-tmpl"}},
			"removed-tmpl": {Template: mcp.ResourceTemplate{Name: "removed-tmpl", URITemplate: "removed-tmpl"}}, // must disappear
		},
	}

	err := srv.resyncSessionResources(context.Background(), sess, "sess-1", nil)
	require.NoError(t, err)

	gotResources := sess.GetSessionResources()
	assert.Len(t, gotResources, 2)
	assert.Contains(t, gotResources, "kept")
	assert.Contains(t, gotResources, "added")
	assert.NotContains(t, gotResources, "removed",
		"resync must REPLACE the resource store, dropping a no-longer-advertised resource")
	assert.Equal(t, 1, sess.setSessionResourcesCalls)

	gotTemplates := sess.GetSessionResourceTemplates()
	assert.Len(t, gotTemplates, 1)
	assert.Contains(t, gotTemplates, "kept-tmpl")
	assert.NotContains(t, gotTemplates, "removed-tmpl",
		"resync must REPLACE the resource-template store, dropping a no-longer-advertised template")
	assert.Equal(t, 1, sess.setSessionResourceTemplatesCalls)
}

// TestResyncSessionPrompts_ReplacesRatherThanMerges verifies (#5969) that
// resyncSessionPrompts REPLACES the session's prompt store with the freshly
// core-derived set — unlike setSessionPromptsDirect's registration-time
// MERGE — so a prompt the backend removed (and the core therefore no longer
// advertises) disappears rather than lingering.
func TestResyncSessionPrompts_ReplacesRatherThanMerges(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{prompts: []vmcp.Prompt{{Name: "kept"}, {Name: "added"}}}
	srv := &Server{core: fc}

	sess := &fakeCapsSession{id: "sess-1", prompts: map[string]server.ServerPrompt{
		"kept":    {Prompt: mcp.Prompt{Name: "kept"}},
		"removed": {Prompt: mcp.Prompt{Name: "removed"}}, // must disappear after resync
	}}

	err := srv.resyncSessionPrompts(context.Background(), sess, "sess-1", nil)
	require.NoError(t, err)

	got := sess.GetSessionPrompts()
	assert.Len(t, got, 2)
	assert.Contains(t, got, "kept")
	assert.Contains(t, got, "added")
	assert.NotContains(t, got, "removed", "resync must REPLACE the prompt store, dropping a no-longer-advertised prompt")
	assert.Equal(t, 1, sess.setSessionPromptsCalls)
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

// TestRunListChangedResync verifies (#5748, extended by #5969) the resync
// body run off the receive loop, per capability kind: it skips terminated
// sessions (liveness guard), and for a live session it invalidates the cache
// and reconstructs a context carrying the captured identity + forwarded
// headers (B-HIGH-1) before re-deriving/replacing ONLY the store belonging to
// the notification's kind.
func TestRunListChangedResync(t *testing.T) {
	t.Parallel()

	const fwdKey = "X-Tenant"
	identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "alice"}}
	fwd := map[string]string{fwdKey: "acme"}

	tests := []struct {
		name string
		kind vmcpsession.ChangeKind
	}{
		{"KindTools", vmcpsession.KindTools},
		{"KindResources", vmcpsession.KindResources},
		{"KindPrompts", vmcpsession.KindPrompts},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			t.Run("terminated session: skipped entirely", func(t *testing.T) {
				t.Parallel()
				fc := &fakeCore{
					tools:             []vmcp.Tool{{Name: "fresh"}},
					resources:         []vmcp.Resource{{Name: "fresh", URI: "fresh"}},
					resourceTemplates: []vmcp.ResourceTemplate{{Name: "fresh", URITemplate: "fresh"}},
					prompts:           []vmcp.Prompt{{Name: "fresh"}},
				}
				srv := &Server{core: fc, vmcpSessionMgr: &stubSessionManager{alive: false}}
				toolsSess := &fakeToolsSession{id: "sess-1"}
				capsSess := &fakeCapsSession{id: "sess-1"}

				var sess server.ClientSession = toolsSess
				if tc.kind != vmcpsession.KindTools {
					sess = capsSess
				}

				srv.runListChangedResync(context.Background(), "sess-1", sess, identity, fwd, tc.kind)

				assert.Equal(t, int32(0), fc.invalidateCacheCalls.Load(), "terminated session must not invalidate the cache")
				assert.Equal(t, int32(0), fc.listToolsCalls.Load(), "terminated session must not re-aggregate")
				assert.Equal(t, int32(0), fc.listResourcesCalls.Load(), "terminated session must not re-aggregate")
				assert.Equal(t, int32(0), fc.listResourceTemplatesCalls.Load(), "terminated session must not re-aggregate")
				assert.Equal(t, int32(0), fc.listPromptsCalls.Load(), "terminated session must not re-aggregate")
			})

			t.Run("live session: invalidates, reconstructs ctx, replaces only this kind", func(t *testing.T) {
				t.Parallel()
				fc := &fakeCore{
					tools:             []vmcp.Tool{{Name: "fresh"}},
					resources:         []vmcp.Resource{{Name: "fresh", URI: "fresh"}},
					resourceTemplates: []vmcp.ResourceTemplate{{Name: "fresh", URITemplate: "fresh"}},
					prompts:           []vmcp.Prompt{{Name: "fresh"}},
				}
				srv := &Server{core: fc, vmcpSessionMgr: &stubSessionManager{alive: true}}
				toolsSess := &fakeToolsSession{id: "sess-1", tools: map[string]server.ServerTool{
					"stale": {Tool: mcp.Tool{Name: "stale"}},
				}}
				capsSess := &fakeCapsSession{
					id: "sess-1",
					resources: map[string]server.ServerResource{
						"stale": {Resource: mcp.Resource{Name: "stale", URI: "stale"}},
					},
					resourceTemplates: map[string]server.ServerResourceTemplate{
						"stale": {Template: mcp.ResourceTemplate{Name: "stale", URITemplate: "stale"}},
					},
					prompts: map[string]server.ServerPrompt{
						"stale": {Prompt: mcp.Prompt{Name: "stale"}},
					},
				}

				var sess server.ClientSession = toolsSess
				if tc.kind != vmcpsession.KindTools {
					sess = capsSess
				}

				srv.runListChangedResync(context.Background(), "sess-1", sess, identity, fwd, tc.kind)

				assert.Equal(t, int32(1), fc.invalidateCacheCalls.Load(), "live session must invalidate the cache once")

				var resyncCtx context.Context
				switch tc.kind {
				case vmcpsession.KindTools:
					assert.Equal(t, int32(1), fc.listToolsCalls.Load())
					assert.Equal(t, int32(0), fc.listResourcesCalls.Load())
					assert.Equal(t, int32(0), fc.listResourceTemplatesCalls.Load())
					assert.Equal(t, int32(0), fc.listPromptsCalls.Load())
					assert.Equal(t, 1, toolsSess.setSessionToolsCalls)
					got := toolsSess.GetSessionTools()
					require.Len(t, got, 1)
					assert.Contains(t, got, "fresh")
					assert.NotContains(t, got, "stale", "resync must replace the stale set")
					box := fc.lastListToolsCtx.Load()
					require.NotNil(t, box, "ListTools must have been called")
					resyncCtx = box.ctx
				case vmcpsession.KindResources:
					assert.Equal(t, int32(0), fc.listToolsCalls.Load())
					assert.Equal(t, int32(1), fc.listResourcesCalls.Load())
					assert.Equal(t, int32(1), fc.listResourceTemplatesCalls.Load())
					assert.Equal(t, int32(0), fc.listPromptsCalls.Load())
					assert.Equal(t, 1, capsSess.setSessionResourcesCalls)
					assert.Equal(t, 1, capsSess.setSessionResourceTemplatesCalls)
					gotResources := capsSess.GetSessionResources()
					require.Len(t, gotResources, 1)
					assert.Contains(t, gotResources, "fresh")
					assert.NotContains(t, gotResources, "stale", "resync must replace the stale set")
					gotTemplates := capsSess.GetSessionResourceTemplates()
					require.Len(t, gotTemplates, 1)
					assert.Contains(t, gotTemplates, "fresh")
					assert.NotContains(t, gotTemplates, "stale", "resync must replace the stale set")
					box := fc.lastListResourcesCtx.Load()
					require.NotNil(t, box, "ListResources must have been called")
					resyncCtx = box.ctx
				case vmcpsession.KindPrompts:
					assert.Equal(t, int32(0), fc.listToolsCalls.Load())
					assert.Equal(t, int32(0), fc.listResourcesCalls.Load())
					assert.Equal(t, int32(0), fc.listResourceTemplatesCalls.Load())
					assert.Equal(t, int32(1), fc.listPromptsCalls.Load())
					assert.Equal(t, 1, capsSess.setSessionPromptsCalls)
					got := capsSess.GetSessionPrompts()
					require.Len(t, got, 1)
					assert.Contains(t, got, "fresh")
					assert.NotContains(t, got, "stale", "resync must replace the stale set")
					box := fc.lastListPromptsCtx.Load()
					require.NotNil(t, box, "ListPrompts must have been called")
					resyncCtx = box.ctx
				}

				// B-HIGH-1 (all kinds): the resync must call the core with a context
				// carrying the captured identity AND forwarded headers, so the cache
				// key and outbound backend auth match a real request from this
				// principal. Asserting this for every kind guards a future refactor
				// from silently dropping identity/header scoping on the
				// resources/prompts paths.
				require.NotNil(t, resyncCtx)
				gotID, ok := auth.IdentityFromContext(resyncCtx)
				require.True(t, ok, "resync context must carry the captured identity")
				assert.Equal(t, "alice", gotID.Subject)
				assert.Equal(t, "acme", headerforward.ForwardedHeadersFromContext(resyncCtx)[fwdKey],
					"resync context must carry the captured forwarded headers")
			})
		})
	}
}

// TestBuildListChangedSink_DispatchesByKind verifies (#5969) the sink's
// per-kind worker dispatch: a KindResources or KindPrompts notification
// triggers its own worker (invalidating the cache and resyncing only that
// kind's store), and an unknown ChangeKind is dropped synchronously (no
// worker goroutine, no cache invalidation, no resync).
func TestBuildListChangedSink_DispatchesByKind(t *testing.T) {
	t.Parallel()

	t.Run("KindResources triggers a resources-only resync", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{resources: []vmcp.Resource{{Name: "fresh", URI: "fresh"}}}
		srv := &Server{
			core:           fc,
			vmcpSessionMgr: &stubSessionManager{alive: true},
			resyncBaseCtx:  context.Background(),
		}
		sess := &fakeCapsSession{id: "sess-1"}

		sink := srv.buildListChangedSink("sess-1", sess, nil, nil)
		sink(context.Background(), "backend-1", vmcpsession.KindResources)

		require.Eventually(t, func() bool { return fc.invalidateCacheCalls.Load() >= 1 },
			2*time.Second, 10*time.Millisecond, "KindResources must invalidate the cache")
		require.Eventually(t, func() bool { return len(sess.GetSessionResources()) == 1 },
			2*time.Second, 10*time.Millisecond, "KindResources must resync the session's resources")
		assert.Equal(t, int32(0), fc.listToolsCalls.Load(), "a resources notification must not resync tools")
		assert.Equal(t, int32(0), fc.listPromptsCalls.Load(), "a resources notification must not resync prompts")
	})

	t.Run("KindPrompts triggers a prompts-only resync", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{prompts: []vmcp.Prompt{{Name: "fresh"}}}
		srv := &Server{
			core:           fc,
			vmcpSessionMgr: &stubSessionManager{alive: true},
			resyncBaseCtx:  context.Background(),
		}
		sess := &fakeCapsSession{id: "sess-1"}

		sink := srv.buildListChangedSink("sess-1", sess, nil, nil)
		sink(context.Background(), "backend-1", vmcpsession.KindPrompts)

		require.Eventually(t, func() bool { return fc.invalidateCacheCalls.Load() >= 1 },
			2*time.Second, 10*time.Millisecond, "KindPrompts must invalidate the cache")
		require.Eventually(t, func() bool { return len(sess.GetSessionPrompts()) == 1 },
			2*time.Second, 10*time.Millisecond, "KindPrompts must resync the session's prompts")
		assert.Equal(t, int32(0), fc.listToolsCalls.Load(), "a prompts notification must not resync tools")
		assert.Equal(t, int32(0), fc.listResourcesCalls.Load(), "a prompts notification must not resync resources")
	})

	t.Run("unknown kind is dropped synchronously", func(t *testing.T) {
		t.Parallel()
		fc := &fakeCore{tools: []vmcp.Tool{{Name: "fresh"}}}
		srv := &Server{
			core:           fc,
			vmcpSessionMgr: &stubSessionManager{alive: true},
			resyncBaseCtx:  context.Background(),
		}
		sess := &fakeToolsSession{id: "sess-1"}

		sink := srv.buildListChangedSink("sess-1", sess, nil, nil)
		sink(context.Background(), "backend-1", vmcpsession.ChangeKind("unknown"))

		assert.Equal(t, int32(0), fc.invalidateCacheCalls.Load(), "unknown kind must not invalidate the cache")
		assert.Equal(t, 0, sess.setSessionToolsCalls, "unknown kind must not resync anything")
	})
}
