// Package config provides the configuration model for Virtual MCP Server.
//
// This package defines a platform-agnostic configuration model that works
// for both CLI (YAML) and Kubernetes (CRD) deployments. Platform-specific
// adapters transform their native formats into this unified model.
package config

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/stacklok/toolhive/pkg/audit"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// Duration is a wrapper around time.Duration that marshals/unmarshals as a duration string.
// This ensures duration values are serialized as "30s", "1m", etc. instead of nanosecond integers.
type Duration time.Duration

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	*d = Duration(dur)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (interface{}, error) {
	return time.Duration(d).String(), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	*d = Duration(dur)
	return nil
}

// Config is the unified configuration model for Virtual MCP Server.
// This is platform-agnostic and used by both CLI and Kubernetes deployments.
//
// Platform-specific adapters (CLI YAML loader, Kubernetes CRD converter)
// transform their native formats into this model.
// +kubebuilder:object:generate=true
// +kubebuilder:pruning:PreserveUnknownFields
// +kubebuilder:validation:Type=object
type Config struct {
	// Name is the virtual MCP server name.
	// +optional
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Group references an existing MCPGroup that defines backend workloads.
	// In Kubernetes, the referenced MCPGroup must exist in the same namespace.
	// +kubebuilder:validation:Required
	Group string `json:"groupRef" yaml:"groupRef"`

	// IncomingAuth configures how clients authenticate to the virtual MCP server.
	IncomingAuth *IncomingAuthConfig `json:"incomingAuth,omitempty" yaml:"incomingAuth,omitempty"`

	// OutgoingAuth configures how the virtual MCP server authenticates to backends.
	OutgoingAuth *OutgoingAuthConfig `json:"outgoingAuth,omitempty" yaml:"outgoingAuth,omitempty"`

	// Aggregation configures capability aggregation and conflict resolution.
	Aggregation *AggregationConfig `json:"aggregation,omitempty" yaml:"aggregation,omitempty"`

	// CompositeTools defines inline composite tool workflows.
	// Full workflow definitions are embedded in the configuration.
	// For Kubernetes, complex workflows can also reference VirtualMCPCompositeToolDefinition CRDs.
	CompositeTools []*CompositeToolConfig `json:"compositeTools,omitempty" yaml:"compositeTools,omitempty"`

	// Operational configures operational settings.
	Operational *OperationalConfig `json:"operational,omitempty" yaml:"operational,omitempty"`

	// Metadata stores additional configuration metadata.
	Metadata map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`

	// Telemetry configures OpenTelemetry-based observability for the Virtual MCP server
	// including distributed tracing, OTLP metrics export, and Prometheus metrics endpoint.
	// +optional
	Telemetry *telemetry.Config `json:"telemetry,omitempty" yaml:"telemetry,omitempty"`

	// Audit configures audit logging for the Virtual MCP server.
	// When present, audit logs include MCP protocol operations.
	// See audit.Config for available configuration options.
	// +optional
	Audit *audit.Config `json:"audit,omitempty" yaml:"audit,omitempty"`
}

// IncomingAuthConfig configures client authentication to the virtual MCP server.
// +kubebuilder:object:generate=true
type IncomingAuthConfig struct {
	// Type is the auth type: "oidc", "local", "anonymous"
	Type string `json:"type" yaml:"type"`

	// OIDC contains OIDC configuration (when Type = "oidc").
	OIDC *OIDCConfig `json:"oidc,omitempty" yaml:"oidc,omitempty"`

	// Authz contains authorization configuration (optional).
	Authz *AuthzConfig `json:"authz,omitempty" yaml:"authz,omitempty"`
}

// OIDCConfig configures OpenID Connect authentication.
// +kubebuilder:object:generate=true
type OIDCConfig struct {
	// Issuer is the OIDC issuer URL.
	Issuer string `json:"issuer" yaml:"issuer"`

	// ClientID is the OAuth client ID.
	ClientID string `json:"clientId" yaml:"clientId"`

	// ClientSecretEnv is the name of the environment variable containing the client secret.
	// This is the secure way to reference secrets - the actual secret value is never stored
	// in configuration files, only the environment variable name.
	// The secret value will be resolved from this environment variable at runtime.
	ClientSecretEnv string `json:"clientSecretEnv,omitempty" yaml:"clientSecretEnv,omitempty"`

	// Audience is the required token audience.
	Audience string `json:"audience" yaml:"audience"`

	// Resource is the OAuth 2.0 resource indicator (RFC 8707).
	// Used in WWW-Authenticate header and OAuth discovery metadata (RFC 9728).
	// If not specified, defaults to Audience.
	Resource string `json:"resource,omitempty" yaml:"resource,omitempty"`

	// Scopes are the required OAuth scopes.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`

	// ProtectedResourceAllowPrivateIP allows protected resource endpoint on private IP addresses
	// Use with caution - only enable for trusted internal IDPs or testing
	ProtectedResourceAllowPrivateIP bool `json:"protectedResourceAllowPrivateIp,omitempty" yaml:"protectedResourceAllowPrivateIp,omitempty"` //nolint:lll

	// InsecureAllowHTTP allows HTTP (non-HTTPS) OIDC issuers for development/testing
	// WARNING: This is insecure and should NEVER be used in production
	InsecureAllowHTTP bool `json:"insecureAllowHttp,omitempty" yaml:"insecureAllowHttp,omitempty"`
}

