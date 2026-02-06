// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HeaderForwardConfig defines header forward configuration for remote servers.
type HeaderForwardConfig struct {
	// AddPlaintextHeaders is a map of header names to literal values to inject into requests.
	// WARNING: Values are stored in plaintext and visible via kubectl commands.
	// Use addHeadersFromSecret for sensitive data like API keys or tokens.
	// +optional
	AddPlaintextHeaders map[string]string `json:"addPlaintextHeaders,omitempty"`

	// AddHeadersFromSecret references Kubernetes Secrets for sensitive header values.
	// +optional
	AddHeadersFromSecret []HeaderFromSecret `json:"addHeadersFromSecret,omitempty"`
}

// HeaderFromSecret defines a header whose value comes from a Kubernetes Secret.
type HeaderFromSecret struct {
	// HeaderName is the HTTP header name (e.g., "X-API-Key")
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	HeaderName string `json:"headerName"`

	// ValueSecretRef references the Secret and key containing the header value
	// +kubebuilder:validation:Required
	ValueSecretRef *SecretKeyRef `json:"valueSecretRef"`
}

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
	// Deprecated: Use ProxyPort instead
	Port int32 `json:"port,omitempty"`

	// ProxyPort is the port to expose the MCP proxy on
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	ProxyPort int32 `json:"proxyPort,omitempty"`

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

	// HeaderForward configures headers to inject into requests to the remote MCP server.
	// Use this to add custom headers like X-Tenant-ID or correlation IDs.
	// +optional
	HeaderForward *HeaderForwardConfig `json:"headerForward,omitempty"`

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

	// ServiceAccount is the name of an already existing service account to use by the proxy.
	// If not specified, a ServiceAccount will be created automatically and used by the proxy.
	// +optional
	ServiceAccount *string `json:"serviceAccount,omitempty"`

	// TrustProxyHeaders indicates whether to trust X-Forwarded-* headers from reverse proxies
	// When enabled, the proxy will use X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-Port,
	// and X-Forwarded-Prefix headers to construct endpoint URLs
	// +kubebuilder:default=false
	// +optional
	TrustProxyHeaders bool `json:"trustProxyHeaders,omitempty"`

	// EndpointPrefix is the path prefix to prepend to SSE endpoint URLs.
	// This is used to handle path-based ingress routing scenarios where the ingress
	// strips a path prefix before forwarding to the backend.
	// +optional
	EndpointPrefix string `json:"endpointPrefix,omitempty"`

	// ResourceOverrides allows overriding annotations and labels for resources created by the operator
	// +optional
	ResourceOverrides *ResourceOverrides `json:"resourceOverrides,omitempty"`

	// GroupRef is the name of the MCPGroup this proxy belongs to
	// Must reference an existing MCPGroup in the same namespace
	// +optional
	GroupRef string `json:"groupRef,omitempty"`
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

	// ConditionTypeMCPRemoteProxyGroupRefValidated indicates whether the GroupRef is valid
	ConditionTypeMCPRemoteProxyGroupRefValidated = "GroupRefValidated"

	// ConditionTypeMCPRemoteProxyToolConfigValidated indicates whether the ToolConfigRef is valid
	ConditionTypeMCPRemoteProxyToolConfigValidated = "ToolConfigValidated"

	// ConditionTypeMCPRemoteProxyExternalAuthConfigValidated indicates whether the ExternalAuthConfigRef is valid
	ConditionTypeMCPRemoteProxyExternalAuthConfigValidated = "ExternalAuthConfigValidated"
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

	// ConditionReasonMCPRemoteProxyGroupRefValidated indicates the GroupRef is valid
	ConditionReasonMCPRemoteProxyGroupRefValidated = "GroupRefIsValid"

	// ConditionReasonMCPRemoteProxyGroupRefNotFound indicates the GroupRef is invalid
	ConditionReasonMCPRemoteProxyGroupRefNotFound = "GroupRefNotFound"

	// ConditionReasonMCPRemoteProxyGroupRefNotReady indicates the referenced MCPGroup is not in the Ready state
	ConditionReasonMCPRemoteProxyGroupRefNotReady = "GroupRefNotReady"

	// ConditionReasonMCPRemoteProxyToolConfigValid indicates the ToolConfigRef is valid
	ConditionReasonMCPRemoteProxyToolConfigValid = "ToolConfigValid"

	// ConditionReasonMCPRemoteProxyToolConfigNotFound indicates the referenced MCPToolConfig was not found
	ConditionReasonMCPRemoteProxyToolConfigNotFound = "ToolConfigNotFound"

	// ConditionReasonMCPRemoteProxyToolConfigFetchError indicates an error occurred fetching the MCPToolConfig
	ConditionReasonMCPRemoteProxyToolConfigFetchError = "ToolConfigFetchError"

	// ConditionReasonMCPRemoteProxyExternalAuthConfigValid indicates the ExternalAuthConfigRef is valid
	ConditionReasonMCPRemoteProxyExternalAuthConfigValid = "ExternalAuthConfigValid"

	// ConditionReasonMCPRemoteProxyExternalAuthConfigNotFound indicates the referenced MCPExternalAuthConfig was not found
	ConditionReasonMCPRemoteProxyExternalAuthConfigNotFound = "ExternalAuthConfigNotFound"

	// ConditionReasonMCPRemoteProxyExternalAuthConfigFetchError indicates an error occurred fetching the MCPExternalAuthConfig
	ConditionReasonMCPRemoteProxyExternalAuthConfigFetchError = "ExternalAuthConfigFetchError"
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

// GetProxyPort returns the proxy port of the MCPRemoteProxy
func (m *MCPRemoteProxy) GetProxyPort() int32 {
	const defaultProxyPort int32 = 8080

	// If the legacy Port is set and ProxyPort is only the default,
	// prefer Port to preserve backward compatibility when ProxyPort
	// was defaulted by the API server.
	if m.Spec.Port > 0 && m.Spec.ProxyPort == defaultProxyPort {
		return m.Spec.Port
	}

	if m.Spec.ProxyPort > 0 {
		return m.Spec.ProxyPort
	}

	// the below is deprecated and will be removed in a future version
	// we need to keep it here to avoid breaking changes
	if m.Spec.Port > 0 {
		return m.Spec.Port
	}

	// default to 8080 if no port is specified
	return defaultProxyPort
}
