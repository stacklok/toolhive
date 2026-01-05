package config

import (
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
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
		return fmt.Errorf("incoming_auth is required")
	}

	// Validate auth type
	validTypes := []string{"oidc", "local", "anonymous"}
	if !contains(validTypes, auth.Type) {
		return fmt.Errorf("incoming_auth.type must be one of: %s", strings.Join(validTypes, ", "))
	}

	// Validate OIDC configuration
	if auth.Type == "oidc" {
		if auth.OIDC == nil {
			return fmt.Errorf("incoming_auth.oidc is required when type is 'oidc'")
		}

		if auth.OIDC.Issuer == "" {
			return fmt.Errorf("incoming_auth.oidc.issuer is required")
		}

		if auth.OIDC.Audience == "" {
			return fmt.Errorf("incoming_auth.oidc.audience is required")
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
			return fmt.Errorf("incoming_auth.authz: %w", err)
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
		return fmt.Errorf("outgoing_auth is required")
	}

	// Validate source
	validSources := []string{"inline", "discovered"}
	if !contains(validSources, auth.Source) {
		return fmt.Errorf("outgoing_auth.source must be one of: %s", strings.Join(validSources, ", "))
	}

	// Validate default strategy
	if auth.Default != nil {
		if err := v.validateBackendAuthStrategy("default", auth.Default); err != nil {
			return fmt.Errorf("outgoing_auth.default: %w", err)
		}
	}

	// Validate per-backend strategies
	for backendName, strategy := range auth.Backends {
		if err := v.validateBackendAuthStrategy(backendName, strategy); err != nil {
			return fmt.Errorf("outgoing_auth.backends.%s: %w", backendName, err)
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
		// Token exchange requires TokenExchange config with token_url
		if strategy.TokenExchange == nil {
			return fmt.Errorf("token_exchange requires TokenExchange configuration")
		}
		if strategy.TokenExchange.TokenURL == "" {
			return fmt.Errorf("token_exchange requires token_url field")
		}

	case authtypes.StrategyTypeHeaderInjection:
		// Header injection requires HeaderInjection config with header name and value
		if strategy.HeaderInjection == nil {
			return fmt.Errorf("header_injection requires HeaderInjection configuration")
		}
		if strategy.HeaderInjection.HeaderName == "" {
			return fmt.Errorf("header_injection requires header_name field")
		}
		if strategy.HeaderInjection.HeaderValue == "" {
			return fmt.Errorf("header_injection requires header_value field")
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
		return fmt.Errorf("conflict_resolution must be one of: prefix, priority, manual")
	}

	// Validate strategy-specific configuration
	if agg.ConflictResolutionConfig == nil {
		return fmt.Errorf("conflict_resolution_config is required")
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
			return fmt.Errorf("prefix_format is required for prefix strategy")
		}

	case vmcp.ConflictStrategyPriority:
		if len(agg.ConflictResolutionConfig.PriorityOrder) == 0 {
			return fmt.Errorf("priority_order is required for priority strategy")
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
				return fmt.Errorf("operational.timeouts.per_workload.%s must be positive", workload)
			}
		}
	}

	// Validate failure handling
	if ops.FailureHandling != nil {
		if err := v.validateFailureHandling(ops.FailureHandling); err != nil {
			return fmt.Errorf("operational.failure_handling: %w", err)
		}
	}

	return nil
}

func (*DefaultValidator) validateFailureHandling(fh *FailureHandlingConfig) error {
	if fh.HealthCheckInterval <= 0 {
		return fmt.Errorf("health_check_interval must be positive")
	}

	if fh.UnhealthyThreshold <= 0 {
		return fmt.Errorf("unhealthy_threshold must be positive")
	}

	validModes := []string{vmcp.PartialFailureModeStrict, vmcp.PartialFailureModeLenient}
	if !contains(validModes, fh.PartialFailureMode) {
		return fmt.Errorf("partial_failure_mode must be one of: %s", strings.Join(validModes, ", "))
	}

	// Validate circuit breaker
	if fh.CircuitBreaker != nil && fh.CircuitBreaker.Enabled {
		if fh.CircuitBreaker.FailureThreshold <= 0 {
			return fmt.Errorf("circuit_breaker.failure_threshold must be positive")
		}
		if fh.CircuitBreaker.Timeout <= 0 {
			return fmt.Errorf("circuit_breaker.timeout must be positive")
		}
	}

	return nil
}

