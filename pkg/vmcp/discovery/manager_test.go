package discovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	aggmocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
)

func TestNewManager(t *testing.T) {
	t.Parallel()

	t.Run("success with valid aggregator", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		mgr, err := NewManager(mockAgg)

		require.NoError(t, err)
		assert.NotNil(t, mgr)
		assert.IsType(t, &DefaultManager{}, mgr)
	})

	t.Run("error with nil aggregator", func(t *testing.T) {
		t.Parallel()

		mgr, err := NewManager(nil)

		require.Error(t, err)
		assert.Nil(t, mgr)
		assert.ErrorIs(t, err, ErrAggregatorNil)
	})
}

func TestDefaultManager_Discover(t *testing.T) {
	t.Parallel()

	t.Run("successful discovery", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil)

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		// Create context with user identity
		identity := &auth.Identity{Subject: "user123", Name: "Test User"}
		ctx := auth.WithIdentity(context.Background(), identity)

		caps, err := mgr.Discover(ctx, backends)

		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps)
	})

	t.Run("error when user identity missing from context", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends := []vmcp.Backend{newTestBackend("backend1")}

		// No expectation on mockAgg - should fail before calling aggregator

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)

		// Use context without user identity
		caps, err := mgr.Discover(context.Background(), backends)

		require.Error(t, err)
		assert.Nil(t, caps)
		assert.ErrorIs(t, err, ErrNoIdentity)
		assert.Contains(t, err.Error(), "ensure auth middleware runs before discovery middleware")
	})

	t.Run("discovery failure from aggregator", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends := []vmcp.Backend{
			newTestBackend("backend1"),
		}

		expectedErr := errors.New("aggregation failed: connection timeout")

		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(nil, expectedErr)

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		// Create context with user identity
		identity := &auth.Identity{Subject: "user456"}
		ctx := auth.WithIdentity(context.Background(), identity)

		caps, err := mgr.Discover(ctx, backends)

		require.Error(t, err)
		assert.Nil(t, caps)
		assert.ErrorIs(t, err, ErrDiscoveryFailed)
	})
}

func TestDefaultManager_Caching(t *testing.T) {
	t.Parallel()

	t.Run("cache hit for same user and backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect only one call to aggregator
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(1)

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		identity := &auth.Identity{Subject: "user123", Name: "Test User"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// First call - should hit aggregator
		caps1, err := mgr.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps1)

		// Second call - should hit cache
		caps2, err := mgr.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps2)
	})

	t.Run("cache miss for different user", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect two calls to aggregator (one per user)
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(2)

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		// User 1
		identity1 := &auth.Identity{Subject: "user123"}
		ctx1 := auth.WithIdentity(context.Background(), identity1)
		caps1, err := mgr.Discover(ctx1, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps1)

		// User 2 - different user, should not hit cache
		identity2 := &auth.Identity{Subject: "user456"}
		ctx2 := auth.WithIdentity(context.Background(), identity2)
		caps2, err := mgr.Discover(ctx2, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps2)
	})

	t.Run("cache miss for different backends", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends1 := []vmcp.Backend{newTestBackend("backend1")}
		backends2 := []vmcp.Backend{newTestBackend("backend2")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect two calls to aggregator (one per backend set)
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends1).
			Return(expectedCaps, nil).
			Times(1)
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends2).
			Return(expectedCaps, nil).
			Times(1)

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		dm := mgr.(*DefaultManager)

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// First backend set
		caps1, err := mgr.Discover(ctx, backends1)
		require.NoError(t, err)
		assert.NotNil(t, caps1)

		// Different backend set - should not hit cache
		caps2, err := mgr.Discover(ctx, backends2)
		require.NoError(t, err)
		assert.NotNil(t, caps2)

		// Verify cache contains 2 entries (one per backend set)
		dm.cacheMu.RLock()
		cacheSize := len(dm.cache)
		dm.cacheMu.RUnlock()
		assert.Equal(t, 2, cacheSize)
	})

	t.Run("cache key stable regardless of backend order", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends1 := []vmcp.Backend{
			newTestBackend("backend1"),
			newTestBackend("backend2"),
		}
		backends2 := []vmcp.Backend{
			newTestBackend("backend2"), // Reversed order
			newTestBackend("backend1"),
		}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect only one call - cache should hit on second call despite order
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), gomock.Any()).
			Return(expectedCaps, nil).
			Times(1)

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// First call
		caps1, err := mgr.Discover(ctx, backends1)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps1)

		// Second call with reversed backend order - should hit cache
		caps2, err := mgr.Discover(ctx, backends2)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps2)
	})

	t.Run("concurrent access is thread-safe", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Should only call aggregator once due to caching
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			MinTimes(1).
			MaxTimes(10) // Allow some race condition calls

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		dm := mgr.(*DefaultManager)

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		var wg sync.WaitGroup
		numGoroutines := 10

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				caps, err := mgr.Discover(ctx, backends)
				assert.NoError(t, err)
				assert.NotNil(t, caps)
			}()
		}

		wg.Wait()

		// Verify cache contains only one entry for this user+backend combination
		dm.cacheMu.RLock()
		cacheSize := len(dm.cache)
		dm.cacheMu.RUnlock()
		assert.Equal(t, 1, cacheSize)
	})
}

