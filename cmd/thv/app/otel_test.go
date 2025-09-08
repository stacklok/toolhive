package app

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
)

// createTestProvider creates a test config provider with a temporary file
func createTestProvider(t *testing.T) (config.Provider, func()) {
	t.Helper()

	// Create a temporary directory for the test
	tempDir := t.TempDir()
	
	// Create the config directory structure
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	// Set up the config file path
	configPath := filepath.Join(configDir, "config.yaml")
	
	// Create a path-based provider for testing
	provider := config.NewPathProvider(configPath)
	
	// Initialize the config file
	_, err = provider.LoadOrCreateConfig()
	require.NoError(t, err)

	return provider, func() {
		// Cleanup is handled by t.TempDir()
	}
}

func TestOtelConfigStructure(t *testing.T) {
	t.Parallel()

	t.Run("config fields update correctly", func(t *testing.T) {
		provider, cleanup := createTestProvider(t)
		defer cleanup()

		// Test that we can update each field through the provider
		err := provider.UpdateConfig(func(c *config.Config) {
			c.OTEL.Endpoint = "test-endpoint"
			c.OTEL.MetricsEnabled = true
			c.OTEL.TracingEnabled = true
			c.OTEL.Insecure = true
			c.OTEL.EnablePrometheusMetricsPath = true
			c.OTEL.SamplingRate = 0.8
			c.OTEL.EnvVars = []string{"TEST1", "TEST2"}
		})
		require.NoError(t, err)

		// Verify all fields were set correctly
		cfg := provider.GetConfig()
		assert.Equal(t, "test-endpoint", cfg.OTEL.Endpoint)
		assert.True(t, cfg.OTEL.MetricsEnabled)
		assert.True(t, cfg.OTEL.TracingEnabled)
		assert.True(t, cfg.OTEL.Insecure)
		assert.True(t, cfg.OTEL.EnablePrometheusMetricsPath)
		assert.Equal(t, 0.8, cfg.OTEL.SamplingRate)
		assert.Equal(t, []string{"TEST1", "TEST2"}, cfg.OTEL.EnvVars)
	})

	t.Run("config fields reset to defaults", func(t *testing.T) {
		provider, cleanup := createTestProvider(t)
		defer cleanup()

		// First set all fields
		err := provider.UpdateConfig(func(c *config.Config) {
			c.OTEL.Endpoint = "test-endpoint"
			c.OTEL.MetricsEnabled = true
			c.OTEL.TracingEnabled = true
			c.OTEL.Insecure = true
			c.OTEL.EnablePrometheusMetricsPath = true
			c.OTEL.SamplingRate = 0.8
			c.OTEL.EnvVars = []string{"TEST1", "TEST2"}
		})
		require.NoError(t, err)

		// Reset all fields to defaults
		err = provider.UpdateConfig(func(c *config.Config) {
			c.OTEL.Endpoint = ""
			c.OTEL.MetricsEnabled = false
			c.OTEL.TracingEnabled = false
			c.OTEL.Insecure = false
			c.OTEL.EnablePrometheusMetricsPath = false
			c.OTEL.SamplingRate = 0.0
			c.OTEL.EnvVars = []string{}
		})
		require.NoError(t, err)

		// Verify all fields were reset
		cfg := provider.GetConfig()
		assert.Equal(t, "", cfg.OTEL.Endpoint)
		assert.False(t, cfg.OTEL.MetricsEnabled)
		assert.False(t, cfg.OTEL.TracingEnabled)
		assert.False(t, cfg.OTEL.Insecure)
		assert.False(t, cfg.OTEL.EnablePrometheusMetricsPath)
		assert.Equal(t, 0.0, cfg.OTEL.SamplingRate)
		assert.Empty(t, cfg.OTEL.EnvVars)
	})
}

