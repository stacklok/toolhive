// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// HeaderForwardConfig defines header forward configuration for remote servers.
type HeaderForwardConfig struct {
	// AddPlaintextHeaders is a map of header names to literal values to inject into requests.
	// WARNING: Values are stored in plaintext and visible via kubectl commands.
	// Use addHeadersFromSecret for sensitive data like API keys or tokens.
	// +optional
	AddPlaintextHeaders map[string]string `json:"addPlaintextHeaders,omitempty"`

	// AddHeadersFromSecret references Kubernetes Secrets for sensitive header values.
	// +listType=map
	// +listMapKey=headerName
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
//
// +kubebuilder:validation:XValidation:rule="!(has(self.authzConfig) && has(self.authzConfigRef))",message="authzConfig and authzConfigRef are mutually exclusive; use authzConfigRef to reference a shared MCPAuthzConfig"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPRemoteProxySpec struct {
	// RemoteURL is the URL of the remote MCP server to proxy
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^https?://`
	RemoteURL string `json:"remoteUrl"`

	// ProxyPort is the port to expose the MCP proxy on
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	ProxyPort int32 `json:"proxyPort,omitempty"`

	// Transport is the transport method for the remote proxy (sse or streamable-http)
	// +kubebuilder:validation:Enum=sse;streamable-http
	// +kubebuilder:default=streamable-http
	Transport string `json:"transport,omitempty"`

	// OIDCConfigRef references a shared MCPOIDCConfig resource for OIDC authentication.
	// The referenced MCPOIDCConfig must exist in the same namespace as this MCPRemoteProxy.
	// Per-server overrides (audience, scopes) are specified here; shared provider config
	// lives in the MCPOIDCConfig resource.
	//
	// SECURITY: if this field is omitted and no other authentication source is configured,
	// the proxy runs UNAUTHENTICATED. It accepts every request that can reach its port and
	// forwards it to the remote MCP server under a synthetic local-user identity, with no
	// token or credential check. Set this field to enforce identity-based access control
	// per request.
	// +optional
	OIDCConfigRef *MCPOIDCConfigReference `json:"oidcConfigRef,omitempty"`

	// ExternalAuthConfigRef references a MCPExternalAuthConfig resource for token exchange.
	// When specified, the proxy will exchange validated incoming tokens for remote service tokens.
	// The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPRemoteProxy.
	// +optional
	ExternalAuthConfigRef *ExternalAuthConfigRef `json:"externalAuthConfigRef,omitempty"`

	// AuthServerRef optionally references a resource that configures an embedded
	// OAuth 2.0/OIDC authorization server to authenticate MCP clients.
	// Currently the only supported kind is MCPExternalAuthConfig (type: embeddedAuthServer).
	// +optional
	AuthServerRef *AuthServerRef `json:"authServerRef,omitempty"`

	// HeaderForward configures headers to inject into requests to the remote MCP server.
	// Use this to add custom headers like X-Tenant-ID or correlation IDs.
	// +optional
	HeaderForward *HeaderForwardConfig `json:"headerForward,omitempty"`

	// AuthzConfig defines authorization policy configuration for the proxy.
	// AuthzConfig and AuthzConfigRef are mutually exclusive.
	// +optional
	AuthzConfig *AuthzConfigRef `json:"authzConfig,omitempty"`

	// AuthzConfigRef references a shared MCPAuthzConfig resource for authorization.
	// The referenced MCPAuthzConfig must exist in the same namespace as this MCPRemoteProxy.
	// Mutually exclusive with authzConfig.
	// +optional
	AuthzConfigRef *MCPAuthzConfigReference `json:"authzConfigRef,omitempty"`

	// Audit defines audit logging configuration for the proxy
	// +optional
	Audit *AuditConfig `json:"audit,omitempty"`

	// ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.
	// The referenced MCPToolConfig must exist in the same namespace as this MCPRemoteProxy.
	// Cross-namespace references are not supported for security and isolation reasons.
	// If specified, this allows filtering and overriding tools from the remote MCP server.
	// +optional
	ToolConfigRef *ToolConfigRef `json:"toolConfigRef,omitempty"`

	// TelemetryConfigRef references an MCPTelemetryConfig resource for shared telemetry configuration.
	// The referenced MCPTelemetryConfig must exist in the same namespace as this MCPRemoteProxy.
	// Cross-namespace references are not supported for security and isolation reasons.
	// +optional
	TelemetryConfigRef *MCPTelemetryConfigReference `json:"telemetryConfigRef,omitempty"`

	// Resources defines the resource requirements for the proxy container
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`

	// ServiceAccount is the name of an already existing service account to use by the proxy.
	// If not specified, a ServiceAccount will be created automatically and used by the proxy.
	// +optional
	ServiceAccount *string `json:"serviceAccount,omitempty"`

	// PodTemplateSpec defines the pod template to use for the MCPRemoteProxy
	// This allows for customizing the pod configuration beyond what is provided by the other fields.
	// Note that to modify the specific container the remote proxy runs in, you must specify
	// the `toolhive` container name in the PodTemplateSpec.
	// This field accepts a PodTemplateSpec object as JSON/YAML.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`

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

	// GroupRef references the MCPGroup this proxy belongs to.
	// The referenced MCPGroup must be in the same namespace.
	// +optional
	GroupRef *MCPGroupRef `json:"groupRef,omitempty"`

	// SessionAffinity controls whether the Service routes repeated client connections to the same pod.
	// MCP protocols (SSE, streamable-http) are stateful, so ClientIP is the default.
	// Set to "None" for stateless servers or when using an external load balancer with its own affinity.
	//
	// Interaction with sessionStorage: when running multiple replicas with
	// sessionStorage.provider "redis", set this to "None" so requests are
	// distributed across replicas and sessions resolve via the shared store.
	// Conversely, "None" without Redis-backed sessionStorage breaks session
	// continuity — any request landing on a different pod fails with
	// "Session not found".
	// +kubebuilder:validation:Enum=ClientIP;None
	// +kubebuilder:default=ClientIP
	// +optional
	SessionAffinity string `json:"sessionAffinity,omitempty"`

	// Replicas is the desired number of proxy pod replicas.
	// MCPRemoteProxy creates a single Deployment for the proxy process, so there
	// is only one replicas field (mirrors VirtualMCPServer.spec.replicas).
	// When nil, the operator does not set Deployment.Spec.Replicas, leaving replica
	// management to an HPA or other external controller.
	// When set above 1, also configure sessionStorage with the redis provider and
	// sessionAffinity: "None" so sessions resolve across replicas; otherwise a
	// SessionStorageWarning condition is surfaced on the resource status.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// SessionStorage configures session storage for stateful horizontal scaling.
	// When nil, no session storage is configured and the proxy falls back to
	// pod-local in-memory session state — incompatible with multi-replica
	// deployments behind load balancers that don't preserve client-IP affinity
	// (e.g. AWS ALB across multiple AZs).
	//
	// The transparent proxy validates `Mcp-Session-Id` against this store on
	// every non-initialize request (see pkg/transport/proxy/transparent/
	// transparent_proxy.go) and rewrites client-facing session IDs to backend
	// session IDs using session metadata. Both lookups require shared state
	// across replicas.
	//
	// When using the Redis provider, also set sessionAffinity to "None" so the
	// Service routes requests round-robin and all replicas rely on the shared
	// session store rather than pod-local state.
	//
	// Mirrors MCPServer.spec.sessionStorage and VirtualMCPServer.spec.sessionStorage.
	// +optional
	SessionStorage *SessionStorageConfig `json:"sessionStorage,omitempty"`
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
	ExternalURL string `json:"externalUrl,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed MCPRemoteProxy
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the MCPRemoteProxy's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ToolConfigHash stores the hash of the referenced ToolConfig for change detection
	// +optional
	ToolConfigHash string `json:"toolConfigHash,omitempty"`

	// TelemetryConfigHash stores the hash of the referenced MCPTelemetryConfig for change detection
	// +optional
	TelemetryConfigHash string `json:"telemetryConfigHash,omitempty"`

	// ExternalAuthConfigHash is the hash of the referenced MCPExternalAuthConfig spec
	// +optional
	ExternalAuthConfigHash string `json:"externalAuthConfigHash,omitempty"`

	// AuthServerConfigHash is the hash of the referenced authServerRef spec,
	// used to detect configuration changes and trigger reconciliation.
	// +optional
	AuthServerConfigHash string `json:"authServerConfigHash,omitempty"`

	// AuthzConfigHash is the hash of the referenced MCPAuthzConfig spec for change detection
	// +optional
	AuthzConfigHash string `json:"authzConfigHash,omitempty"`

	// OIDCConfigHash is the hash of the referenced MCPOIDCConfig spec for change detection
	// +optional
	OIDCConfigHash string `json:"oidcConfigHash,omitempty"`

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

	// ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated indicates whether the TelemetryConfigRef is valid
	ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated = "TelemetryConfigRefValidated"

	// ConditionTypeMCPRemoteProxyExternalAuthConfigValidated indicates whether the ExternalAuthConfigRef is valid
	ConditionTypeMCPRemoteProxyExternalAuthConfigValidated = "ExternalAuthConfigValidated"

	// ConditionTypeMCPRemoteProxyAuthServerRefValidated indicates whether the AuthServerRef is valid
	ConditionTypeMCPRemoteProxyAuthServerRefValidated = "AuthServerRefValidated"

	// ConditionTypeMCPRemoteProxyPodTemplateValid indicates whether the PodTemplateSpec is valid
	ConditionTypeMCPRemoteProxyPodTemplateValid = "PodTemplateValid"

	// ConditionTypeMCPRemoteProxyCABundleRefValidated indicates whether the OIDC CA bundle reference is valid
	ConditionTypeMCPRemoteProxyCABundleRefValidated = "CABundleRefValidated"

	// ConditionTypeConfigurationValid indicates whether the proxy spec has passed all pre-deployment validation checks
	ConditionTypeConfigurationValid = "ConfigurationValid"
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

	// ConditionReasonMCPRemoteProxyTelemetryConfigRefValid indicates the TelemetryConfigRef is valid
	ConditionReasonMCPRemoteProxyTelemetryConfigRefValid = "TelemetryConfigRefValid"

	// ConditionReasonMCPRemoteProxyTelemetryConfigRefNotFound indicates the referenced MCPTelemetryConfig was not found
	ConditionReasonMCPRemoteProxyTelemetryConfigRefNotFound = "TelemetryConfigRefNotFound"

	// ConditionReasonMCPRemoteProxyTelemetryConfigRefInvalid indicates the referenced MCPTelemetryConfig is invalid
	ConditionReasonMCPRemoteProxyTelemetryConfigRefInvalid = "TelemetryConfigRefInvalid"

	// ConditionReasonMCPRemoteProxyTelemetryConfigRefFetchError indicates an error occurred fetching the MCPTelemetryConfig
	ConditionReasonMCPRemoteProxyTelemetryConfigRefFetchError = "TelemetryConfigRefFetchError"

	// ConditionReasonMCPRemoteProxyExternalAuthConfigValid indicates the ExternalAuthConfigRef is valid
	ConditionReasonMCPRemoteProxyExternalAuthConfigValid = "ExternalAuthConfigValid"

	// ConditionReasonMCPRemoteProxyExternalAuthConfigNotFound indicates the referenced MCPExternalAuthConfig was not found
	ConditionReasonMCPRemoteProxyExternalAuthConfigNotFound = "ExternalAuthConfigNotFound"

	// ConditionReasonMCPRemoteProxyExternalAuthConfigFetchError indicates an error occurred fetching the MCPExternalAuthConfig
	ConditionReasonMCPRemoteProxyExternalAuthConfigFetchError = "ExternalAuthConfigFetchError"

	// ConditionReasonMCPRemoteProxyExternalAuthConfigMultiUpstream indicates multi-upstream is not supported
	// for MCPRemoteProxy (use VirtualMCPServer for multi-upstream).
	ConditionReasonMCPRemoteProxyExternalAuthConfigMultiUpstream = "MultiUpstreamNotSupported"

	// ConditionReasonMCPRemoteProxyAuthServerRefValid indicates the AuthServerRef is valid
	ConditionReasonMCPRemoteProxyAuthServerRefValid = "AuthServerRefValid"

	// ConditionReasonMCPRemoteProxyAuthServerRefNotFound indicates the referenced auth server config was not found
	ConditionReasonMCPRemoteProxyAuthServerRefNotFound = "AuthServerRefNotFound"

	// ConditionReasonMCPRemoteProxyAuthServerRefFetchError indicates an error occurred fetching the auth server config
	ConditionReasonMCPRemoteProxyAuthServerRefFetchError = "AuthServerRefFetchError"

	// ConditionReasonMCPRemoteProxyAuthServerRefInvalidKind indicates the authServerRef kind is not supported
	ConditionReasonMCPRemoteProxyAuthServerRefInvalidKind = "AuthServerRefInvalidKind"

	// ConditionReasonMCPRemoteProxyAuthServerRefInvalidType indicates the referenced config is not an embeddedAuthServer
	ConditionReasonMCPRemoteProxyAuthServerRefInvalidType = "AuthServerRefInvalidType"

	// ConditionReasonMCPRemoteProxyAuthServerRefMultiUpstream indicates multi-upstream is not supported
	ConditionReasonMCPRemoteProxyAuthServerRefMultiUpstream = "MultiUpstreamNotSupported"

	// ConditionReasonMCPRemoteProxyPodTemplateValid indicates PodTemplateSpec validation succeeded
	ConditionReasonMCPRemoteProxyPodTemplateValid = "ValidPodTemplateSpec"

	// ConditionReasonMCPRemoteProxyPodTemplateInvalid indicates PodTemplateSpec validation failed
	ConditionReasonMCPRemoteProxyPodTemplateInvalid = "InvalidPodTemplateSpec"

	// ConditionReasonMCPRemoteProxyCABundleRefValid indicates the CA bundle ref is valid and the ConfigMap exists
	ConditionReasonMCPRemoteProxyCABundleRefValid = "CABundleRefValid"

	// ConditionReasonMCPRemoteProxyCABundleRefNotFound indicates the referenced CA bundle ConfigMap was not found
	ConditionReasonMCPRemoteProxyCABundleRefNotFound = "CABundleRefNotFound"

	// ConditionReasonMCPRemoteProxyCABundleRefInvalid indicates the CA bundle ref configuration is invalid
	ConditionReasonMCPRemoteProxyCABundleRefInvalid = "CABundleRefInvalid"

	// ConditionReasonConfigurationValid indicates all configuration validations passed
	ConditionReasonConfigurationValid = "ConfigurationValid"

	// ConditionReasonOIDCIssuerInsecure indicates the OIDC issuer URL uses HTTP instead of HTTPS
	ConditionReasonOIDCIssuerInsecure = "OIDCIssuerInsecure"

	// ConditionReasonOIDCIssuerInvalid indicates the OIDC issuer URL is malformed
	ConditionReasonOIDCIssuerInvalid = "OIDCIssuerInvalid"

	// ConditionReasonAuthzPolicySyntaxInvalid indicates an inline Cedar policy has a syntax error
	ConditionReasonAuthzPolicySyntaxInvalid = "AuthzPolicySyntaxInvalid"

	// ConditionReasonAuthzConfigMapNotFound indicates the referenced authz ConfigMap was not found.
	// Shared with VirtualMCPServer (see virtualmcpserver_types.go); both reconcilers use this
	// reason when the ConfigMap itself is absent.
	ConditionReasonAuthzConfigMapNotFound = "AuthzConfigMapNotFound"

	// ConditionReasonAuthzConfigMapInvalid indicates the referenced authz ConfigMap was found
	// but its payload is missing/empty/malformed, fails validation, or does not contain a
	// Cedar-flavoured config. Shared with VirtualMCPServer; both reconcilers use this reason
	// to distinguish a malformed payload from a missing ConfigMap.
	ConditionReasonAuthzConfigMapInvalid = "AuthzConfigMapInvalid"

	// ConditionReasonHeaderSecretNotFound indicates a referenced header Secret was not found
	ConditionReasonHeaderSecretNotFound = "HeaderSecretNotFound"

	// ConditionReasonRemoteURLInvalid indicates the remoteUrl is malformed or has an invalid scheme
	ConditionReasonRemoteURLInvalid = "RemoteURLInvalid"

	// ConditionReasonJWKSURLInvalid indicates the JWKS URL is malformed or has an invalid scheme
	ConditionReasonJWKSURLInvalid = "JWKSURLInvalid"
)

//+kubebuilder:object:root=true
//+kubebuilder:storageversion
//+kubebuilder:subresource:status
//+kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
//+kubebuilder:resource:shortName=rp;mcprp,categories=toolhive
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Remote URL",type="string",JSONPath=".spec.remoteUrl"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
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

// GetProxyPort returns the proxy port of the MCPRemoteProxy
func (m *MCPRemoteProxy) GetProxyPort() int32 {
	if m.Spec.ProxyPort > 0 {
		return m.Spec.ProxyPort
	}
	return 8080
}
