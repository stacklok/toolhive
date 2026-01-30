// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCollector(t *testing.T) {
	t.Parallel()

	collector, err := NewCollector()
	require.NoError(t, err)
	require.NotNil(t, collector)

	// Verify initial state
	assert.NotEmpty(t, collector.instanceID, "Instance ID should be generated")
	assert.Equal(t, int64(0), collector.GetCurrentCount(), "Initial count should be 0")
	assert.NotEmpty(t, collector.currentDate, "Current date should be set")

	// Cleanup
	ctx := context.Background()
	collector.Shutdown(ctx)
}

func TestCollector_IncrementToolCall(t *testing.T) {
	t.Parallel()

	collector, err := NewCollector()
	require.NoError(t, err)
	defer func() {
		ctx := context.Background()
		collector.Shutdown(ctx)
	}()

	// Verify initial count
	assert.Equal(t, int64(0), collector.GetCurrentCount())

	// Increment once
	collector.IncrementToolCall()
	assert.Equal(t, int64(1), collector.GetCurrentCount())

	// Increment multiple times
	for i := 0; i < 10; i++ {
		collector.IncrementToolCall()
	}
	assert.Equal(t, int64(11), collector.GetCurrentCount())
}

func TestCollector_Shutdown(t *testing.T) {
	t.Parallel()

	collector, err := NewCollector()
	require.NoError(t, err)

	// Increment some calls
	collector.IncrementToolCall()
	collector.IncrementToolCall()

	ctx := context.Background()

	// Shutdown should not error
	collector.Shutdown(ctx)

	// Second shutdown should be idempotent
	collector.Shutdown(ctx)
}

func TestCollector_Start_PreventsDuplicateGoroutines(t *testing.T) {
	t.Parallel()

	collector, err := NewCollector()
	require.NoError(t, err)
	defer func() {
		ctx := context.Background()
		collector.Shutdown(ctx)
	}()

	// Call Start multiple times
	collector.Start()
	collector.Start()
	collector.Start()

	// Verify started flag is set
	assert.True(t, collector.started.Load(), "Collector should be marked as started")

	// If multiple goroutines were created, we'd see issues with concurrent
	// access to the channels. The test passes if no race conditions occur.
	// The -race flag in our test suite will catch this.
}

func TestShouldEnableMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		configDisabled bool
		envVarValue    string
		isCI           bool
		expected       bool
	}{
		{
			name:           "default enabled",
			configDisabled: false,
			envVarValue:    "",
			isCI:           false,
			expected:       true,
		},
		{
			name:           "config disabled",
			configDisabled: true,
			envVarValue:    "",
			isCI:           false,
			expected:       false,
		},
		{
			name:           "env var opt-out",
			configDisabled: false,
			envVarValue:    "false",
			isCI:           false,
			expected:       false,
		},
		{
			name:           "config disabled overrides env enabled",
			configDisabled: true,
			envVarValue:    "true",
			isCI:           false,
			expected:       false,
		},
		{
			name:           "CI environment disables metrics",
			configDisabled: false,
			envVarValue:    "",
			isCI:           true,
			expected:       false,
		},
		{
			name:           "CI environment overrides config and env",
			configDisabled: false,
			envVarValue:    "true",
			isCI:           true,
			expected:       false,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Mock CI detection
			mockIsCI := func() bool {
				return tt.isCI
			}

			// Mock environment variable getter
			mockGetEnv := func(key string) string {
				if key == EnvVarUsageMetricsEnabled {
					return tt.envVarValue
				}
				return ""
			}

			result := shouldEnableMetrics(tt.configDisabled, mockIsCI, mockGetEnv)
			assert.Equal(t, tt.expected, result, "shouldEnableMetrics(%v) = %v, want %v", tt.configDisabled, result, tt.expected)
		})
	}
}
