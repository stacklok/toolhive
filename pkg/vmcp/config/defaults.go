// Package config provides the configuration model for Virtual MCP Server.
package config

import "time"

// Default constants for operational configuration.
// These values match the kubebuilder defaults in the VirtualMCPServer CRD.
const (
	// defaultHealthCheckInterval is the default interval between health checks.
	defaultHealthCheckInterval = 30 * time.Second

	// defaultUnhealthyThreshold is the default number of consecutive failures
	// before marking a backend as unhealthy.
	defaultUnhealthyThreshold = 3

	// defaultPartialFailureMode defines the default behavior when some backends fail.
	// "fail" means the entire request fails if any backend is unavailable.
	defaultPartialFailureMode = "fail"

	// defaultTimeoutDefault is the default timeout for backend requests.
	defaultTimeoutDefault = 30 * time.Second

	// defaultCircuitBreakerFailureThreshold is the default number of failures
	// before opening the circuit breaker.
	defaultCircuitBreakerFailureThreshold = 5

	// defaultCircuitBreakerTimeout is the default duration to wait before
	// attempting to close the circuit.
	defaultCircuitBreakerTimeout = 60 * time.Second

	// defaultCircuitBreakerEnabled is the default state of the circuit breaker.
	defaultCircuitBreakerEnabled = false
)

// DefaultOperationalConfig returns a fully populated OperationalConfig with default values.
// This is the SINGLE SOURCE OF TRUTH for all operational defaults.
// This should be used when no operational config is provided.
func DefaultOperationalConfig() *OperationalConfig {
	return &OperationalConfig{
		Timeouts: &TimeoutConfig{
			Default:     Duration(defaultTimeoutDefault),
			PerWorkload: nil,
		},
		FailureHandling: &FailureHandlingConfig{
			HealthCheckInterval: Duration(defaultHealthCheckInterval),
			UnhealthyThreshold:  defaultUnhealthyThreshold,
			PartialFailureMode:  defaultPartialFailureMode,
			CircuitBreaker: &CircuitBreakerConfig{
				Enabled:          defaultCircuitBreakerEnabled,
				FailureThreshold: defaultCircuitBreakerFailureThreshold,
				Timeout:          Duration(defaultCircuitBreakerTimeout),
			},
		},
	}
}

// EnsureOperationalDefaults ensures that the Config has a fully populated
// OperationalConfig with default values for any missing fields.
// If Operational is nil, it sets it to DefaultOperationalConfig().
// If Operational exists but has nil or zero-value nested fields, those fields
// are filled with defaults while preserving any user-provided values.
func (c *Config) EnsureOperationalDefaults() {
	if c == nil {
		return
	}

	defaults := DefaultOperationalConfig()

	if c.Operational == nil {
		c.Operational = defaults
		return
	}

	// Merge user config over defaults - only fill missing values
	mergeOperationalConfig(c.Operational, defaults)
}

// mergeOperationalConfig merges defaults into target, only filling nil/zero values.
// The target contains user-provided values; defaults provides fallback values.
func mergeOperationalConfig(target, defaults *OperationalConfig) {
	// Handle Timeouts
	if target.Timeouts == nil {
		target.Timeouts = defaults.Timeouts
	} else {
		mergeTimeoutConfig(target.Timeouts, defaults.Timeouts)
	}

	// Handle FailureHandling
	if target.FailureHandling == nil {
		target.FailureHandling = defaults.FailureHandling
	} else {
		mergeFailureHandlingConfig(target.FailureHandling, defaults.FailureHandling)
	}
}

// mergeTimeoutConfig merges defaults into target, only filling zero values.
func mergeTimeoutConfig(target, defaults *TimeoutConfig) {
	if target.Default == 0 {
		target.Default = defaults.Default
	}
	// Note: PerWorkload is not merged - if target has it set (even empty map), we keep it.
	// If target.PerWorkload is nil, it stays nil (no default per-workload timeouts).
}

// mergeFailureHandlingConfig merges defaults into target, only filling zero/nil values.
func mergeFailureHandlingConfig(target, defaults *FailureHandlingConfig) {
	if target.HealthCheckInterval == 0 {
		target.HealthCheckInterval = defaults.HealthCheckInterval
	}
	if target.UnhealthyThreshold == 0 {
		target.UnhealthyThreshold = defaults.UnhealthyThreshold
	}
	if target.PartialFailureMode == "" {
		target.PartialFailureMode = defaults.PartialFailureMode
	}

	// Handle CircuitBreaker
	if target.CircuitBreaker == nil {
		target.CircuitBreaker = defaults.CircuitBreaker
	} else {
		mergeCircuitBreakerConfig(target.CircuitBreaker, defaults.CircuitBreaker)
	}
}

// mergeCircuitBreakerConfig merges defaults into target, only filling zero values.
// Note: Enabled is a bool where false is a valid intentional value, so we do NOT
// merge it - the zero value (false) is the default and an explicit user choice.
func mergeCircuitBreakerConfig(target, defaults *CircuitBreakerConfig) {
	// Note: Enabled defaults to false, which is the zero value for bool.
	// We intentionally do NOT merge it - a zero value is the intentional default.
	if target.FailureThreshold == 0 {
		target.FailureThreshold = defaults.FailureThreshold
	}
	if target.Timeout == 0 {
		target.Timeout = defaults.Timeout
	}
}