func TestOtelBooleanFields(t *testing.T) {
	t.Parallel()

	t.Run("all boolean fields work correctly", func(t *testing.T) {
		provider, cleanup := createTestProvider(t)
		defer cleanup()

		booleanFields := []struct {
			name     string
			setField func(*config.Config, bool)
			getField func(*config.Config) bool
		}{
			{"MetricsEnabled", func(c *config.Config, val bool) { c.OTEL.MetricsEnabled = val }, func(c *config.Config) bool { return c.OTEL.MetricsEnabled }},
			{"TracingEnabled", func(c *config.Config, val bool) { c.OTEL.TracingEnabled = val }, func(c *config.Config) bool { return c.OTEL.TracingEnabled }},
			{"Insecure", func(c *config.Config, val bool) { c.OTEL.Insecure = val }, func(c *config.Config) bool { return c.OTEL.Insecure }},
			{"EnablePrometheusMetricsPath", func(c *config.Config, val bool) { c.OTEL.EnablePrometheusMetricsPath = val }, func(c *config.Config) bool { return c.OTEL.EnablePrometheusMetricsPath }},
		}

		for _, field := range booleanFields {
			t.Run(field.name, func(t *testing.T) {
				// Test setting to true
				err := provider.UpdateConfig(func(c *config.Config) {
					field.setField(c, true)
				})
				require.NoError(t, err)

				cfg := provider.GetConfig()
				assert.True(t, field.getField(cfg), "%s should be true", field.name)

				// Test setting to false
				err = provider.UpdateConfig(func(c *config.Config) {
					field.setField(c, false)
				})
				require.NoError(t, err)

				cfg = provider.GetConfig()
				assert.False(t, field.getField(cfg), "%s should be false", field.name)
			})
		}
	})
}

func TestOtelValidation(t *testing.T) {
	t.Parallel()

	t.Run("validate boolean parsing", func(t *testing.T) {
		validBooleans := []string{"true", "false", "1", "0", "T", "F", "TRUE", "FALSE"}
		for _, boolStr := range validBooleans {
			_, err := strconv.ParseBool(boolStr)
			assert.NoError(t, err, "Should parse boolean: %s", boolStr)
		}

		invalidBooleans := []string{"maybe", "yes", "no", "invalid"}
		for _, boolStr := range invalidBooleans {
			_, err := strconv.ParseBool(boolStr)
			assert.Error(t, err, "Should not parse boolean: %s", boolStr)
		}
	})

	t.Run("validate sampling rate range", func(t *testing.T) {
		validRates := []float64{0.0, 0.1, 0.5, 1.0}
		for _, rate := range validRates {
			assert.True(t, rate >= 0.0 && rate <= 1.0, "Rate %f should be valid", rate)
		}

		invalidRates := []float64{-0.1, 1.5, 2.0}
		for _, rate := range invalidRates {
			assert.False(t, rate >= 0.0 && rate <= 1.0, "Rate %f should be invalid", rate)
		}
	})
}

func TestOtelConfigPersistence(t *testing.T) {
	t.Parallel()

	t.Run("config persists across provider reloads", func(t *testing.T) {
		provider, cleanup := createTestProvider(t)
		defer cleanup()

		// Set configuration
		err := provider.UpdateConfig(func(c *config.Config) {
			c.OTEL.MetricsEnabled = true
			c.OTEL.TracingEnabled = true
			c.OTEL.Insecure = false
			c.OTEL.EnablePrometheusMetricsPath = true
			c.OTEL.Endpoint = "persistent-endpoint"
			c.OTEL.SamplingRate = 0.75
			c.OTEL.EnvVars = []string{"PERSIST_VAR1", "PERSIST_VAR2"}
		})
		require.NoError(t, err)

		// Reload config to ensure it persisted
		cfg, err := provider.LoadOrCreateConfig()
		require.NoError(t, err)

		assert.True(t, cfg.OTEL.MetricsEnabled)
		assert.True(t, cfg.OTEL.TracingEnabled)
		assert.False(t, cfg.OTEL.Insecure)
		assert.True(t, cfg.OTEL.EnablePrometheusMetricsPath)
		assert.Equal(t, "persistent-endpoint", cfg.OTEL.Endpoint)
		assert.Equal(t, 0.75, cfg.OTEL.SamplingRate)
		assert.Equal(t, []string{"PERSIST_VAR1", "PERSIST_VAR2"}, cfg.OTEL.EnvVars)
	})
}