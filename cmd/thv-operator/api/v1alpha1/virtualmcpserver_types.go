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
	// +optional
	IncomingAuth *IncomingAuthConfig `json:"incomingAuth,omitempty"`

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

	// TokenCache configures token caching behavior
	// +optional
	TokenCache *TokenCacheConfig `json:"tokenCache,omitempty"`

	// Operational defines operational settings like timeouts and health checks
	// +optional
	Operational *OperationalConfig `json:"operational,omitempty"`

	// PodTemplateSpec defines the pod template to use for the Virtual MCP server
	// This allows for customizing the pod configuration beyond what is provided by the other fields.
	// Note that to modify the specific container the Virtual MCP server runs in, you must specify
	// the 'vmcp' container name in the PodTemplateSpec.
	// This field accepts a PodTemplateSpec object as JSON/YAML.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`
}

// GroupRef references an MCPGroup resource
type GroupRef struct {
	// Name is the name of the MCPGroup resource in the same namespace
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// IncomingAuthConfig configures authentication for clients connecting to the Virtual MCP server
type IncomingAuthConfig struct {
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
	// - mixed: Discover most, override specific backends
	// +kubebuilder:validation:Enum=discovered;inline;mixed
	// +kubebuilder:default=discovered
	// +optional
	Source string `json:"source,omitempty"`

	// Default defines default behavior for backends without explicit auth config
	// +optional
	Default *BackendAuthConfig `json:"default,omitempty"`

	// Backends defines per-backend authentication overrides
	// Works in all modes (discovered, inline, mixed)
	// +optional
	Backends map[string]BackendAuthConfig `json:"backends,omitempty"`
}

// BackendAuthConfig defines authentication configuration for a backend MCPServer
type BackendAuthConfig struct {
	// Type defines the authentication type
	// +kubebuilder:validation:Enum=discovered;pass_through;service_account;external_auth_config_ref
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// ServiceAccount configures service account authentication
	// Only used when Type is "service_account"
	// +optional
	ServiceAccount *ServiceAccountAuth `json:"serviceAccount,omitempty"`

	// ExternalAuthConfigRef references an MCPExternalAuthConfig resource
	// Only used when Type is "external_auth_config_ref"
	// +optional
	ExternalAuthConfigRef *ExternalAuthConfigRef `json:"externalAuthConfigRef,omitempty"`
}

// ServiceAccountAuth defines service account authentication
type ServiceAccountAuth struct {
	// CredentialsRef references a secret containing the service account credentials
	// +kubebuilder:validation:Required
	CredentialsRef SecretKeyRef `json:"credentialsRef"`

	// HeaderName is the HTTP header name for the credentials
	// +kubebuilder:default=Authorization
	// +optional
	HeaderName string `json:"headerName,omitempty"`

	// HeaderFormat is the format string for the header value
	// Use {token} as placeholder for the credential value
	// +kubebuilder:default="Bearer {token}"
	// +optional
	HeaderFormat string `json:"headerFormat,omitempty"`
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

	// Parameters defines the input parameters for the composite tool
	// +optional
	Parameters map[string]ParameterSpec `json:"parameters,omitempty"`

	// Steps defines the workflow steps
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Steps []WorkflowStep `json:"steps"`

	// Timeout is the maximum execution time for the composite tool
	// +kubebuilder:default="30m"
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// ParameterSpec defines a parameter for a composite tool
type ParameterSpec struct {
	// Type is the parameter type (string, integer, boolean, etc.)
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Description describes the parameter
	// +optional
	Description string `json:"description,omitempty"`

	// Default is the default value for the parameter
	// +optional
	Default string `json:"default,omitempty"`

	// Required indicates if the parameter is required
	// +kubebuilder:default=false
	// +optional
	Required bool `json:"required,omitempty"`
}

// WorkflowStep defines a step in a composite tool workflow
type WorkflowStep struct {
	// ID is the unique identifier for this step
	// +kubebuilder:validation:Required
	ID string `json:"id"`

	// Type is the step type (tool_call, elicitation, etc.)
	// +kubebuilder:validation:Enum=tool_call;elicitation
	// +kubebuilder:default=tool_call
	// +optional
	Type string `json:"type,omitempty"`

	// Tool is the tool to call (format: "workload.tool_name")
	// Only used when Type is "tool_call"
	// +optional
	Tool string `json:"tool,omitempty"`

	// Arguments is a map of argument templates
	// Supports Go template syntax with .params and .steps
	// +optional
	Arguments map[string]string `json:"arguments,omitempty"`

	// Message is the elicitation message
	// Only used when Type is "elicitation"
	// +optional
	Message string `json:"message,omitempty"`

	// Schema defines the expected response schema for elicitation
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Schema *runtime.RawExtension `json:"schema,omitempty"`

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
}

// TokenCacheConfig configures token caching behavior
type TokenCacheConfig struct {
	// Provider defines the cache provider type
	// +kubebuilder:validation:Enum=memory;redis
	// +kubebuilder:default=memory
	// +optional
	Provider string `json:"provider,omitempty"`

	// Memory configures in-memory token caching
	// Only used when Provider is "memory"
	// +optional
	Memory *MemoryCacheConfig `json:"memory,omitempty"`

	// Redis configures Redis token caching
	// Only used when Provider is "redis"
	// +optional
	Redis *RedisCacheConfig `json:"redis,omitempty"`
}

// MemoryCacheConfig configures in-memory token caching
type MemoryCacheConfig struct {
	// MaxEntries is the maximum number of cache entries
	// +kubebuilder:default=1000
	// +optional
	MaxEntries int `json:"maxEntries,omitempty"`

	// TTLOffset is the duration before token expiry to refresh
	// +kubebuilder:default="5m"
	// +optional
	TTLOffset string `json:"ttlOffset,omitempty"`
}

// RedisCacheConfig configures Redis token caching
type RedisCacheConfig struct {
	// Address is the Redis server address
	// +kubebuilder:validation:Required
	Address string `json:"address"`

	// DB is the Redis database number
	// +kubebuilder:default=0
	// +optional
	DB int `json:"db,omitempty"`

	// KeyPrefix is the prefix for cache keys
	// +kubebuilder:default="vmcp:tokens:"
	// +optional
	KeyPrefix string `json:"keyPrefix,omitempty"`

	// PasswordRef references a secret containing the Redis password
	// +optional
	PasswordRef *SecretKeyRef `json:"passwordRef,omitempty"`

	// TLS enables TLS for Redis connections
	// +kubebuilder:default=false
	// +optional
	TLS bool `json:"tls,omitempty"`
}

// OperationalConfig defines operational settings
type OperationalConfig struct {
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

// VirtualMCPServerStatus defines the observed state of VirtualMCPServer
type VirtualMCPServerStatus struct {
	// Conditions represent the latest available observations of the VirtualMCPServer's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// DiscoveredBackends lists discovered backend configurations when source=discovered
	// +optional
	DiscoveredBackends []DiscoveredBackend `json:"discoveredBackends,omitempty"`

	// Capabilities summarizes aggregated capabilities from all backends
	// +optional
	Capabilities *CapabilitiesSummary `json:"capabilities,omitempty"`

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
}

// DiscoveredBackend represents a discovered backend MCPServer
type DiscoveredBackend struct {
	// Name is the name of the backend MCPServer
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// AuthConfigRef is the name of the discovered MCPExternalAuthConfig
	// Empty if backend has no external auth config
	// +optional
	AuthConfigRef string `json:"authConfigRef,omitempty"`

	// AuthType is the type of authentication configured
	// +optional
	AuthType string `json:"authType,omitempty"`

	// Status is the current status of the backend
	// +kubebuilder:validation:Enum=ready;degraded;unavailable
	// +optional
	Status string `json:"status,omitempty"`

	// LastHealthCheck is the timestamp of the last health check
	// +optional
	LastHealthCheck *metav1.Time `json:"lastHealthCheck,omitempty"`

	// URL is the URL of the backend MCPServer
	// +optional
	URL string `json:"url,omitempty"`
}

// CapabilitiesSummary summarizes aggregated capabilities
type CapabilitiesSummary struct {
	// ToolCount is the total number of tools exposed
	// +optional
	ToolCount int `json:"toolCount,omitempty"`

	// ResourceCount is the total number of resources exposed
	// +optional
	ResourceCount int `json:"resourceCount,omitempty"`

	// PromptCount is the total number of prompts exposed
	// +optional
	PromptCount int `json:"promptCount,omitempty"`

	// CompositeToolCount is the number of composite tools defined
	// +optional
	CompositeToolCount int `json:"compositeToolCount,omitempty"`
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

	// ConditionTypeBackendsDiscovered indicates whether backends have been discovered
	ConditionTypeBackendsDiscovered = "BackendsDiscovered"

	// ConditionTypeVirtualMCPServerGroupRefValidated indicates whether the GroupRef is valid
	ConditionTypeVirtualMCPServerGroupRefValidated = "GroupRefValidated"
)

// Condition reasons for VirtualMCPServer
const (
	// ConditionReasonAllBackendsReady indicates all backends are ready
	ConditionReasonAllBackendsReady = "AllBackendsReady"

	// ConditionReasonSomeBackendsUnavailable indicates some backends are unavailable
	ConditionReasonSomeBackendsUnavailable = "SomeBackendsUnavailable"

	// ConditionReasonNoBackends indicates no backends were discovered
	ConditionReasonNoBackends = "NoBackends"

	// ConditionReasonIncomingAuthValid indicates incoming auth is valid
	ConditionReasonIncomingAuthValid = "IncomingAuthValid"

	// ConditionReasonIncomingAuthInvalid indicates incoming auth is invalid
	ConditionReasonIncomingAuthInvalid = "IncomingAuthInvalid"

	// ConditionReasonDiscoveryComplete indicates backend discovery is complete
	ConditionReasonDiscoveryComplete = "DiscoveryComplete"

	// ConditionReasonDiscoveryFailed indicates backend discovery failed
	ConditionReasonDiscoveryFailed = "DiscoveryFailed"

	// ConditionReasonGroupRefValid indicates the GroupRef is valid
	ConditionReasonVirtualMCPServerGroupRefValid = "GroupRefValid"

	// ConditionReasonGroupRefNotFound indicates the referenced MCPGroup was not found
	ConditionReasonVirtualMCPServerGroupRefNotFound = "GroupRefNotFound"

	// ConditionReasonGroupRefNotReady indicates the referenced MCPGroup is not ready
	ConditionReasonVirtualMCPServerGroupRefNotReady = "GroupRefNotReady"
)

// Backend authentication types
const (
	// BackendAuthTypeDiscovered automatically discovers from backend's externalAuthConfigRef
	BackendAuthTypeDiscovered = "discovered"

	// BackendAuthTypePassThrough forwards client token unchanged
	BackendAuthTypePassThrough = "pass_through"

	// BackendAuthTypeServiceAccount uses service account credentials
	BackendAuthTypeServiceAccount = "service_account"

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
	WorkflowStepTypeToolCall = "tool_call"

	// WorkflowStepTypeElicitation requests user input
	WorkflowStepTypeElicitation = "elicitation"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=vmcp;virtualmcp
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The phase of the VirtualMCPServer"
//+kubebuilder:printcolumn:name="Tools",type="integer",JSONPath=".status.capabilities.toolCount",description="Total tools"
//+kubebuilder:printcolumn:name="Backends",type="integer",JSONPath=".status.discoveredBackends[*]",description="Backends"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url",description="Virtual MCP server URL"
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

func init() {
	SchemeBuilder.Register(&VirtualMCPServer{}, &VirtualMCPServerList{})
}
