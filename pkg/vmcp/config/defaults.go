// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package config provides the configuration model for Virtual MCP Server.
package config

import (
	"time"

	"dario.cat/mergo"
)

// Default constants for operational configuration.
// These values match the kubebuilder defaults in the VirtualMCPServer CRD.
const (
	// defaultHealthCheckInterval is the default interval between health checks.
	defaultHealthCheckInterval = 30 * time.Second

	// defaultUnhealthyThreshold is the default number of consecutive failures
	// before marking a backend as unhealthy.
	defaultUnhealthyThreshold = 3

	// defaultStatusReportingInterval is the default interval for reporting status updates.
	defaultStatusReportingInterval = 30 * time.Second

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

// boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool {
	return &b
}

// DefaultOperationalConfig returns a fully populated OperationalConfig with default values.
// This is the SINGLE SOURCE OF TRUTH for all operational defaults.
// This should be used when no operational config is provided.
func DefaultOperationalConfig() *OperationalConfig {
	return &OperationalConfig{
		SessionManagementV2: boolPtr(true),
		Timeouts: &TimeoutConfig{
			Default:     Duration(defaultTimeoutDefault),
			PerWorkload: nil,
		},
		FailureHandling: &FailureHandlingConfig{
			HealthCheckInterval:     Duration(defaultHealthCheckInterval),
			UnhealthyThreshold:      defaultUnhealthyThreshold,
			StatusReportingInterval: Duration(defaultStatusReportingInterval),
			PartialFailureMode:      defaultPartialFailureMode,
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
//
// Note: SessionManagementV2 is a *bool. nil means "unset" → default true.
// mergo dereferences pointers and treats *false as a zero-value bool, so an
// explicit *false opt-out is saved before the merge and restored afterwards.
func (c *Config) EnsureOperationalDefaults() {
	if c == nil {
		return
	}

	if c.Operational == nil {
		c.Operational = DefaultOperationalConfig()
		return
	}

	// mergo treats the bool zero-value (false) as "unset" even through a pointer,
	// and overwrites the value at the pointer address. Save the original pointer
	// and the value it holds so they can be restored after the merge, preserving
	// pointer identity for any caller that retains a reference.
	origPtr := c.Operational.SessionManagementV2
	var origVal bool
	if origPtr != nil {
		origVal = *origPtr
	}

	// Merge defaults into target, only filling zero/nil values.
	// User-provided values are preserved (except for the *bool caveat above).
	_ = mergo.Merge(c.Operational, DefaultOperationalConfig())

	// Restore the user-provided value through the original pointer so that
	// pointer identity is preserved and the explicit opt-out is not lost.
	if origPtr != nil {
		*origPtr = origVal
		c.Operational.SessionManagementV2 = origPtr
	}
}
