// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

// Transport type constants for static backend configuration.
// These define the allowed network transport protocols for vMCP backends in static mode.
const (
	// TransportSSE is the Server-Sent Events transport protocol.
	TransportSSE = "sse"
	// TransportStreamableHTTP is the streamable HTTP transport protocol.
	TransportStreamableHTTP = "streamable-http"
)

// StaticModeAllowedTransports lists all transport types allowed for static backend configuration.
// This must be kept in sync with the CRD enum validation in StaticBackendConfig.Transport.
var StaticModeAllowedTransports = []string{TransportSSE, TransportStreamableHTTP}

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

	// Backends defines pre-configured backend servers for static mode.
	// When OutgoingAuth.Source is "inline", this field contains the full list of backend
	// servers with their URLs and transport types, eliminating the need for K8s API access.
	// When OutgoingAuth.Source is "discovered", this field is empty and backends are
	// discovered at runtime via Kubernetes API.
	// +optional
	Backends []StaticBackendConfig `json:"backends,omitempty" yaml:"backends,omitempty"`

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

	// Optimizer configures the MCP optimizer for context optimization on large toolsets.
	// When enabled, vMCP exposes only find_tool and call_tool operations to clients
	// instead of all backend tools directly. This reduces token usage by allowing
	// LLMs to discover relevant tools on demand rather than receiving all tool definitions.
	// +optional
	Optimizer *OptimizerConfig `json:"optimizer,omitempty" yaml:"optimizer,omitempty"`
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
	// +kubebuilder:validation:Pattern=`^https?://`
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

