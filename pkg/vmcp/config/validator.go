// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// Incoming auth type constants.
const (
	IncomingAuthTypeOIDC      = "oidc"
	IncomingAuthTypeLocal     = "local"
	IncomingAuthTypeAnonymous = "anonymous"
)

// defaultStrategyKey is the synthetic map key used for the default outgoing auth
// strategy in collectAllBackendStrategies. It is deliberately different from
// authserver.DefaultUpstreamName ("default") to avoid confusion with upstream
// provider names and to prevent key collisions with user-defined backend names.
const defaultStrategyKey = "<default-strategy>"

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

	// Note: Optimizer validation is handled by optimizer.GetAndValidateConfig
	// in pkg/vmcp/optimizer/optimizer.go when the optimizer is constructed.

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
	if !slices.Contains(validTypes, auth.Type) {
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
	if !slices.Contains(validTypes, authz.Type) {
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
	if !slices.Contains(validSources, auth.Source) {
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
		authtypes.StrategyTypeUpstreamInject,
	}
	if !slices.Contains(validTypes, strategy.Type) {
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

	case authtypes.StrategyTypeUpstreamInject:
		if strategy.UpstreamInject == nil {
			return fmt.Errorf("upstream_inject requires UpstreamInject configuration")
		}
		// Note: empty ProviderName is allowed here; ValidateAuthServerIntegration
		// handles provider name resolution including the empty→"default" mapping.
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
	if !slices.Contains(validStrategies, agg.ConflictResolution) {
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

	// Validate health check timeout
	// Zero means no timeout (not recommended but valid)
	// Negative values are invalid
	healthCheckTimeout := time.Duration(fh.HealthCheckTimeout)
	if healthCheckTimeout < 0 {
		return fmt.Errorf("healthCheckTimeout must be >= 0 (zero means no timeout), got %v", healthCheckTimeout)
	}

	// If timeout is configured (non-zero), validate that it's less than interval
	if healthCheckTimeout > 0 {
		checkInterval := time.Duration(fh.HealthCheckInterval)

		// Validate that timeout is less than interval to prevent checks from queuing up
		if healthCheckTimeout >= checkInterval {
			return fmt.Errorf("healthCheckTimeout (%v) must be less than healthCheckInterval (%v) to prevent checks from queuing up",
				healthCheckTimeout, checkInterval)
		}
	}

	validModes := []string{"fail", "bestEffort"}
	if !slices.Contains(validModes, fh.PartialFailureMode) {
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

// InjectSubjectProviderNames auto-populates SubjectProviderName on every
// token_exchange strategy in cfg.OutgoingAuth that has it unset, when an
// embedded auth server RunConfig is active.
//
// This mirrors the operator's injectSubjectProviderIfNeeded helper and ensures
// YAML-based vMCP deployments receive the same automatic default: without it a
// token_exchange strategy with no SubjectProviderName would silently fall back to
// identity.Token (the ToolHive-issued JWT), which the exchange endpoint rejects.
//
// When rc is nil the config is returned unchanged. The provider name is resolved
// from the first upstream in rc.Upstreams (normalised via authserver.ResolveUpstreamName);
// if there are no upstreams it falls back to authserver.DefaultUpstreamName.
func InjectSubjectProviderNames(cfg *Config, rc *authserver.RunConfig) {
	if rc == nil || cfg.OutgoingAuth == nil {
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

// ValidateAuthServerIntegration validates cross-cutting rules between the
// embedded auth server configuration and backend auth strategies.
// This is called separately from Validate() because it needs the runtime-only
// auth server RunConfig that is not part of the serializable Config.
func ValidateAuthServerIntegration(cfg *Config, rc *authserver.RunConfig) error {
	strategies := collectAllBackendStrategies(cfg)
	hasUpstreamInject := hasStrategyType(strategies, authtypes.StrategyTypeUpstreamInject)

	// Guard clause: nothing to validate if no auth server and no upstream_inject backends.
	if rc == nil && !hasUpstreamInject {
		return nil
	}

	// upstream_inject requires an auth server to obtain upstream tokens.
	if hasUpstreamInject && rc == nil {
		return fmt.Errorf("upstream_inject requires an embedded auth server (authServer must be configured)")
	}

	// Structural validation of the auth server RunConfig.
	if err := validateAuthServerRunConfig(rc); err != nil {
		return err
	}

	// Auth server requires OIDC incoming auth to validate issued tokens.
	if err := validateAuthServerRequiresOIDC(cfg); err != nil {
		return err
	}

	// Every upstream_inject providerName must reference an existing upstream.
	if err := validateUpstreamInjectProviders(rc, strategies); err != nil {
		return err
	}

	// Issuer and audience consistency between auth server and incoming auth.
	return validateAuthServerIncomingAuthConsistency(cfg, rc)
}

// validateAuthServerRunConfig performs lightweight structural validation of the
// auth server RunConfig (issuer, upstreams, allowed audiences).
func validateAuthServerRunConfig(rc *authserver.RunConfig) error {
	if rc == nil {
		return nil
	}
	if rc.Issuer == "" {
		return fmt.Errorf("auth server issuer is required")
	}
	if len(rc.Upstreams) == 0 {
		return fmt.Errorf("auth server requires at least one upstream")
	}
	// AllowedAudiences is required for MCP compliance (RFC 8707).
	if len(rc.AllowedAudiences) == 0 {
		return fmt.Errorf("auth server requires at least one allowed audience (MCP clients must send RFC 8707 resource parameter)")
	}
	return nil
}

// validateUpstreamInjectProviders checks that every upstream_inject strategy
// references a provider that exists in the auth server upstreams.
func validateUpstreamInjectProviders(
	rc *authserver.RunConfig,
	strategies map[string]*authtypes.BackendAuthStrategy,
) error {
	if rc == nil {
		return nil
	}
	for name, strategy := range strategies {
		if strategy.Type != authtypes.StrategyTypeUpstreamInject || strategy.UpstreamInject == nil {
			continue
		}
		if !upstreamExists(rc, strategy.UpstreamInject.ProviderName) {
			return fmt.Errorf(
				"backend %q: upstream_inject providerName %q not found in auth server upstreams",
				name, strategy.UpstreamInject.ProviderName,
			)
		}
	}
	return nil
}

// validateAuthServerIncomingAuthConsistency checks that the embedded auth server
// and the incoming OIDC middleware agree on issuer and audience.
//
// This is a general consistency check that applies whenever both the embedded AS
// and OIDC incoming auth are configured, regardless of which outgoing backend
// strategies (upstream_inject, token_exchange, etc.) are in use.
//
// The embedded AS issues tokens that the OIDC incoming auth middleware validates.
// If these two components disagree on issuer or audience, the middleware will
// reject every token the AS issues, and no authenticated request will succeed.
func validateAuthServerIncomingAuthConsistency(cfg *Config, rc *authserver.RunConfig) error {
	if !hasAuthServerWithOIDCIncoming(cfg, rc) {
		return nil
	}
	oidc := cfg.IncomingAuth.OIDC

	// The OIDC middleware validates the "iss" claim against incomingAuth.oidc.issuer.
	// If the AS uses a different issuer, every token it issues will fail validation.
	if rc.Issuer != oidc.Issuer {
		return fmt.Errorf(
			"auth server issuer mismatch: auth server issuer %q != incomingAuth.oidc.issuer %q",
			rc.Issuer, oidc.Issuer,
		)
	}

	// The embedded AS uses the RFC 8707 resource parameter value as the
	// token's aud claim (identity mapping). AllowedAudiences gates which resource
	// values the AS accepts. If incomingAuth expects an audience not in that list,
	// the AS will never issue a matching token.
	// Note: oidc.Audience is required when incomingAuth.type is "oidc" (enforced
	// by validateIncomingAuth), so the empty check is defensive for callers that
	// invoke ValidateAuthServerIntegration independently.
	if oidc.Audience != "" && !slices.Contains(rc.AllowedAudiences, oidc.Audience) {
		return fmt.Errorf(
			"incomingAuth.oidc.audience %q not in auth server's allowed audiences %v",
			oidc.Audience, rc.AllowedAudiences,
		)
	}

	return nil
}

// hasOIDCIncoming reports whether the config has OIDC incoming auth fully configured.
func hasOIDCIncoming(cfg *Config) bool {
	return cfg.IncomingAuth != nil &&
		cfg.IncomingAuth.Type == IncomingAuthTypeOIDC &&
		cfg.IncomingAuth.OIDC != nil
}

// validateAuthServerRequiresOIDC checks that when the auth server is configured,
// incomingAuth is OIDC. The AS issues tokens that the OIDC middleware
// validates; without OIDC incoming auth the entire OAuth flow is pointless.
func validateAuthServerRequiresOIDC(cfg *Config) error {
	if !hasOIDCIncoming(cfg) {
		return fmt.Errorf("embedded auth server requires OIDC incoming auth")
	}
	return nil
}

// hasAuthServerWithOIDCIncoming returns true when both the auth server and
// incoming OIDC auth are configured, enabling cross-cutting validation.
func hasAuthServerWithOIDCIncoming(cfg *Config, rc *authserver.RunConfig) bool {
	return rc != nil && hasOIDCIncoming(cfg)
}

// collectAllBackendStrategies returns all backend auth strategies from the config.
func collectAllBackendStrategies(cfg *Config) map[string]*authtypes.BackendAuthStrategy {
	result := make(map[string]*authtypes.BackendAuthStrategy)
	if cfg.OutgoingAuth == nil {
		return result
	}
	if cfg.OutgoingAuth.Default != nil {
		result[defaultStrategyKey] = cfg.OutgoingAuth.Default
	}
	for name, strategy := range cfg.OutgoingAuth.Backends {
		result[name] = strategy
	}
	return result
}

// hasStrategyType checks if any strategy in the map uses the given type.
func hasStrategyType(strategies map[string]*authtypes.BackendAuthStrategy, strategyType string) bool {
	for _, s := range strategies {
		if s.Type == strategyType {
			return true
		}
	}
	return false
}

// upstreamExists checks if a provider name exists in the RunConfig's upstreams.
// Provider names and upstream names are resolved via authserver.ResolveUpstreamName
// before comparison to ensure consistent empty→"default" normalization.
func upstreamExists(rc *authserver.RunConfig, providerName string) bool {
	if rc == nil {
		return false
	}
	resolved := authserver.ResolveUpstreamName(providerName)
	for i := range rc.Upstreams {
		if authserver.ResolveUpstreamName(rc.Upstreams[i].Name) == resolved {
			return true
		}
	}
	return false
}
