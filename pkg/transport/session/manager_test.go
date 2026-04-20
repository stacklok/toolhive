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

const (
	uuidFoo       = "11111111-1111-1111-1111-111111111111"
	uuidDup       = "22222222-2222-2222-2222-222222222222"
	uuidDel       = "33333333-3333-3333-3333-333333333333"
	uuidTouchme   = "44444444-4444-4444-4444-444444444444"
	uuidOld       = "55555555-5555-5555-5555-555555555555"
	uuidNew       = "66666666-6666-6666-6666-666666666666"
	uuidStay      = "77777777-7777-7777-7777-777777777777"
	uuidBrandNew  = "88888888-8888-8888-8888-888888888888"
	uuidReplaceMe = "99999999-9999-9999-9999-999999999999"
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

	require.NoError(t, m.AddWithID(uuidFoo))

	sess, ok := m.Get(uuidFoo)
	require.True(t, ok, "session foo should exist")
	assert.Equal(t, uuidFoo, sess.ID())
	assert.Contains(t, factory.createdIDs, uuidFoo)
}

func TestInvalidSessionID(t *testing.T) {
	t.Parallel()
	factory := &stubFactory{fixedTime: time.Now()}
	m := NewManager(time.Hour, factory.New)
	t.Cleanup(func() { m.Stop() })

	t.Run("AddWithID rejects non-UUID", func(t *testing.T) {
		t.Parallel()
		err := m.AddWithID("not-a-uuid")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid session ID format")
	})

	t.Run("AddSession rejects non-UUID", func(t *testing.T) {
		t.Parallel()
		err := m.AddSession(&ProxySession{id: "not-a-uuid"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid session ID format")
	})

	t.Run("UpsertSession rejects non-UUID", func(t *testing.T) {
		t.Parallel()
		err := m.UpsertSession(&ProxySession{id: "not-a-uuid"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid session ID format")
	})

	t.Run("Delete rejects non-UUID", func(t *testing.T) {
		t.Parallel()
		err := m.Delete("not-a-uuid")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid session ID format")
	})
}

func TestAddDuplicate(t *testing.T) {
	t.Parallel()
	factory := &stubFactory{fixedTime: time.Now()}

	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID(uuidDup))

	err := m.AddWithID(uuidDup)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestDeleteSession(t *testing.T) {
	t.Parallel()
	factory := &stubFactory{fixedTime: time.Now()}

	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID(uuidDel))
	require.NoError(t, m.Delete(uuidDel))

	_, ok := m.Get(uuidDel)
	assert.False(t, ok, "deleted session should not be found")
}

func TestGetPreventsEviction(t *testing.T) {
	t.Parallel()
	oldTime := time.Now().Add(-2 * time.Hour)
	factory := &stubFactory{fixedTime: oldTime}
	ttl := 1 * time.Hour

	m := NewManager(ttl, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID(uuidTouchme))

	// LocalStorage.Store() stamps lastAccessNano = time.Now(), so the entry is
	// always fresh after AddWithID. Backdate it so the session looks expired and
	// would be evicted if Get() did not refresh the timestamp.
	ls := m.storage.(*LocalStorage)
	val, ok := ls.sessions.Load(uuidTouchme)
	require.True(t, ok, "entry must exist in storage before backdating")
	val.(*localEntry).lastAccessNano.Store(oldTime.UnixNano())

	// Get() refreshes the storage-level last-access time by swapping in a new entry.
	_, ok = m.Get(uuidTouchme)
	require.True(t, ok)

	// Cleanup with a cutoff of "now minus ttl" should NOT evict the session
	// because Get() just refreshed its last-access timestamp.
	require.NoError(t, m.cleanupExpiredOnce())

	_, stillPresent := m.Get(uuidTouchme)
	assert.True(t, stillPresent, "session should survive cleanup after a recent Get()")
}
func TestCleanupExpired_ManualTrigger(t *testing.T) {
	t.Parallel()

	factory := &stubFactory{fixedTime: time.Now()}
	ttl := 50 * time.Millisecond

	m := NewManager(ttl, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID(uuidOld))

	// Wait for the session's last-access time to become older than the TTL.
	time.Sleep(ttl * 2)

	// Run cleanup — the stale session should be evicted.
	m.cleanupExpiredOnce()

	_, okOld := m.Get(uuidOld)
	assert.False(t, okOld, "expired session should have been cleaned")

	// A freshly-added session must survive the next cleanup run.
	require.NoError(t, m.AddWithID(uuidNew))
	m.cleanupExpiredOnce()
	_, okNew := m.Get(uuidNew)
	assert.True(t, okNew, "new session should still exist after cleanup")
}

func TestStopDisablesCleanup(t *testing.T) {
	t.Parallel()
	ttl := 50 * time.Millisecond
	factory := &stubFactory{fixedTime: time.Now()}

	m := NewManager(ttl, factory.New)
	m.Stop() // disable cleanup before any session expires

	require.NoError(t, m.AddWithID(uuidStay))
	time.Sleep(ttl * 2)

	_, ok := m.Get(uuidStay)
	assert.True(t, ok, "session should still be present even after Stop() and TTL elapsed")
}

func TestUpsertSession_NilSessionReturnsError(t *testing.T) {
	t.Parallel()

	factory := &stubFactory{fixedTime: time.Now()}
	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	err := m.UpsertSession(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")
}

func TestUpsertSession_EmptyIDReturnsError(t *testing.T) {
	t.Parallel()

	factory := &stubFactory{fixedTime: time.Now()}
	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	// A session with an empty ID should be rejected.
	sess := &ProxySession{id: ""}
	err := m.UpsertSession(sess)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be empty")
}

func TestUpsertSession_UpsertNewSession(t *testing.T) {
	t.Parallel()

	factory := &stubFactory{fixedTime: time.Now()}
	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	// UpsertSession on an ID that does not exist yet should store it.
	newSess := NewStreamableSession(uuidBrandNew)
	err := m.UpsertSession(newSess)
	require.NoError(t, err)

	got, ok := m.Get(uuidBrandNew)
	require.True(t, ok, "session should exist after UpsertSession upsert")
	assert.Equal(t, uuidBrandNew, got.ID())
}

func TestUpsertSession_ReplacesExistingSession(t *testing.T) {
	t.Parallel()

	factory := &stubFactory{fixedTime: time.Now()}
	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	const sessionID = uuidReplaceMe

	// Phase 1: store a placeholder via AddWithID (creates a ProxySession via factory).
	require.NoError(t, m.AddWithID(sessionID))

	// Confirm the placeholder is a *ProxySession.
	placeholder, ok := m.Get(sessionID)
	require.True(t, ok, "placeholder should exist before replacement")
	_, isProxy := placeholder.(*ProxySession)
	assert.True(t, isProxy, "placeholder should be a *ProxySession")

	// Phase 2: replace with a StreamableSession (different concrete type).
	replacement := NewStreamableSession(sessionID)
	err := m.UpsertSession(replacement)
	require.NoError(t, err)

	// Verify that Get() now returns the replacement.
	got, ok := m.Get(sessionID)
	require.True(t, ok, "session should still exist after replacement")
	_, isStreamable := got.(*StreamableSession)
	assert.True(t, isStreamable, "stored session should now be a *StreamableSession (the replacement)")
}
