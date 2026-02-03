// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testBackendID = "backend-1"

func TestBackendClientPool_GetOrCreate(t *testing.T) {
	t.Parallel()

	t.Run("creates new client on first access", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()
		backendID := testBackendID

		var callCount int
		factory := func(_ context.Context) (*client.Client, error) {
			callCount++
			return nil, nil
		}

		_, err := pool.GetOrCreate(ctx, backendID, factory)
		require.NoError(t, err)
		assert.Equal(t, 1, callCount, "factory should be called once")
	})

	t.Run("returns same client for same backend", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()
		backendID := testBackendID

		var callCount int
		factory := func(_ context.Context) (*client.Client, error) {
			callCount++
			return nil, nil
		}

		c1, err := pool.GetOrCreate(ctx, backendID, factory)
		require.NoError(t, err)

		c2, err := pool.GetOrCreate(ctx, backendID, factory)
		require.NoError(t, err)

		assert.Same(t, c1, c2, "should return same client instance")
		assert.Equal(t, 1, callCount, "factory should be called only once")
	})

	t.Run("creates different clients for different backends", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()

		var callCount int
		factory := func(_ context.Context) (*client.Client, error) {
			callCount++
			return nil, nil
		}

		_, err := pool.GetOrCreate(ctx, "backend-1", factory)
		require.NoError(t, err)

		_, err = pool.GetOrCreate(ctx, "backend-2", factory)
		require.NoError(t, err)

		// Verify factory was called twice (once per backend)
		assert.Equal(t, 2, callCount, "factory should be called twice")

		// Verify pool has 2 entries
		pool.mu.RLock()
		assert.Len(t, pool.clients, 2, "pool should have 2 clients")
		pool.mu.RUnlock()
	})

	t.Run("returns error when factory fails", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()
		backendID := testBackendID
		expectedErr := errors.New("factory failed")

		factory := func(_ context.Context) (*client.Client, error) {
			return nil, expectedErr
		}

		c, err := pool.GetOrCreate(ctx, backendID, factory)
		assert.Error(t, err)
		assert.Nil(t, c)
		assert.Contains(t, err.Error(), "factory failed")
	})

	t.Run("concurrent access creates only one client", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()
		backendID := testBackendID

		var callCount int
		var mu sync.Mutex
		//nolint:unparam // Test helper that intentionally returns nil
		factory := func(_ context.Context) (*client.Client, error) {
			mu.Lock()
			callCount++
			mu.Unlock()
			// Sleep a bit to increase contention
			// time.Sleep(10 * time.Millisecond)
			return nil, nil
		}

		// Launch multiple goroutines trying to get the same client
		var wg sync.WaitGroup
		goroutines := 10
		clients := make([]*client.Client, goroutines)

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				c, err := pool.GetOrCreate(ctx, backendID, factory)
				require.NoError(t, err)
				clients[idx] = c
			}(i)
		}

		wg.Wait()

		// All clients should be the same instance
		for i := 1; i < goroutines; i++ {
			assert.Same(t, clients[0], clients[i], "all goroutines should get same client")
		}

		// Factory should be called only once (double-checked locking works)
		assert.Equal(t, 1, callCount, "factory should be called only once despite concurrent access")
	})
}

func TestBackendClientPool_MarkUnhealthy(t *testing.T) {
	t.Parallel()

	t.Run("removes client from pool", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()
		backendID := testBackendID

		var callCount int
		factory := func(_ context.Context) (*client.Client, error) {
			callCount++
			return nil, nil
		}

		// Create initial client
		_, err := pool.GetOrCreate(ctx, backendID, factory)
		require.NoError(t, err)
		assert.Equal(t, 1, callCount)

		// Mark as unhealthy
		pool.MarkUnhealthy(backendID)

		// Verify client was removed from pool
		pool.mu.RLock()
		_, exists := pool.clients[backendID]
		pool.mu.RUnlock()
		assert.False(t, exists, "client should be removed from pool")

		// Next GetOrCreate should create new client
		_, err = pool.GetOrCreate(ctx, backendID, factory)
		require.NoError(t, err)
		assert.Equal(t, 2, callCount, "factory should be called again")
	})

	t.Run("marking non-existent backend is safe", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()

		// Should not panic
		assert.NotPanics(t, func() {
			pool.MarkUnhealthy("non-existent-backend")
		})
	})

	t.Run("closes client when marked unhealthy", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		backendID := testBackendID

		// Create a client through the factory
		factory := func(_ context.Context) (*client.Client, error) {
			return nil, nil
		}

		_, err := pool.GetOrCreate(context.Background(), backendID, factory)
		require.NoError(t, err)

		// Mark as unhealthy - this should call Close() on the client
		// We can't easily test that Close() was called without a mock,
		// but we can verify the client is removed from the pool
		pool.MarkUnhealthy(backendID)

		pool.mu.RLock()
		_, exists := pool.clients[backendID]
		pool.mu.RUnlock()

		assert.False(t, exists, "client should be removed from pool when marked unhealthy")
	})
}

