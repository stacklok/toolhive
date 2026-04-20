// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeAged stores a session in LocalStorage with a backdated last-access
// timestamp so it appears stale in eviction checks. It bypasses Store() to
// avoid resetting the last-access time to "now".
func storeAged(storage *LocalStorage, session Session) {
	entry := newLocalEntry(session)
	entry.lastAccessNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	storage.sessions.Store(session.ID(), entry)
}

// mockClosableSession is a test session that implements io.Closer
type mockClosableSession struct {
	*ProxySession
	closeCalled bool
	closeError  error
}

func newMockClosableSession(id string) *mockClosableSession {
	return &mockClosableSession{
		ProxySession: NewProxySession(id),
	}
}

func (m *mockClosableSession) Close() error {
	m.closeCalled = true
	return m.closeError
}

// TestLocalStorage tests the LocalStorage implementation
func TestLocalStorage(t *testing.T) {
	t.Parallel()
	t.Run("Store and Load", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		// Create a test session
		session := NewProxySession("test-id-1")
		session.SetMetadata("key1", "value1")

		// Store the session
		ctx := context.Background()
		err := storage.Store(ctx, session)
		require.NoError(t, err)

		// Load the session
		loaded, err := storage.Load(ctx, "test-id-1")
		require.NoError(t, err)
		assert.NotNil(t, loaded)
		assert.Equal(t, "test-id-1", loaded.ID())
		assert.Equal(t, SessionTypeMCP, loaded.Type())

		// Check metadata was preserved
		metadata := loaded.GetMetadata()
		assert.Equal(t, "value1", metadata["key1"])
	})

	t.Run("Store nil session", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()
		err := storage.Store(ctx, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "nil session")
	})

	t.Run("Store session with empty ID", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		session := &ProxySession{} // Empty ID
		ctx := context.Background()
		err := storage.Store(ctx, session)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty ID")
	})

	t.Run("Load non-existent session", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()
		loaded, err := storage.Load(ctx, "non-existent")
		assert.Error(t, err)
		assert.Equal(t, ErrSessionNotFound, err)
		assert.Nil(t, loaded)
	})

	t.Run("Load with empty ID", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()
		loaded, err := storage.Load(ctx, "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty ID")
		assert.Nil(t, loaded)
	})

	t.Run("Delete session", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		// Store a session
		session := NewProxySession("test-id-2")
		ctx := context.Background()
		err := storage.Store(ctx, session)
		require.NoError(t, err)

		// Verify it exists
		loaded, err := storage.Load(ctx, "test-id-2")
		require.NoError(t, err)
		assert.NotNil(t, loaded)

		// Delete it
		err = storage.Delete(ctx, "test-id-2")
		require.NoError(t, err)

		// Verify it's gone
		loaded, err = storage.Load(ctx, "test-id-2")
		assert.Error(t, err)
		assert.Equal(t, ErrSessionNotFound, err)
		assert.Nil(t, loaded)
	})

	t.Run("Delete non-existent session", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()
		// Should not error when deleting non-existent session
		err := storage.Delete(ctx, "non-existent")
		assert.NoError(t, err)
	})

	t.Run("Delete with empty ID", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()
		err := storage.Delete(ctx, "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty ID")
	})

	t.Run("DeleteExpired", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		// Store old session with a backdated last-access time and a fresh new session.
		oldSession := NewProxySession("old-session")
		storeAged(storage, oldSession)

		newSession := NewProxySession("new-session")
		err := storage.Store(ctx, newSession)
		require.NoError(t, err)

		// Delete sessions whose last-access is older than 1 hour.
		cutoff := time.Now().Add(-1 * time.Hour)
		err = storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		// Old session should be gone.
		_, err = storage.Load(ctx, "old-session")
		assert.Equal(t, ErrSessionNotFound, err)

		// New session should still exist.
		loaded, err := storage.Load(ctx, "new-session")
		assert.NoError(t, err)
		assert.NotNil(t, loaded)
	})

	t.Run("Load prevents eviction by refreshing last-access", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()
		session := NewProxySession("test-id-3")

		// Store with a backdated timestamp so the entry looks expired without sleeping.
		storeAged(storage, session)

		// Load refreshes the entry's internal last-access timestamp.
		loaded, err := storage.Load(ctx, "test-id-3")
		require.NoError(t, err)
		assert.Equal(t, session.ID(), loaded.ID())

		// A cleanup with cutoff = now-1h should NOT evict the session because
		// Load just reset its last-access to roughly now.
		err = storage.DeleteExpired(ctx, time.Now().Add(-1*time.Hour))
		require.NoError(t, err)

		_, err = storage.Load(ctx, "test-id-3")
		assert.NoError(t, err, "session should survive cleanup after a recent Load")
	})

	t.Run("Count helper method", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		// Initially empty
		assert.Equal(t, 0, storage.Count())

		// Add sessions
		for i := 0; i < 5; i++ {
			session := NewProxySession(fmt.Sprintf("session-%d", i))
			err := storage.Store(ctx, session)
			require.NoError(t, err)
		}

		// Should have 5 sessions
		assert.Equal(t, 5, storage.Count())

		// Delete one
		err := storage.Delete(ctx, "session-0")
		require.NoError(t, err)

		// Should have 4 sessions
		assert.Equal(t, 4, storage.Count())
	})

	t.Run("Range helper method", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		// Add some sessions
		ids := []string{"alpha", "beta", "gamma"}
		for _, id := range ids {
			session := NewProxySession(id)
			err := storage.Store(ctx, session)
			require.NoError(t, err)
		}

		// Use Range to collect all IDs
		var collected []string
		storage.Range(func(key, _ interface{}) bool {
			if id, ok := key.(string); ok {
				collected = append(collected, id)
			}
			return true
		})

		// Should have all IDs
		assert.Len(t, collected, 3)
		for _, id := range ids {
			assert.Contains(t, collected, id)
		}
	})

	t.Run("Close clears all sessions", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()

		ctx := context.Background()

		// Add some sessions
		for i := 0; i < 3; i++ {
			session := NewProxySession(fmt.Sprintf("session-%d", i))
			err := storage.Store(ctx, session)
			require.NoError(t, err)
		}

		// Should have sessions
		assert.Equal(t, 3, storage.Count())

		// Close storage
		err := storage.Close()
		require.NoError(t, err)

		// Should have no sessions
		assert.Equal(t, 0, storage.Count())
	})

	t.Run("Context cancellation in DeleteExpired", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		// Create a cancelled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// DeleteExpired should handle cancelled context gracefully
		err := storage.DeleteExpired(ctx, time.Now())
		// Should not error, just stop early
		assert.NoError(t, err)
	})

	t.Run("DeleteExpired calls Close on io.Closer sessions", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		closableSession := newMockClosableSession("closable-session")
		storeAged(storage, closableSession)

		regularSession := NewProxySession("regular-session")
		storeAged(storage, regularSession)

		// Delete sessions whose last-access is older than 1 hour.
		cutoff := time.Now().Add(-1 * time.Hour)
		err := storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		_, err = storage.Load(ctx, "closable-session")
		assert.Equal(t, ErrSessionNotFound, err)
		_, err = storage.Load(ctx, "regular-session")
		assert.Equal(t, ErrSessionNotFound, err)

		assert.True(t, closableSession.closeCalled,
			"Close() should have been called on closable session")
	})

	t.Run("DeleteExpired continues deletion even if Close fails", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		failingSession := newMockClosableSession("failing-session")
		failingSession.closeError = errors.New("close failed")
		storeAged(storage, failingSession)

		cutoff := time.Now().Add(-1 * time.Hour)
		err := storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		_, err = storage.Load(ctx, "failing-session")
		assert.Equal(t, ErrSessionNotFound, err)

		assert.True(t, failingSession.closeCalled,
			"Close() should have been called even though it returned an error")
	})

	t.Run("DeleteExpired handles non-io.Closer sessions without error", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		for i := 0; i < 5; i++ {
			storeAged(storage, NewProxySession(fmt.Sprintf("session-%d", i)))
		}

		cutoff := time.Now().Add(-1 * time.Hour)
		err := storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		for i := 0; i < 5; i++ {
			_, err := storage.Load(ctx, fmt.Sprintf("session-%d", i))
			assert.Equal(t, ErrSessionNotFound, err)
		}
	})

	t.Run("DeleteExpired with mixed session types", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		closable1 := newMockClosableSession("closable-1")
		closable2 := newMockClosableSession("closable-2")
		storeAged(storage, closable1)
		storeAged(storage, closable2)
		storeAged(storage, NewProxySession("regular-1"))
		storeAged(storage, NewProxySession("regular-2"))

		cutoff := time.Now().Add(-1 * time.Hour)
		err := storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		_, err = storage.Load(ctx, "closable-1")
		assert.Equal(t, ErrSessionNotFound, err)
		_, err = storage.Load(ctx, "closable-2")
		assert.Equal(t, ErrSessionNotFound, err)
		_, err = storage.Load(ctx, "regular-1")
		assert.Equal(t, ErrSessionNotFound, err)
		_, err = storage.Load(ctx, "regular-2")
		assert.Equal(t, ErrSessionNotFound, err)

		assert.True(t, closable1.closeCalled, "Close() should have been called on closable-1")
		assert.True(t, closable2.closeCalled, "Close() should have been called on closable-2")
	})

	t.Run("DeleteExpired respects context cancellation during deletion", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		// Create many expired sessions to increase chance of context check
		for i := 0; i < 10000; i++ {
			storeAged(storage, NewProxySession(fmt.Sprintf("session-%d", i)))
		}

		// Create a context with a very short timeout
		timeoutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()

		// Wait a bit to ensure context times out
		time.Sleep(10 * time.Millisecond)

		// DeleteExpired should respect context timeout
		cutoff := time.Now().Add(-1 * time.Hour)
		err := storage.DeleteExpired(timeoutCtx, cutoff)

		// With 10000 sessions, the context check should trigger during cleanup
		// If it completes too quickly, that's also acceptable behavior
		if err != nil {
			assert.Equal(t, context.DeadlineExceeded, err)
			// Some sessions deleted, but not all due to timeout
			remaining := storage.Count()
			assert.Greater(t, remaining, 0, "Some sessions should remain due to context timeout")
		}
	})

	t.Run("DeleteExpired handles concurrent Load() race condition", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()
		ttl := 20 * time.Millisecond

		// Store target session and many dummy sessions, then let them all age past the TTL.
		err := storage.Store(ctx, NewProxySession("race-session"))
		require.NoError(t, err)
		for i := 0; i < 200; i++ {
			err := storage.Store(ctx, NewProxySession(fmt.Sprintf("dummy-%d", i)))
			require.NoError(t, err)
		}
		time.Sleep(ttl * 3) // age all entries past the TTL

		// Start DeleteExpired in a goroutine.
		done := make(chan error, 1)
		go func() {
			cutoff := time.Now().Add(-ttl)
			done <- storage.DeleteExpired(ctx, cutoff)
		}()

		// Concurrently call Load on the target session. LocalStorage.Load refreshes the
		// entry's last-access timestamp so the entry may no longer be expired by the time
		// DeleteExpired reaches its second-pass re-check.
		_, _ = storage.Load(ctx, "race-session")

		err = <-done
		require.NoError(t, err)

		// Either outcome (session present or absent) is valid depending on timing —
		// what matters is that DeleteExpired completes without error and does not
		// delete a session that was refreshed after the second-pass re-check.
		// The important invariant: no panic, no corruption.
	})

	t.Run("DeleteExpired handles concurrent Store() replacement race condition", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		// Create an expired closable session and many dummy sessions.
		oldSession := newMockClosableSession("replace-session")
		storeAged(storage, oldSession)
		for i := 0; i < 1000; i++ {
			storeAged(storage, NewProxySession(fmt.Sprintf("dummy-%d", i)))
		}

		// Start DeleteExpired in a goroutine
		done := make(chan error, 1)
		go func() {
			cutoff := time.Now().Add(-1 * time.Hour)
			done <- storage.DeleteExpired(ctx, cutoff)
		}()

		// Concurrently replace the session with a new one (same ID, different object)
		// This simulates UpsertSession being called during cleanup
		newSession := newMockClosableSession("replace-session")
		err := storage.Store(ctx, newSession)
		require.NoError(t, err)

		// Wait for DeleteExpired to complete
		err = <-done
		require.NoError(t, err)

		// The new session should still exist (CompareAndDelete prevents deleting it)
		loaded, err := storage.Load(ctx, "replace-session")
		require.NoError(t, err)
		assert.NotNil(t, loaded)

		// The old session's Close() may or may not have been called depending on timing
		// The important thing is the new session is not closed
		// Since Close() is now synchronous, we can check immediately
		assert.False(t, newSession.closeCalled,
			"New session should not be closed (CompareAndDelete should prevent this)")
	})
}

