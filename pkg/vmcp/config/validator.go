// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// Incoming auth type constants.
const (
	IncomingAuthTypeOIDC      = "oidc"
	IncomingAuthTypeLocal     = "local"
	IncomingAuthTypeAnonymous = "anonymous"
)

// DefaultValidator implements comprehensive configuration validation.
type DefaultValidator struct{}

// NewValidator creates a new configuration validator.
func NewValidator() *DefaultValidator {
	return &DefaultValidator{}
}

// Validate performs comprehensive validation of the configuration.
func (v *DefaultValidator) Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("%w: configuration is nil", vmcp.ErrInvalidConfig)
	}

	var errors []string

	// Validate basic fields
	if err := v.validateBasicFields(cfg); err != nil {
		errors = append(errors, err.Error())
	}

	// Validate incoming authentication
	if err := v.validateIncomingAuth(cfg.IncomingAuth); err != nil {
		errors = append(errors, err.Error())
	}

	// Validate outgoing authentication
	if err := v.validateOutgoingAuth(cfg.OutgoingAuth); err != nil {
		errors = append(errors, err.Error())
	}

	// Validate aggregation configuration
	if err := v.validateAggregation(cfg.Aggregation); err != nil {
		errors = append(errors, err.Error())
	}

	// Validate operational configuration
	if err := v.validateOperational(cfg.Operational); err != nil {
		errors = append(errors, err.Error())
	}

	// Validate composite tools
	if err := v.validateCompositeTools(cfg.CompositeTools); err != nil {
		errors = append(errors, err.Error())
	}

	// Validate composite tool references
	if err := v.validateCompositeToolRefs(cfg.CompositeToolRefs); err != nil {
		errors = append(errors, err.Error())
	}

	if len(errors) > 0 {
		return fmt.Errorf("%w:\n  - %s", vmcp.ErrInvalidConfig, strings.Join(errors, "\n  - "))
	}

	return nil
}

func (*DefaultValidator) validateBasicFields(cfg *Config) error {
	if cfg.Name == "" {
		return fmt.Errorf("name is required")
	}

	if cfg.Group == "" {
		return fmt.Errorf("group reference is required")
	}

	return nil
}

func (v *DefaultValidator) validateIncomingAuth(auth *IncomingAuthConfig) error {
	if auth == nil {
		return fmt.Errorf("incomingAuth is required")
	}

	// Validate auth type
	validTypes := []string{IncomingAuthTypeOIDC, IncomingAuthTypeLocal, IncomingAuthTypeAnonymous}
	if !contains(validTypes, auth.Type) {
		return fmt.Errorf("incomingAuth.type must be one of: %s", strings.Join(validTypes, ", "))
	}

	// Validate OIDC configuration
	if auth.Type == IncomingAuthTypeOIDC {
		if auth.OIDC == nil {
			return fmt.Errorf("incomingAuth.oidc is required when type is 'oidc'")
		}

		if auth.OIDC.Issuer == "" {
			return fmt.Errorf("incomingAuth.oidc.issuer is required")
		}

		if auth.OIDC.Audience == "" {
			return fmt.Errorf("incomingAuth.oidc.audience is required")
		}

		// ClientID is optional - only required for specific flows:
		// - Token introspection with client credentials
		// - Some OAuth flows requiring client identification
		// Not required for standard JWT validation using JWKS

		// ClientSecretEnv is optional - some OIDC flows don't require client secrets:
		// - PKCE flows (public clients)
		// - Token validation without introspection
		// - Kubernetes service account token validation
	}

	// Validate authorization configuration
	if auth.Authz != nil {
		if err := v.validateAuthz(auth.Authz); err != nil {
			return fmt.Errorf("incomingAuth.authz: %w", err)
		}
	}

	return nil
}

func (*DefaultValidator) validateAuthz(authz *AuthzConfig) error {
	validTypes := []string{"cedar", "none"}
	if !contains(validTypes, authz.Type) {
		return fmt.Errorf("type must be one of: %s", strings.Join(validTypes, ", "))
	}

	if authz.Type == "cedar" && len(authz.Policies) == 0 {
		return fmt.Errorf("policies are required when type is 'cedar'")
	}

	return nil
}