func TestDefaultManager_CacheExpiration(t *testing.T) {
	t.Parallel()

	t.Run("expired entries are not returned", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect two calls - once for initial, once after expiration
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(2)

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		// Get the underlying manager to manipulate cache directly
		dm := mgr.(*DefaultManager)

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// First call
		caps1, err := dm.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps1)

		// Manually expire the cache entry
		cacheKey := dm.generateCacheKey(identity.Subject, backends)
		dm.cacheMu.Lock()
		dm.cache[cacheKey].expiresAt = time.Now().Add(-1 * time.Second)
		dm.cacheMu.Unlock()

		// Second call - should not use expired cache
		caps2, err := dm.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps2)
	})

	t.Run("background cleanup removes expired entries", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(1)

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		dm := mgr.(*DefaultManager)

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// Add entry to cache
		_, err = dm.Discover(ctx, backends)
		require.NoError(t, err)

		// Verify entry is in cache
		dm.cacheMu.RLock()
		initialCount := len(dm.cache)
		dm.cacheMu.RUnlock()
		assert.Equal(t, 1, initialCount)

		// Manually expire the entry
		cacheKey := dm.generateCacheKey(identity.Subject, backends)
		dm.cacheMu.Lock()
		dm.cache[cacheKey].expiresAt = time.Now().Add(-1 * time.Second)
		dm.cacheMu.Unlock()

		// Manually trigger cleanup
		dm.removeExpiredEntries()

		// Verify entry was removed
		dm.cacheMu.RLock()
		finalCount := len(dm.cache)
		dm.cacheMu.RUnlock()
		assert.Equal(t, 0, finalCount)
	})
}

func TestDefaultManager_CacheSizeLimit(t *testing.T) {
	t.Parallel()

	t.Run("stops caching at size limit", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect many calls since we'll exceed cache size
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), gomock.Any()).
			Return(expectedCaps, nil).
			AnyTimes()

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		dm := mgr.(*DefaultManager)
		ctx := context.Background()

		// Fill cache to capacity
		for i := 0; i < maxCacheSize; i++ {
			identity := &auth.Identity{Subject: "user" + string(rune(i))}
			ctxWithIdentity := auth.WithIdentity(ctx, identity)
			backends := []vmcp.Backend{newTestBackend("backend1")}
			_, err := dm.Discover(ctxWithIdentity, backends)
			require.NoError(t, err)
		}

		// Verify cache is at capacity
		dm.cacheMu.RLock()
		cacheSize := len(dm.cache)
		dm.cacheMu.RUnlock()
		assert.Equal(t, maxCacheSize, cacheSize)

		// Try to add one more - should not be cached
		newIdentity := &auth.Identity{Subject: "user-overflow"}
		ctxWithNewIdentity := auth.WithIdentity(ctx, newIdentity)
		backends := []vmcp.Backend{newTestBackend("backend2")}
		_, err = dm.Discover(ctxWithNewIdentity, backends)
		require.NoError(t, err)

		// Verify cache size didn't increase
		dm.cacheMu.RLock()
		finalSize := len(dm.cache)
		dm.cacheMu.RUnlock()
		assert.Equal(t, maxCacheSize, finalSize)

		// Verify new entry is not in cache
		cacheKey := dm.generateCacheKey(newIdentity.Subject, backends)
		dm.cacheMu.RLock()
		_, exists := dm.cache[cacheKey]
		dm.cacheMu.RUnlock()
		assert.False(t, exists)
	})
}