// AuthzConfig configures authorization.
// +kubebuilder:object:generate=true
type AuthzConfig struct {
	// Type is the authz type: "cedar", "none"
	Type string `json:"type" yaml:"type"`

	// Policies contains Cedar policy definitions (when Type = "cedar").
	Policies []string `json:"policies,omitempty" yaml:"policies,omitempty"`
}

// OutgoingAuthConfig configures backend authentication.
// +kubebuilder:object:generate=true
type OutgoingAuthConfig struct {
	// Source defines how to discover backend auth: "inline", "discovered"
	// - inline: Explicit configuration in OutgoingAuth
	// - discovered: Auto-discover from backend MCPServer.externalAuthConfigRef (Kubernetes only)
	Source string `json:"source" yaml:"source"`

	// Default is the default auth strategy for backends without explicit config.
	Default *authtypes.BackendAuthStrategy `json:"default,omitempty" yaml:"default,omitempty"`

	// Backends contains per-backend auth configuration.
	Backends map[string]*authtypes.BackendAuthStrategy `json:"backends,omitempty" yaml:"backends,omitempty"`
}

// ResolveForBackend returns the auth strategy for a given backend ID.
// It checks for backend-specific config first, then falls back to default.
// Returns nil if no authentication is configured.
func (c *OutgoingAuthConfig) ResolveForBackend(backendID string) *authtypes.BackendAuthStrategy {
	if c == nil {
		return nil
	}

	// Check for backend-specific configuration
	if strategy, exists := c.Backends[backendID]; exists && strategy != nil {
		return strategy
	}

	// Fall back to default configuration
	if c.Default != nil {
		return c.Default
	}

	// No authentication configured
	return nil
}

// AggregationConfig configures capability aggregation.
// +kubebuilder:object:generate=true
type AggregationConfig struct {
	// ConflictResolution is the strategy: "prefix", "priority", "manual"
	ConflictResolution vmcp.ConflictResolutionStrategy `json:"conflictResolution" yaml:"conflictResolution"`

	// ConflictResolutionConfig contains strategy-specific configuration.
	ConflictResolutionConfig *ConflictResolutionConfig `json:"conflictResolutionConfig,omitempty" yaml:"conflictResolutionConfig,omitempty"` //nolint:lll

	// Tools contains per-workload tool configuration.
	Tools []*WorkloadToolConfig `json:"tools,omitempty" yaml:"tools,omitempty"`

	ExcludeAllTools bool `json:"excludeAllTools,omitempty" yaml:"excludeAllTools,omitempty"`
}

// ConflictResolutionConfig contains conflict resolution settings.
// +kubebuilder:object:generate=true
type ConflictResolutionConfig struct {
	// PrefixFormat is the prefix format (for prefix strategy).
	// Options: "{workload}", "{workload}_", "{workload}.", custom string
	PrefixFormat string `json:"prefixFormat,omitempty" yaml:"prefixFormat,omitempty"`

	// PriorityOrder is the explicit priority ordering (for priority strategy).
	PriorityOrder []string `json:"priorityOrder,omitempty" yaml:"priorityOrder,omitempty"`
}

// WorkloadToolConfig configures tool filtering/overrides for a workload.
// +kubebuilder:object:generate=true
type WorkloadToolConfig struct {
	// Workload is the workload name/ID.
	Workload string `json:"workload" yaml:"workload"`

	// Filter is the list of tools to include (nil = include all).
	Filter []string `json:"filter,omitempty" yaml:"filter,omitempty"`

	// Overrides maps tool names to override configurations.
	Overrides map[string]*ToolOverride `json:"overrides,omitempty" yaml:"overrides,omitempty"`

	ExcludeAll bool `json:"excludeAll,omitempty" yaml:"excludeAll,omitempty"`
}