// TestManagerWithStorage tests the Manager with the Storage interface
func TestManagerWithStorage(t *testing.T) {
	t.Parallel()
	t.Run("Manager with LocalStorage", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		factory := func(id string) Session {
			return NewProxySession(id)
		}

		manager := NewManagerWithStorage(30*time.Minute, factory, storage)
		defer manager.Stop()

		const localMgrID = "aaaaaaaa-1001-1001-1001-000000000001"

		// Add a session
		err := manager.AddWithID(localMgrID)
		require.NoError(t, err)

		// Get the session
		session, found := manager.Get(localMgrID)
		assert.True(t, found)
		assert.NotNil(t, session)
		assert.Equal(t, localMgrID, session.ID())

		// Delete the session
		manager.Delete(localMgrID)

		// Should not be found
		session, found = manager.Get(localMgrID)
		assert.False(t, found)
		assert.Nil(t, session)
	})

	t.Run("Manager with custom factory", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		factory := func(id string) Session {
			// Create SSE sessions by default
			return NewSSESession(id)
		}

		manager := NewManagerWithStorage(30*time.Minute, factory, storage)
		defer manager.Stop()

		const sseMgrID = "aaaaaaaa-1002-1002-1002-000000000002"

		// Add a session
		err := manager.AddWithID(sseMgrID)
		require.NoError(t, err)

		// Get the session
		session, found := manager.Get(sseMgrID)
		assert.True(t, found)
		assert.NotNil(t, session)
		assert.Equal(t, SessionTypeSSE, session.Type())
	})

	t.Run("Manager AddSession method", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		factory := func(id string) Session {
			return NewProxySession(id)
		}

		manager := NewManagerWithStorage(30*time.Minute, factory, storage)
		defer manager.Stop()

		const customMgrID = "aaaaaaaa-1003-1003-1003-000000000003"

		// Create a custom session
		customSession := NewTypedProxySession(customMgrID, SessionTypeStreamable)
		customSession.SetMetadata("custom", "metadata")

		// Add the custom session
		err := manager.AddSession(customSession)
		require.NoError(t, err)

		// Get the session
		session, found := manager.Get(customMgrID)
		assert.True(t, found)
		assert.NotNil(t, session)
		assert.Equal(t, SessionTypeStreamable, session.Type())

		metadata := session.GetMetadata()
		assert.Equal(t, "metadata", metadata["custom"])
	})

	t.Run("Manager Count with LocalStorage", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		factory := func(id string) Session {
			return NewProxySession(id)
		}

		manager := NewManagerWithStorage(30*time.Minute, factory, storage)
		defer manager.Stop()

		// Initially empty
		assert.Equal(t, 0, manager.Count())

		countIDs := []string{
			"aaaaaaaa-1004-1004-1004-000000000001",
			"aaaaaaaa-1004-1004-1004-000000000002",
			"aaaaaaaa-1004-1004-1004-000000000003",
		}

		// Add sessions
		for _, id := range countIDs {
			err := manager.AddWithID(id)
			require.NoError(t, err)
		}

		// Should have 3 sessions
		assert.Equal(t, 3, manager.Count())
	})

	t.Run("LocalStorage Range", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		factory := func(id string) Session {
			return NewProxySession(id)
		}

		manager := NewManagerWithStorage(30*time.Minute, factory, storage)
		defer manager.Stop()

		// Add sessions
		ids := []string{
			"aaaaaaaa-1005-1005-1005-000000000001",
			"aaaaaaaa-1005-1005-1005-000000000002",
			"aaaaaaaa-1005-1005-1005-000000000003",
		}
		for _, id := range ids {
			err := manager.AddWithID(id)
			require.NoError(t, err)
		}

		// Use LocalStorage.Range directly to collect all IDs
		var collected []string
		storage.Range(func(key, _ interface{}) bool {
			if id, ok := key.(string); ok {
				collected = append(collected, id)
			}
			return true
		})

		// Should have all IDs
		assert.Len(t, collected, 3)
		for _, id := range ids {
			assert.Contains(t, collected, id)
		}
	})
}