func TestDefaultManager_Stop(t *testing.T) {
	t.Parallel()

	t.Run("stop terminates cleanup goroutine", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)

		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)

		dm := mgr.(*DefaultManager)

		// Verify cleanup goroutine is running
		select {
		case <-dm.stopCh:
			t.Fatal("stopCh should not be closed yet")
		default:
			// Expected - stopCh is still open
		}

		// Stop should complete without hanging
		done := make(chan struct{})
		go func() {
			dm.Stop()
			close(done)
		}()

		select {
		case <-done:
			// Success - Stop() completed
		case <-time.After(2 * time.Second):
			t.Fatal("Stop() did not complete within timeout")
		}

		// Verify stopCh is closed (which signals cleanup goroutine to exit)
		select {
		case <-dm.stopCh:
			// Expected - stopCh is now closed
		default:
			t.Fatal("stopCh should be closed after Stop()")
		}
	})
}

// Test helpers

func newTestBackend(id string) vmcp.Backend {
	return vmcp.Backend{
		ID:            id,
		Name:          id,
		BaseURL:       "http://localhost:8080",
		TransportType: "streamable-http",
		HealthStatus:  vmcp.BackendHealthy,
	}
}

//nolint:unparam // name parameter kept for flexibility in future tests
func newTestTool(name, backendID string) vmcp.Tool {
	return vmcp.Tool{
		Name:        name,
		Description: name + " description",
		InputSchema: map[string]any{"type": "object"},
		BackendID:   backendID,
	}
}

// Version-based cache invalidation tests

func TestNewManagerWithRegistry(t *testing.T) {
	t.Parallel()

	t.Run("success with valid aggregator and registry", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		registry := vmcp.NewDynamicRegistry(nil)

		mgr, err := NewManagerWithRegistry(mockAgg, registry)

		require.NoError(t, err)
		assert.NotNil(t, mgr)
		assert.IsType(t, &DefaultManager{}, mgr)

		dm := mgr.(*DefaultManager)
		assert.NotNil(t, dm.registry)
	})

	t.Run("success with nil registry", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)

		mgr, err := NewManagerWithRegistry(mockAgg, nil)

		require.NoError(t, err)
		assert.NotNil(t, mgr)

		dm := mgr.(*DefaultManager)
		assert.Nil(t, dm.registry)
	})

	t.Run("error with nil aggregator", func(t *testing.T) {
		t.Parallel()

		registry := vmcp.NewDynamicRegistry(nil)

		mgr, err := NewManagerWithRegistry(nil, registry)

		require.Error(t, err)
		assert.Nil(t, mgr)
		assert.ErrorIs(t, err, ErrAggregatorNil)
	})
}

