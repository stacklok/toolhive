package factory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/env"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
)

func TestNewOutgoingAuthRegistry(t *testing.T) {
	t.Parallel()

	t.Run("creates registry with all strategies registered", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)

		require.NoError(t, err)
		require.NotNil(t, registry)

		// Verify all three strategies are registered
		strategies := []string{
			strategies.StrategyTypeUnauthenticated,
			strategies.StrategyTypeHeaderInjection,
			strategies.StrategyTypeTokenExchange,
		}

		for _, strategyType := range strategies {
			strategy, err := registry.GetStrategy(strategyType)
			require.NoError(t, err, "strategy %s should be registered", strategyType)
			assert.NotNil(t, strategy, "strategy %s should not be nil", strategyType)
		}
	})

	t.Run("unknown strategy returns error", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)

		require.NoError(t, err)
		require.NotNil(t, registry)

		// Try to get a strategy that doesn't exist
		_, err = registry.GetStrategy("nonexistent_strategy")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("header_injection strategy can be retrieved and used", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)

		require.NoError(t, err)
		require.NotNil(t, registry)

		// Get header injection strategy
		strategy, err := registry.GetStrategy(strategies.StrategyTypeHeaderInjection)
		require.NoError(t, err)
		require.NotNil(t, strategy)

		// Verify it's the correct type
		assert.Equal(t, strategies.StrategyTypeHeaderInjection, strategy.Name())

		// Verify it can validate metadata
		validMetadata := map[string]any{
			"header_name":  "X-API-Key",
			"header_value": "test-key",
		}
		err = strategy.Validate(validMetadata)
		assert.NoError(t, err, "valid metadata should pass validation")

		// Verify it rejects invalid metadata
		invalidMetadata := map[string]any{
			"header_name": "X-API-Key",
			// missing header_value
		}
		err = strategy.Validate(invalidMetadata)
		assert.Error(t, err, "invalid metadata should fail validation")
		assert.Contains(t, err.Error(), "header_value")
	})

	t.Run("token_exchange strategy can be retrieved and used", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)

		require.NoError(t, err)
		require.NotNil(t, registry)

		// Get token exchange strategy
		strategy, err := registry.GetStrategy(strategies.StrategyTypeTokenExchange)
		require.NoError(t, err)
		require.NotNil(t, strategy)

		// Verify it's the correct type
		assert.Equal(t, strategies.StrategyTypeTokenExchange, strategy.Name())
	})

	t.Run("unauthenticated strategy can be retrieved and used", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)

		require.NoError(t, err)
		require.NotNil(t, registry)

		// Get unauthenticated strategy
		strategy, err := registry.GetStrategy(strategies.StrategyTypeUnauthenticated)
		require.NoError(t, err)
		require.NotNil(t, strategy)

		// Verify it's the correct type
		assert.Equal(t, strategies.StrategyTypeUnauthenticated, strategy.Name())

		// Verify it validates any metadata (no-op validation)
		err = strategy.Validate(nil)
		assert.NoError(t, err, "unauthenticated strategy should accept nil metadata")

		err = strategy.Validate(map[string]any{})
		assert.NoError(t, err, "unauthenticated strategy should accept empty metadata")
	})

	t.Run("all strategies have correct names", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)

		require.NoError(t, err)
		require.NotNil(t, registry)

		// Test that strategy names match their types
		testCases := []struct {
			strategyType string
			expectedName string
		}{
			{strategies.StrategyTypeUnauthenticated, "unauthenticated"},
			{strategies.StrategyTypeHeaderInjection, "header_injection"},
			{strategies.StrategyTypeTokenExchange, "token_exchange"},
		}

		for _, tc := range testCases {
			strategy, err := registry.GetStrategy(tc.strategyType)
			require.NoError(t, err, "should retrieve %s strategy", tc.strategyType)
			assert.Equal(t, tc.expectedName, strategy.Name(),
				"strategy type %s should have name %s", tc.strategyType, tc.expectedName)
		}
	})
}
