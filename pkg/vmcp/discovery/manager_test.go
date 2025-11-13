package discovery

import (
	"context"
	"errors"
	"testing"

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

		// Create context with user identity
		identity := &auth.Identity{Subject: "user456"}
		ctx := auth.WithIdentity(context.Background(), identity)

		caps, err := mgr.Discover(ctx, backends)

		require.Error(t, err)
		assert.Nil(t, caps)
		assert.ErrorIs(t, err, ErrDiscoveryFailed)
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

func newTestTool(name, backendID string) vmcp.Tool {
	return vmcp.Tool{
		Name:        name,
		Description: name + " description",
		InputSchema: map[string]any{"type": "object"},
		BackendID:   backendID,
	}
}
