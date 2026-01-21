// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultOperationalConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultOperationalConfig()

	require.NotNil(t, cfg)
	require.NotNil(t, cfg.Timeouts)
	require.NotNil(t, cfg.FailureHandling)
	require.NotNil(t, cfg.FailureHandling.CircuitBreaker)

	// Verify all defaults match constants
	assert.Equal(t, Duration(defaultTimeoutDefault), cfg.Timeouts.Default)
	assert.Nil(t, cfg.Timeouts.PerWorkload)
	assert.Equal(t, Duration(defaultHealthCheckInterval), cfg.FailureHandling.HealthCheckInterval)
	assert.Equal(t, defaultUnhealthyThreshold, cfg.FailureHandling.UnhealthyThreshold)
	assert.Equal(t, defaultPartialFailureMode, cfg.FailureHandling.PartialFailureMode)
	assert.Equal(t, defaultCircuitBreakerEnabled, cfg.FailureHandling.CircuitBreaker.Enabled)
	assert.Equal(t, defaultCircuitBreakerFailureThreshold, cfg.FailureHandling.CircuitBreaker.FailureThreshold)
	assert.Equal(t, Duration(defaultCircuitBreakerTimeout), cfg.FailureHandling.CircuitBreaker.Timeout)
}

func TestDefaultOperationalConfig_MultipleCalls(t *testing.T) {
	t.Parallel()

	// Ensure each call returns a new instance
	cfg1 := DefaultOperationalConfig()
	cfg2 := DefaultOperationalConfig()

	require.NotNil(t, cfg1)
	require.NotNil(t, cfg2)

	// Verify they are different instances
	assert.NotSame(t, cfg1, cfg2, "Each call should return a new instance")
	assert.NotSame(t, cfg1.Timeouts, cfg2.Timeouts, "Timeouts should be different instances")
	assert.NotSame(t, cfg1.FailureHandling, cfg2.FailureHandling, "FailureHandling should be different instances")
	assert.NotSame(t, cfg1.FailureHandling.CircuitBreaker, cfg2.FailureHandling.CircuitBreaker,
		"CircuitBreaker should be different instances")
}

func TestEnsureOperationalDefaults_NilConfig(t *testing.T) {
	t.Parallel()

	// Verify calling on nil Config does not panic
	var cfg *Config
	assert.NotPanics(t, func() {
		cfg.EnsureOperationalDefaults()
	}, "EnsureOperationalDefaults should not panic on nil receiver")
}

func TestEnsureOperationalDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		operational *OperationalConfig
		validate    func(t *testing.T, op *OperationalConfig)
	}{
		{
			name:        "nil operational gets full defaults",
			operational: nil,
			validate: func(t *testing.T, op *OperationalConfig) {
				t.Helper()
				require.NotNil(t, op.Timeouts)
				require.NotNil(t, op.FailureHandling)
				require.NotNil(t, op.FailureHandling.CircuitBreaker)
				assert.Equal(t, Duration(defaultTimeoutDefault), op.Timeouts.Default)
				assert.Equal(t, Duration(defaultHealthCheckInterval), op.FailureHandling.HealthCheckInterval)
			},
		},
		{
			name:        "empty operational gets full defaults",
			operational: &OperationalConfig{},
			validate: func(t *testing.T, op *OperationalConfig) {
				t.Helper()
				require.NotNil(t, op.Timeouts)
				require.NotNil(t, op.FailureHandling)
				require.NotNil(t, op.FailureHandling.CircuitBreaker)
				assert.Equal(t, Duration(defaultTimeoutDefault), op.Timeouts.Default)
				assert.Equal(t, Duration(defaultHealthCheckInterval), op.FailureHandling.HealthCheckInterval)
				assert.Equal(t, defaultUnhealthyThreshold, op.FailureHandling.UnhealthyThreshold)
				assert.Equal(t, defaultPartialFailureMode, op.FailureHandling.PartialFailureMode)
				assert.Equal(t, defaultCircuitBreakerEnabled, op.FailureHandling.CircuitBreaker.Enabled)
				assert.Equal(t, defaultCircuitBreakerFailureThreshold, op.FailureHandling.CircuitBreaker.FailureThreshold)
				assert.Equal(t, Duration(defaultCircuitBreakerTimeout), op.FailureHandling.CircuitBreaker.Timeout)
			},
		},
		{
			name: "only Timeouts provided with zero default",
			operational: &OperationalConfig{
				Timeouts: &TimeoutConfig{
					Default:     0, // zero value, should be filled
					PerWorkload: nil,
				},
			},
			validate: func(t *testing.T, op *OperationalConfig) {
				t.Helper()
				assert.Equal(t, Duration(defaultTimeoutDefault), op.Timeouts.Default,
					"Zero Default should be filled with default")
				require.NotNil(t, op.FailureHandling, "FailureHandling should be created")
				require.NotNil(t, op.FailureHandling.CircuitBreaker, "CircuitBreaker should be created")
			},
		},
		{
			name: "only FailureHandling provided with empty values",
			operational: &OperationalConfig{
				FailureHandling: &FailureHandlingConfig{},
			},
			validate: func(t *testing.T, op *OperationalConfig) {
				t.Helper()
				require.NotNil(t, op.Timeouts, "Timeouts should be created")
				assert.Equal(t, Duration(defaultTimeoutDefault), op.Timeouts.Default)
				assert.Equal(t, Duration(defaultHealthCheckInterval), op.FailureHandling.HealthCheckInterval)
				assert.Equal(t, defaultUnhealthyThreshold, op.FailureHandling.UnhealthyThreshold)
				assert.Equal(t, defaultPartialFailureMode, op.FailureHandling.PartialFailureMode)
				require.NotNil(t, op.FailureHandling.CircuitBreaker, "CircuitBreaker should be created")
			},
		},
		{
			name: "FailureHandling provided with nil CircuitBreaker",
			operational: &OperationalConfig{
				FailureHandling: &FailureHandlingConfig{
					HealthCheckInterval: Duration(15 * time.Second), // custom value
					UnhealthyThreshold:  2,                          // custom value
					PartialFailureMode:  "best_effort",              // custom value
					CircuitBreaker:      nil,                        // should be filled
				},
			},
			validate: func(t *testing.T, op *OperationalConfig) {
				t.Helper()
				// Custom values should be preserved
				assert.Equal(t, Duration(15*time.Second), op.FailureHandling.HealthCheckInterval)
				assert.Equal(t, 2, op.FailureHandling.UnhealthyThreshold)
				assert.Equal(t, "best_effort", op.FailureHandling.PartialFailureMode)
				// CircuitBreaker should be created with defaults
				require.NotNil(t, op.FailureHandling.CircuitBreaker, "CircuitBreaker should be created")
				assert.Equal(t, defaultCircuitBreakerEnabled, op.FailureHandling.CircuitBreaker.Enabled)
				assert.Equal(t, defaultCircuitBreakerFailureThreshold, op.FailureHandling.CircuitBreaker.FailureThreshold)
				assert.Equal(t, Duration(defaultCircuitBreakerTimeout), op.FailureHandling.CircuitBreaker.Timeout)
			},
		},
		{
			name: "CircuitBreaker provided with zero values",
			operational: &OperationalConfig{
				FailureHandling: &FailureHandlingConfig{
					CircuitBreaker: &CircuitBreakerConfig{
						Enabled:          false, // explicit false
						FailureThreshold: 0,     // zero, should be filled
						Timeout:          0,     // zero, should be filled
					},
				},
			},
			validate: func(t *testing.T, op *OperationalConfig) {
				t.Helper()
				// HealthCheckInterval, UnhealthyThreshold, PartialFailureMode should be filled
				assert.Equal(t, Duration(defaultHealthCheckInterval), op.FailureHandling.HealthCheckInterval)
				assert.Equal(t, defaultUnhealthyThreshold, op.FailureHandling.UnhealthyThreshold)
				assert.Equal(t, defaultPartialFailureMode, op.FailureHandling.PartialFailureMode)
				// CircuitBreaker zero values should be filled
				assert.Equal(t, false, op.FailureHandling.CircuitBreaker.Enabled,
					"Enabled should remain false (zero value is intentional)")
				assert.Equal(t, defaultCircuitBreakerFailureThreshold, op.FailureHandling.CircuitBreaker.FailureThreshold)
				assert.Equal(t, Duration(defaultCircuitBreakerTimeout), op.FailureHandling.CircuitBreaker.Timeout)
			},
		},
		{
			name: "Timeouts with PerWorkload but zero Default",
			operational: &OperationalConfig{
				Timeouts: &TimeoutConfig{
					Default: 0,
					PerWorkload: map[string]Duration{
						"workload1": Duration(45 * time.Second),
					},
				},
			},
			validate: func(t *testing.T, op *OperationalConfig) {
				t.Helper()
				assert.Equal(t, Duration(defaultTimeoutDefault), op.Timeouts.Default,
					"Zero Default should be filled")
				assert.Equal(t, Duration(45*time.Second), op.Timeouts.PerWorkload["workload1"],
					"PerWorkload should be preserved")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &Config{
				Name:        "test-vmcp",
				Group:       "test-group",
				Operational: tt.operational,
			}

			cfg.EnsureOperationalDefaults()

			require.NotNil(t, cfg.Operational, "Operational should not be nil after EnsureOperationalDefaults")
			tt.validate(t, cfg.Operational)
		})
	}
}

func TestEnsureOperationalDefaults_Idempotent(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Name:        "test-vmcp",
		Group:       "test-group",
		Operational: nil,
	}

	// Call EnsureOperationalDefaults multiple times
	cfg.EnsureOperationalDefaults()
	firstOp := cfg.Operational

	cfg.EnsureOperationalDefaults()
	secondOp := cfg.Operational

	cfg.EnsureOperationalDefaults()
	thirdOp := cfg.Operational

	// All calls should result in the same operational config (same pointer after first call)
	assert.Same(t, firstOp, secondOp, "Second call should not replace Operational")
	assert.Same(t, secondOp, thirdOp, "Third call should not replace Operational")

	// Values should remain consistent
	assert.Equal(t, Duration(defaultTimeoutDefault), cfg.Operational.Timeouts.Default)
	assert.Equal(t, Duration(defaultHealthCheckInterval), cfg.Operational.FailureHandling.HealthCheckInterval)
	assert.Equal(t, defaultUnhealthyThreshold, cfg.Operational.FailureHandling.UnhealthyThreshold)
}