func TestDefaultManager_VersionBasedCacheInvalidation(t *testing.T) {
	t.Parallel()

	t.Run("cache invalidated when registry version changes", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		registry := vmcp.NewDynamicRegistry(nil)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect two calls to aggregator - once for initial, once after version change
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(2)

		mgr, err := NewManagerWithRegistry(mockAgg, registry)
		require.NoError(t, err)
		defer mgr.Stop()

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// First call - populates cache
		caps1, err := mgr.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps1)
		assert.Equal(t, uint64(0), registry.Version())

		// Mutate registry - increments version
		err = registry.Upsert(&vmcp.Backend{ID: "backend2", Name: "Backend 2"})
		require.NoError(t, err)
		assert.Equal(t, uint64(1), registry.Version())

		// Second call - cache should be invalidated due to version mismatch
		caps2, err := mgr.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps2)
	})

	t.Run("cache hit when registry version unchanged", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		registry := vmcp.NewDynamicRegistry(nil)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect only one call to aggregator
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(1)

		mgr, err := NewManagerWithRegistry(mockAgg, registry)
		require.NoError(t, err)
		defer mgr.Stop()

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// First call
		caps1, err := mgr.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps1)

		// Second call - registry version unchanged, should hit cache
		caps2, err := mgr.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps2)
	})

	t.Run("multiple registry mutations invalidate cache multiple times", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		registry := vmcp.NewDynamicRegistry(nil)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect three calls to aggregator
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(3)

		mgr, err := NewManagerWithRegistry(mockAgg, registry)
		require.NoError(t, err)
		defer mgr.Stop()

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// Call 1 - initial discovery
		_, err = mgr.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, uint64(0), registry.Version())

		// Mutation 1
		_ = registry.Upsert(&vmcp.Backend{ID: "backend2", Name: "Backend 2"})
		assert.Equal(t, uint64(1), registry.Version())

		// Call 2 - cache invalidated
		_, err = mgr.Discover(ctx, backends)
		require.NoError(t, err)

		// Mutation 2
		_ = registry.Remove("backend2")
		assert.Equal(t, uint64(2), registry.Version())

		// Call 3 - cache invalidated again
		_, err = mgr.Discover(ctx, backends)
		require.NoError(t, err)
	})

	t.Run("version check only applies when registry is set", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		// Expect only one call - no version checking without registry
		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(1)

		// Create manager without registry
		mgr, err := NewManager(mockAgg)
		require.NoError(t, err)
		defer mgr.Stop()

		dm := mgr.(*DefaultManager)
		assert.Nil(t, dm.registry)

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// First call
		caps1, err := mgr.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps1)

		// Second call - should hit cache (no version checking)
		caps2, err := mgr.Discover(ctx, backends)
		require.NoError(t, err)
		assert.Equal(t, expectedCaps, caps2)
	})

	t.Run("cache tags entries with registry version", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		registry := vmcp.NewDynamicRegistry(nil)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(1)

		mgr, err := NewManagerWithRegistry(mockAgg, registry)
		require.NoError(t, err)
		defer mgr.Stop()

		dm := mgr.(*DefaultManager)

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// Initial discovery
		_, err = mgr.Discover(ctx, backends)
		require.NoError(t, err)

		// Check cache entry has correct version
		cacheKey := dm.generateCacheKey(identity.Subject, backends)
		dm.cacheMu.RLock()
		entry, exists := dm.cache[cacheKey]
		dm.cacheMu.RUnlock()

		require.True(t, exists)
		assert.Equal(t, uint64(0), entry.registryVersion)
	})

	t.Run("lazy invalidation - stale entries remain until accessed", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockAgg := aggmocks.NewMockAggregator(ctrl)
		registry := vmcp.NewDynamicRegistry(nil)
		backends := []vmcp.Backend{newTestBackend("backend1")}
		expectedCaps := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{newTestTool("tool1", "backend1")},
		}

		mockAgg.EXPECT().
			AggregateCapabilities(gomock.Any(), backends).
			Return(expectedCaps, nil).
			Times(1)

		mgr, err := NewManagerWithRegistry(mockAgg, registry)
		require.NoError(t, err)
		defer mgr.Stop()

		dm := mgr.(*DefaultManager)

		identity := &auth.Identity{Subject: "user123"}
		ctx := auth.WithIdentity(context.Background(), identity)

		// Populate cache
		_, err = mgr.Discover(ctx, backends)
		require.NoError(t, err)

		// Verify cache entry exists
		cacheKey := dm.generateCacheKey(identity.Subject, backends)
		dm.cacheMu.RLock()
		_, exists := dm.cache[cacheKey]
		dm.cacheMu.RUnlock()
		assert.True(t, exists)

		// Mutate registry multiple times - stale entry should remain in cache
		_ = registry.Upsert(&vmcp.Backend{ID: "backend2", Name: "Backend 2"})
		_ = registry.Upsert(&vmcp.Backend{ID: "backend3", Name: "Backend 3"})
		_ = registry.Remove("backend2")

		// Stale entry still exists (lazy invalidation)
		dm.cacheMu.RLock()
		_, stillExists := dm.cache[cacheKey]
		dm.cacheMu.RUnlock()
		assert.True(t, stillExists, "stale entry should remain until accessed")
	})
}
