package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUsageMetricsProviderOperations(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		provider func(t *testing.T) Provider
	}{
		{
			name: "PathProvider_UsageMetricsOperations",
			provider: func(t *testing.T) Provider {
				t.Helper()
				tempDir := t.TempDir()
				configPath := filepath.Join(tempDir, "config.yaml")
				return NewPathProvider(configPath)
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := tc.provider(t)

			// Test default state - metrics should be enabled
			enabled := provider.GetUsageMetricsEnabled()
			assert.True(t, enabled, "Usage metrics should be enabled by default")

			// Test disabling usage metrics
			err := provider.SetUsageMetricsEnabled(false)
			require.NoError(t, err)

			enabled = provider.GetUsageMetricsEnabled()
			assert.False(t, enabled, "Usage metrics should be disabled after SetUsageMetricsEnabled(false)")

			// Verify config file was updated correctly
			cfg := provider.GetConfig()
			assert.True(t, cfg.DisableUsageMetrics, "DisableUsageMetrics should be true in config")

			// Test enabling usage metrics
			err = provider.SetUsageMetricsEnabled(true)
			require.NoError(t, err)

			enabled = provider.GetUsageMetricsEnabled()
			assert.True(t, enabled, "Usage metrics should be enabled after SetUsageMetricsEnabled(true)")

			// Verify config file was updated correctly
			cfg = provider.GetConfig()
			assert.False(t, cfg.DisableUsageMetrics, "DisableUsageMetrics should be false in config")
		})
	}
}

func TestUsageMetricsKubernetesProvider(t *testing.T) {
	t.Parallel()

	provider := NewKubernetesProvider()

	// Kubernetes provider should always return false for usage metrics
	enabled := provider.GetUsageMetricsEnabled()
	assert.False(t, enabled, "Kubernetes provider should always return false for usage metrics")

	// Setting should be a no-op
	err := provider.SetUsageMetricsEnabled(true)
	assert.NoError(t, err)

	enabled = provider.GetUsageMetricsEnabled()
	assert.False(t, enabled, "Kubernetes provider should still return false after SetUsageMetricsEnabled")
}

func TestUsageMetricsConfigPersistence(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	// Create provider and disable metrics
	provider1 := NewPathProvider(configPath)
	err := provider1.SetUsageMetricsEnabled(false)
	require.NoError(t, err)

	// Create a new provider with the same path to verify persistence
	provider2 := NewPathProvider(configPath)
	enabled := provider2.GetUsageMetricsEnabled()
	assert.False(t, enabled, "Usage metrics disabled state should persist across provider instances")

	// Enable and verify persistence again
	err = provider2.SetUsageMetricsEnabled(true)
	require.NoError(t, err)

	provider3 := NewPathProvider(configPath)
	enabled = provider3.GetUsageMetricsEnabled()
	assert.True(t, enabled, "Usage metrics enabled state should persist across provider instances")
}

func TestUsageMetricsOmitEmptyBehavior(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")

	provider := NewPathProvider(configPath)

	// Enable metrics (DisableUsageMetrics = false)
	err := provider.SetUsageMetricsEnabled(true)
	require.NoError(t, err)

	// Read the config file and verify the field is omitted (because false is zero value)
	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NotContains(t, string(content), "disable_usage_metrics", "disable_usage_metrics should be omitted when false")

	// Disable metrics (DisableUsageMetrics = true)
	err = provider.SetUsageMetricsEnabled(false)
	require.NoError(t, err)

	// Read the config file and verify the field is present
	content, err = os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "disable_usage_metrics: true", "disable_usage_metrics should be present when true")
}