// StaticBackendConfig defines a pre-configured backend server for static mode.
// This allows vMCP to operate without Kubernetes API access by embedding all backend
// information directly in the configuration.
// +gendoc
// +kubebuilder:object:generate=true
type StaticBackendConfig struct {
	// Name is the backend identifier.
	// Must match the backend name from the MCPGroup for auth config resolution.
	// +kubebuilder:validation:Required
	Name string `json:"name" yaml:"name"`

	// URL is the backend's MCP server base URL.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://`
	URL string `json:"url" yaml:"url"`

	// Transport is the MCP transport protocol: "sse" or "streamable-http"
	// Only network transports supported by vMCP client are allowed.
	// +kubebuilder:validation:Enum=sse;streamable-http
	// +kubebuilder:validation:Required
	Transport string `json:"transport" yaml:"transport"`

	// Metadata is a custom key-value map for storing additional backend information
	// such as labels, tags, or other arbitrary data (e.g., "env": "prod", "region": "us-east-1").
	// This is NOT Kubernetes ObjectMeta - it's a simple string map for user-defined metadata.
	// Reserved keys: "group" is automatically set by vMCP and any user-provided value will be overridden.
	// +optional
	Metadata map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
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

// AggregationConfig defines tool aggregation, filtering, and conflict resolution strategies.
//
// Tool Visibility vs Routing:
//   - ExcludeAllTools, per-workload ExcludeAll, and Filter control which tools are
//     advertised to MCP clients (visible in tools/list responses).
//   - ALL backend tools remain available in the internal routing table, allowing
//     composite tools to call hidden backend tools.
//   - This enables curated experiences where raw backend tools are hidden from
//     MCP clients but accessible through composite tool workflows.
//
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

	// ExcludeAllTools hides all backend tools from MCP clients when true.
	// Hidden tools are NOT advertised in tools/list responses, but they ARE
	// available in the routing table for composite tools to use.
	// This enables the use case where you want to hide raw backend tools from
	// direct client access while exposing curated composite tool workflows.
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

	// Filter is an allow-list of tool names to advertise to MCP clients.
	// Tools NOT in this list are hidden from clients (not in tools/list response)
	// but remain available in the routing table for composite tools to use.
	// This enables selective exposure of backend tools while allowing composite
	// workflows to orchestrate all backend capabilities.
	// Only used if ToolConfigRef is not specified.
	// +optional
	Filter []string `json:"filter,omitempty" yaml:"filter,omitempty"`

	// Overrides is an inline map of tool overrides for renaming and description changes.
	// Overrides are applied to tools before conflict resolution and affect both
	// advertising and routing (the overridden name is used everywhere).
	// Only used if ToolConfigRef is not specified.
	// +optional
	Overrides map[string]*ToolOverride `json:"overrides,omitempty" yaml:"overrides,omitempty"`

	// ExcludeAll hides all tools from this workload from MCP clients when true.
	// Hidden tools are NOT advertised in tools/list responses, but they ARE
	// available in the routing table for composite tools to use.
	// This enables the use case where you want to hide raw backend tools from
	// direct client access while exposing curated composite tool workflows.
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
// OperationalConfig defines operational settings like timeouts and health checks.
// +kubebuilder:object:generate=true
// +gendoc
type OperationalConfig struct {
	// LogLevel sets the logging level for the Virtual MCP server.
	// The only valid value is "debug" to enable debug logging.
	// When omitted or empty, the server uses info level logging.
	// +kubebuilder:validation:Enum=debug
	// +optional
	LogLevel string `json:"logLevel,omitempty" yaml:"logLevel,omitempty"`

	// Timeouts configures timeout settings.
	// +optional
	Timeouts *TimeoutConfig `json:"timeouts,omitempty" yaml:"timeouts,omitempty"`

	// FailureHandling configures failure handling behavior.
	// +optional
	FailureHandling *FailureHandlingConfig `json:"failureHandling,omitempty" yaml:"failureHandling,omitempty"`
}

// TimeoutConfig configures timeout settings.
// +kubebuilder:object:generate=true
// +gendoc
type TimeoutConfig struct {
	// Default is the default timeout for backend requests.
	// +kubebuilder:default="30s"
	// +optional
	Default Duration `json:"default,omitempty" yaml:"default,omitempty"`

	// PerWorkload defines per-workload timeout overrides.
	// +optional
	PerWorkload map[string]Duration `json:"perWorkload,omitempty" yaml:"perWorkload,omitempty"`
}

// FailureHandlingConfig configures failure handling behavior.
// +kubebuilder:object:generate=true
// +gendoc
type FailureHandlingConfig struct {
	// HealthCheckInterval is the interval between health checks.
	// +kubebuilder:default="30s"
	// +optional
	HealthCheckInterval Duration `json:"healthCheckInterval,omitempty" yaml:"healthCheckInterval,omitempty"`

	// UnhealthyThreshold is the number of consecutive failures before marking unhealthy.
	// +kubebuilder:default=3
	// +optional
	UnhealthyThreshold int `json:"unhealthyThreshold,omitempty" yaml:"unhealthyThreshold,omitempty"`

	// HealthCheckTimeout is the maximum duration for a single health check operation.
	// Should be less than HealthCheckInterval to prevent checks from queuing up.
	// +kubebuilder:default="10s"
	// +optional
	HealthCheckTimeout Duration `json:"healthCheckTimeout,omitempty" yaml:"healthCheckTimeout,omitempty"`

	// StatusReportingInterval is the interval for reporting status updates to Kubernetes.
	// This controls how often the vMCP runtime reports backend health and phase changes.
	// Lower values provide faster status updates but increase API server load.
	// +kubebuilder:default="30s"
	// +optional
	StatusReportingInterval Duration `json:"statusReportingInterval,omitempty" yaml:"statusReportingInterval,omitempty"`

	// PartialFailureMode defines behavior when some backends are unavailable.
	// - fail: Fail entire request if any backend is unavailable
	// - best_effort: Continue with available backends
	// +kubebuilder:validation:Enum=fail;best_effort
	// +kubebuilder:default=fail
	// +optional
	PartialFailureMode string `json:"partialFailureMode,omitempty" yaml:"partialFailureMode,omitempty"`

	// CircuitBreaker configures circuit breaker behavior.
	// +optional
	CircuitBreaker *CircuitBreakerConfig `json:"circuitBreaker,omitempty" yaml:"circuitBreaker,omitempty"`
}

// CircuitBreakerConfig configures circuit breaker behavior.
// +kubebuilder:object:generate=true
// +gendoc
type CircuitBreakerConfig struct {
	// Enabled controls whether circuit breaker is enabled.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`

	// FailureThreshold is the number of failures before opening the circuit.
	// Must be >= 1.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	// +optional
	FailureThreshold int `json:"failureThreshold,omitempty" yaml:"failureThreshold,omitempty"`

	// Timeout is the duration to wait before attempting to close the circuit.
	// Must be >= 1s to prevent thrashing.
	// +kubebuilder:default="60s"
	// +kubebuilder:validation:XValidation:rule="self == '' || duration(self) >= duration('1s')",message="timeout must be >= 1s"
	// +optional
	Timeout Duration `json:"timeout,omitempty" yaml:"timeout,omitempty"`
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

// OptimizerConfig configures the MCP optimizer.
// When enabled, vMCP exposes only find_tool and call_tool operations to clients
// instead of all backend tools directly.
// +kubebuilder:object:generate=true
// +gendoc
type OptimizerConfig struct {
	// EmbeddingService is the name of a Kubernetes Service that provides the embedding service
	// for semantic tool discovery. The service must implement the optimizer embedding API.
	// +kubebuilder:validation:Required
	EmbeddingService string `json:"embeddingService" yaml:"embeddingService"`
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
