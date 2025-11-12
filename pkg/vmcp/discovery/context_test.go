package discovery

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

func TestWithDiscoveredCapabilities(t *testing.T) {
	t.Parallel()

	t.Run("no context value returns nil, false", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()

		retrieved, ok := DiscoveredCapabilitiesFromContext(ctx)

		assert.False(t, ok)
		assert.Nil(t, retrieved)
	})

	t.Run("capabilities stored in context", func(t *testing.T) {
		t.Parallel()

		caps := &aggregator.AggregatedCapabilities{
			Metadata: &aggregator.AggregationMetadata{
				BackendCount: 1,
			},
		}

		ctx := context.Background()
		enrichedCtx := WithDiscoveredCapabilities(ctx, caps)

		require.NotNil(t, enrichedCtx)

		// Verify we can retrieve the capabilities
		retrieved, ok := DiscoveredCapabilitiesFromContext(enrichedCtx)
		assert.True(t, ok)
		require.NotNil(t, retrieved)
		assert.Equal(t, caps, retrieved)
	})

	t.Run("nil capabilities returns original context", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		enrichedCtx := WithDiscoveredCapabilities(ctx, nil)

		// Should return original context unchanged
		assert.Equal(t, ctx, enrichedCtx)

		// Attempting to retrieve should return nil, false
		retrieved, ok := DiscoveredCapabilitiesFromContext(enrichedCtx)
		assert.False(t, ok)
		assert.Nil(t, retrieved)
	})

	t.Run("capabilities can be overwritten", func(t *testing.T) {
		t.Parallel()

		caps1 := &aggregator.AggregatedCapabilities{
			Metadata: &aggregator.AggregationMetadata{
				BackendCount: 1,
			},
		}

		caps2 := &aggregator.AggregatedCapabilities{
			Metadata: &aggregator.AggregationMetadata{
				BackendCount: 2,
			},
		}

		ctx := context.Background()
		ctx = WithDiscoveredCapabilities(ctx, caps1)
		ctx = WithDiscoveredCapabilities(ctx, caps2)

		retrieved, ok := DiscoveredCapabilitiesFromContext(ctx)
		assert.True(t, ok)
		require.NotNil(t, retrieved)
		assert.Equal(t, caps2, retrieved)
		assert.NotEqual(t, caps1, retrieved)
	})
}