func (v *DefaultValidator) validateOutgoingAuth(auth *OutgoingAuthConfig) error {
	if auth == nil {
		return fmt.Errorf("outgoingAuth is required")
	}

	// Validate source
	validSources := []string{"inline", "discovered"}
	if !contains(validSources, auth.Source) {
		return fmt.Errorf("outgoingAuth.source must be one of: %s", strings.Join(validSources, ", "))
	}

	// Validate default strategy
	if auth.Default != nil {
		if err := v.validateBackendAuthStrategy("default", auth.Default); err != nil {
			return fmt.Errorf("outgoingAuth.default: %w", err)
		}
	}

	// Validate per-backend strategies
	for backendName, strategy := range auth.Backends {
		if err := v.validateBackendAuthStrategy(backendName, strategy); err != nil {
			return fmt.Errorf("outgoingAuth.backends.%s: %w", backendName, err)
		}
	}

	return nil
}

func (*DefaultValidator) validateBackendAuthStrategy(_ string, strategy *authtypes.BackendAuthStrategy) error {
	if strategy == nil {
		return fmt.Errorf("strategy is nil")
	}

	validTypes := []string{
		authtypes.StrategyTypeUnauthenticated,
		authtypes.StrategyTypeHeaderInjection,
		authtypes.StrategyTypeTokenExchange,
		// TODO: Add more as strategies are implemented:
		// "pass_through", "client_credentials", "oauth_proxy",
	}
	if !contains(validTypes, strategy.Type) {
		return fmt.Errorf("type must be one of: %s", strings.Join(validTypes, ", "))
	}

	// Validate type-specific requirements
	switch strategy.Type {
	case authtypes.StrategyTypeTokenExchange:
		// Token exchange requires TokenExchange config with tokenUrl
		if strategy.TokenExchange == nil {
			return fmt.Errorf("tokenExchange requires TokenExchange configuration")
		}
		if strategy.TokenExchange.TokenURL == "" {
			return fmt.Errorf("tokenExchange requires tokenUrl field")
		}

	case authtypes.StrategyTypeHeaderInjection:
		// Header injection requires HeaderInjection config with header name and value
		if strategy.HeaderInjection == nil {
			return fmt.Errorf("headerInjection requires HeaderInjection configuration")
		}
		if strategy.HeaderInjection.HeaderName == "" {
			return fmt.Errorf("headerInjection requires headerName field")
		}
		if strategy.HeaderInjection.HeaderValue == "" {
			return fmt.Errorf("headerInjection requires headerValue field")
		}
	}

	return nil
}

func (v *DefaultValidator) validateAggregation(agg *AggregationConfig) error {
	if agg == nil {
		return fmt.Errorf("aggregation is required")
	}

	// Validate conflict resolution strategy
	validStrategies := []vmcp.ConflictResolutionStrategy{
		vmcp.ConflictStrategyPrefix,
		vmcp.ConflictStrategyPriority,
		vmcp.ConflictStrategyManual,
	}
	if !containsStrategy(validStrategies, agg.ConflictResolution) {
		return fmt.Errorf("conflictResolution must be one of: prefix, priority, manual")
	}

	// Validate strategy-specific configuration
	if agg.ConflictResolutionConfig == nil {
		return fmt.Errorf("conflictResolutionConfig is required")
	}

	if err := v.validateConflictStrategy(agg); err != nil {
		return err
	}

	return v.validateToolConfigurations(agg.Tools)
}

// validateConflictStrategy validates strategy-specific configuration
func (*DefaultValidator) validateConflictStrategy(agg *AggregationConfig) error {
	switch agg.ConflictResolution {
	case vmcp.ConflictStrategyPrefix:
		if agg.ConflictResolutionConfig.PrefixFormat == "" {
			return fmt.Errorf("prefixFormat is required for prefix strategy")
		}

	case vmcp.ConflictStrategyPriority:
		if len(agg.ConflictResolutionConfig.PriorityOrder) == 0 {
			return fmt.Errorf("priorityOrder is required for priority strategy")
		}

	case vmcp.ConflictStrategyManual:
		// Manual strategy requires explicit overrides
		if len(agg.Tools) == 0 {
			return fmt.Errorf("tool overrides are required for manual strategy")
		}
	}

	return nil
}

// validateToolConfigurations validates tool override configurations
func (v *DefaultValidator) validateToolConfigurations(tools []*WorkloadToolConfig) error {
	workloadNames := make(map[string]bool)
	for i, tool := range tools {
		if tool.Workload == "" {
			return fmt.Errorf("tools[%d].workload is required", i)
		}

		if workloadNames[tool.Workload] {
			return fmt.Errorf("duplicate workload configuration: %s", tool.Workload)
		}
		workloadNames[tool.Workload] = true

		if err := v.validateToolOverrides(tool.Overrides, i); err != nil {
			return err
		}
	}

	return nil
}

