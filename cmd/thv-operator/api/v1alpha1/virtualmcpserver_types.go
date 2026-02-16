// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// VirtualMCPServerSpec defines the desired state of VirtualMCPServer
type VirtualMCPServerSpec struct {
	// IncomingAuth configures authentication for clients connecting to the Virtual MCP server.
	// Must be explicitly set - use "anonymous" type when no authentication is required.
	// This field takes precedence over config.IncomingAuth and should be preferred because it
	// supports Kubernetes-native secret references (SecretKeyRef, ConfigMapRef) for secure
	// dynamic discovery of credentials, rather than requiring secrets to be embedded in config.
	// +kubebuilder:validation:Required
	IncomingAuth *IncomingAuthConfig `json:"incomingAuth"`

	// OutgoingAuth configures authentication from Virtual MCP to backend MCPServers.
	// This field takes precedence over config.OutgoingAuth and should be preferred because it
	// supports Kubernetes-native secret references (SecretKeyRef, ConfigMapRef) for secure
	// dynamic discovery of credentials, rather than requiring secrets to be embedded in config.
	// +optional
	OutgoingAuth *OutgoingAuthConfig `json:"outgoingAuth,omitempty"`

	// ServiceType specifies the Kubernetes service type for the Virtual MCP server
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	// +kubebuilder:default=ClusterIP
	// +optional
	ServiceType string `json:"serviceType,omitempty"`

	// ServiceAccount is the name of an already existing service account to use by the Virtual MCP server.
	// If not specified, a ServiceAccount will be created automatically and used by the Virtual MCP server.
	// +optional
	ServiceAccount *string `json:"serviceAccount,omitempty"`

	// PodTemplateSpec defines the pod template to use for the Virtual MCP server
	// This allows for customizing the pod configuration beyond what is provided by the other fields.
	// Note that to modify the specific container the Virtual MCP server runs in, you must specify
	// the 'vmcp' container name in the PodTemplateSpec.
	// This field accepts a PodTemplateSpec object as JSON/YAML.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`

	// Config is the Virtual MCP server configuration
	// The only field currently required within config is `config.groupRef`.
	// GroupRef references an existing MCPGroup that defines backend workloads.
	// The referenced MCPGroup must exist in the same namespace.
	// The telemetry and audit config from here are also supported, but not required.
	// +optional
	Config config.Config `json:"config,omitempty"`

	// EmbeddingServer optionally deploys an owned EmbeddingServer when the optimizer is enabled.
	// If set, the controller creates an EmbeddingServer CR and auto-populates
	// the optimizer's embeddingService field with the generated service name.
	// Mutually exclusive with EmbeddingServerRef.
	// +optional
	EmbeddingServer *EmbeddingServerSpec `json:"embeddingServer,omitempty"`

	// EmbeddingServerRef references an existing EmbeddingServer resource by name.
	// Use this instead of EmbeddingServer when multiple VirtualMCPServers should share
	// a single EmbeddingServer (e.g., when using the same embedding model).
	// The referenced EmbeddingServer must exist in the same namespace and be ready.
	// Mutually exclusive with EmbeddingServer.
	// +optional
	EmbeddingServerRef *EmbeddingServerRef `json:"embeddingServerRef,omitempty"`
}

