package config

import "fmt"

// setUsageMetricsEnabled enables or disables usage metrics collection.
// When enabled is true, usage metrics will be collected (DisableUsageMetrics = false).
// When enabled is false, usage metrics will be disabled (DisableUsageMetrics = true).
// After updating the configuration, it resets the singleton to ensure the changes are reflected.
func setUsageMetricsEnabled(provider Provider, enabled bool) error {
	// Update the configuration
	// Note: We invert the boolean because the config field is "DisableUsageMetrics"
	err := provider.UpdateConfig(func(c *Config) {
		c.DisableUsageMetrics = !enabled
	})
	if err != nil {
		return fmt.Errorf("failed to update configuration: %w", err)
	}

	// Reset the singleton to ensure the updated config is reflected
	ResetSingleton()

	return nil
}

// getUsageMetricsEnabled returns whether usage metrics collection is currently enabled.
// Returns true if metrics are enabled (DisableUsageMetrics = false).
// Returns false if metrics are disabled (DisableUsageMetrics = true).
func getUsageMetricsEnabled(provider Provider) bool {
	cfg := provider.GetConfig()
	return !cfg.DisableUsageMetrics
}