func TestBackendClientPool_Close(t *testing.T) {
	t.Parallel()

	t.Run("closes all clients", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()

		// Create multiple clients through factory
		factory := func(_ context.Context) (*client.Client, error) {
			return nil, nil
		}

		_, err := pool.GetOrCreate(ctx, "backend-1", factory)
		require.NoError(t, err)

		_, err = pool.GetOrCreate(ctx, "backend-2", factory)
		require.NoError(t, err)

		_, err = pool.GetOrCreate(ctx, "backend-3", factory)
		require.NoError(t, err)

		// Pool should have 3 clients
		pool.mu.RLock()
		initialCount := len(pool.clients)
		pool.mu.RUnlock()
		assert.Equal(t, 3, initialCount)

		err = pool.Close()
		require.NoError(t, err)

		// Pool should be empty
		pool.mu.RLock()
		finalCount := len(pool.clients)
		pool.mu.RUnlock()
		assert.Equal(t, 0, finalCount, "all clients should be removed after close")
	})

	t.Run("clears the pool after close", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()

		// Create client
		factory := func(_ context.Context) (*client.Client, error) {
			return nil, nil
		}

		_, err := pool.GetOrCreate(ctx, "backend-1", factory)
		require.NoError(t, err)

		// Close pool
		err = pool.Close()
		require.NoError(t, err)

		// Pool should be empty
		pool.mu.RLock()
		assert.Empty(t, pool.clients, "pool should be empty after close")
		pool.mu.RUnlock()
	})

	t.Run("close on empty pool is safe", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()

		// Close should not panic or error on empty pool
		err := pool.Close()
		assert.NoError(t, err)
	})
}

func TestBackendClientPool_ConcurrentOperations(t *testing.T) {
	t.Parallel()

	t.Run("concurrent GetOrCreate and MarkUnhealthy", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()
		backendID := testBackendID

		var callCount int
		var mu sync.Mutex
		//nolint:unparam // Test helper that intentionally returns nil
		factory := func(_ context.Context) (*client.Client, error) {
			mu.Lock()
			callCount++
			mu.Unlock()
			return nil, nil
		}

		var wg sync.WaitGroup
		goroutines := 20

		// Half will create, half will mark unhealthy
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			if i%2 == 0 {
				go func() {
					defer wg.Done()
					_, _ = pool.GetOrCreate(ctx, backendID, factory)
				}()
			} else {
				go func() {
					defer wg.Done()
					pool.MarkUnhealthy(backendID)
				}()
			}
		}

		wg.Wait()

		// Should not panic and should have created at least one client
		assert.True(t, callCount > 0, "should have created at least one client")
	})

	t.Run("concurrent operations on different backends", func(t *testing.T) {
		t.Parallel()
		pool := NewBackendClientPool()
		ctx := context.Background()

		var wg sync.WaitGroup
		backends := []string{"backend-1", "backend-2", "backend-3", "backend-4"}

		for _, backendID := range backends {
			for i := 0; i < 5; i++ {
				wg.Add(1)
				go func(bid string) {
					defer wg.Done()
					//nolint:unparam // Test helper that intentionally returns nil
					factory := func(_ context.Context) (*client.Client, error) {
						return nil, nil
					}
					_, _ = pool.GetOrCreate(ctx, bid, factory)
				}(backendID)
			}
		}

		wg.Wait()

		// Should have created clients for all backends
		pool.mu.RLock()
		assert.Len(t, pool.clients, len(backends))
		pool.mu.RUnlock()
	})
}