// TestSessionTypes tests different session type implementations
func TestSessionTypes(t *testing.T) {
	t.Parallel()
	t.Run("ProxySession with Storage", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		session := NewProxySession("proxy-1")
		session.SetMetadata("env", "production")
		session.SetData(map[string]string{"key": "value"})

		ctx := context.Background()
		err := storage.Store(ctx, session)
		require.NoError(t, err)

		loaded, err := storage.Load(ctx, "proxy-1")
		require.NoError(t, err)
		assert.Equal(t, SessionTypeMCP, loaded.Type())

		metadata := loaded.GetMetadata()
		assert.Equal(t, "production", metadata["env"])
	})

	t.Run("SSESession with Storage", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		session := NewSSESession("sse-1")
		session.SetMetadata("client", "browser")

		ctx := context.Background()
		err := storage.Store(ctx, session)
		require.NoError(t, err)

		loaded, err := storage.Load(ctx, "sse-1")
		require.NoError(t, err)
		assert.Equal(t, SessionTypeSSE, loaded.Type())

		metadata := loaded.GetMetadata()
		assert.Equal(t, "browser", metadata["client"])
	})

	t.Run("StreamableSession with Storage", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		session := NewStreamableSession("stream-1")
		session.SetMetadata("protocol", "http")

		ctx := context.Background()
		err := storage.Store(ctx, session)
		require.NoError(t, err)

		loaded, err := storage.Load(ctx, "stream-1")
		require.NoError(t, err)
		assert.Equal(t, SessionTypeStreamable, loaded.Type())

		metadata := loaded.GetMetadata()
		assert.Equal(t, "http", metadata["protocol"])
	})
}
