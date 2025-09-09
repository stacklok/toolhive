package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MCPServerSpec defines the desired state of MCPServer
type MCPServerSpec struct {
	// Image is the container image for the MCP server
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Transport is the transport method for the MCP server (stdio, streamable-http or sse)
	// +kubebuilder:validation:Enum=stdio;streamable-http;sse
	// +kubebuilder:default=stdio
	Transport string `json:"transport,omitempty"`

	// Port is the port to expose the MCP server on
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	Port int32 `json:"port,omitempty"`

	// TargetPort is the port that MCP server listens to
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	TargetPort int32 `json:"targetPort,omitempty"`

	// Args are additional arguments to pass to the MCP server
	// +optional
	Args []string `json:"args,omitempty"`

	// Env are environment variables to set in the MCP server container
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// Volumes are volumes to mount in the MCP server container
	// +optional
	Volumes []Volume `json:"volumes,omitempty"`

	// Resources defines the resource requirements for the MCP server container
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`

	// Secrets are references to secrets to mount in the MCP server container
	// +optional
	Secrets []SecretRef `json:"secrets,omitempty"`

	// ServiceAccount is the name of an already existing service account to use by the MCP server.
	// If not specified, a ServiceAccount will be created automatically and used by the MCP server.
	// +optional
	ServiceAccount *string `json:"serviceAccount,omitempty"`

	// PermissionProfile defines the permission profile to use
	// +optional
	PermissionProfile *PermissionProfileRef `json:"permissionProfile,omitempty"`

	// PodTemplateSpec defines the pod template to use for the MCP server
	// This allows for customizing the pod configuration beyond what is provided by the other fields.
	// Note that to modify the specific container the MCP server runs in, you must specify
	// the `mcp` container name in the PodTemplateSpec.
	// +optional
	PodTemplateSpec *corev1.PodTemplateSpec `json:"podTemplateSpec,omitempty"`

	// ResourceOverrides allows overriding annotations and labels for resources created by the operator
	// +optional
	ResourceOverrides *ResourceOverrides `json:"resourceOverrides,omitempty"`

	// OIDCConfig defines OIDC authentication configuration for the MCP server
	// +optional
	OIDCConfig *OIDCConfigRef `json:"oidcConfig,omitempty"`

	// AuthzConfig defines authorization policy configuration for the MCP server
	// +optional
	AuthzConfig *AuthzConfigRef `json:"authzConfig,omitempty"`

	// Audit defines audit logging configuration for the MCP server
	// +optional
	Audit *AuditConfig `json:"audit,omitempty"`

	// ToolsFilter is the filter on tools applied to the MCP server
	// +optional
	ToolsFilter []string `json:"tools,omitempty"`

	// Telemetry defines observability configuration for the MCP server
	// +optional
	Telemetry *TelemetryConfig `json:"telemetry,omitempty"`
}

// ResourceOverrides defines overrides for annotations and labels on created resources
type ResourceOverrides struct {
	// ProxyDeployment defines overrides for the Proxy Deployment resource (toolhive proxy)
	// +optional
	ProxyDeployment *ProxyDeploymentOverrides `json:"proxyDeployment,omitempty"`

	// ProxyService defines overrides for the Proxy Service resource (points to the proxy deployment)
	// +optional
	ProxyService *ResourceMetadataOverrides `json:"proxyService,omitempty"`
}

// ProxyDeploymentOverrides defines overrides specific to the proxy deployment
type ProxyDeploymentOverrides struct {
	// ResourceMetadataOverrides is embedded to inherit annotations and labels fields
	ResourceMetadataOverrides `json:",inline"` // nolint:revive

	PodTemplateMetadataOverrides *ResourceMetadataOverrides `json:"podTemplateMetadataOverrides,omitempty"`

	// Env are environment variables to set in the proxy container (thv run process)
	// These affect the toolhive proxy itself, not the MCP server it manages
	// +optional
	Env []EnvVar `json:"env,omitempty"`
}

// ResourceMetadataOverrides defines metadata overrides for a resource
type ResourceMetadataOverrides struct {
	// Annotations to add or override on the resource
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// Labels to add or override on the resource
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// EnvVar represents an environment variable in a container
type EnvVar struct {
	// Name of the environment variable
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Value of the environment variable
	// +kubebuilder:validation:Required
	Value string `json:"value"`
}

// Volume represents a volume to mount in a container
type Volume struct {
	// Name is the name of the volume
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// HostPath is the path on the host to mount
	// +kubebuilder:validation:Required
	HostPath string `json:"hostPath"`

	// MountPath is the path in the container to mount to
	// +kubebuilder:validation:Required
	MountPath string `json:"mountPath"`

	// ReadOnly specifies whether the volume should be mounted read-only
	// +kubebuilder:default=false
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`
}

