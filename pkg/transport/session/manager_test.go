package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddAndGetWithStubSession(t *testing.T) {
	orig := NewProxySession
	NewProxySession = func(id string) *ProxySession {
		ts := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		return &ProxySession{id: id, created: ts, updated: ts}
	}
	defer func() { NewProxySession = orig }()

	m := NewManager(1 * time.Hour)
	defer m.Stop()

	require.NoError(t, m.AddWithID("foo"))

	sess, ok := m.Get("foo")
	require.True(t, ok)
	assert.Equal(t, "foo", sess.ID())
}

func TestAddDuplicate(t *testing.T) {
	m := NewManager(time.Hour)
	defer m.Stop()

	err := m.AddWithID("dup")
	assert.NoError(t, err)

	err2 := m.AddWithID("dup")
	assert.Error(t, err2)
	assert.Contains(t, err2.Error(), "already exists")
}

func TestDeleteSession(t *testing.T) {
	m := NewManager(time.Hour)
	defer m.Stop()

	require.NoError(t, m.AddWithID("del"))
	m.Delete("del")

	_, ok := m.Get("del")
	assert.False(t, ok)
}

func TestGetUpdatesTimestamp(t *testing.T) {
	orig := NewProxySession
	NewProxySession = func(id string) *ProxySession {
		ts := time.Now().Add(-1 * time.Minute)
		return &ProxySession{id: id, created: ts, updated: ts}
	}
	defer func() { NewProxySession = orig }()

	m := NewManager(1 * time.Hour)
	defer m.Stop()

	require.NoError(t, m.AddWithID("touchme"))
	s1, _ := m.Get("touchme")
	t0 := s1.UpdatedAt()

	time.Sleep(5 * time.Millisecond)
	s2, _ := m.Get("touchme")
	t1 := s2.UpdatedAt()

	assert.True(t, t1.After(t0), "UpdatedAt should update on Get()")
}

func TestCleanupExpired(t *testing.T) {
	ttl := 50 * time.Millisecond
	orig := NewProxySession
	NewProxySession = func(id string) *ProxySession {
		return &ProxySession{
			id:      id,
			created: time.Now(),
			updated: time.Now(),
		}
	}
	defer func() { NewProxySession = orig }()

	m := NewManager(ttl)
	defer m.Stop()

	require.NoError(t, m.AddWithID("old"))
	time.Sleep(ttl * 2) // allow old to expire

	require.NoError(t, m.AddWithID("new"))
	time.Sleep(ttl) // let cleanup execute

	_, okOld := m.Get("old")
	_, okNew := m.Get("new")
	assert.False(t, okOld, "expired session should be cleaned")
	assert.True(t, okNew, "recent session should remain")
}

func TestStopDisablesCleanup(t *testing.T) {
	ttl := 50 * time.Millisecond
	m := NewManager(ttl)
	m.Stop() // stop cleanup upfront

	require.NoError(t, m.AddWithID("stay"))
	time.Sleep(ttl * 2)

	_, ok := m.Get("stay")
	assert.True(t, ok, "session should persist after Stop()")
}
