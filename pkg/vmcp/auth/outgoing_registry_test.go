// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp/auth/mocks"
)

func TestDefaultOutgoingAuthRegistry_RegisterStrategy(t *testing.T) {
	t.Parallel()
	t.Run("register valid strategy succeeds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		registry := NewDefaultOutgoingAuthRegistry()
		strategy := mocks.NewMockStrategy(ctrl)
		strategy.EXPECT().Name().Return("bearer").AnyTimes()

		err := registry.RegisterStrategy("bearer", strategy)

		require.NoError(t, err)
		// Verify strategy was registered
		retrieved, err := registry.GetStrategy("bearer")
		require.NoError(t, err)
		assert.Equal(t, strategy, retrieved)
	})

	t.Run("register empty name fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		registry := NewDefaultOutgoingAuthRegistry()
		strategy := mocks.NewMockStrategy(ctrl)

		err := registry.RegisterStrategy("", strategy)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "strategy name cannot be empty")
	})

	t.Run("register nil strategy fails", func(t *testing.T) {
		t.Parallel()
		registry := NewDefaultOutgoingAuthRegistry()

		err := registry.RegisterStrategy("bearer", nil)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "strategy cannot be nil")
	})

	t.Run("register duplicate name fails", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		registry := NewDefaultOutgoingAuthRegistry()
		strategy1 := mocks.NewMockStrategy(ctrl)
		strategy1.EXPECT().Name().Return("bearer").AnyTimes()
		strategy2 := mocks.NewMockStrategy(ctrl)
		strategy2.EXPECT().Name().Return("bearer").AnyTimes()

		err := registry.RegisterStrategy("bearer", strategy1)
		require.NoError(t, err)

		err = registry.RegisterStrategy("bearer", strategy2)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
		assert.Contains(t, err.Error(), "bearer")
	})

	t.Run("register multiple different strategies succeeds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		registry := NewDefaultOutgoingAuthRegistry()
		bearer := mocks.NewMockStrategy(ctrl)
		bearer.EXPECT().Name().Return("bearer").AnyTimes()
		basic := mocks.NewMockStrategy(ctrl)
		basic.EXPECT().Name().Return("basic").AnyTimes()
		apiKey := mocks.NewMockStrategy(ctrl)
		apiKey.EXPECT().Name().Return("api-key").AnyTimes()

		require.NoError(t, registry.RegisterStrategy("bearer", bearer))
		require.NoError(t, registry.RegisterStrategy("basic", basic))
		require.NoError(t, registry.RegisterStrategy("api-key", apiKey))

		// Verify all strategies are registered
		s1, err := registry.GetStrategy("bearer")
		require.NoError(t, err)
		assert.Equal(t, bearer, s1)

		s2, err := registry.GetStrategy("basic")
		require.NoError(t, err)
		assert.Equal(t, basic, s2)

		s3, err := registry.GetStrategy("api-key")
		require.NoError(t, err)
		assert.Equal(t, apiKey, s3)
	})
}

func TestDefaultOutgoingAuthRegistry_GetStrategy(t *testing.T) {
	t.Parallel()
	t.Run("get existing strategy succeeds", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		registry := NewDefaultOutgoingAuthRegistry()
		strategy := mocks.NewMockStrategy(ctrl)
		strategy.EXPECT().Name().Return("bearer").AnyTimes()
		require.NoError(t, registry.RegisterStrategy("bearer", strategy))

		retrieved, err := registry.GetStrategy("bearer")

		require.NoError(t, err)
		assert.Equal(t, strategy, retrieved)
	})

	t.Run("get non-existent strategy fails", func(t *testing.T) {
		t.Parallel()
		registry := NewDefaultOutgoingAuthRegistry()

		retrieved, err := registry.GetStrategy("non-existent")

		assert.Error(t, err)
		assert.Nil(t, retrieved)
		assert.Contains(t, err.Error(), "not found")
		assert.Contains(t, err.Error(), "non-existent")
	})

	t.Run("get from empty registry fails", func(t *testing.T) {
		t.Parallel()
		registry := NewDefaultOutgoingAuthRegistry()

		retrieved, err := registry.GetStrategy("bearer")

		assert.Error(t, err)
		assert.Nil(t, retrieved)
	})
}

func TestDefaultOutgoingAuthRegistry_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	t.Run("concurrent GetStrategy calls are thread-safe", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		registry := NewDefaultOutgoingAuthRegistry()

		// Register multiple strategies
		strategies := []string{"bearer", "basic", "api-key", "oauth2", "jwt"}
		for _, name := range strategies {
			strategy := mocks.NewMockStrategy(ctrl)
			strategy.EXPECT().Name().Return(name).AnyTimes()
			require.NoError(t, registry.RegisterStrategy(name, strategy))
		}

		// Test concurrent reads with -race detector
		const numGoroutines = 100
		const numOperations = 1000

		var wg sync.WaitGroup
		wg.Add(numGoroutines)

		errs := make(chan error, numGoroutines*numOperations)

		for i := 0; i < numGoroutines; i++ {
			go func(_ int) {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					// Rotate through strategies
					strategyName := strategies[j%len(strategies)]
					strategy, err := registry.GetStrategy(strategyName)
					if err != nil {
						errs <- err
						return
					}
					if strategy.Name() != strategyName {
						errs <- errors.New("strategy name mismatch")
						return
					}
				}
			}(i)
		}

		wg.Wait()
		close(errs)

		// Check for errors
		var collectedErrors []error
		for err := range errs {
			collectedErrors = append(collectedErrors, err)
		}

		if len(collectedErrors) > 0 {
			t.Fatalf("concurrent access produced errors: %v", collectedErrors)
		}
	})

	t.Run("concurrent RegisterStrategy and GetStrategy are thread-safe", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		registry := NewDefaultOutgoingAuthRegistry()

		const numRegister = 50
		const numGet = 50

		var wg sync.WaitGroup
		wg.Add(numRegister + numGet)

		errs := make(chan error, numRegister+numGet)

		// Goroutines registering strategies
		for i := 0; i < numRegister; i++ {
			go func(id int) {
				defer wg.Done()
				strategyName := "strategy-" + string(rune('A'+id%26)) + string(rune('0'+id/26))
				strategy := mocks.NewMockStrategy(ctrl)
				strategy.EXPECT().Name().Return(strategyName).AnyTimes()
				err := registry.RegisterStrategy(strategyName, strategy)
				if err != nil {
					errs <- err
				}
			}(i)
		}

		// Goroutines reading strategies (will mostly fail, but shouldn't race)
		for i := 0; i < numGet; i++ {
			go func(id int) {
				defer wg.Done()
				strategyName := "strategy-" + string(rune('A'+id%26)) + string(rune('0'+id/26))
				// GetStrategy may return error if not registered yet, that's OK
				_, _ = registry.GetStrategy(strategyName)
			}(i)
		}

		wg.Wait()
		close(errs)

		// Check for unexpected errors (registration errors are not expected)
		var collectedErrors []error
		for err := range errs {
			collectedErrors = append(collectedErrors, err)
		}

		if len(collectedErrors) > 0 {
			t.Fatalf("concurrent RegisterStrategy/GetStrategy produced errors: %v", collectedErrors)
		}
	})
}