// ResourceRequirements describes the compute resource requirements
type ResourceRequirements struct {
	// Limits describes the maximum amount of compute resources allowed
	// +optional
	Limits ResourceList `json:"limits,omitempty"`

	// Requests describes the minimum amount of compute resources required
	// +optional
	Requests ResourceList `json:"requests,omitempty"`
}

// ResourceList is a set of (resource name, quantity) pairs
type ResourceList struct {
	// CPU is the CPU limit in cores (e.g., "500m" for 0.5 cores)
	// +optional
	CPU string `json:"cpu,omitempty"`

	// Memory is the memory limit in bytes (e.g., "64Mi" for 64 megabytes)
	// +optional
	Memory string `json:"memory,omitempty"`
}

// SecretRef is a reference to a secret
type SecretRef struct {
	// Name is the name of the secret
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key is the key in the secret itself
	// +kubebuilder:validation:Required
	Key string `json:"key"`

	// TargetEnvName is the environment variable to be used when setting up the secret in the MCP server
	// If left unspecified, it defaults to the key
	// +optional
	TargetEnvName string `json:"targetEnvName,omitempty"`
}

// Permission profile types
const (
	// PermissionProfileTypeBuiltin is the type for built-in permission profiles
	PermissionProfileTypeBuiltin = "builtin"

	// PermissionProfileTypeConfigMap is the type for permission profiles stored in ConfigMaps
	PermissionProfileTypeConfigMap = "configmap"
)

// OIDC configuration types
const (
	// OIDCConfigTypeKubernetes is the type for Kubernetes service account token validation
	OIDCConfigTypeKubernetes = "kubernetes"

	// OIDCConfigTypeConfigMap is the type for OIDC configuration stored in ConfigMaps
	OIDCConfigTypeConfigMap = "configMap"

	// OIDCConfigTypeInline is the type for inline OIDC configuration
	OIDCConfigTypeInline = "inline"
)

// Authorization configuration types
const (
	// AuthzConfigTypeConfigMap is the type for authorization configuration stored in ConfigMaps
	AuthzConfigTypeConfigMap = "configMap"

	// AuthzConfigTypeInline is the type for inline authorization configuration
	AuthzConfigTypeInline = "inline"
)

// PermissionProfileRef defines a reference to a permission profile
type PermissionProfileRef struct {
	// Type is the type of permission profile reference
	// +kubebuilder:validation:Enum=builtin;configmap
	// +kubebuilder:default=builtin
	Type string `json:"type"`

	// Name is the name of the permission profile
	// If Type is "builtin", Name must be one of: "none", "network"
	// If Type is "configmap", Name is the name of the ConfigMap
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key is the key in the ConfigMap that contains the permission profile
	// Only used when Type is "configmap"
	// +optional
	Key string `json:"key,omitempty"`
}

// PermissionProfileSpec defines the permissions for an MCP server
type PermissionProfileSpec struct {
	// Read is a list of paths that the MCP server can read from
	// +optional
	Read []string `json:"read,omitempty"`

	// Write is a list of paths that the MCP server can write to
	// +optional
	Write []string `json:"write,omitempty"`

	// Network defines the network permissions for the MCP server
	// +optional
	Network *NetworkPermissions `json:"network,omitempty"`
}

// NetworkPermissions defines the network permissions for an MCP server
type NetworkPermissions struct {
	// Outbound defines the outbound network permissions
	// +optional
	Outbound *OutboundNetworkPermissions `json:"outbound,omitempty"`
}

// OutboundNetworkPermissions defines the outbound network permissions
type OutboundNetworkPermissions struct {
	// InsecureAllowAll allows all outbound network connections (not recommended)
	// +kubebuilder:default=false
	// +optional
	InsecureAllowAll bool `json:"insecureAllowAll,omitempty"`

	// AllowHost is a list of hosts to allow connections to
	// +optional
	AllowHost []string `json:"allowHost,omitempty"`

	// AllowPort is a list of ports to allow connections to
	// +optional
	AllowPort []int32 `json:"allowPort,omitempty"`
}