// EmbeddingServerRef references an existing EmbeddingServer resource by name.
// This follows the same pattern as ExternalAuthConfigRef and ToolConfigRef.
type EmbeddingServerRef struct {
	// Name is the name of the EmbeddingServer resource
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// IncomingAuthConfig configures authentication for clients connecting to the Virtual MCP server
//
// +kubebuilder:validation:XValidation:rule="self.type == 'oidc' ? has(self.oidcConfig) : true",message="spec.incomingAuth.oidcConfig is required when type is oidc"
//
//nolint:lll // CEL validation rule exceeds line length limit
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

// OperationalConfig defines operational settings

// Backend status constants for DiscoveredBackend.Status
// These are the user-facing values stored in VirtualMCPServer.Status.DiscoveredBackends.
// Use BackendHealthStatus.ToCRDStatus() to convert from internal health status.
const (
	BackendStatusReady       = "ready"
	BackendStatusUnavailable = "unavailable"
	BackendStatusDegraded    = "degraded"
	BackendStatusUnknown     = "unknown"
)

// DiscoveredBackend is an alias to the canonical definition in pkg/vmcp/types.go
// This provides a local name for use in the CRD status.
type DiscoveredBackend = vmcptypes.DiscoveredBackend

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

	// BackendCount is the number of healthy/ready backends
	// (excludes unavailable, degraded, and unknown backends)
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

	// ConditionTypeEmbeddingServerReady indicates whether the EmbeddingServer is ready
	ConditionTypeEmbeddingServerReady = "EmbeddingServerReady"
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

	// ConditionReasonEmbeddingServerReady indicates the EmbeddingServer is ready
	ConditionReasonEmbeddingServerReady = "EmbeddingServerReady"

	// ConditionReasonEmbeddingServerNotFound indicates the referenced EmbeddingServer was not found
	ConditionReasonEmbeddingServerNotFound = "EmbeddingServerNotFound"

	// ConditionReasonEmbeddingServerNotReady indicates the referenced EmbeddingServer is not ready
	ConditionReasonEmbeddingServerNotReady = "EmbeddingServerNotReady"
)

