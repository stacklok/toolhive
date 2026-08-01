// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package config provides the configuration model for Virtual MCP Server.
package config

import (
	"fmt"
	"time"

	"dario.cat/mergo"
	"gopkg.in/yaml.v3"

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

// legacyEmissionKeys probes whether the raw YAML set the legacy-emission
// toggles. telemetry.Config carries them as plain bools, so after unmarshalling
// an omitted key is indistinguishable from an explicit false — and false is the
// wrong default here.
type legacyEmissionKeys struct {
	Telemetry *struct {
		UseLegacyAttributes *bool `yaml:"useLegacyAttributes"`
		UseLegacyMetrics    *bool `yaml:"useLegacyMetrics"`
	} `yaml:"telemetry"`
}

// EnsureTelemetryLegacyDefaults turns the legacy-emission toggles on for any
// telemetry block that omits them, so a standalone vMCP config keeps emitting
// the pre-rename metric and attribute names it did before the rename. raw is the
// YAML the config was decoded from; when a key is absent there the documented
// default of true wins, and an explicit false is preserved.
//
// Without this, a config carrying any telemetry block without these keys would
// take the Go zero value false and silently drop every legacy name — the
// opposite of the documented default. The thv run and thv serve paths get the
// same treatment via BuildTelemetryConfigFromAppConfig and boolOrDefaultTrue.
func (c *Config) EnsureTelemetryLegacyDefaults(raw []byte) {
	if c == nil || c.Telemetry == nil {
		return
	}

	var probe legacyEmissionKeys
	if err := yaml.Unmarshal(raw, &probe); err != nil || probe.Telemetry == nil {
		// A failed probe means the caller built the config in memory rather than
		// from this YAML; default both on, matching an absent block.
		c.Telemetry.UseLegacyAttributes = true
		c.Telemetry.UseLegacyMetrics = true
		return
	}

	if probe.Telemetry.UseLegacyAttributes == nil {
		c.Telemetry.UseLegacyAttributes = true
	}
	if probe.Telemetry.UseLegacyMetrics == nil {
		c.Telemetry.UseLegacyMetrics = true
	}
}

// InjectSubjectProviderNames auto-populates SubjectProviderName on every
// token_exchange, aws_sts, or xaa strategy in cfg.OutgoingAuth that has it
// unset, when an embedded auth server RunConfig is active.
//
// This is a defaulting operation: it ensures YAML-based vMCP deployments
// behave the same as the Kubernetes operator path. Without it a token_exchange
// strategy with no SubjectProviderName would silently fall back to
// identity.Token (the ToolHive-issued JWT), which the exchange endpoint rejects.
//
// When cfg or rc is nil the call is a no-op. The provider name is derived via
// authserver.ResolveFirstUpstreamName over rc.Upstreams. For xaa, if more than
// one upstream is configured and SubjectProviderName is empty, defaulting is
// ambiguous and this returns an error wrapping
// authtypes.ErrAmbiguousSubjectProvider instead of silently defaulting;
// processing stops at the first error. Strategy structs are replaced via
// copy-then-assign rather than mutated in place, but cfg.OutgoingAuth itself
// is still updated by this call.
func InjectSubjectProviderNames(cfg *Config, rc *authserver.RunConfig) error {
	if cfg == nil || rc == nil || cfg.OutgoingAuth == nil {
		return nil
	}

	names := make([]string, len(rc.Upstreams))
	for i, u := range rc.Upstreams {
		names[i] = u.Name
	}
	providerName := authserver.ResolveFirstUpstreamName(names)
	hasMultipleUpstreams := len(names) > 1

	defaulted, err := authtypes.DefaultSubjectProviderName(cfg.OutgoingAuth.Default, providerName, hasMultipleUpstreams)
	if err != nil {
		return fmt.Errorf("default backend: %w", err)
	}
	cfg.OutgoingAuth.Default = defaulted

	for name, strategy := range cfg.OutgoingAuth.Backends {
		defaulted, err := authtypes.DefaultSubjectProviderName(strategy, providerName, hasMultipleUpstreams)
		if err != nil {
			return fmt.Errorf("backend %q: %w", name, err)
		}
		cfg.OutgoingAuth.Backends[name] = defaulted
	}

	return nil
}