// OIDCConfigRef defines a reference to OIDC configuration
type OIDCConfigRef struct {
	// Type is the type of OIDC configuration
	// +kubebuilder:validation:Enum=kubernetes;configMap;inline
	// +kubebuilder:default=kubernetes
	Type string `json:"type"`

	// ResourceURL is the explicit resource URL for OAuth discovery endpoint (RFC 9728)
	// If not specified, defaults to the in-cluster Kubernetes service URL
	// +optional
	ResourceURL string `json:"resourceUrl,omitempty"`

	// Kubernetes configures OIDC for Kubernetes service account token validation
	// Only used when Type is "kubernetes"
	// +optional
	Kubernetes *KubernetesOIDCConfig `json:"kubernetes,omitempty"`

	// ConfigMap references a ConfigMap containing OIDC configuration
	// Only used when Type is "configmap"
	// +optional
	ConfigMap *ConfigMapOIDCRef `json:"configMap,omitempty"`

	// Inline contains direct OIDC configuration
	// Only used when Type is "inline"
	// +optional
	Inline *InlineOIDCConfig `json:"inline,omitempty"`
}

// KubernetesOIDCConfig configures OIDC for Kubernetes service account token validation
type KubernetesOIDCConfig struct {
	// ServiceAccount is the name of the service account to validate tokens for
	// If empty, uses the pod's service account
	// +optional
	ServiceAccount string `json:"serviceAccount,omitempty"`

	// Namespace is the namespace of the service account
	// If empty, uses the MCPServer's namespace
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Audience is the expected audience for the token
	// +kubebuilder:default=toolhive
	// +optional
	Audience string `json:"audience,omitempty"`

	// Issuer is the OIDC issuer URL
	// +kubebuilder:default="https://kubernetes.default.svc"
	// +optional
	Issuer string `json:"issuer,omitempty"`

	// JWKSURL is the URL to fetch the JWKS from
	// If empty, OIDC discovery will be used to automatically determine the JWKS URL
	// +optional
	JWKSURL string `json:"jwksUrl,omitempty"`

	// IntrospectionURL is the URL for token introspection endpoint
	// If empty, OIDC discovery will be used to automatically determine the introspection URL
	// +optional
	IntrospectionURL string `json:"introspectionUrl,omitempty"`

	// UseClusterAuth enables using the Kubernetes cluster's CA bundle and service account token
	// When true, uses /var/run/secrets/kubernetes.io/serviceaccount/ca.crt for TLS verification
	// and /var/run/secrets/kubernetes.io/serviceaccount/token for bearer token authentication
	// Defaults to true if not specified
	// +optional
	UseClusterAuth *bool `json:"useClusterAuth"`
}

// ConfigMapOIDCRef references a ConfigMap containing OIDC configuration
type ConfigMapOIDCRef struct {
	// Name is the name of the ConfigMap
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key is the key in the ConfigMap that contains the OIDC configuration
	// +kubebuilder:default=oidc.json
	// +optional
	Key string `json:"key,omitempty"`
}

// InlineOIDCConfig contains direct OIDC configuration
type InlineOIDCConfig struct {
	// Issuer is the OIDC issuer URL
	// +kubebuilder:validation:Required
	Issuer string `json:"issuer"`

	// Audience is the expected audience for the token
	// +optional
	Audience string `json:"audience,omitempty"`

	// JWKSURL is the URL to fetch the JWKS from
	// +optional
	JWKSURL string `json:"jwksUrl,omitempty"`

	// IntrospectionURL is the URL for token introspection endpoint
	// +optional
	IntrospectionURL string `json:"introspectionUrl,omitempty"`

	// ClientID is the OIDC client ID
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// ClientSecret is the client secret for introspection (optional)
	// +optional
	ClientSecret string `json:"clientSecret,omitempty"`

	// ThvCABundlePath is the path to CA certificate bundle file for HTTPS requests
	// The file must be mounted into the pod (e.g., via ConfigMap or Secret volume)
	// +optional
	ThvCABundlePath string `json:"thvCABundlePath,omitempty"`

	// JWKSAuthTokenPath is the path to file containing bearer token for JWKS/OIDC requests
	// The file must be mounted into the pod (e.g., via Secret volume)
	// +optional
	JWKSAuthTokenPath string `json:"jwksAuthTokenPath,omitempty"`

	// JWKSAllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses
	// Use with caution - only enable for trusted internal IDPs
	// +kubebuilder:default=false
	// +optional
	JWKSAllowPrivateIP bool `json:"jwksAllowPrivateIP"`
}

// AuthzConfigRef defines a reference to authorization configuration
type AuthzConfigRef struct {
	// Type is the type of authorization configuration
	// +kubebuilder:validation:Enum=configMap;inline
	// +kubebuilder:default=configMap
	Type string `json:"type"`

	// ConfigMap references a ConfigMap containing authorization configuration
	// Only used when Type is "configMap"
	// +optional
	ConfigMap *ConfigMapAuthzRef `json:"configMap,omitempty"`

	// Inline contains direct authorization configuration
	// Only used when Type is "inline"
	// +optional
	Inline *InlineAuthzConfig `json:"inline,omitempty"`
}

