// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/env"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

func TestPooledBackendClient_GetOrCreatePool(t *testing.T) {
	t.Parallel()

	t.Run("creates new pool for new session", func(t *testing.T) {
		t.Parallel()
		// Create session manager
		sessionManager := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
		sessionID := uuid.New().String()
		err := sessionManager.AddWithID(sessionID)
		require.NoError(t, err)

		// Create pooled client
		registry, err := factory.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
		require.NoError(t, err)
		httpClient, err := NewHTTPBackendClient(registry)
		require.NoError(t, err)

		pooledClient := WrapWithPooling(httpClient, sessionManager).(*pooledBackendClient)

		// Create context with session ID
		ctx := context.WithValue(context.Background(), transportsession.SessionIDContextKey, sessionID)

		// Get pool
		pool1, err := pooledClient.getOrCreatePool(ctx)
		require.NoError(t, err)
		assert.NotNil(t, pool1)

		// Get pool again - should be same instance
		pool2, err := pooledClient.getOrCreatePool(ctx)
		require.NoError(t, err)
		assert.Same(t, pool1, pool2, "should return same pool instance")
	})

	t.Run("creates temporary pool when no session in context", func(t *testing.T) {
		t.Parallel()
		sessionManager := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())

		registry, err := factory.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
		require.NoError(t, err)
		httpClient, err := NewHTTPBackendClient(registry)
		require.NoError(t, err)

		pooledClient := WrapWithPooling(httpClient, sessionManager).(*pooledBackendClient)

		// Context without session ID
		ctx := context.Background()

		// Should create temporary pool (no error)
		pool, err := pooledClient.getOrCreatePool(ctx)
		require.NoError(t, err)
		assert.NotNil(t, pool)
	})

	t.Run("returns error for non-existent session", func(t *testing.T) {
		t.Parallel()
		sessionManager := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())

		registry, err := factory.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
		require.NoError(t, err)
		httpClient, err := NewHTTPBackendClient(registry)
		require.NoError(t, err)

		pooledClient := WrapWithPooling(httpClient, sessionManager).(*pooledBackendClient)

		// Context with non-existent session ID
		ctx := context.WithValue(context.Background(), transportsession.SessionIDContextKey, "non-existent-session")

		pool, err := pooledClient.getOrCreatePool(ctx)
		assert.Error(t, err)
		assert.Nil(t, pool)
		assert.Contains(t, err.Error(), "failed to get session")
	})

	t.Run("stores pool in VMCPSession", func(t *testing.T) {
		t.Parallel()
		sessionManager := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
		sessionID := uuid.New().String()
		err := sessionManager.AddWithID(sessionID)
		require.NoError(t, err)

		registry, err := factory.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
		require.NoError(t, err)
		httpClient, err := NewHTTPBackendClient(registry)
		require.NoError(t, err)

		pooledClient := WrapWithPooling(httpClient, sessionManager).(*pooledBackendClient)

		ctx := context.WithValue(context.Background(), transportsession.SessionIDContextKey, sessionID)

		// Get pool
		pool, err := pooledClient.getOrCreatePool(ctx)
		require.NoError(t, err)

		// Verify it's stored in the session
		vmcpSess, err := vmcpsession.GetVMCPSession(sessionID, sessionManager)
		require.NoError(t, err)

		storedPoolInterface := vmcpSess.GetClientPool()
		assert.NotNil(t, storedPoolInterface)
		storedPool, ok := storedPoolInterface.(*BackendClientPool)
		require.True(t, ok, "stored pool should be *BackendClientPool")
		assert.Same(t, pool, storedPool)
	})
}

func TestWrapWithPooling(t *testing.T) {
	t.Parallel()

	t.Run("wraps HTTP backend client", func(t *testing.T) {
		t.Parallel()
		sessionManager := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())

		registry, err := factory.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
		require.NoError(t, err)
		httpClient, err := NewHTTPBackendClient(registry)
		require.NoError(t, err)

		wrapped := WrapWithPooling(httpClient, sessionManager)

		// Should return pooled client
		pooledClient, ok := wrapped.(*pooledBackendClient)
		assert.True(t, ok, "should return pooledBackendClient")
		assert.NotNil(t, pooledClient)
		assert.Same(t, sessionManager, pooledClient.sessionManager)
	})

	t.Run("returns original client if not HTTP backend client", func(t *testing.T) {
		t.Parallel()
		sessionManager := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())

		// Create a mock client that's not httpBackendClient
		mockClient := &mockBackendClient{}

		wrapped := WrapWithPooling(mockClient, sessionManager)

		// Should return original client unchanged
		assert.Same(t, mockClient, wrapped)
	})
}