// Backend authentication types
const (
	// BackendAuthTypeDiscovered automatically discovers from backend's externalAuthConfigRef
	BackendAuthTypeDiscovered = "discovered"

	// BackendAuthTypeExternalAuthConfigRef references an MCPExternalAuthConfig resource
	BackendAuthTypeExternalAuthConfigRef = "external_auth_config_ref"
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

// Validate performs validation for VirtualMCPServer
// This method is called by the controller during reconciliation
func (r *VirtualMCPServer) Validate() error {
	// Validate Group is set (required field)
	// Note: CEL cannot validate embedded types from other packages
	if r.Spec.Config.Group == "" {
		return fmt.Errorf("spec.config.groupRef is required")
	}

	// Note: IncomingAuth validation is handled by kubebuilder markers and CEL rules

	// Validate OutgoingAuth backend configurations
	if r.Spec.OutgoingAuth != nil {
		for backendName, backendAuth := range r.Spec.OutgoingAuth.Backends {
			if err := r.validateBackendAuth(backendName, backendAuth); err != nil {
				return err
			}
		}
	}

	// Validate Aggregation configuration
	if r.Spec.Config.Aggregation != nil {
		if err := r.validateAggregation(); err != nil {
			return err
		}
	}

	// Validate CompositeTools
	if len(r.Spec.Config.CompositeTools) > 0 {
		if err := r.validateCompositeTools(); err != nil {
			return err
		}
	}

	// Validate EmbeddingServer / EmbeddingServerRef
	return r.validateEmbeddingServer()
}

// validateEmbeddingServer validates EmbeddingServer and EmbeddingServerRef configuration.
// Rules:
// - embeddingServer and embeddingServerRef are mutually exclusive
// - If config.optimizer is set, exactly one of embeddingServer or embeddingServerRef must be set
// - If embeddingServer or embeddingServerRef is set, config.optimizer must be set
// - embeddingServerRef.name must be non-empty when ref is provided
func (r *VirtualMCPServer) validateEmbeddingServer() error {
	hasInline := r.Spec.EmbeddingServer != nil
	hasRef := r.Spec.EmbeddingServerRef != nil
	hasOptimizer := r.Spec.Config.Optimizer != nil

	// Mutually exclusive check
	if hasInline && hasRef {
		return fmt.Errorf("spec.embeddingServer and spec.embeddingServerRef are mutually exclusive")
	}

	// If optimizer is set, exactly one embedding source must be set
	if hasOptimizer && !hasInline && !hasRef {
		return fmt.Errorf("spec.config.optimizer requires either spec.embeddingServer or spec.embeddingServerRef to be set")
	}

	// If embedding source is set, optimizer must be set
	if (hasInline || hasRef) && !hasOptimizer {
		return fmt.Errorf("spec.embeddingServer or spec.embeddingServerRef requires spec.config.optimizer to be set")
	}

	// Validate ref name is non-empty
	if hasRef && r.Spec.EmbeddingServerRef.Name == "" {
		return fmt.Errorf("spec.embeddingServerRef.name is required")
	}

	return nil
}

// validateBackendAuth validates a single backend auth configuration
func (*VirtualMCPServer) validateBackendAuth(backendName string, auth BackendAuthConfig) error {
	// Validate type is set
	if auth.Type == "" {
		return fmt.Errorf("spec.outgoingAuth.backends[%s].type is required", backendName)
	}

	// Validate type-specific configurations
	switch auth.Type {
	case BackendAuthTypeExternalAuthConfigRef:
		if auth.ExternalAuthConfigRef == nil {
			return fmt.Errorf(
				"spec.outgoingAuth.backends[%s].externalAuthConfigRef is required when type is external_auth_config_ref",
				backendName)
		}
		if auth.ExternalAuthConfigRef.Name == "" {
			return fmt.Errorf("spec.outgoingAuth.backends[%s].externalAuthConfigRef.name is required", backendName)
		}

	case BackendAuthTypeDiscovered:
		// No additional validation needed

	default:
		return fmt.Errorf(
			"spec.outgoingAuth.backends[%s].type must be one of: discovered, external_auth_config_ref",
			backendName)
	}

	return nil
}

// validateAggregation validates Aggregation configuration
func (r *VirtualMCPServer) validateAggregation() error {
	agg := r.Spec.Config.Aggregation

	// Validate conflict resolution strategy
	if agg.ConflictResolution != "" {
		validStrategies := map[vmcptypes.ConflictResolutionStrategy]bool{
			vmcptypes.ConflictStrategyPrefix:   true,
			vmcptypes.ConflictStrategyPriority: true,
			vmcptypes.ConflictStrategyManual:   true,
		}
		if !validStrategies[agg.ConflictResolution] {
			return fmt.Errorf("config.aggregation.conflictResolution must be one of: prefix, priority, manual")
		}
	}

	// Validate conflict resolution config based on strategy
	if agg.ConflictResolutionConfig != nil {
		resConfig := agg.ConflictResolutionConfig

		switch agg.ConflictResolution {
		case vmcptypes.ConflictStrategyPrefix:
			// Prefix strategy uses PrefixFormat if specified, otherwise defaults
			// No additional validation required

		case vmcptypes.ConflictStrategyPriority:
			if len(resConfig.PriorityOrder) == 0 {
				return fmt.Errorf("config.aggregation.conflictResolutionConfig.priorityOrder is required when conflictResolution is priority")
			}

		case vmcptypes.ConflictStrategyManual:
			// For manual resolution, tools must define explicit overrides
			// This will be validated at runtime when conflicts are detected
		}
	}

	// Validate per-workload tool configurations
	for i, toolConfig := range agg.Tools {
		if toolConfig.Workload == "" {
			return fmt.Errorf("config.aggregation.tools[%d].workload is required", i)
		}

		// If ToolConfigRef is specified, ensure it has a name
		if toolConfig.ToolConfigRef != nil && toolConfig.ToolConfigRef.Name == "" {
			return fmt.Errorf("config.aggregation.tools[%d].toolConfigRef.name is required when toolConfigRef is specified", i)
		}
	}

	return nil
}

// validateCompositeTools validates composite tool definitions in spec.config.compositeTools.
// Uses shared validation from pkg/vmcp/config/composite_validation.go.
func (r *VirtualMCPServer) validateCompositeTools() error {
	toolNames := make(map[string]bool)

	for i := range r.Spec.Config.CompositeTools {
		tool := &r.Spec.Config.CompositeTools[i]

		// Check for duplicate tool names
		if toolNames[tool.Name] {
			return fmt.Errorf("spec.config.compositeTools[%d].name %q is duplicated", i, tool.Name)
		}
		toolNames[tool.Name] = true

		// Use shared validation
		if err := config.ValidateCompositeToolConfig(
			fmt.Sprintf("spec.config.compositeTools[%d]", i), tool,
		); err != nil {
			return err
		}
	}

	return nil
}

func init() {
	SchemeBuilder.Register(&VirtualMCPServer{}, &VirtualMCPServerList{})
}
