package usagemetrics

import (
	"context"
	"os"
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

func TestShouldEnableMetrics(t *testing.T) { //nolint:paralleltest // Test modifies environment variables
	tests := []struct {
		name           string
		configDisabled bool
		envVarValue    string
		ciEnvVar       string
		expected       bool
	}{
		{
			name:           "default enabled",
			configDisabled: false,
			envVarValue:    "",
			ciEnvVar:       "",
			expected:       true,
		},
		{
			name:           "config disabled",
			configDisabled: true,
			envVarValue:    "",
			ciEnvVar:       "",
			expected:       false,
		},
		{
			name:           "env var opt-out",
			configDisabled: false,
			envVarValue:    "false",
			ciEnvVar:       "",
			expected:       false,
		},
		{
			name:           "config disabled overrides env enabled",
			configDisabled: true,
			envVarValue:    "true",
			ciEnvVar:       "",
			expected:       false,
		},
		{
			name:           "CI environment disables metrics",
			configDisabled: false,
			envVarValue:    "",
			ciEnvVar:       "GITHUB_ACTIONS",
			expected:       false,
		},
		{
			name:           "CI environment overrides config and env",
			configDisabled: false,
			envVarValue:    "true",
			ciEnvVar:       "CI",
			expected:       false,
		},
	}

	for _, tt := range tests { //nolint:paralleltest // Test modifies environment variables
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment variables
			if tt.envVarValue != "" {
				os.Setenv(EnvVarUsageMetricsEnabled, tt.envVarValue)
				defer os.Unsetenv(EnvVarUsageMetricsEnabled)
			} else {
				os.Unsetenv(EnvVarUsageMetricsEnabled)
			}

			if tt.ciEnvVar != "" {
				os.Setenv(tt.ciEnvVar, "true")
				defer os.Unsetenv(tt.ciEnvVar)
			}

			result := ShouldEnableMetrics(tt.configDisabled)
			assert.Equal(t, tt.expected, result, "ShouldEnableMetrics(%v) = %v, want %v", tt.configDisabled, result, tt.expected)
		})
	}
}
