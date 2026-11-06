// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// registerResyncSession registers a live session for identity with srv's resync registry,
// the way session registration does, and returns its fake SDK session.
func registerResyncSession(srv *Server, id string, identity *auth.Identity) *fakeToolsSession {
	sess := &fakeToolsSession{id: id}
	_, toolsWorker := srv.buildListChangedSink(id, sess, identity, nil)
	srv.healthResync.add(id, toolsWorker)
	return sess
}

func newResyncTestServer(fc *fakeCore) *Server {
	return &Server{
		core:           fc,
		vmcpSessionMgr: &stubSessionManager{alive: true},
		resyncBaseCtx:  context.Background(),
	}
}

// TestResyncSessionTools_ResyncsOnlyMatchingSessions verifies an embedder can resync the
// sessions of one identity after a change only that identity sees: the matching session's
// tool store is replaced with the core's current view, the other session is untouched, and
// the shared capability cache is purged once for the call.
func TestResyncTools_ResyncsOnlyMatchingSessions(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "recovered"}}}
	srv := newResyncTestServer(fc)
	alice := registerResyncSession(srv, "alice-session", &auth.Identity{Subject: "alice"})
	bob := registerResyncSession(srv, "bob-session", &auth.Identity{Subject: "bob"})

	triggered := srv.ResyncTools(func(id *auth.Identity) bool {
		return id != nil && id.Subject == "alice"
	})

	assert.Equal(t, 1, triggered)
	require.Eventually(t, func() bool { return alice.setToolsCalls() > 0 },
		2*time.Second, 10*time.Millisecond, "the matching session must be resynced")
	assert.Contains(t, alice.GetSessionTools(), "recovered")
	assert.Equal(t, 0, bob.setToolsCalls(), "a session whose identity did not match must not be resynced")
	assert.Equal(t, int32(1), fc.invalidateCacheCalls.Load(),
		"the call must purge the capability cache once, so the re-derivation re-sweeps backends")
}

// TestResyncSessionTools_NilMatchResyncsEverySession verifies a nil match selects every
// registered session, including one with no captured identity.
func TestResyncTools_NilMatchResyncsEverySession(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv := newResyncTestServer(fc)
	sessions := []*fakeToolsSession{
		registerResyncSession(srv, "alice-session", &auth.Identity{Subject: "alice"}),
		registerResyncSession(srv, "anonymous-session", nil),
	}

	triggered := srv.ResyncTools(nil)

	assert.Equal(t, 2, triggered)
	for _, sess := range sessions {
		require.Eventually(t, func() bool { return sess.setToolsCalls() > 0 },
			2*time.Second, 10*time.Millisecond, "every registered session must be resynced")
	}
}

// TestResyncSessionTools_MatchReceivesNilForAnAnonymousSession verifies the match function is
// called with nil, not skipped, for a session registered without an identity, so the embedder
// decides whether such a session is in scope.
func TestResyncTools_MatchReceivesNilForAnAnonymousSession(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv := newResyncTestServer(fc)
	anonymous := registerResyncSession(srv, "anonymous-session", nil)

	triggered := srv.ResyncTools(func(id *auth.Identity) bool { return id == nil })

	assert.Equal(t, 1, triggered)
	require.Eventually(t, func() bool { return anonymous.setToolsCalls() > 0 },
		2*time.Second, 10*time.Millisecond, "an anonymous session the match function accepts must be resynced")
}

// TestResyncSessionTools_NoMatchDoesNothing verifies a call that selects no session neither
// triggers a resync nor purges the capability cache.
func TestResyncTools_NoMatchDoesNothing(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv := newResyncTestServer(fc)
	alice := registerResyncSession(srv, "alice-session", &auth.Identity{Subject: "alice"})

	triggered := srv.ResyncTools(func(*auth.Identity) bool { return false })

	assert.Equal(t, 0, triggered)
	assert.Equal(t, 0, alice.setToolsCalls())
	assert.Equal(t, int32(0), fc.invalidateCacheCalls.Load(), "a call that selects nothing must not purge the cache")
}

// TestResyncSessionTools_OptimizerModeDoesNothing verifies optimizer mode is a no-op: the
// advertised set is the find_tool/call_tool meta-tools, which a per-identity change does not
// alter.
func TestResyncTools_OptimizerModeDoesNothing(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{tools: []vmcp.Tool{{Name: "t"}}}
	srv := newResyncTestServer(fc)
	srv.optimizerFactory = stubOptimizerFactory
	alice := registerResyncSession(srv, "alice-session", &auth.Identity{Subject: "alice"})

	triggered := srv.ResyncTools(nil)

	assert.Equal(t, 0, triggered)
	assert.Equal(t, 0, alice.setToolsCalls())
	assert.Equal(t, int32(0), fc.invalidateCacheCalls.Load())
}
