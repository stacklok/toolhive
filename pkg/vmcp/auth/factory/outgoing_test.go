// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/env"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
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
		strategyTypes := []string{
			authtypes.StrategyTypeUnauthenticated,
			authtypes.StrategyTypeHeaderInjection,
			authtypes.StrategyTypeTokenExchange,
		}

		for _, strategyType := range strategyTypes {
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
		strategy, err := registry.GetStrategy(authtypes.StrategyTypeHeaderInjection)
		require.NoError(t, err)
		require.NotNil(t, strategy)

		// Verify it's the correct type
		assert.Equal(t, authtypes.StrategyTypeHeaderInjection, strategy.Name())

		// Verify it can validate strategy
		validStrategy := &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeHeaderInjection,
			HeaderInjection: &authtypes.HeaderInjectionConfig{
				HeaderName:  "X-API-Key",
				HeaderValue: "test-key",
			},
		}
		err = strategy.Validate(validStrategy)
		assert.NoError(t, err, "valid strategy should pass validation")

		// Verify it rejects invalid strategy
		invalidStrategy := &authtypes.BackendAuthStrategy{
			Type: authtypes.StrategyTypeHeaderInjection,
			HeaderInjection: &authtypes.HeaderInjectionConfig{
				HeaderName: "X-API-Key",
				// missing header_value
			},
		}
		err = strategy.Validate(invalidStrategy)
		assert.Error(t, err, "invalid strategy should fail validation")
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
		strategy, err := registry.GetStrategy(authtypes.StrategyTypeTokenExchange)
		require.NoError(t, err)
		require.NotNil(t, strategy)

		// Verify it's the correct type
		assert.Equal(t, authtypes.StrategyTypeTokenExchange, strategy.Name())
	})

	t.Run("unauthenticated strategy can be retrieved and used", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		envReader := &env.OSReader{}
		registry, err := NewOutgoingAuthRegistry(ctx, envReader)

		require.NoError(t, err)
		require.NotNil(t, registry)

		// Get unauthenticated strategy
		strategy, err := registry.GetStrategy(authtypes.StrategyTypeUnauthenticated)
		require.NoError(t, err)
		require.NotNil(t, strategy)

		// Verify it's the correct type
		assert.Equal(t, authtypes.StrategyTypeUnauthenticated, strategy.Name())

		// Verify it validates any strategy (no-op validation)
		err = strategy.Validate(nil)
		assert.NoError(t, err, "unauthenticated strategy should accept nil strategy")

		err = strategy.Validate(&authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUnauthenticated})
		assert.NoError(t, err, "unauthenticated strategy should accept empty strategy")
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
			{authtypes.StrategyTypeUnauthenticated, "unauthenticated"},
			{authtypes.StrategyTypeHeaderInjection, "header_injection"},
			{authtypes.StrategyTypeTokenExchange, "token_exchange"},
		}

		for _, tc := range testCases {
			strategy, err := registry.GetStrategy(tc.strategyType)
			require.NoError(t, err, "should retrieve %s strategy", tc.strategyType)
			assert.Equal(t, tc.expectedName, strategy.Name(),
				"strategy type %s should have name %s", tc.strategyType, tc.expectedName)
		}
	})
}