// ToolOverride defines tool name/description overrides.
// +kubebuilder:object:generate=true
type ToolOverride struct {
	// Name is the new tool name (for renaming).
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Description is the new tool description (for updating).
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// OperationalConfig contains operational settings.
// +kubebuilder:object:generate=true
type OperationalConfig struct {
	// Timeouts configures request timeouts.
	Timeouts *TimeoutConfig `json:"timeouts,omitempty" yaml:"timeouts,omitempty"`

	// FailureHandling configures failure handling.
	FailureHandling *FailureHandlingConfig `json:"failureHandling,omitempty" yaml:"failureHandling,omitempty"`
}

// TimeoutConfig configures timeouts.
// +kubebuilder:object:generate=true
type TimeoutConfig struct {
	// Default is the default timeout for backend requests.
	Default Duration `json:"default" yaml:"default"`

	// PerWorkload contains per-workload timeout overrides.
	PerWorkload map[string]Duration `json:"perWorkload,omitempty" yaml:"perWorkload,omitempty"`
}

// FailureHandlingConfig configures failure handling.
// +kubebuilder:object:generate=true
type FailureHandlingConfig struct {
	// HealthCheckInterval is how often to check backend health.
	HealthCheckInterval Duration `json:"healthCheckInterval" yaml:"healthCheckInterval"`

	// UnhealthyThreshold is how many failures before marking unhealthy.
	UnhealthyThreshold int `json:"unhealthyThreshold" yaml:"unhealthyThreshold"`

	// PartialFailureMode defines behavior when some backends fail.
	// Options: "fail" (fail entire request), "best_effort" (return partial results)
	PartialFailureMode string `json:"partialFailureMode" yaml:"partialFailureMode"`

	// CircuitBreaker configures circuit breaker settings.
	CircuitBreaker *CircuitBreakerConfig `json:"circuitBreaker,omitempty" yaml:"circuitBreaker,omitempty"`
}

// CircuitBreakerConfig configures circuit breaker.
// +kubebuilder:object:generate=true
type CircuitBreakerConfig struct {
	// Enabled indicates if circuit breaker is enabled.
	Enabled bool `json:"enabled" yaml:"enabled"`

	// FailureThreshold is how many failures trigger open circuit.
	FailureThreshold int `json:"failureThreshold" yaml:"failureThreshold"`

	// Timeout is how long to keep circuit open.
	Timeout Duration `json:"timeout" yaml:"timeout"`
}

// CompositeToolConfig defines a composite tool workflow.
// This matches the YAML structure from the proposal (lines 173-255).
// +kubebuilder:object:generate=true
type CompositeToolConfig struct {
	// Name is the workflow name (unique identifier).
	Name string `json:"name" yaml:"name"`

	// Description describes what the workflow does.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Parameters defines input parameter schema in JSON Schema format.
	// Should be a JSON Schema object with "type": "object" and "properties".
	// Example:
	//   {
	//     "type": "object",
	//     "properties": {
	//       "param1": {"type": "string", "default": "value"},
	//       "param2": {"type": "integer"}
	//     },
	//     "required": ["param2"]
	//   }
	//
	// We use json.Map rather than a typed struct because JSON Schema is highly
	// flexible with many optional fields (default, enum, minimum, maximum, pattern,
	// items, additionalProperties, oneOf, anyOf, allOf, etc.). Using json.Map
	// allows full JSON Schema compatibility without needing to define every possible
	// field, and matches how the MCP SDK handles inputSchema.
	// +optional
	Parameters thvjson.Map `json:"parameters,omitempty" yaml:"parameters,omitempty"`

	// Timeout is the maximum workflow execution time.
	Timeout Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	// Steps are the workflow steps to execute.
	Steps []*WorkflowStepConfig `json:"steps" yaml:"steps"`

	// Output defines the structured output schema for this workflow.
	// If not specified, the workflow returns the last step's output (backward compatible).
	// +optional
	Output *OutputConfig `json:"output,omitempty" yaml:"output,omitempty"`
}

// WorkflowStepConfig defines a single workflow step.
// This matches the proposal's step configuration (lines 180-255).
// +kubebuilder:object:generate=true
type WorkflowStepConfig struct {
	// ID uniquely identifies this step.
	ID string `json:"id" yaml:"id"`

	// Type is the step type: "tool", "elicitation"
	Type string `json:"type" yaml:"type"`

	// Tool is the tool name to call (for tool steps).
	Tool string `json:"tool,omitempty" yaml:"tool,omitempty"`

	// Arguments are the tool arguments (supports template expansion).
	// +optional
	Arguments thvjson.Map `json:"arguments,omitempty" yaml:"arguments,omitempty"`

	// Condition is an optional execution condition (template syntax).
	Condition string `json:"condition,omitempty" yaml:"condition,omitempty"`

	// DependsOn lists step IDs that must complete first (for DAG execution).
	DependsOn []string `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`

	// OnError defines error handling for this step.
	OnError *StepErrorHandling `json:"onError,omitempty" yaml:"onError,omitempty"`

	// Elicitation config (for elicitation steps).
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
	// +optional
	Schema  thvjson.Map `json:"schema,omitempty" yaml:"schema,omitempty"`
	Timeout Duration    `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	// Elicitation response handlers.
	OnDecline *ElicitationResponseConfig `json:"onDecline,omitempty" yaml:"onDecline,omitempty"`
	OnCancel  *ElicitationResponseConfig `json:"onCancel,omitempty" yaml:"onCancel,omitempty"`

	// DefaultResults provides fallback output values when this step is skipped
	// (due to condition evaluating to false) or fails (when onError.action is "continue").
	// Each key corresponds to an output field name referenced by downstream steps.
	// +optional
	DefaultResults thvjson.Map `json:"defaultResults,omitempty" yaml:"defaultResults,omitempty"`
}

// StepErrorHandling defines error handling for a workflow step.
// +kubebuilder:object:generate=true
type StepErrorHandling struct {
	// Action: "abort", "continue", "retry"
	Action string `json:"action" yaml:"action"`

	// RetryCount is the number of retry attempts (for retry action).
	RetryCount int `json:"retryCount,omitempty" yaml:"retryCount,omitempty"`

	// RetryDelay is the initial delay between retries.
	RetryDelay Duration `json:"retryDelay,omitempty" yaml:"retryDelay,omitempty"`
}

// ElicitationResponseConfig defines how to handle elicitation responses.
// +kubebuilder:object:generate=true
type ElicitationResponseConfig struct {
	// Action: "skip_remaining", "abort", "continue"
	Action string `json:"action" yaml:"action"`
}

// OutputConfig defines the structured output schema for a composite tool workflow.
// This follows the same pattern as the Parameters field, defining both the
// MCP output schema (type, description) and runtime value construction (value, default).
// +kubebuilder:object:generate=true
type OutputConfig struct {
	// Properties defines the output properties.
	// Map key is the property name, value is the property definition.
	Properties map[string]OutputProperty `json:"properties" yaml:"properties"`

	// Required lists property names that must be present in the output.
	// +optional
	Required []string `json:"required,omitempty" yaml:"required,omitempty"`
}

// OutputProperty defines a single output property.
// For non-object types, Value is required.
// For object types, either Value or Properties must be specified (but not both).
// +kubebuilder:object:generate=true
type OutputProperty struct {
	// Type is the JSON Schema type: "string", "integer", "number", "boolean", "object", "array".
	Type string `json:"type" yaml:"type"`

	// Description is a human-readable description exposed to clients and models.
	Description string `json:"description" yaml:"description"`

	// Value is a template string for constructing the runtime value.
	// For object types, this can be a JSON string that will be deserialized.
	// Supports template syntax: {{.steps.step_id.output.field}}, {{.params.param_name}}
	// +optional
	Value string `json:"value,omitempty" yaml:"value,omitempty"`

	// Properties defines nested properties for object types.
	// Each nested property has full metadata (type, description, value/properties).
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +kubebuilder:validation:Schemaless
	Properties map[string]OutputProperty `json:"properties,omitempty" yaml:"properties,omitempty"`

	// Default is the fallback value if template expansion fails.
	// Type coercion is applied to match the declared Type.
	// +optional
	Default thvjson.Any `json:"default,omitempty" yaml:"default,omitempty"`
}

// Validator validates configuration.
type Validator interface {
	// Validate checks if the configuration is valid.
	// Returns detailed validation errors.
	Validate(cfg *Config) error
}

// Loader loads configuration from a source.
type Loader interface {
	// Load loads configuration from the source.
	Load() (*Config, error)
}
