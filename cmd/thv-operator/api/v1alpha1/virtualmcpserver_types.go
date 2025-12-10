package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// VirtualMCPServerSpec defines the desired state of VirtualMCPServer
type VirtualMCPServerSpec struct {
	// GroupRef references an existing MCPGroup that defines backend workloads
	// The referenced MCPGroup must exist in the same namespace
	// +kubebuilder:validation:Required
	GroupRef GroupRef `json:"groupRef"`

	// IncomingAuth configures authentication for clients connecting to the Virtual MCP server
	// Must be explicitly set - use "anonymous" type when no authentication is required
	// +kubebuilder:validation:Required
	IncomingAuth *IncomingAuthConfig `json:"incomingAuth"`

	// OutgoingAuth configures authentication from Virtual MCP to backend MCPServers
	// +optional
	OutgoingAuth *OutgoingAuthConfig `json:"outgoingAuth,omitempty"`

	// Aggregation defines tool aggregation and conflict resolution strategies
	// +optional
	Aggregation *AggregationConfig `json:"aggregation,omitempty"`

	// CompositeTools defines inline composite tool definitions
	// For complex workflows, reference VirtualMCPCompositeToolDefinition resources instead
	// +optional
	CompositeTools []CompositeToolSpec `json:"compositeTools,omitempty"`

	// CompositeToolRefs references VirtualMCPCompositeToolDefinition resources
	// for complex, reusable workflows
	// +optional
	CompositeToolRefs []CompositeToolDefinitionRef `json:"compositeToolRefs,omitempty"`

	// Operational defines operational settings like timeouts and health checks
	// +optional
	Operational *OperationalConfig `json:"operational,omitempty"`

	// ServiceType specifies the Kubernetes service type for the Virtual MCP server
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	// +optional
	ServiceType string `json:"serviceType,omitempty"`

	// PodTemplateSpec defines the pod template to use for the Virtual MCP server
	// This allows for customizing the pod configuration beyond what is provided by the other fields.
	// Note that to modify the specific container the Virtual MCP server runs in, you must specify
	// the 'vmcp' container name in the PodTemplateSpec.
	// This field accepts a PodTemplateSpec object as JSON/YAML.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`

	// Telemetry configures OpenTelemetry-based observability for the Virtual MCP server
	// including distributed tracing, OTLP metrics export, and Prometheus metrics endpoint
	// +optional
	Telemetry *TelemetryConfig `json:"telemetry,omitempty"`
}

