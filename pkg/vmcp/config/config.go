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
// +kubebuilder:validation:Type=string
// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
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
// +gendoc
type Config struct {
	// Name is the virtual MCP server name.
	// +optional
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Group references an existing MCPGroup that defines backend workloads.
	// In Kubernetes, the referenced MCPGroup must exist in the same namespace.
	// +kubebuilder:validation:Required
	Group string `json:"groupRef" yaml:"groupRef"`

	// IncomingAuth configures how clients authenticate to the virtual MCP server.
	// When using the Kubernetes operator, this is populated by the converter from
	// VirtualMCPServerSpec.IncomingAuth and any values set here will be superseded.
	// +optional
	IncomingAuth *IncomingAuthConfig `json:"incomingAuth,omitempty" yaml:"incomingAuth,omitempty"`

	// OutgoingAuth configures how the virtual MCP server authenticates to backends.
	// When using the Kubernetes operator, this is populated by the converter from
	// VirtualMCPServerSpec.OutgoingAuth and any values set here will be superseded.
	// +optional
	OutgoingAuth *OutgoingAuthConfig `json:"outgoingAuth,omitempty" yaml:"outgoingAuth,omitempty"`

	// Aggregation defines tool aggregation and conflict resolution strategies.
	// Supports ToolConfigRef for Kubernetes-native MCPToolConfig resource references.
	// +optional
	Aggregation *AggregationConfig `json:"aggregation,omitempty" yaml:"aggregation,omitempty"`

	// CompositeTools defines inline composite tool workflows.
	// Full workflow definitions are embedded in the configuration.
	// For Kubernetes, complex workflows can also reference VirtualMCPCompositeToolDefinition CRDs.
	// +optional
	CompositeTools []CompositeToolConfig `json:"compositeTools,omitempty" yaml:"compositeTools,omitempty"`

	// CompositeToolRefs references VirtualMCPCompositeToolDefinition resources
	// for complex, reusable workflows. Only applicable when running in Kubernetes.
	// Referenced resources must be in the same namespace as the VirtualMCPServer.
	// +optional
	CompositeToolRefs []CompositeToolRef `json:"compositeToolRefs,omitempty" yaml:"compositeToolRefs,omitempty"`

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
//
// Note: When using the Kubernetes operator (VirtualMCPServer CRD), the
// VirtualMCPServerSpec.IncomingAuth field is the authoritative source for
// authentication configuration. The operator's converter will resolve the CRD's
// IncomingAuth (which supports Kubernetes-native references like SecretKeyRef,
// ConfigMapRef, etc.) and populate this IncomingAuthConfig with the resolved values.
// Any values set here directly will be superseded by the CRD configuration.
//
// +kubebuilder:object:generate=true
// +gendoc
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
// +gendoc
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
// +gendoc
type AuthzConfig struct {
	// Type is the authz type: "cedar", "none"
	Type string `json:"type" yaml:"type"`

	// Policies contains Cedar policy definitions (when Type = "cedar").
	Policies []string `json:"policies,omitempty" yaml:"policies,omitempty"`
}

// OutgoingAuthConfig configures backend authentication.
//
// Note: When using the Kubernetes operator (VirtualMCPServer CRD), the
// VirtualMCPServerSpec.OutgoingAuth field is the authoritative source for
// backend authentication configuration. The operator's converter will resolve
// the CRD's OutgoingAuth (which supports Kubernetes-native references like
// SecretKeyRef, ConfigMapRef, etc.) and populate this OutgoingAuthConfig with
// the resolved values. Any values set here directly will be superseded by the
// CRD configuration.
//
// +kubebuilder:object:generate=true
// +gendoc
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

// AggregationConfig defines tool aggregation and conflict resolution strategies.
// +kubebuilder:object:generate=true
// +gendoc
type AggregationConfig struct {
	// ConflictResolution defines the strategy for resolving tool name conflicts.
	// - prefix: Automatically prefix tool names with workload identifier
	// - priority: First workload in priority order wins
	// - manual: Explicitly define overrides for all conflicts
	// +kubebuilder:validation:Enum=prefix;priority;manual
	// +kubebuilder:default=prefix
	// +optional
	ConflictResolution vmcp.ConflictResolutionStrategy `json:"conflictResolution" yaml:"conflictResolution"`

	// ConflictResolutionConfig provides configuration for the chosen strategy.
	// +optional
	ConflictResolutionConfig *ConflictResolutionConfig `json:"conflictResolutionConfig,omitempty" yaml:"conflictResolutionConfig,omitempty"` //nolint:lll

	// Tools defines per-workload tool filtering and overrides.
	// +optional
	Tools []*WorkloadToolConfig `json:"tools,omitempty" yaml:"tools,omitempty"`

	// ExcludeAllTools excludes all tools from aggregation when true.
	// +optional
	ExcludeAllTools bool `json:"excludeAllTools,omitempty" yaml:"excludeAllTools,omitempty"`
}

// ConflictResolutionConfig provides configuration for conflict resolution strategies.
// +kubebuilder:object:generate=true
// +gendoc
type ConflictResolutionConfig struct {
	// PrefixFormat defines the prefix format for the "prefix" strategy.
	// Supports placeholders: {workload}, {workload}_, {workload}.
	// +kubebuilder:default="{workload}_"
	// +optional
	PrefixFormat string `json:"prefixFormat,omitempty" yaml:"prefixFormat,omitempty"`

	// PriorityOrder defines the workload priority order for the "priority" strategy.
	// +optional
	PriorityOrder []string `json:"priorityOrder,omitempty" yaml:"priorityOrder,omitempty"`
}

// WorkloadToolConfig defines tool filtering and overrides for a specific workload.
// +kubebuilder:object:generate=true
// +gendoc
type WorkloadToolConfig struct {
	// Workload is the name of the backend MCPServer workload.
	// +kubebuilder:validation:Required
	Workload string `json:"workload" yaml:"workload"`

	// ToolConfigRef references an MCPToolConfig resource for tool filtering and renaming.
	// If specified, Filter and Overrides are ignored.
	// Only used when running in Kubernetes with the operator.
	// +optional
	ToolConfigRef *ToolConfigRef `json:"toolConfigRef,omitempty" yaml:"toolConfigRef,omitempty"`

	// Filter is an inline list of tool names to allow (allow list).
	// Only used if ToolConfigRef is not specified.
	// +optional
	Filter []string `json:"filter,omitempty" yaml:"filter,omitempty"`

	// Overrides is an inline map of tool overrides.
	// Only used if ToolConfigRef is not specified.
	// +optional
	Overrides map[string]*ToolOverride `json:"overrides,omitempty" yaml:"overrides,omitempty"`

	// ExcludeAll excludes all tools from this workload when true.
	// +optional
	ExcludeAll bool `json:"excludeAll,omitempty" yaml:"excludeAll,omitempty"`
}

// ToolConfigRef references an MCPToolConfig resource for tool filtering and renaming.
// Only used when running in Kubernetes with the operator.
// +kubebuilder:object:generate=true
// +gendoc
type ToolConfigRef struct {
	// Name is the name of the MCPToolConfig resource in the same namespace.
	// +kubebuilder:validation:Required
	Name string `json:"name" yaml:"name"`
}

// ToolOverride defines tool name and description overrides.
// +kubebuilder:object:generate=true
// +gendoc
type ToolOverride struct {
	// Name is the new tool name (for renaming).
	// +optional
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Description is the new tool description.
	// +optional
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// OperationalConfig contains operational settings.
// +kubebuilder:object:generate=true
// +gendoc
type OperationalConfig struct {
	// Timeouts configures request timeouts.
	Timeouts *TimeoutConfig `json:"timeouts,omitempty" yaml:"timeouts,omitempty"`

	// FailureHandling configures failure handling.
	FailureHandling *FailureHandlingConfig `json:"failureHandling,omitempty" yaml:"failureHandling,omitempty"`
}

// TimeoutConfig configures timeouts.
// +kubebuilder:object:generate=true
// +gendoc
type TimeoutConfig struct {
	// Default is the default timeout for backend requests.
	Default Duration `json:"default" yaml:"default"`

	// PerWorkload contains per-workload timeout overrides.
	PerWorkload map[string]Duration `json:"perWorkload,omitempty" yaml:"perWorkload,omitempty"`
}

// FailureHandlingConfig configures failure handling.
// +kubebuilder:object:generate=true
// +gendoc
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
// +gendoc
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
// +gendoc
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
	Steps []WorkflowStepConfig `json:"steps" yaml:"steps"`

	// Output defines the structured output schema for this workflow.
	// If not specified, the workflow returns the last step's output (backward compatible).
	// +optional
	Output *OutputConfig `json:"output,omitempty" yaml:"output,omitempty"`
}

// CompositeToolRef defines a reference to a VirtualMCPCompositeToolDefinition resource.
// The referenced resource must be in the same namespace as the VirtualMCPServer.
// +kubebuilder:object:generate=true
// +gendoc
type CompositeToolRef struct {
	// Name is the name of the VirtualMCPCompositeToolDefinition resource in the same namespace.
	// +kubebuilder:validation:Required
	Name string `json:"name" yaml:"name"`
}

// WorkflowStepConfig defines a single workflow step.
// This matches the proposal's step configuration (lines 180-255).
// +kubebuilder:object:generate=true
// +gendoc
type WorkflowStepConfig struct {
	// ID is the unique identifier for this step.
	// +kubebuilder:validation:Required
	ID string `json:"id" yaml:"id"`

	// Type is the step type (tool, elicitation, etc.)
	// +kubebuilder:validation:Enum=tool;elicitation
	// +kubebuilder:default=tool
	// +optional
	Type string `json:"type,omitempty" yaml:"type,omitempty"`

	// Tool is the tool to call (format: "workload.tool_name")
	// Only used when Type is "tool"
	// +optional
	Tool string `json:"tool,omitempty" yaml:"tool,omitempty"`

	// Arguments is a map of argument values with template expansion support.
	// Supports Go template syntax with .params and .steps for string values.
	// Non-string values (integers, booleans, arrays, objects) are passed as-is.
	// Note: the templating is only supported on the first level of the key-value pairs.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Arguments thvjson.Map `json:"arguments,omitempty" yaml:"arguments,omitempty"`

	// Condition is a template expression that determines if the step should execute
	// +optional
	Condition string `json:"condition,omitempty" yaml:"condition,omitempty"`

	// DependsOn lists step IDs that must complete before this step
	// +optional
	DependsOn []string `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`

	// OnError defines error handling behavior
	// +optional
	OnError *StepErrorHandling `json:"onError,omitempty" yaml:"onError,omitempty"`

	// Message is the elicitation message
	// Only used when Type is "elicitation"
	// +optional
	Message string `json:"message,omitempty" yaml:"message,omitempty"`

	// Schema defines the expected response schema for elicitation
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Schema thvjson.Map `json:"schema,omitempty" yaml:"schema,omitempty"`

	// Timeout is the maximum execution time for this step
	// +optional
	Timeout Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	// OnDecline defines the action to take when the user explicitly declines the elicitation
	// Only used when Type is "elicitation"
	// +optional
	OnDecline *ElicitationResponseConfig `json:"onDecline,omitempty" yaml:"onDecline,omitempty"`

	// OnCancel defines the action to take when the user cancels/dismisses the elicitation
	// Only used when Type is "elicitation"
	// +optional
	OnCancel *ElicitationResponseConfig `json:"onCancel,omitempty" yaml:"onCancel,omitempty"`

	// DefaultResults provides fallback output values when this step is skipped
	// (due to condition evaluating to false) or fails (when onError.action is "continue").
	// Each key corresponds to an output field name referenced by downstream steps.
	// Required if the step may be skipped AND downstream steps reference this step's output.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	DefaultResults thvjson.Map `json:"defaultResults,omitempty" yaml:"defaultResults,omitempty"`
}

// StepErrorHandling defines error handling behavior for workflow steps.
// +kubebuilder:object:generate=true
// +gendoc
type StepErrorHandling struct {
	// Action defines the action to take on error
	// +kubebuilder:validation:Enum=abort;continue;retry
	// +kubebuilder:default=abort
	// +optional
	Action string `json:"action,omitempty" yaml:"action,omitempty"`

	// RetryCount is the maximum number of retries
	// Only used when Action is "retry"
	// +optional
	RetryCount int `json:"retryCount,omitempty" yaml:"retryCount,omitempty"`

	// RetryDelay is the delay between retry attempts
	// Only used when Action is "retry"
	// +optional
	RetryDelay Duration `json:"retryDelay,omitempty" yaml:"retryDelay,omitempty"`
}

// ElicitationResponseConfig defines how to handle user responses to elicitation requests.
// +kubebuilder:object:generate=true
// +gendoc
type ElicitationResponseConfig struct {
	// Action defines the action to take when the user declines or cancels
	// - skip_remaining: Skip remaining steps in the workflow
	// - abort: Abort the entire workflow execution
	// - continue: Continue to the next step
	// +kubebuilder:validation:Enum=skip_remaining;abort;continue
	// +kubebuilder:default=abort
	// +optional
	Action string `json:"action,omitempty" yaml:"action,omitempty"`
}

// OutputConfig defines the structured output schema for a composite tool workflow.
// This follows the same pattern as the Parameters field, defining both the
// MCP output schema (type, description) and runtime value construction (value, default).
// +kubebuilder:object:generate=true
// +gendoc
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
// +gendoc
type OutputProperty struct {
	// Type is the JSON Schema type: "string", "integer", "number", "boolean", "object", "array"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=string;integer;number;boolean;object;array
	Type string `json:"type" yaml:"type"`

	// Description is a human-readable description exposed to clients and models
	// +optional
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
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
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