// ConfigMapAuthzRef references a ConfigMap containing authorization configuration
type ConfigMapAuthzRef struct {
	// Name is the name of the ConfigMap
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Key is the key in the ConfigMap that contains the authorization configuration
	// +kubebuilder:default=authz.json
	// +optional
	Key string `json:"key,omitempty"`
}

// InlineAuthzConfig contains direct authorization configuration
type InlineAuthzConfig struct {
	// Policies is a list of Cedar policy strings
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Policies []string `json:"policies"`

	// EntitiesJSON is a JSON string representing Cedar entities
	// +kubebuilder:default="[]"
	// +optional
	EntitiesJSON string `json:"entitiesJson,omitempty"`
}

// AuditConfig defines audit logging configuration for the MCP server
type AuditConfig struct {
	// Enabled controls whether audit logging is enabled
	// When true, enables audit logging with default configuration
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// TelemetryConfig defines observability configuration for the MCP server
type TelemetryConfig struct {
	// OpenTelemetry defines OpenTelemetry configuration
	// +optional
	OpenTelemetry *OpenTelemetryConfig `json:"openTelemetry,omitempty"`

	// Prometheus defines Prometheus-specific configuration
	// +optional
	Prometheus *PrometheusConfig `json:"prometheus,omitempty"`
}

// OpenTelemetryConfig defines pure OpenTelemetry configuration
type OpenTelemetryConfig struct {
	// Enabled controls whether OpenTelemetry is enabled
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Endpoint is the OTLP endpoint URL for tracing and metrics
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ServiceName is the service name for telemetry
	// If not specified, defaults to the MCPServer name
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// Headers contains authentication headers for the OTLP endpoint
	// Specified as key=value pairs
	// +optional
	Headers []string `json:"headers,omitempty"`

	// Insecure indicates whether to use HTTP instead of HTTPS for the OTLP endpoint
	// +kubebuilder:default=false
	// +optional
	Insecure bool `json:"insecure,omitempty"`

	// Metrics defines OpenTelemetry metrics-specific configuration
	// +optional
	Metrics *OpenTelemetryMetricsConfig `json:"metrics,omitempty"`

	// Tracing defines OpenTelemetry tracing configuration
	// +optional
	Tracing *OpenTelemetryTracingConfig `json:"tracing,omitempty"`
}

// PrometheusConfig defines Prometheus-specific configuration
type PrometheusConfig struct {
	// Enabled controls whether Prometheus metrics endpoint is exposed
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// OpenTelemetryTracingConfig defines OpenTelemetry tracing configuration
type OpenTelemetryTracingConfig struct {
	// Enabled controls whether OTLP tracing is sent
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SamplingRate is the trace sampling rate (0.0-1.0)
	// +kubebuilder:default="0.05"
	// +optional
	SamplingRate string `json:"samplingRate,omitempty"`
}

// OpenTelemetryMetricsConfig defines OpenTelemetry metrics configuration
type OpenTelemetryMetricsConfig struct {
	// Enabled controls whether OTLP metrics are sent
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// MCPServerStatus defines the observed state of MCPServer
type MCPServerStatus struct {
	// Conditions represent the latest available observations of the MCPServer's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// URL is the URL where the MCP server can be accessed
	// +optional
	URL string `json:"url,omitempty"`

	// Phase is the current phase of the MCPServer
	// +optional
	Phase MCPServerPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`
}

// MCPServerPhase is the phase of the MCPServer
// +kubebuilder:validation:Enum=Pending;Running;Failed;Terminating
type MCPServerPhase string

const (
	// MCPServerPhasePending means the MCPServer is being created
	MCPServerPhasePending MCPServerPhase = "Pending"

	// MCPServerPhaseRunning means the MCPServer is running
	MCPServerPhaseRunning MCPServerPhase = "Running"

	// MCPServerPhaseFailed means the MCPServer failed to start
	MCPServerPhaseFailed MCPServerPhase = "Failed"

	// MCPServerPhaseTerminating means the MCPServer is being deleted
	MCPServerPhaseTerminating MCPServerPhase = "Terminating"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPServer is the Schema for the mcpservers API
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPServerSpec   `json:"spec,omitempty"`
	Status MCPServerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPServerList contains a list of MCPServer
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MCPServer{}, &MCPServerList{})
}
