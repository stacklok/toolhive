// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/session"
)

func TestSessionIDAdapter_Generate(t *testing.T) {
	t.Parallel()

	t.Run("generates valid UUID session ID", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		// Generate session ID
		sessionID := adapter.Generate()

		// Should be non-empty
		assert.NotEmpty(t, sessionID)

		// Should be valid UUID format (contains hyphens)
		assert.Contains(t, sessionID, "-")

		// Session should exist in manager
		sess, exists := mgr.Get(sessionID)
		assert.True(t, exists)
		assert.NotNil(t, sess)
		assert.Equal(t, sessionID, sess.ID())
	})

	t.Run("generates unique session IDs", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		// Generate multiple session IDs
		id1 := adapter.Generate()
		id2 := adapter.Generate()
		id3 := adapter.Generate()

		// All should be unique
		assert.NotEqual(t, id1, id2)
		assert.NotEqual(t, id2, id3)
		assert.NotEqual(t, id1, id3)

		// All should exist in manager
		_, exists1 := mgr.Get(id1)
		_, exists2 := mgr.Get(id2)
		_, exists3 := mgr.Get(id3)
		assert.True(t, exists1)
		assert.True(t, exists2)
		assert.True(t, exists3)
	})
}

func TestSessionIDAdapter_Validate(t *testing.T) {
	t.Parallel()

	t.Run("returns error for empty session ID", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		isTerminated, err := adapter.Validate("")
		assert.Error(t, err)
		assert.False(t, isTerminated)
		assert.Contains(t, err.Error(), "empty session ID")
	})

	t.Run("returns error for non-existent session", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		isTerminated, err := adapter.Validate("non-existent-id")
		assert.Error(t, err)
		assert.False(t, isTerminated)
		assert.Contains(t, err.Error(), "session not found")
	})

	t.Run("validates active session successfully", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		// Generate a session
		sessionID := adapter.Generate()

		// Validate should succeed
		isTerminated, err := adapter.Validate(sessionID)
		assert.NoError(t, err)
		assert.False(t, isTerminated)
	})

	t.Run("detects terminated session", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		// Generate a session
		sessionID := adapter.Generate()

		// Terminate it
		isNotAllowed, err := adapter.Terminate(sessionID)
		require.NoError(t, err)
		require.False(t, isNotAllowed)

		// Validate should detect it's terminated
		isTerminated, err := adapter.Validate(sessionID)
		assert.NoError(t, err)
		assert.True(t, isTerminated, "Session should be marked as terminated")
	})

	t.Run("updates session activity on validation", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		// Generate a session
		sessionID := adapter.Generate()

		// Get initial update time
		sess1, _ := mgr.Get(sessionID)
		time1 := sess1.UpdatedAt()

		// Wait a bit
		time.Sleep(10 * time.Millisecond)

		// Validate (which calls manager.Get, which touches the session)
		_, err := adapter.Validate(sessionID)
		require.NoError(t, err)

		// Get updated time
		sess2, _ := mgr.Get(sessionID)
		time2 := sess2.UpdatedAt()

		// UpdatedAt should have been updated
		assert.True(t, time2.After(time1), "Validation should update session timestamp")
	})
}

func TestSessionIDAdapter_Terminate(t *testing.T) {
	t.Parallel()

	t.Run("returns error for empty session ID", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		isNotAllowed, err := adapter.Terminate("")
		assert.Error(t, err)
		assert.False(t, isNotAllowed)
		assert.Contains(t, err.Error(), "empty session ID")
	})

	t.Run("terminates existing session", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		// Generate a session
		sessionID := adapter.Generate()

		// Terminate it
		isNotAllowed, err := adapter.Terminate(sessionID)
		assert.NoError(t, err)
		assert.False(t, isNotAllowed, "Client termination should be allowed")

		// Session should still exist but be marked as terminated
		sess, exists := mgr.Get(sessionID)
		assert.True(t, exists, "Session should still exist after termination")
		assert.Equal(t, "true", sess.GetMetadata()["terminated"])
	})

	t.Run("terminating non-existent session succeeds", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		// Terminate a non-existent session (client may retry deletion)
		isNotAllowed, err := adapter.Terminate("non-existent-id")
		assert.NoError(t, err)
		assert.False(t, isNotAllowed)
	})

	t.Run("allows client-initiated termination", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		sessionID := adapter.Generate()

		// Per MCP spec, server MAY prevent client termination by returning isNotAllowed=true
		// Our implementation allows it (returns false)
		isNotAllowed, err := adapter.Terminate(sessionID)
		assert.NoError(t, err)
		assert.False(t, isNotAllowed, "Our implementation allows client-initiated termination")
	})
}

func TestSessionIDAdapter_LifecycleIntegration(t *testing.T) {
	t.Parallel()

	t.Run("full session lifecycle", func(t *testing.T) {
		t.Parallel()

		mgr := session.NewTypedManager(30*time.Minute, session.SessionTypeStreamable)
		t.Cleanup(func() { _ = mgr.Stop() })

		adapter := newSessionIDAdapter(mgr)

		// 1. Generate new session (MCP initialize)
		sessionID := adapter.Generate()
		assert.NotEmpty(t, sessionID)

		// 2. Validate session (subsequent requests)
		isTerminated, err := adapter.Validate(sessionID)
		require.NoError(t, err)
		assert.False(t, isTerminated)

		// 3. Multiple validations work
		for i := 0; i < 5; i++ {
			isTerminated, err := adapter.Validate(sessionID)
			require.NoError(t, err)
			assert.False(t, isTerminated)
		}

		// 4. Terminate session (HTTP DELETE)
		isNotAllowed, err := adapter.Terminate(sessionID)
		require.NoError(t, err)
		assert.False(t, isNotAllowed)

		// 5. Subsequent validation detects termination
		isTerminated, err = adapter.Validate(sessionID)
		assert.NoError(t, err)
		assert.True(t, isTerminated)
	})
}
