// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubFactory returns ProxySessions with fixed timestamps and records IDs.
type stubFactory struct {
	mu         sync.Mutex
	createdIDs []string
	fixedTime  time.Time
}

func (f *stubFactory) New(id string) *ProxySession {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdIDs = append(f.createdIDs, id)
	return &ProxySession{
		id:      id,
		created: f.fixedTime,
		updated: f.fixedTime,
	}
}

func TestAddAndGetWithStubSession(t *testing.T) {
	t.Parallel()
	now := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	factory := &stubFactory{fixedTime: now}

	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID("foo"))

	sess, ok := m.Get("foo")
	require.True(t, ok, "session foo should exist")
	assert.Equal(t, "foo", sess.ID())
	assert.Contains(t, factory.createdIDs, "foo")
}

func TestAddDuplicate(t *testing.T) {
	t.Parallel()
	factory := &stubFactory{fixedTime: time.Now()}

	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID("dup"))

	err := m.AddWithID("dup")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestDeleteSession(t *testing.T) {
	t.Parallel()
	factory := &stubFactory{fixedTime: time.Now()}

	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID("del"))
	require.NoError(t, m.Delete("del"))

	_, ok := m.Get("del")
	assert.False(t, ok, "deleted session should not be found")
}

func TestGetUpdatesTimestamp(t *testing.T) {
	t.Parallel()
	oldTime := time.Now().Add(-1 * time.Minute)
	factory := &stubFactory{fixedTime: oldTime}

	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID("touchme"))
	s1, ok := m.Get("touchme")
	require.True(t, ok)
	t0 := s1.UpdatedAt()

	time.Sleep(10 * time.Millisecond)
	s2, ok2 := m.Get("touchme")
	require.True(t, ok2)
	t1 := s2.UpdatedAt()

	assert.True(t, t1.After(t0), "UpdatedAt should update on repeated Get()")
}
func TestCleanupExpired_ManualTrigger(t *testing.T) {
	t.Parallel()

	// Stub factory: all sessions start with UpdatedAt = `now`
	now := time.Now()
	factory := &stubFactory{fixedTime: now}
	ttl := 50 * time.Millisecond

	m := NewManager(ttl, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID("old"))

	// Retrieve and expire session manually
	sess, ok := m.Get("old")
	require.True(t, ok)
	ps := sess.(*ProxySession)
	ps.updated = now.Add(-ttl * 2)

	// Run cleanup manually
	m.cleanupExpiredOnce()

	// Now it should be gone
	_, okOld := m.Get("old")
	assert.False(t, okOld, "expired session should have been cleaned")

	// Add fresh session and assert it remains after cleanup
	require.NoError(t, m.AddWithID("new"))
	m.cleanupExpiredOnce()
	_, okNew := m.Get("new")
	assert.True(t, okNew, "new session should still exist after cleanup")
}

func TestStopDisablesCleanup(t *testing.T) {
	t.Parallel()
	ttl := 50 * time.Millisecond
	factory := &stubFactory{fixedTime: time.Now()}

	m := NewManager(ttl, factory.New)
	m.Stop() // disable cleanup before any session expires

	require.NoError(t, m.AddWithID("stay"))
	time.Sleep(ttl * 2)

	_, ok := m.Get("stay")
	assert.True(t, ok, "session should still be present even after Stop() and TTL elapsed")
}

func TestReplaceSession_NilSessionReturnsError(t *testing.T) {
	t.Parallel()

	factory := &stubFactory{fixedTime: time.Now()}
	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	err := m.ReplaceSession(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")
}

func TestReplaceSession_EmptyIDReturnsError(t *testing.T) {
	t.Parallel()

	factory := &stubFactory{fixedTime: time.Now()}
	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	// A session with an empty ID should be rejected.
	sess := &ProxySession{id: ""}
	err := m.ReplaceSession(sess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be empty")
}

func TestReplaceSession_UpsertNewSession(t *testing.T) {
	t.Parallel()

	factory := &stubFactory{fixedTime: time.Now()}
	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	// ReplaceSession on an ID that does not exist yet should store it.
	newSess := NewStreamableSession("brand-new-id")
	err := m.ReplaceSession(newSess)
	require.NoError(t, err)

	got, ok := m.Get("brand-new-id")
	require.True(t, ok, "session should exist after ReplaceSession upsert")
	assert.Equal(t, "brand-new-id", got.ID())
}

func TestReplaceSession_ReplacesExistingSession(t *testing.T) {
	t.Parallel()

	factory := &stubFactory{fixedTime: time.Now()}
	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	const sessionID = "replace-me"

	// Phase 1: store a placeholder via AddWithID (creates a ProxySession via factory).
	require.NoError(t, m.AddWithID(sessionID))

	// Confirm the placeholder is a *ProxySession.
	placeholder, ok := m.Get(sessionID)
	require.True(t, ok, "placeholder should exist before replacement")
	_, isProxy := placeholder.(*ProxySession)
	assert.True(t, isProxy, "placeholder should be a *ProxySession")

	// Phase 2: replace with a StreamableSession (different concrete type).
	replacement := NewStreamableSession(sessionID)
	err := m.ReplaceSession(replacement)
	require.NoError(t, err)

	// Verify that Get() now returns the replacement.
	got, ok := m.Get(sessionID)
	require.True(t, ok, "session should still exist after replacement")
	_, isStreamable := got.(*StreamableSession)
	assert.True(t, isStreamable, "stored session should now be a *StreamableSession (the replacement)")
}