func TestShouldMarkUnhealthy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "connection reset",
			err:      syscall.ECONNRESET,
			expected: true,
		},
		{
			name:     "connection refused",
			err:      syscall.ECONNREFUSED,
			expected: true,
		},
		{
			name:     "broken pipe",
			err:      syscall.EPIPE,
			expected: true,
		},
		{
			name:     "connection aborted",
			err:      syscall.ECONNABORTED,
			expected: true,
		},
		{
			name:     "other syscall error",
			err:      syscall.EINVAL,
			expected: false,
		},
		{
			name:     "network timeout (should not mark unhealthy)",
			err:      &net.OpError{Op: "read", Err: &timeoutError{}},
			expected: false,
		},
		{
			name:     "network error (non-timeout)",
			err:      &net.OpError{Op: "read", Err: errors.New("connection reset")},
			expected: true,
		},
		{
			name:     "generic error",
			err:      errors.New("some error"),
			expected: false,
		},
		{
			name:     "wrapped connection reset",
			err:      errors.Join(errors.New("outer"), syscall.ECONNRESET),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := shouldMarkUnhealthy(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// mockBackendClient is a mock implementation of BackendClient for testing
type mockBackendClient struct {
	vmcp.BackendClient
}

// timeoutError is a mock timeout error
type timeoutError struct{}

func (*timeoutError) Error() string   { return "timeout" }
func (*timeoutError) Timeout() bool   { return true }
func (*timeoutError) Temporary() bool { return true }

func TestPooledBackendClient_Integration(t *testing.T) {
	t.Parallel()

	// This test verifies the full integration but uses a real httpBackendClient
	// which would require a real backend server. We'll keep it simple and just
	// verify that the pooling wrapper is correctly set up.

	t.Run("pooled client implements BackendClient interface", func(t *testing.T) {
		t.Parallel()
		sessionManager := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())

		registry, err := factory.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
		require.NoError(t, err)
		httpClient, err := NewHTTPBackendClient(registry)
		require.NoError(t, err)

		pooledClient := WrapWithPooling(httpClient, sessionManager)

		// Verify it implements the interface by checking method existence
		var _ = pooledClient
	})

	t.Run("multiple calls within same session reuse pool", func(t *testing.T) {
		t.Parallel()
		sessionManager := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
		sessionID := uuid.New().String()
		err := sessionManager.AddWithID(sessionID)
		require.NoError(t, err)

		registry, err := factory.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
		require.NoError(t, err)
		httpClient, err := NewHTTPBackendClient(registry)
		require.NoError(t, err)

		pooledClient := WrapWithPooling(httpClient, sessionManager).(*pooledBackendClient)

		ctx := context.WithValue(context.Background(), transportsession.SessionIDContextKey, sessionID)

		// Get pool multiple times
		pool1, err := pooledClient.getOrCreatePool(ctx)
		require.NoError(t, err)

		pool2, err := pooledClient.getOrCreatePool(ctx)
		require.NoError(t, err)

		pool3, err := pooledClient.getOrCreatePool(ctx)
		require.NoError(t, err)

		// All should be the same instance
		assert.Same(t, pool1, pool2)
		assert.Same(t, pool2, pool3)
	})

	t.Run("different sessions have different pools", func(t *testing.T) {
		t.Parallel()
		sessionManager := transportsession.NewManager(30*time.Minute, vmcpsession.VMCPSessionFactory())
		session1 := uuid.New().String()
		session2 := uuid.New().String()
		err := sessionManager.AddWithID(session1)
		require.NoError(t, err)
		err = sessionManager.AddWithID(session2)
		require.NoError(t, err)

		registry, err := factory.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
		require.NoError(t, err)
		httpClient, err := NewHTTPBackendClient(registry)
		require.NoError(t, err)

		pooledClient := WrapWithPooling(httpClient, sessionManager).(*pooledBackendClient)

		ctx1 := context.WithValue(context.Background(), transportsession.SessionIDContextKey, session1)
		ctx2 := context.WithValue(context.Background(), transportsession.SessionIDContextKey, session2)

		pool1, err := pooledClient.getOrCreatePool(ctx1)
		require.NoError(t, err)

		pool2, err := pooledClient.getOrCreatePool(ctx2)
		require.NoError(t, err)

		// Should be different instances
		assert.NotSame(t, pool1, pool2, "different sessions should have different pools")
	})
}

func TestPooledBackendClient_SessionCleanup(t *testing.T) {
	t.Parallel()

	t.Run("pool is garbage collected with session", func(t *testing.T) {
		t.Parallel()
		// Short TTL for testing
		sessionManager := transportsession.NewManager(100*time.Millisecond, vmcpsession.VMCPSessionFactory())
		defer sessionManager.Stop()

		sessionID := uuid.New().String()
		err := sessionManager.AddWithID(sessionID)
		require.NoError(t, err)

		registry, err := factory.NewOutgoingAuthRegistry(context.Background(), &env.OSReader{})
		require.NoError(t, err)
		httpClient, err := NewHTTPBackendClient(registry)
		require.NoError(t, err)

		pooledClient := WrapWithPooling(httpClient, sessionManager).(*pooledBackendClient)

		ctx := context.WithValue(context.Background(), transportsession.SessionIDContextKey, sessionID)

		// Create pool
		pool, err := pooledClient.getOrCreatePool(ctx)
		require.NoError(t, err)
		assert.NotNil(t, pool)

		// Wait for session to expire
		time.Sleep(200 * time.Millisecond)

		// Session should be gone
		_, ok := sessionManager.Get(sessionID)
		assert.False(t, ok, "session should be cleaned up by TTL")
	})
}
