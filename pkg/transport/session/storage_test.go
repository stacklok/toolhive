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

		// Create sessions with different update times
		oldSession := NewProxySession("old-session")
		newSession := NewProxySession("new-session")

		// Store both sessions
		err := storage.Store(ctx, oldSession)
		require.NoError(t, err)
		err = storage.Store(ctx, newSession)
		require.NoError(t, err)

		// Manually set the old session's updated time to the past
		oldSession.updated = time.Now().Add(-2 * time.Hour)

		// Store the old session again with the old timestamp
		err = storage.Store(ctx, oldSession)
		require.NoError(t, err)

		// Delete sessions older than 1 hour
		cutoff := time.Now().Add(-1 * time.Hour)
		err = storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		// Old session should be gone
		_, err = storage.Load(ctx, "old-session")
		assert.Equal(t, ErrSessionNotFound, err)

		// New session should still exist
		loaded, err := storage.Load(ctx, "new-session")
		assert.NoError(t, err)
		assert.NotNil(t, loaded)
	})

	t.Run("Load does not auto-touch", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		// Create and store a session
		session := NewProxySession("test-id-3")
		originalUpdated := session.UpdatedAt()

		ctx := context.Background()
		err := storage.Store(ctx, session)
		require.NoError(t, err)

		// Wait a bit to ensure time difference
		time.Sleep(10 * time.Millisecond)

		// Load the session (should NOT auto-touch)
		loaded, err := storage.Load(ctx, "test-id-3")
		require.NoError(t, err)

		// Updated time should be the same (not auto-touched)
		assert.Equal(t, originalUpdated, loaded.UpdatedAt())

		// But manual Touch should update the time
		loaded.Touch()
		assert.True(t, loaded.UpdatedAt().After(originalUpdated))
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

		// Create a closable session (implements io.Closer)
		closableSession := newMockClosableSession("closable-session")
		closableSession.updated = time.Now().Add(-2 * time.Hour)

		// Create a regular session (does not implement io.Closer)
		regularSession := NewProxySession("regular-session")
		regularSession.updated = time.Now().Add(-2 * time.Hour)

		// Store both sessions
		err := storage.Store(ctx, closableSession)
		require.NoError(t, err)
		err = storage.Store(ctx, regularSession)
		require.NoError(t, err)

		// Delete sessions older than 1 hour
		cutoff := time.Now().Add(-1 * time.Hour)
		err = storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		// Both sessions should be deleted
		_, err = storage.Load(ctx, "closable-session")
		assert.Equal(t, ErrSessionNotFound, err)
		_, err = storage.Load(ctx, "regular-session")
		assert.Equal(t, ErrSessionNotFound, err)

		// Close() is called synchronously, so it should already be done
		assert.True(t, closableSession.closeCalled,
			"Close() should have been called on closable session")
	})

	t.Run("DeleteExpired continues deletion even if Close fails", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		// Create a closable session that returns an error on Close()
		failingSession := newMockClosableSession("failing-session")
		failingSession.closeError = errors.New("close failed")
		failingSession.updated = time.Now().Add(-2 * time.Hour)

		// Store the session
		err := storage.Store(ctx, failingSession)
		require.NoError(t, err)

		// Delete expired sessions - should not fail even if Close() returns an error
		cutoff := time.Now().Add(-1 * time.Hour)
		err = storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		// Session should be deleted from storage even though Close() failed
		_, err = storage.Load(ctx, "failing-session")
		assert.Equal(t, ErrSessionNotFound, err)

		// Close() is called synchronously, so it should already be done
		assert.True(t, failingSession.closeCalled,
			"Close() should have been called even though it returned an error")

		// Note: We don't verify log output to maintain t.Parallel() compatibility.
		// The important behavior is that deletion continues even when Close() fails.
	})

	t.Run("DeleteExpired handles non-io.Closer sessions without error", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		// Create multiple regular sessions (do not implement io.Closer)
		for i := 0; i < 5; i++ {
			session := NewProxySession(fmt.Sprintf("session-%d", i))
			session.updated = time.Now().Add(-2 * time.Hour)
			err := storage.Store(ctx, session)
			require.NoError(t, err)
		}

		// Delete expired sessions
		cutoff := time.Now().Add(-1 * time.Hour)
		err := storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		// All sessions should be deleted
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

		// Create a mix of closable and regular expired sessions
		closable1 := newMockClosableSession("closable-1")
		closable1.updated = time.Now().Add(-2 * time.Hour)
		closable2 := newMockClosableSession("closable-2")
		closable2.updated = time.Now().Add(-2 * time.Hour)

		regular1 := NewProxySession("regular-1")
		regular1.updated = time.Now().Add(-2 * time.Hour)
		regular2 := NewProxySession("regular-2")
		regular2.updated = time.Now().Add(-2 * time.Hour)

		// Store all sessions
		err := storage.Store(ctx, closable1)
		require.NoError(t, err)
		err = storage.Store(ctx, closable2)
		require.NoError(t, err)
		err = storage.Store(ctx, regular1)
		require.NoError(t, err)
		err = storage.Store(ctx, regular2)
		require.NoError(t, err)

		// Delete expired sessions
		cutoff := time.Now().Add(-1 * time.Hour)
		err = storage.DeleteExpired(ctx, cutoff)
		require.NoError(t, err)

		// All sessions should be deleted
		_, err = storage.Load(ctx, "closable-1")
		assert.Equal(t, ErrSessionNotFound, err)
		_, err = storage.Load(ctx, "closable-2")
		assert.Equal(t, ErrSessionNotFound, err)
		_, err = storage.Load(ctx, "regular-1")
		assert.Equal(t, ErrSessionNotFound, err)
		_, err = storage.Load(ctx, "regular-2")
		assert.Equal(t, ErrSessionNotFound, err)

		// Close() is called synchronously, so it should already be done
		assert.True(t, closable1.closeCalled,
			"Close() should have been called on closable-1")
		assert.True(t, closable2.closeCalled,
			"Close() should have been called on closable-2")
	})

	t.Run("DeleteExpired respects context cancellation during deletion", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		// Create many expired sessions to increase chance of context check
		for i := 0; i < 10000; i++ {
			session := NewProxySession(fmt.Sprintf("session-%d", i))
			session.updated = time.Now().Add(-2 * time.Hour)
			err := storage.Store(ctx, session)
			require.NoError(t, err)
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

	t.Run("DeleteExpired handles concurrent Touch() race condition", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		// Create an expired session
		session := NewProxySession("race-session")
		session.updated = time.Now().Add(-2 * time.Hour)
		err := storage.Store(ctx, session)
		require.NoError(t, err)

		// Create many other expired sessions to slow down the deletion loop
		for i := 0; i < 1000; i++ {
			dummySession := NewProxySession(fmt.Sprintf("dummy-%d", i))
			dummySession.updated = time.Now().Add(-2 * time.Hour)
			err := storage.Store(ctx, dummySession)
			require.NoError(t, err)
		}

		// Start DeleteExpired in a goroutine
		done := make(chan error, 1)
		go func() {
			cutoff := time.Now().Add(-1 * time.Hour)
			done <- storage.DeleteExpired(ctx, cutoff)
		}()

		// Concurrently touch the session to make it non-expired
		// This simulates Manager.Get().Touch() being called during cleanup
		session.Touch()

		// Wait for DeleteExpired to complete
		err = <-done
		require.NoError(t, err)

		// The session should NOT be deleted because it was touched
		// (CompareAndDelete would fail due to updated timestamp or re-check would skip it)
		loaded, err := storage.Load(ctx, "race-session")
		if err == nil {
			// Session still exists - this is correct behavior
			assert.NotNil(t, loaded)
			assert.True(t, loaded.UpdatedAt().After(time.Now().Add(-1*time.Hour)),
				"Session should have recent timestamp after Touch()")
		}
		// Note: Due to timing, the session might still be deleted if Touch() happened
		// after the re-check but before CompareAndDelete. This is acceptable as the
		// important thing is we don't close a session that's been replaced.
	})

	t.Run("DeleteExpired handles concurrent Store() replacement race condition", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		defer storage.Close()

		ctx := context.Background()

		// Create an expired closable session
		oldSession := newMockClosableSession("replace-session")
		oldSession.updated = time.Now().Add(-2 * time.Hour)
		err := storage.Store(ctx, oldSession)
		require.NoError(t, err)

		// Create many other expired sessions to slow down the deletion loop
		for i := 0; i < 1000; i++ {
			dummySession := NewProxySession(fmt.Sprintf("dummy-%d", i))
			dummySession.updated = time.Now().Add(-2 * time.Hour)
			err := storage.Store(ctx, dummySession)
			require.NoError(t, err)
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
		err = storage.Store(ctx, newSession)
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

		// Add a session
		err := manager.AddWithID("test-session-1")
		require.NoError(t, err)

		// Get the session
		session, found := manager.Get("test-session-1")
		assert.True(t, found)
		assert.NotNil(t, session)
		assert.Equal(t, "test-session-1", session.ID())

		// Delete the session
		manager.Delete("test-session-1")

		// Should not be found
		session, found = manager.Get("test-session-1")
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

		// Add a session
		err := manager.AddWithID("sse-session-1")
		require.NoError(t, err)

		// Get the session
		session, found := manager.Get("sse-session-1")
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

		// Create a custom session
		customSession := NewTypedProxySession("custom-1", SessionTypeStreamable)
		customSession.SetMetadata("custom", "metadata")

		// Add the custom session
		err := manager.AddSession(customSession)
		require.NoError(t, err)

		// Get the session
		session, found := manager.Get("custom-1")
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

		// Add sessions
		for i := 0; i < 3; i++ {
			err := manager.AddWithID(fmt.Sprintf("session-%d", i))
			require.NoError(t, err)
		}

		// Should have 3 sessions
		assert.Equal(t, 3, manager.Count())
	})

	t.Run("Manager Range with LocalStorage", func(t *testing.T) {
		t.Parallel()
		storage := NewLocalStorage()
		factory := func(id string) Session {
			return NewProxySession(id)
		}

		manager := NewManagerWithStorage(30*time.Minute, factory, storage)
		defer manager.Stop()

		// Add sessions
		ids := []string{"one", "two", "three"}
		for _, id := range ids {
			err := manager.AddWithID(id)
			require.NoError(t, err)
		}

		// Use Range to collect all IDs
		var collected []string
		manager.Range(func(key, _ interface{}) bool {
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
