// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
)

// mockClientPool is a test implementation of ClientPool that tracks Close() calls
type mockClientPool struct {
	closed bool
}

func (m *mockClientPool) Close() error {
	m.closed = true
	return nil
}

func TestVMCPSession_Close(t *testing.T) {
	t.Parallel()

	t.Run("Close calls Close() on client pool", func(t *testing.T) {
		t.Parallel()

		sess := NewVMCPSession("test-session")
		pool := &mockClientPool{}
		sess.SetClientPool(pool)

		// Verify pool is not closed initially
		assert.False(t, pool.closed)

		// Close the session
		err := sess.Close()
		require.NoError(t, err)

		// Verify pool was closed
		assert.True(t, pool.closed)
	})

	t.Run("Close with nil pool does not panic", func(t *testing.T) {
		t.Parallel()

		sess := NewVMCPSession("test-session")
		// Don't set a client pool

		// Should not panic
		err := sess.Close()
		require.NoError(t, err)
	})

	t.Run("Close multiple times is safe", func(t *testing.T) {
		t.Parallel()

		sess := NewVMCPSession("test-session")
		pool := &mockClientPool{}
		sess.SetClientPool(pool)

		// Close twice
		err := sess.Close()
		require.NoError(t, err)
		err = sess.Close()
		require.NoError(t, err)

		// Should be closed once
		assert.True(t, pool.closed)
	})
}

func TestSessionManager_CallsCloseOnExpiration(t *testing.T) {
	t.Parallel()

	// Create a session manager with very short TTL
	shortTTL := 100 * time.Millisecond
	mgr := transportsession.NewManager(shortTTL, VMCPSessionFactory())

	// Create a session with a mock client pool
	sessionID := "test-session-expire"
	err := mgr.AddWithID(sessionID)
	require.NoError(t, err)

	// Get the session and add a mock pool
	sess, ok := mgr.Get(sessionID)
	require.True(t, ok)
	vmcpSess, ok := sess.(*VMCPSession)
	require.True(t, ok)

	pool := &mockClientPool{}
	vmcpSess.SetClientPool(pool)

	// Verify pool is not closed initially
	assert.False(t, pool.closed)

	// Wait for session to expire (TTL + cleanup interval)
	// Cleanup runs every TTL/2, so wait for 2*TTL to be safe
	time.Sleep(shortTTL * 3)

	// Session should be expired and removed
	_, ok = mgr.Get(sessionID)
	assert.False(t, ok, "session should be expired and removed")

	// Pool should have been closed
	assert.True(t, pool.closed, "client pool should be closed on session expiration")

	// Cleanup
	err = mgr.Stop()
	require.NoError(t, err)
}

func TestSessionManager_CallsCloseOnDelete(t *testing.T) {
	t.Parallel()

	mgr := transportsession.NewManager(30*time.Minute, VMCPSessionFactory())

	// Create a session with a mock client pool
	sessionID := "test-session-delete"
	err := mgr.AddWithID(sessionID)
	require.NoError(t, err)

	// Get the session and add a mock pool
	sess, ok := mgr.Get(sessionID)
	require.True(t, ok)
	vmcpSess, ok := sess.(*VMCPSession)
	require.True(t, ok)

	pool := &mockClientPool{}
	vmcpSess.SetClientPool(pool)

	// Verify pool is not closed initially
	assert.False(t, pool.closed)

	// Explicitly delete the session
	err = mgr.Delete(sessionID)
	require.NoError(t, err)

	// Pool should have been closed
	assert.True(t, pool.closed, "client pool should be closed on session deletion")

	// Cleanup
	err = mgr.Stop()
	require.NoError(t, err)
}

func TestSessionManager_CallsCloseOnStop(t *testing.T) {
	t.Parallel()

	mgr := transportsession.NewManager(30*time.Minute, VMCPSessionFactory())

	// Create a session with a mock client pool
	sessionID := "test-session-stop"
	err := mgr.AddWithID(sessionID)
	require.NoError(t, err)

	// Get the session and add a mock pool
	sess, ok := mgr.Get(sessionID)
	require.True(t, ok)
	vmcpSess, ok := sess.(*VMCPSession)
	require.True(t, ok)

	pool := &mockClientPool{}
	vmcpSess.SetClientPool(pool)

	// Verify pool is not closed initially
	assert.False(t, pool.closed)

	// Stop the manager (should close all sessions)
	err = mgr.Stop()
	require.NoError(t, err)

	// Pool should have been closed
	assert.True(t, pool.closed, "client pool should be closed when manager stops")
}
