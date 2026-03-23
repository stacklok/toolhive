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

func TestGetUpdatesTimestamp(t *testing.T) {
	t.Parallel()
	oldTime := time.Now().Add(-1 * time.Minute)
	factory := &stubFactory{fixedTime: oldTime}

	m := NewManager(time.Hour, factory.New)
	defer m.Stop()

	require.NoError(t, m.AddWithID(uuidTouchme))
	s1, ok := m.Get(uuidTouchme)
	require.True(t, ok)
	t0 := s1.UpdatedAt()

	time.Sleep(10 * time.Millisecond)
	s2, ok2 := m.Get(uuidTouchme)
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

	require.NoError(t, m.AddWithID(uuidOld))

	// Retrieve and expire session manually
	sess, ok := m.Get(uuidOld)
	require.True(t, ok)
	ps := sess.(*ProxySession)
	ps.updated = now.Add(-ttl * 2)

	// Run cleanup manually
	m.cleanupExpiredOnce()

	// Now it should be gone
	_, okOld := m.Get(uuidOld)
	assert.False(t, okOld, "expired session should have been cleaned")

	// Add fresh session and assert it remains after cleanup
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
