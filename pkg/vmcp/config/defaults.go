// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package config provides the configuration model for Virtual MCP Server.
package config

import (
	"time"

	"dario.cat/mergo"

	"github.com/stacklok/toolhive/pkg/authserver"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
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
func (c *Config) EnsureOperationalDefaults() {
	if c == nil {
		return
	}

	if c.Operational == nil {
		c.Operational = DefaultOperationalConfig()
		return
	}

	// Merge defaults into target, only filling zero/nil values.
	// User-provided values are preserved.
	_ = mergo.Merge(c.Operational, DefaultOperationalConfig())
}

// InjectSubjectProviderNames auto-populates SubjectProviderName on every
// token_exchange strategy in cfg.OutgoingAuth that has it unset, when an
// embedded auth server RunConfig is active.
//
// This is a defaulting operation: it ensures YAML-based vMCP deployments
// behave the same as the Kubernetes operator path. Without it a token_exchange
// strategy with no SubjectProviderName would silently fall back to
// identity.Token (the ToolHive-issued JWT), which the exchange endpoint rejects.
//
// When cfg or rc is nil the call is a no-op. The provider name is resolved from
// the first upstream in rc.Upstreams (normalised via authserver.ResolveUpstreamName);
// if there are no upstreams it falls back to authserver.DefaultUpstreamName.
func InjectSubjectProviderNames(cfg *Config, rc *authserver.RunConfig) {
	if cfg == nil || rc == nil || cfg.OutgoingAuth == nil {
		return
	}

	providerName := func() string {
		if len(rc.Upstreams) > 0 {
			return authserver.ResolveUpstreamName(rc.Upstreams[0].Name)
		}
		return authserver.DefaultUpstreamName
	}()

	injectIntoStrategy(cfg.OutgoingAuth.Default, providerName)
	for _, strategy := range cfg.OutgoingAuth.Backends {
		injectIntoStrategy(strategy, providerName)
	}
}

// injectIntoStrategy sets SubjectProviderName on a token_exchange strategy when
// the field is empty. It mutates the strategy in place because the OutgoingAuth
// maps hold pointers that are already owned by cfg.
func injectIntoStrategy(strategy *authtypes.BackendAuthStrategy, providerName string) {
	if strategy == nil ||
		strategy.Type != authtypes.StrategyTypeTokenExchange ||
		strategy.TokenExchange == nil ||
		strategy.TokenExchange.SubjectProviderName != "" {
		return
	}
	strategy.TokenExchange.SubjectProviderName = providerName
}
