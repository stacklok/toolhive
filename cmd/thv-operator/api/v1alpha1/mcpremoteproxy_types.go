package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MCPRemoteProxySpec defines the desired state of MCPRemoteProxy
type MCPRemoteProxySpec struct {
	// RemoteURL is the URL of the remote MCP server to proxy
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://`
	RemoteURL string `json:"remoteURL"`

	// Port is the port to expose the MCP proxy on
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	Port int32 `json:"port,omitempty"`

	// Transport is the transport method for the remote proxy (sse or streamable-http)
	// +kubebuilder:validation:Enum=sse;streamable-http
	// +kubebuilder:default=streamable-http
	Transport string `json:"transport,omitempty"`

	// OIDCConfig defines OIDC authentication configuration for the proxy
	// This validates incoming tokens from clients. Required for proxy mode.
	// +kubebuilder:validation:Required
	OIDCConfig OIDCConfigRef `json:"oidcConfig"`

	// ExternalAuthConfigRef references a MCPExternalAuthConfig resource for token exchange.
	// When specified, the proxy will exchange validated incoming tokens for remote service tokens.
	// The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPRemoteProxy.
	// +optional
	ExternalAuthConfigRef *ExternalAuthConfigRef `json:"externalAuthConfigRef,omitempty"`

	// AuthzConfig defines authorization policy configuration for the proxy
	// +optional
	AuthzConfig *AuthzConfigRef `json:"authzConfig,omitempty"`

	// Audit defines audit logging configuration for the proxy
	// +optional
	Audit *AuditConfig `json:"audit,omitempty"`

	// ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.
	// The referenced MCPToolConfig must exist in the same namespace as this MCPRemoteProxy.
	// Cross-namespace references are not supported for security and isolation reasons.
	// If specified, this allows filtering and overriding tools from the remote MCP server.
	// +optional
	ToolConfigRef *ToolConfigRef `json:"toolConfigRef,omitempty"`

	// Telemetry defines observability configuration for the proxy
	// +optional
	Telemetry *TelemetryConfig `json:"telemetry,omitempty"`

	// Resources defines the resource requirements for the proxy container
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`

	// TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies
	// When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,
	// and X-Forwarded-Prefix headers to construct endpoint URLs
	// +kubebuilder:default=false
	// +optional
	TrustProxyHeaders bool `json:"trustProxyHeaders,omitempty"`

	// ResourceOverrides allows overriding annotations and labels for resources created by the operator
	// +optional
	ResourceOverrides *ResourceOverrides `json:"resourceOverrides,omitempty"`
}

// MCPRemoteProxyStatus defines the observed state of MCPRemoteProxy
type MCPRemoteProxyStatus struct {
	// Phase is the current phase of the MCPRemoteProxy
	// +optional
	Phase MCPRemoteProxyPhase `json:"phase,omitempty"`

	// URL is the internal cluster URL where the proxy can be accessed
	// +optional
	URL string `json:"url,omitempty"`

	// ExternalURL is the external URL where the proxy can be accessed (if exposed externally)
	// +optional
	ExternalURL string `json:"externalURL,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed MCPRemoteProxy
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the MCPRemoteProxy's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ToolConfigHash stores the hash of the referenced ToolConfig for change detection
	// +optional
	ToolConfigHash string `json:"toolConfigHash,omitempty"`

	// ExternalAuthConfigHash is the hash of the referenced MCPExternalAuthConfig spec
	// +optional
	ExternalAuthConfigHash string `json:"externalAuthConfigHash,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`
}

// MCPRemoteProxyPhase is a label for the condition of a MCPRemoteProxy at the current time
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Terminating
type MCPRemoteProxyPhase string

const (
	// MCPRemoteProxyPhasePending means the proxy is being created
	MCPRemoteProxyPhasePending MCPRemoteProxyPhase = "Pending"

	// MCPRemoteProxyPhaseReady means the proxy is ready and operational
	MCPRemoteProxyPhaseReady MCPRemoteProxyPhase = "Ready"

	// MCPRemoteProxyPhaseFailed means the proxy failed to start or encountered an error
	MCPRemoteProxyPhaseFailed MCPRemoteProxyPhase = "Failed"

	// MCPRemoteProxyPhaseTerminating means the proxy is being deleted
	MCPRemoteProxyPhaseTerminating MCPRemoteProxyPhase = "Terminating"
)

// Condition types for MCPRemoteProxy
const (
	// ConditionTypeReady indicates overall readiness of the proxy
	ConditionTypeReady = "Ready"

	// ConditionTypeRemoteAvailable indicates whether the remote MCP server is reachable
	ConditionTypeRemoteAvailable = "RemoteAvailable"

	// ConditionTypeAuthConfigured indicates whether authentication is properly configured
	ConditionTypeAuthConfigured = "AuthConfigured"
)

// Condition reasons for MCPRemoteProxy
const (
	// ConditionReasonDeploymentReady indicates the deployment is ready
	ConditionReasonDeploymentReady = "DeploymentReady"

	// ConditionReasonDeploymentNotReady indicates the deployment is not ready
	ConditionReasonDeploymentNotReady = "DeploymentNotReady"

	// ConditionReasonRemoteURLReachable indicates the remote URL is reachable
	ConditionReasonRemoteURLReachable = "RemoteURLReachable"

	// ConditionReasonRemoteURLUnreachable indicates the remote URL is unreachable
	ConditionReasonRemoteURLUnreachable = "RemoteURLUnreachable"

	// ConditionReasonAuthValid indicates authentication configuration is valid
	ConditionReasonAuthValid = "AuthValid"

	// ConditionReasonAuthInvalid indicates authentication configuration is invalid
	ConditionReasonAuthInvalid = "AuthInvalid"

	// ConditionReasonMissingOIDCConfig indicates OIDCConfig is not specified
	ConditionReasonMissingOIDCConfig = "MissingOIDCConfig"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Remote URL",type="string",JSONPath=".spec.remoteURL"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPRemoteProxy is the Schema for the mcpremoteproxies API
// It enables proxying remote MCP servers with authentication, authorization, audit logging, and tool filtering
type MCPRemoteProxy struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPRemoteProxySpec   `json:"spec,omitempty"`
	Status MCPRemoteProxyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPRemoteProxyList contains a list of MCPRemoteProxy
type MCPRemoteProxyList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPRemoteProxy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPRemoteProxy{}, &MCPRemoteProxyList{})
}

// GetName returns the name of the MCPRemoteProxy
func (m *MCPRemoteProxy) GetName() string {
	return m.Name
}

// GetNamespace returns the namespace of the MCPRemoteProxy
func (m *MCPRemoteProxy) GetNamespace() string {
	return m.Namespace
}

// GetOIDCConfig returns the OIDC configuration reference
func (m *MCPRemoteProxy) GetOIDCConfig() *OIDCConfigRef {
	return &m.Spec.OIDCConfig
}

// GetPort returns the port for the MCPRemoteProxy
func (m *MCPRemoteProxy) GetPort() int32 {
	return m.Spec.Port
}