// validateToolOverrides validates individual tool overrides
func (*DefaultValidator) validateToolOverrides(overrides map[string]*ToolOverride, toolIndex int) error {
	for toolName, override := range overrides {
		if override.Name == "" && override.Description == "" {
			return fmt.Errorf("tools[%d].overrides.%s: at least one of name or description must be specified", toolIndex, toolName)
		}
	}
	return nil
}

func (v *DefaultValidator) validateOperational(ops *OperationalConfig) error {
	if ops == nil {
		return nil // Operational config is optional (defaults apply)
	}

	// Validate timeouts
	if ops.Timeouts != nil {
		if ops.Timeouts.Default <= 0 {
			return fmt.Errorf("operational.timeouts.default must be positive")
		}

		for workload, timeout := range ops.Timeouts.PerWorkload {
			if timeout <= 0 {
				return fmt.Errorf("operational.timeouts.perWorkload.%s must be positive", workload)
			}
		}
	}

	// Validate failure handling
	if ops.FailureHandling != nil {
		if err := v.validateFailureHandling(ops.FailureHandling); err != nil {
			return fmt.Errorf("operational.failureHandling: %w", err)
		}
	}

	return nil
}

func (*DefaultValidator) validateFailureHandling(fh *FailureHandlingConfig) error {
	if fh.HealthCheckInterval <= 0 {
		return fmt.Errorf("healthCheckInterval must be positive")
	}

	if fh.UnhealthyThreshold <= 0 {
		return fmt.Errorf("unhealthyThreshold must be positive")
	}

	// Validate health check timeout if provided
	if fh.HealthCheckTimeout > 0 {
		checkInterval := time.Duration(fh.HealthCheckInterval)
		healthCheckTimeout := time.Duration(fh.HealthCheckTimeout)

		// Validate that timeout is less than interval to prevent checks from queuing up
		if healthCheckTimeout >= checkInterval {
			return fmt.Errorf("healthCheckTimeout (%v) must be less than healthCheckInterval (%v) to prevent checks from queuing up",
				healthCheckTimeout, checkInterval)
		}
	}

	validModes := []string{"fail", "bestEffort"}
	if !contains(validModes, fh.PartialFailureMode) {
		return fmt.Errorf("partialFailureMode must be one of: %s", strings.Join(validModes, ", "))
	}

	// Validate circuit breaker
	if fh.CircuitBreaker != nil && fh.CircuitBreaker.Enabled {
		if fh.CircuitBreaker.FailureThreshold < 1 {
			return fmt.Errorf("circuitBreaker.failureThreshold must be >= 1, got %d",
				fh.CircuitBreaker.FailureThreshold)
		}

		cbTimeout := time.Duration(fh.CircuitBreaker.Timeout)
		if cbTimeout <= 0 {
			return fmt.Errorf("circuitBreaker.timeout must be > 0, got %v", cbTimeout)
		}

		if cbTimeout < time.Second {
			return fmt.Errorf("circuitBreaker.timeout must be >= 1s to prevent thrashing, got %v",
				cbTimeout)
		}
	}

	return nil
}

func (*DefaultValidator) validateCompositeTools(tools []CompositeToolConfig) error {
	if len(tools) == 0 {
		return nil // Composite tools are optional
	}

	toolNames := make(map[string]bool)

	for i := range tools {
		tool := &tools[i]

		// Check for duplicate tool names
		if toolNames[tool.Name] {
			return fmt.Errorf("duplicate composite tool name: %s", tool.Name)
		}
		toolNames[tool.Name] = true

		// Use shared validation
		if err := ValidateCompositeToolConfig(fmt.Sprintf("compositeTools[%d]", i), tool); err != nil {
			return err
		}
	}

	return nil
}

func (*DefaultValidator) validateCompositeToolRefs(refs []CompositeToolRef) error {
	if len(refs) == 0 {
		return nil // Composite tool references are optional
	}

	refNames := make(map[string]bool)

	for i := range refs {
		ref := &refs[i]

		if ref.Name == "" {
			return fmt.Errorf("compositeToolRefs[%d].name is required", i)
		}

		if refNames[ref.Name] {
			return fmt.Errorf("duplicate composite tool reference: %s", ref.Name)
		}
		refNames[ref.Name] = true
	}

	return nil
}

// Note: Workflow step validation is now handled by the shared ValidateWorkflowSteps function
// in composite_validation.go, which is called by ValidateCompositeToolConfig.

// Helper functions

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func containsStrategy(slice []vmcp.ConflictResolutionStrategy, item vmcp.ConflictResolutionStrategy) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
