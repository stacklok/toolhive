// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

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

// OperationalConfig defines operational settings

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