// GroupRef references an MCPGroup resource
type GroupRef struct {
	// Name is the name of the MCPGroup resource in the same namespace
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// IncomingAuthConfig configures authentication for clients connecting to the Virtual MCP server
type IncomingAuthConfig struct {
	// Type defines the authentication type: anonymous or oidc
	// When no authentication is required, explicitly set this to "anonymous"
	// +kubebuilder:validation:Enum=anonymous;oidc
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// OIDCConfig defines OIDC authentication configuration
	// Reuses MCPServer OIDC patterns
	// +optional
	OIDCConfig *OIDCConfigRef `json:"oidcConfig,omitempty"`

	// AuthzConfig defines authorization policy configuration
	// Reuses MCPServer authz patterns
	// +optional
	AuthzConfig *AuthzConfigRef `json:"authzConfig,omitempty"`
}

// OutgoingAuthConfig configures authentication from Virtual MCP to backend MCPServers
type OutgoingAuthConfig struct {
	// Source defines how backend authentication configurations are determined
	// - discovered: Automatically discover from backend's MCPServer.spec.externalAuthConfigRef
	// - inline: Explicit per-backend configuration in VirtualMCPServer
	// +kubebuilder:validation:Enum=discovered;inline
	// +kubebuilder:default=discovered
	// +optional
	Source string `json:"source,omitempty"`

	// Default defines default behavior for backends without explicit auth config
	// +optional
	Default *BackendAuthConfig `json:"default,omitempty"`

	// Backends defines per-backend authentication overrides
	// Works in all modes (discovered, inline)
	// +optional
	Backends map[string]BackendAuthConfig `json:"backends,omitempty"`
}

// BackendAuthConfig defines authentication configuration for a backend MCPServer
type BackendAuthConfig struct {
	// Type defines the authentication type
	// +kubebuilder:validation:Enum=discovered;external_auth_config_ref
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// ExternalAuthConfigRef references an MCPExternalAuthConfig resource
	// Only used when Type is "external_auth_config_ref"
	// +optional
	ExternalAuthConfigRef *ExternalAuthConfigRef `json:"externalAuthConfigRef,omitempty"`
}

// AggregationConfig defines tool aggregation and conflict resolution strategies
type AggregationConfig struct {
	// ConflictResolution defines the strategy for resolving tool name conflicts
	// - prefix: Automatically prefix tool names with workload identifier
	// - priority: First workload in priority order wins
	// - manual: Explicitly define overrides for all conflicts
	// +kubebuilder:validation:Enum=prefix;priority;manual
	// +kubebuilder:default=prefix
	// +optional
	ConflictResolution string `json:"conflictResolution,omitempty"`

	// ConflictResolutionConfig provides configuration for the chosen strategy
	// +optional
	ConflictResolutionConfig *ConflictResolutionConfig `json:"conflictResolutionConfig,omitempty"`

	// Tools defines per-workload tool filtering and overrides
	// References existing MCPToolConfig resources
	// +optional
	Tools []WorkloadToolConfig `json:"tools,omitempty"`
}

// ConflictResolutionConfig provides configuration for conflict resolution strategies
type ConflictResolutionConfig struct {
	// PrefixFormat defines the prefix format for the "prefix" strategy
	// Supports placeholders: {workload}, {workload}_, {workload}.
	// +kubebuilder:default="{workload}_"
	// +optional
	PrefixFormat string `json:"prefixFormat,omitempty"`

	// PriorityOrder defines the workload priority order for the "priority" strategy
	// +optional
	PriorityOrder []string `json:"priorityOrder,omitempty"`
}

// WorkloadToolConfig defines tool filtering and overrides for a specific workload
type WorkloadToolConfig struct {
	// Workload is the name of the backend MCPServer workload
	// +kubebuilder:validation:Required
	Workload string `json:"workload"`

	// ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming
	// If specified, Filter and Overrides are ignored
	// +optional
	ToolConfigRef *ToolConfigRef `json:"toolConfigRef,omitempty"`

	// Filter is an inline list of tool names to allow (allow list)
	// Only used if ToolConfigRef is not specified
	// +optional
	Filter []string `json:"filter,omitempty"`

	// Overrides is an inline map of tool overrides
	// Only used if ToolConfigRef is not specified
	// +optional
	Overrides map[string]ToolOverride `json:"overrides,omitempty"`
}

// CompositeToolSpec defines an inline composite tool
// For complex workflows, reference VirtualMCPCompositeToolDefinition resources instead
type CompositeToolSpec struct {
	// Name is the name of the composite tool
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Description describes the composite tool
	// +kubebuilder:validation:Required
	Description string `json:"description"`

	// Parameters defines the input parameter schema in JSON Schema format.
	// Should be a JSON Schema object with "type": "object" and "properties".
	// Per MCP specification, this should follow standard JSON Schema for tool inputSchema.
	// Example:
	//   {
	//     "type": "object",
	//     "properties": {
	//       "param1": {"type": "string", "default": "value"},
	//       "param2": {"type": "integer"}
	//     },
	//     "required": ["param2"]
	//   }
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Parameters *runtime.RawExtension `json:"parameters,omitempty"`

	// Steps defines the workflow steps
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Steps []WorkflowStep `json:"steps"`

	// Timeout is the maximum execution time for the composite tool
	// +kubebuilder:default="30m"
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// Output defines the structured output schema for the composite tool.
	// Specifies how to construct the final output from workflow step results.
	// If not specified, the workflow returns the last step's output (backward compatible).
	// +optional
	Output *OutputSpec `json:"output,omitempty"`
}

// OutputSpec defines the structured output schema for a composite tool workflow
type OutputSpec struct {
	// Properties defines the output properties
	// Map key is the property name, value is the property definition
	// +optional
	Properties map[string]OutputPropertySpec `json:"properties,omitempty"`

	// Required lists property names that must be present in the output
	// +optional
	Required []string `json:"required,omitempty"`
}

// OutputPropertySpec defines a single output property
type OutputPropertySpec struct {
	// Type is the JSON Schema type: "string", "integer", "number", "boolean", "object", "array"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=string;integer;number;boolean;object;array
	Type string `json:"type"`

	// Description is a human-readable description exposed to clients and models
	// +optional
	Description string `json:"description,omitempty"`

	// Value is a template string for constructing the runtime value
	// Supports template syntax: {{.steps.step_id.output.field}}, {{.params.param_name}}
	// For object types, this can be a JSON string that will be deserialized
	// +optional
	Value string `json:"value,omitempty"`

	// Properties defines nested properties for object types
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Properties map[string]OutputPropertySpec `json:"properties,omitempty"`

	// Default is the fallback value if template expansion fails
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Default *runtime.RawExtension `json:"default,omitempty"`
}

// WorkflowStep defines a step in a composite tool workflow
type WorkflowStep struct {
	// ID is the unique identifier for this step
	// +kubebuilder:validation:Required
	ID string `json:"id"`

	// Type is the step type (tool, elicitation, etc.)
	// +kubebuilder:validation:Enum=tool;elicitation
	// +kubebuilder:default=tool
	// +optional
	Type string `json:"type,omitempty"`

	// Tool is the tool to call (format: "workload.tool_name")
	// Only used when Type is "tool"
	// +optional
	Tool string `json:"tool,omitempty"`

	// Arguments is a map of argument values with template expansion support.
	// Supports Go template syntax with .params and .steps for string values.
	// Non-string values (integers, booleans, arrays, objects) are passed as-is.
	// Note: the templating is only supported on the first level of the key-value pairs.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Arguments *runtime.RawExtension `json:"arguments,omitempty"`

	// Message is the elicitation message
	// Only used when Type is "elicitation"
	// +optional
	Message string `json:"message,omitempty"`

	// Schema defines the expected response schema for elicitation
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Schema *runtime.RawExtension `json:"schema,omitempty"`

	// OnDecline defines the action to take when the user explicitly declines the elicitation
	// Only used when Type is "elicitation"
	// +optional
	OnDecline *ElicitationResponseHandler `json:"onDecline,omitempty"`

	// OnCancel defines the action to take when the user cancels/dismisses the elicitation
	// Only used when Type is "elicitation"
	// +optional
	OnCancel *ElicitationResponseHandler `json:"onCancel,omitempty"`

	// DependsOn lists step IDs that must complete before this step
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`

	// Condition is a template expression that determines if the step should execute
	// +optional
	Condition string `json:"condition,omitempty"`

	// OnError defines error handling behavior
	// +optional
	OnError *ErrorHandling `json:"onError,omitempty"`

	// Timeout is the maximum execution time for this step
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// ErrorHandling defines error handling behavior for workflow steps
type ErrorHandling struct {
	// Action defines the action to take on error
	// +kubebuilder:validation:Enum=abort;continue;retry
	// +kubebuilder:default=abort
	// +optional
	Action string `json:"action,omitempty"`

	// MaxRetries is the maximum number of retries
	// Only used when Action is "retry"
	// +optional
	MaxRetries int `json:"maxRetries,omitempty"`

	// RetryDelay is the delay between retry attempts
	// Only used when Action is "retry"
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m))+$`
	// +optional
	RetryDelay string `json:"retryDelay,omitempty"`
}

// ElicitationResponseHandler defines how to handle user responses to elicitation requests
type ElicitationResponseHandler struct {
	// Action defines the action to take when the user declines or cancels
	// - skip_remaining: Skip remaining steps in the workflow
	// - abort: Abort the entire workflow execution
	// - continue: Continue to the next step
	// +kubebuilder:validation:Enum=skip_remaining;abort;continue
	// +kubebuilder:default=abort
	// +optional
	Action string `json:"action,omitempty"`
}

// OperationalConfig defines operational settings
type OperationalConfig struct {
	// LogLevel sets the logging level for the Virtual MCP server.
	// Set to "debug" to enable debug logging. When not set, defaults to info level.
	// +kubebuilder:validation:Enum=debug
	// +optional
	LogLevel string `json:"logLevel,omitempty"`

	// Timeouts configures timeout settings
	// +optional
	Timeouts *TimeoutConfig `json:"timeouts,omitempty"`

	// FailureHandling configures failure handling behavior
	// +optional
	FailureHandling *FailureHandlingConfig `json:"failureHandling,omitempty"`
}

// TimeoutConfig configures timeout settings
type TimeoutConfig struct {
	// Default is the default timeout for backend requests
	// +kubebuilder:default="30s"
	// +optional
	Default string `json:"default,omitempty"`

	// PerWorkload defines per-workload timeout overrides
	// +optional
	PerWorkload map[string]string `json:"perWorkload,omitempty"`
}

// FailureHandlingConfig configures failure handling behavior
type FailureHandlingConfig struct {
	// HealthCheckInterval is the interval between health checks
	// +kubebuilder:default="30s"
	// +optional
	HealthCheckInterval string `json:"healthCheckInterval,omitempty"`

	// UnhealthyThreshold is the number of consecutive failures before marking unhealthy
	// +kubebuilder:default=3
	// +optional
	UnhealthyThreshold int `json:"unhealthyThreshold,omitempty"`

	// PartialFailureMode defines behavior when some backends are unavailable
	// - fail: Fail entire request if any backend is unavailable
	// - best_effort: Continue with available backends
	// +kubebuilder:validation:Enum=fail;best_effort
	// +kubebuilder:default=fail
	// +optional
	PartialFailureMode string `json:"partialFailureMode,omitempty"`

	// CircuitBreaker configures circuit breaker behavior
	// +optional
	CircuitBreaker *CircuitBreakerConfig `json:"circuitBreaker,omitempty"`
}

// CircuitBreakerConfig configures circuit breaker behavior
type CircuitBreakerConfig struct {
	// Enabled controls whether circuit breaker is enabled
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// FailureThreshold is the number of failures before opening the circuit
	// +kubebuilder:default=5
	// +optional
	FailureThreshold int `json:"failureThreshold,omitempty"`

	// Timeout is the duration to wait before attempting to close the circuit
	// +kubebuilder:default="60s"
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// Backend status constants for DiscoveredBackend.Status
const (
	BackendStatusReady       = "ready"
	BackendStatusUnavailable = "unavailable"
	BackendStatusDegraded    = "degraded"
	BackendStatusUnknown     = "unknown"
)

// DiscoveredBackend represents a discovered backend MCPServer in the MCPGroup
type DiscoveredBackend struct {
	// Name is the name of the backend MCPServer
	Name string `json:"name"`

	// AuthConfigRef is the name of the discovered MCPExternalAuthConfig (if any)
	// +optional
	AuthConfigRef string `json:"authConfigRef,omitempty"`

	// AuthType is the type of authentication configured
	// +optional
	AuthType string `json:"authType,omitempty"`

	// Status is the current status of the backend (ready, degraded, unavailable)
	// +optional
	Status string `json:"status,omitempty"`

	// LastHealthCheck is the timestamp of the last health check
	// +optional
	LastHealthCheck metav1.Time `json:"lastHealthCheck,omitempty"`

	// URL is the URL of the backend MCPServer
	// +optional
	URL string `json:"url,omitempty"`
}

// VirtualMCPServerStatus defines the observed state of VirtualMCPServer
type VirtualMCPServerStatus struct {
	// Conditions represent the latest available observations of the VirtualMCPServer's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed for this VirtualMCPServer
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is the current phase of the VirtualMCPServer
	// +optional
	// +kubebuilder:default=Pending
	Phase VirtualMCPServerPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// URL is the URL where the Virtual MCP server can be accessed
	// +optional
	URL string `json:"url,omitempty"`

	// DiscoveredBackends lists discovered backend configurations from the MCPGroup
	// +optional
	DiscoveredBackends []DiscoveredBackend `json:"discoveredBackends,omitempty"`

	// BackendCount is the number of discovered backends
	// +optional
	BackendCount int `json:"backendCount,omitempty"`
}

// VirtualMCPServerPhase represents the lifecycle phase of a VirtualMCPServer
// +kubebuilder:validation:Enum=Pending;Ready;Degraded;Failed
type VirtualMCPServerPhase string

const (
	// VirtualMCPServerPhasePending indicates the VirtualMCPServer is being initialized
	VirtualMCPServerPhasePending VirtualMCPServerPhase = "Pending"

	// VirtualMCPServerPhaseReady indicates the VirtualMCPServer is ready and serving requests
	VirtualMCPServerPhaseReady VirtualMCPServerPhase = "Ready"

	// VirtualMCPServerPhaseDegraded indicates the VirtualMCPServer is running but some backends are unavailable
	VirtualMCPServerPhaseDegraded VirtualMCPServerPhase = "Degraded"

	// VirtualMCPServerPhaseFailed indicates the VirtualMCPServer has failed
	VirtualMCPServerPhaseFailed VirtualMCPServerPhase = "Failed"
)

// Condition types for VirtualMCPServer
// Note: ConditionTypeAuthConfigured is shared with MCPRemoteProxy and defined in mcpremoteproxy_types.go
const (
	// ConditionTypeVirtualMCPServerReady indicates whether the VirtualMCPServer is ready
	ConditionTypeVirtualMCPServerReady = "Ready"

	// ConditionTypeVirtualMCPServerGroupRefValidated indicates whether the GroupRef is valid
	ConditionTypeVirtualMCPServerGroupRefValidated = "GroupRefValidated"

	// ConditionTypeCompositeToolRefsValidated indicates whether the CompositeToolRefs are valid
	ConditionTypeCompositeToolRefsValidated = "CompositeToolRefsValidated"
	// ConditionTypeVirtualMCPServerPodTemplateSpecValid indicates whether the PodTemplateSpec is valid
	ConditionTypeVirtualMCPServerPodTemplateSpecValid = "PodTemplateSpecValid"

	// ConditionTypeVirtualMCPServerBackendsDiscovered indicates whether backends have been discovered
	ConditionTypeVirtualMCPServerBackendsDiscovered = "BackendsDiscovered"
)

// Condition reasons for VirtualMCPServer
const (
	// ConditionReasonIncomingAuthValid indicates incoming auth is valid
	ConditionReasonIncomingAuthValid = "IncomingAuthValid"

	// ConditionReasonIncomingAuthInvalid indicates incoming auth is invalid
	ConditionReasonIncomingAuthInvalid = "IncomingAuthInvalid"

	// ConditionReasonGroupRefValid indicates the GroupRef is valid
	ConditionReasonVirtualMCPServerGroupRefValid = "GroupRefValid"

	// ConditionReasonGroupRefNotFound indicates the referenced MCPGroup was not found
	ConditionReasonVirtualMCPServerGroupRefNotFound = "GroupRefNotFound"

	// ConditionReasonGroupRefNotReady indicates the referenced MCPGroup is not ready
	ConditionReasonVirtualMCPServerGroupRefNotReady = "GroupRefNotReady"

	// ConditionReasonCompositeToolRefsValid indicates the CompositeToolRefs are valid
	ConditionReasonCompositeToolRefsValid = "CompositeToolRefsValid"

	// ConditionReasonCompositeToolRefNotFound indicates a referenced VirtualMCPCompositeToolDefinition was not found
	ConditionReasonCompositeToolRefNotFound = "CompositeToolRefNotFound"

	// ConditionReasonCompositeToolRefInvalid indicates a referenced VirtualMCPCompositeToolDefinition is invalid
	ConditionReasonCompositeToolRefInvalid = "CompositeToolRefInvalid"

	// ConditionReasonVirtualMCPServerPodTemplateSpecValid indicates PodTemplateSpec validation succeeded
	ConditionReasonVirtualMCPServerPodTemplateSpecValid = "PodTemplateSpecValid"

	// ConditionReasonVirtualMCPServerPodTemplateSpecInvalid indicates PodTemplateSpec validation failed
	ConditionReasonVirtualMCPServerPodTemplateSpecInvalid = "InvalidPodTemplateSpec"

	// ConditionReasonVirtualMCPServerBackendsDiscoveredSuccessfully indicates backends were discovered successfully
	ConditionReasonVirtualMCPServerBackendsDiscoveredSuccessfully = "BackendsDiscoveredSuccessfully"

	// ConditionReasonVirtualMCPServerBackendDiscoveryFailed indicates backend discovery failed
	ConditionReasonVirtualMCPServerBackendDiscoveryFailed = "BackendDiscoveryFailed"

	// ConditionReasonVirtualMCPServerDeploymentFailed indicates the deployment failed
	ConditionReasonVirtualMCPServerDeploymentFailed = "DeploymentFailed"

	// ConditionReasonVirtualMCPServerDeploymentReady indicates the deployment is ready
	ConditionReasonVirtualMCPServerDeploymentReady = "DeploymentReady"

	// ConditionReasonVirtualMCPServerDeploymentNotReady indicates the deployment is not ready
	ConditionReasonVirtualMCPServerDeploymentNotReady = "DeploymentNotReady"
)

// Backend authentication types
const (
	// BackendAuthTypeDiscovered automatically discovers from backend's externalAuthConfigRef
	BackendAuthTypeDiscovered = "discovered"

	// BackendAuthTypeExternalAuthConfigRef references an MCPExternalAuthConfig resource
	BackendAuthTypeExternalAuthConfigRef = "external_auth_config_ref"
)

// Conflict resolution strategies
const (
	// ConflictResolutionPrefix prefixes tool names with workload identifier
	ConflictResolutionPrefix = "prefix"

	// ConflictResolutionPriority uses priority order to resolve conflicts
	ConflictResolutionPriority = "priority"

	// ConflictResolutionManual requires explicit overrides for all conflicts
	ConflictResolutionManual = "manual"
)

// Workflow step types
const (
	// WorkflowStepTypeToolCall calls a backend tool
	WorkflowStepTypeToolCall = "tool"

	// WorkflowStepTypeElicitation requests user input
	WorkflowStepTypeElicitation = "elicitation"
)

// Error handling actions
const (
	// ErrorActionAbort aborts the workflow on error
	ErrorActionAbort = "abort"

	// ErrorActionContinue continues the workflow on error
	ErrorActionContinue = "continue"

	// ErrorActionRetry retries the step on error
	ErrorActionRetry = "retry"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=vmcp;virtualmcp
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The phase of the VirtualMCPServer"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url",description="Virtual MCP server URL"
//+kubebuilder:printcolumn:name="Backends",type="integer",JSONPath=".status.backendCount",description="Discovered backends count"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"

// VirtualMCPServer is the Schema for the virtualmcpservers API
// VirtualMCPServer aggregates multiple backend MCPServers into a unified endpoint
type VirtualMCPServer struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualMCPServerSpec   `json:"spec,omitempty"`
	Status VirtualMCPServerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VirtualMCPServerList contains a list of VirtualMCPServer
type VirtualMCPServerList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMCPServer `json:"items"`
}

// GetOIDCConfig returns the OIDC configuration reference for incoming auth.
// This implements the OIDCConfigurable interface to allow the OIDC resolver
// to resolve Kubernetes and ConfigMap OIDC configurations.
func (v *VirtualMCPServer) GetOIDCConfig() *OIDCConfigRef {
	if v.Spec.IncomingAuth == nil {
		return nil
	}
	return v.Spec.IncomingAuth.OIDCConfig
}

// GetProxyPort returns the proxy port for the VirtualMCPServer.
// This implements the OIDCConfigurable interface.
// vMCP uses port 4483 by default.
func (*VirtualMCPServer) GetProxyPort() int32 {
	return 4483
}

func init() {
	SchemeBuilder.Register(&VirtualMCPServer{}, &VirtualMCPServerList{})
}