func (v *DefaultValidator) validateCompositeTools(tools []*CompositeToolConfig) error {
	if len(tools) == 0 {
		return nil // Composite tools are optional
	}

	toolNames := make(map[string]bool)

	for i, tool := range tools {
		// Validate basic fields
		if tool.Name == "" {
			return fmt.Errorf("composite_tools[%d].name is required", i)
		}

		if toolNames[tool.Name] {
			return fmt.Errorf("duplicate composite tool name: %s", tool.Name)
		}
		toolNames[tool.Name] = true

		if tool.Description == "" {
			return fmt.Errorf("composite_tools[%d].description is required", i)
		}

		// Timeout can be 0 (uses default) or positive (explicit timeout)
		if tool.Timeout < 0 {
			return fmt.Errorf("composite_tools[%d].timeout cannot be negative", i)
		}

		// Validate steps
		if len(tool.Steps) == 0 {
			return fmt.Errorf("composite_tools[%d] must have at least one step", i)
		}

		if err := v.validateWorkflowSteps(tool.Name, tool.Steps); err != nil {
			return fmt.Errorf("composite_tools[%d]: %w", i, err)
		}
	}

	return nil
}

func (v *DefaultValidator) validateWorkflowSteps(_ string, steps []*WorkflowStepConfig) error {
	stepIDs := make(map[string]bool)

	for i, step := range steps {
		if err := v.validateStepBasics(step, i, stepIDs); err != nil {
			return err
		}

		if err := v.validateStepType(step, i); err != nil {
			return err
		}

		if err := v.validateStepDependencies(step, i, stepIDs); err != nil {
			return err
		}

		if err := v.validateStepErrorHandling(step, i); err != nil {
			return err
		}
	}

	return nil
}

// validateStepBasics validates basic step requirements (ID uniqueness)
func (*DefaultValidator) validateStepBasics(step *WorkflowStepConfig, index int, stepIDs map[string]bool) error {
	if step.ID == "" {
		return fmt.Errorf("step[%d].id is required", index)
	}

	if stepIDs[step.ID] {
		return fmt.Errorf("duplicate step ID: %s", step.ID)
	}
	stepIDs[step.ID] = true

	return nil
}

// validateStepType validates step type and type-specific requirements.
// The type should have been inferred during loading if the 'tool' field is present.
// Elicitation steps must always specify type explicitly for clarity.
func (*DefaultValidator) validateStepType(step *WorkflowStepConfig, index int) error {
	// Check for ambiguous configuration: both tool and message fields present
	if step.Tool != "" && step.Message != "" {
		return fmt.Errorf("step[%d] cannot have both tool and message fields - use explicit type to clarify intent", index)
	}

	// Type is required at this point (should have been inferred during loading)
	if step.Type == "" {
		return fmt.Errorf("step[%d].type is required (or omit for tool steps with 'tool' field present)", index)
	}

	validTypes := []string{"tool", "elicitation"}
	if !contains(validTypes, step.Type) {
		return fmt.Errorf("step[%d].type must be one of: %s", index, strings.Join(validTypes, ", "))
	}

	switch step.Type {
	case "tool":
		if step.Tool == "" {
			return fmt.Errorf("step[%d].tool is required for tool steps", index)
		}

	case "elicitation":
		if step.Message == "" {
			return fmt.Errorf("step[%d].message is required for elicitation steps", index)
		}
		if len(step.Schema) == 0 {
			return fmt.Errorf("step[%d].schema is required for elicitation steps", index)
		}
		// Note: timeout validation is optional - defaults are set during loading
	}

	return nil
}

// validateStepDependencies validates step dependency references
func (*DefaultValidator) validateStepDependencies(step *WorkflowStepConfig, index int, stepIDs map[string]bool) error {
	for _, depID := range step.DependsOn {
		if !stepIDs[depID] {
			return fmt.Errorf("step[%d].depends_on references non-existent step: %s", index, depID)
		}
	}
	return nil
}

// validateStepErrorHandling validates step error handling configuration
func (*DefaultValidator) validateStepErrorHandling(step *WorkflowStepConfig, index int) error {
	if step.OnError == nil {
		return nil
	}

	validActions := []string{"abort", "continue", "retry"}
	if !contains(validActions, step.OnError.Action) {
		return fmt.Errorf("step[%d].on_error.action must be one of: %s", index, strings.Join(validActions, ", "))
	}

	if step.OnError.Action == "retry" && step.OnError.RetryCount <= 0 {
		return fmt.Errorf("step[%d].on_error.retry_count must be positive for retry action", index)
	}

	return nil
}

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
