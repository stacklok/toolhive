package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IngressConfig defines the configuration for Ingress resources
type IngressConfig struct {
	// Enabled controls whether to create an Ingress resource
	// +kubebuilder:default=true
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Host is the hostname for the Ingress rule
	// +kubebuilder:validation:Required
	Host string `json:"host"`

	// Path is the path for the Ingress rule
	// +kubebuilder:default=/
	// +optional
	Path string `json:"path,omitempty"`

	// PathType specifies the interpretation of the Path matching
	// +kubebuilder:validation:Enum=Exact;Prefix;ImplementationSpecific
	// +kubebuilder:default=Prefix
	// +optional
	PathType string `json:"pathType,omitempty"`

	// IngressClassName is the name of the ingress class to use
	// +optional
	IngressClassName *string `json:"ingressClassName,omitempty"`

	// TLS configuration for the Ingress
	// +optional
	TLS *IngressTLSConfig `json:"tls,omitempty"`

	// Annotations to add to the Ingress resource
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// IngressTLSConfig defines TLS configuration for Ingress
type IngressTLSConfig struct {
	// Enabled controls whether to enable TLS
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// SecretName is the name of the secret containing the TLS certificate
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// MCPServerSpec defines the desired state of MCPServer
type MCPServerSpec struct {
	// Image is the container image for the MCP server
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Transport is the transport method for the MCP server (stdio or sse)
	// +kubebuilder:validation:Enum=stdio;sse
	// +kubebuilder:default=stdio
	Transport string `json:"transport,omitempty"`

	// Port is the port to expose the MCP server on
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	Port int32 `json:"port,omitempty"`

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

	// PermissionProfile defines the permission profile to use
	// +optional
	PermissionProfile *PermissionProfileRef `json:"permissionProfile,omitempty"`

	// PodTemplateSpec defines the pod template to use for the MCP server
	// This allows for customizing the pod configuration beyond what is provided by the other fields.
	// Note that to modify the specific container the MCP server runs in, you must specify
	// the `mcp` container name in the PodTemplateSpec.
	// +optional
	PodTemplateSpec *corev1.PodTemplateSpec `json:"podTemplateSpec,omitempty"`

	// Ingress configuration for exposing the MCP server externally
	// +optional
	Ingress *IngressConfig `json:"ingress,omitempty"`
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

	// AllowTransport is a list of transport protocols to allow (e.g., "tcp", "udp")
	// +optional
	AllowTransport []string `json:"allowTransport,omitempty"`

	// AllowHost is a list of hosts to allow connections to
	// +optional
	AllowHost []string `json:"allowHost,omitempty"`

	// AllowPort is a list of ports to allow connections to
	// +optional
	AllowPort []int32 `json:"allowPort,omitempty"`
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
