package discovery

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	aggmocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	vmcpmocks "github.com/stacklok/toolhive/pkg/vmcp/mocks"
)

// TestDefaultManager_CacheVersionMismatch tests cache invalidation on version mismatch
func TestDefaultManager_CacheVersionMismatch(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockAggregator := aggmocks.NewMockAggregator(ctrl)
	mockRegistry := vmcpmocks.NewMockDynamicRegistry(ctrl)

	// First call - version 1
	mockRegistry.EXPECT().Version().Return(uint64(1)).Times(2)
	mockAggregator.EXPECT().
		AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).
		Times(1)

	manager, err := NewManagerWithRegistry(mockAggregator, mockRegistry)
	require.NoError(t, err)
	defer manager.Stop()

	ctx := context.WithValue(context.Background(), auth.IdentityContextKey{}, &auth.Identity{
		Subject: "user-1",
	})

	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1"},
	}

	// First discovery - should cache
	caps1, err := manager.Discover(ctx, backends)
	require.NoError(t, err)
	require.NotNil(t, caps1)

	// Second discovery with same version - should use cache
	mockRegistry.EXPECT().Version().Return(uint64(1)).Times(1)
	caps2, err := manager.Discover(ctx, backends)
	require.NoError(t, err)
	require.NotNil(t, caps2)

	// Third discovery with different version - should invalidate cache
	mockRegistry.EXPECT().Version().Return(uint64(2)).Times(2)
	mockAggregator.EXPECT().
		AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).
		Times(1)

	caps3, err := manager.Discover(ctx, backends)
	require.NoError(t, err)
	require.NotNil(t, caps3)
}

// TestDefaultManager_CacheAtCapacity tests cache eviction when at capacity
func TestDefaultManager_CacheAtCapacity(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockAggregator := aggmocks.NewMockAggregator(ctrl)

	// Create many different cache keys to fill cache
	mockAggregator.EXPECT().
		AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).
		Times(maxCacheSize + 1) // One more than capacity

	manager, err := NewManager(mockAggregator)
	require.NoError(t, err)
	defer manager.Stop()

	// Fill cache to capacity
	for i := 0; i < maxCacheSize; i++ {
		ctx := context.WithValue(context.Background(), auth.IdentityContextKey{}, &auth.Identity{
			Subject: "user-" + string(rune(i)),
		})

		backends := []vmcp.Backend{
			{ID: "backend-" + string(rune(i)), Name: "Backend"},
		}

		_, err := manager.Discover(ctx, backends)
		require.NoError(t, err)
	}

	// Next discovery should not cache (at capacity)
	ctx := context.WithValue(context.Background(), auth.IdentityContextKey{}, &auth.Identity{
		Subject: "user-new",
	})

	backends := []vmcp.Backend{
		{ID: "backend-new", Name: "Backend"},
	}

	_, err = manager.Discover(ctx, backends)
	require.NoError(t, err)
}

// TestDefaultManager_CacheAtCapacity_ExistingKey tests cache update when at capacity but key exists
func TestDefaultManager_CacheAtCapacity_ExistingKey(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockAggregator := aggmocks.NewMockAggregator(ctrl)

	// First call
	mockAggregator.EXPECT().
		AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).
		Times(1)

	manager, err := NewManager(mockAggregator)
	require.NoError(t, err)
	defer manager.Stop()

	ctx := context.WithValue(context.Background(), auth.IdentityContextKey{}, &auth.Identity{
		Subject: "user-1",
	})

	backends := []vmcp.Backend{
		{ID: "backend-1", Name: "Backend 1"},
	}

	// First discovery
	_, err = manager.Discover(ctx, backends)
	require.NoError(t, err)

	// Fill cache to capacity with other keys
	for i := 0; i < maxCacheSize-1; i++ {
		ctxOther := context.WithValue(context.Background(), auth.IdentityContextKey{}, &auth.Identity{
			Subject: "user-" + string(rune(i+2)),
		})

		backendsOther := []vmcp.Backend{
			{ID: "backend-" + string(rune(i+2)), Name: "Backend"},
		}

		mockAggregator.EXPECT().
			AggregateCapabilities(gomock.Any(), gomock.Any()).
			Return(&aggregator.AggregatedCapabilities{}, nil).
			Times(1)

		_, err := manager.Discover(ctxOther, backendsOther)
		require.NoError(t, err)
	}

	// Update existing key should work even at capacity
	mockAggregator.EXPECT().
		AggregateCapabilities(gomock.Any(), gomock.Any()).
		Return(&aggregator.AggregatedCapabilities{}, nil).
		Times(1)

	_, err = manager.Discover(ctx, backends)
	require.NoError(t, err)
}
