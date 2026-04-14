// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Condition types for MCPServer
// Note: ConditionTypeReady is shared across multiple resources and defined in mcpremoteproxy_types.go
const (
	// ConditionGroupRefValidated indicates whether the GroupRef is valid
	ConditionGroupRefValidated = "GroupRefValidated"

	// ConditionPodTemplateValid indicates whether the PodTemplateSpec is valid
	ConditionPodTemplateValid = "PodTemplateValid"
)

const (
	// ConditionReasonReady indicates the MCPServer is ready
	ConditionReasonReady = "Ready"

	// ConditionReasonNotReady indicates the MCPServer is not ready
	ConditionReasonNotReady = "NotReady"
)

const (
	// ConditionReasonGroupRefValidated indicates the GroupRef is valid
	ConditionReasonGroupRefValidated = "GroupRefIsValid"

	// ConditionReasonGroupRefNotFound indicates the GroupRef is invalid
	ConditionReasonGroupRefNotFound = "GroupRefNotFound"

	// ConditionReasonGroupRefNotReady indicates the referenced MCPGroup is not in the Ready state
	ConditionReasonGroupRefNotReady = "GroupRefNotReady"
)

const (
	// ConditionReasonPodTemplateValid indicates PodTemplateSpec validation succeeded
	ConditionReasonPodTemplateValid = "ValidPodTemplateSpec"

	// ConditionReasonPodTemplateInvalid indicates PodTemplateSpec validation failed
	ConditionReasonPodTemplateInvalid = "InvalidPodTemplateSpec"
)

// Condition type for CA bundle validation
const (
	// ConditionCABundleRefValidated indicates whether the CABundleRef is valid
	ConditionCABundleRefValidated = "CABundleRefValidated"
)

// Condition type for MCPOIDCConfig reference validation
const (
	// ConditionOIDCConfigRefValidated indicates whether the OIDCConfigRef is valid
	ConditionOIDCConfigRefValidated = "OIDCConfigRefValidated"
)

const (
	// ConditionReasonOIDCConfigRefValid indicates the referenced MCPOIDCConfig is valid and ready
	ConditionReasonOIDCConfigRefValid = "OIDCConfigRefValid"

	// ConditionReasonOIDCConfigRefNotFound indicates the referenced MCPOIDCConfig was not found
	ConditionReasonOIDCConfigRefNotFound = "OIDCConfigRefNotFound"

	// ConditionReasonOIDCConfigRefNotValid indicates the referenced MCPOIDCConfig is not valid
	ConditionReasonOIDCConfigRefNotValid = "OIDCConfigRefNotValid"

	// ConditionReasonOIDCConfigRefError indicates an error occurred validating the OIDCConfigRef
	ConditionReasonOIDCConfigRefError = "OIDCConfigRefError"
)

const (
	// ConditionReasonCABundleRefValid indicates the CABundleRef is valid and the ConfigMap exists
	ConditionReasonCABundleRefValid = "CABundleRefValid"

	// ConditionReasonCABundleRefNotFound indicates the referenced ConfigMap was not found
	ConditionReasonCABundleRefNotFound = "CABundleRefNotFound"

	// ConditionReasonCABundleRefInvalid indicates the CABundleRef configuration is invalid
	ConditionReasonCABundleRefInvalid = "CABundleRefInvalid"
)

const (
	// ConditionTypeExternalAuthConfigValidated indicates whether the ExternalAuthConfig is valid
	ConditionTypeExternalAuthConfigValidated = "ExternalAuthConfigValidated"
)

const (
	// ConditionReasonExternalAuthConfigMultiUpstream indicates the ExternalAuthConfig has multiple upstreams,
	// which is not supported for MCPServer (use VirtualMCPServer for multi-upstream).
	ConditionReasonExternalAuthConfigMultiUpstream = "MultiUpstreamNotSupported"
)

const (
	// ConditionTypeAuthServerRefValidated indicates whether the AuthServerRef is valid
	ConditionTypeAuthServerRefValidated = "AuthServerRefValidated"
)

const (
	// ConditionReasonAuthServerRefValid indicates the referenced auth server config is valid
	ConditionReasonAuthServerRefValid = "AuthServerRefValid"

	// ConditionReasonAuthServerRefNotFound indicates the referenced auth server config was not found
	ConditionReasonAuthServerRefNotFound = "AuthServerRefNotFound"

	// ConditionReasonAuthServerRefFetchError indicates an error occurred fetching the auth server config
	ConditionReasonAuthServerRefFetchError = "AuthServerRefFetchError"

	// ConditionReasonAuthServerRefInvalidKind indicates the authServerRef kind is not supported
	ConditionReasonAuthServerRefInvalidKind = "AuthServerRefInvalidKind"

	// ConditionReasonAuthServerRefInvalidType indicates the referenced config is not an embeddedAuthServer
	ConditionReasonAuthServerRefInvalidType = "AuthServerRefInvalidType"

	// ConditionReasonAuthServerRefMultiUpstream indicates multi-upstream is not supported
	ConditionReasonAuthServerRefMultiUpstream = "MultiUpstreamNotSupported"
)

// ConditionTelemetryConfigRefValidated indicates whether the TelemetryConfigRef is valid
const ConditionTelemetryConfigRefValidated = "TelemetryConfigRefValidated"

const (
	// ConditionReasonTelemetryConfigRefValid indicates the referenced MCPTelemetryConfig is valid
	ConditionReasonTelemetryConfigRefValid = "TelemetryConfigRefValid"

	// ConditionReasonTelemetryConfigRefNotFound indicates the referenced MCPTelemetryConfig was not found
	ConditionReasonTelemetryConfigRefNotFound = "TelemetryConfigRefNotFound"

	// ConditionReasonTelemetryConfigRefInvalid indicates the referenced MCPTelemetryConfig is not valid
	ConditionReasonTelemetryConfigRefInvalid = "TelemetryConfigRefInvalid"

	// ConditionReasonTelemetryConfigRefError indicates a transient error occurred fetching the config
	ConditionReasonTelemetryConfigRefError = "TelemetryConfigRefError"
)

// ConditionStdioReplicaCapped indicates spec.replicas was capped at 1 for stdio transport.
const ConditionStdioReplicaCapped = "StdioReplicaCapped"

const (
	// ConditionReasonStdioReplicaCapped is set when spec.replicas > 1 for a stdio transport.
	ConditionReasonStdioReplicaCapped = "StdioTransportCapAt1"
	// ConditionReasonStdioReplicaCapNotActive is set when the stdio replica cap does not apply.
	ConditionReasonStdioReplicaCapNotActive = "StdioReplicaCapNotActive"
)

// ConditionSessionStorageWarning indicates replicas > 1 but no Redis session storage is configured.
const ConditionSessionStorageWarning = "SessionStorageWarning"

const (
	// ConditionReasonSessionStorageMissing is set when replicas > 1 and no Redis session storage is configured.
	ConditionReasonSessionStorageMissing = "SessionStorageMissingForReplicas"
	// ConditionReasonSessionStorageConfigured is set when replicas > 1 and Redis session storage is configured.
	ConditionReasonSessionStorageConfigured = "SessionStorageConfigured"
	// ConditionReasonSessionStorageNotApplicable is set when replicas is nil or <= 1 and the warning is not active.
	ConditionReasonSessionStorageNotApplicable = "SessionStorageWarningNotApplicable"
)

// ConditionRateLimitConfigValid indicates whether the rate limit configuration is valid.
const ConditionRateLimitConfigValid = "RateLimitConfigValid"

const (
	// ConditionReasonRateLimitConfigValid indicates the rate limit configuration is valid.
	ConditionReasonRateLimitConfigValid = "RateLimitConfigValid"
	// ConditionReasonRateLimitPerUserRequiresAuth indicates perUser rate limiting requires authentication.
	ConditionReasonRateLimitPerUserRequiresAuth = "PerUserRequiresAuth"
	// ConditionReasonRateLimitNotApplicable indicates rate limiting is not configured.
	ConditionReasonRateLimitNotApplicable = "RateLimitNotApplicable"
)

// SessionStorageProviderRedis is the provider name for Redis-backed session storage.
const SessionStorageProviderRedis = "redis"

// MCPServerSpec defines the desired state of MCPServer
//
// +kubebuilder:validation:XValidation:rule="!(has(self.oidcConfig) && has(self.oidcConfigRef))",message="oidcConfig and oidcConfigRef are mutually exclusive; use oidcConfigRef to reference a shared MCPOIDCConfig"
// +kubebuilder:validation:XValidation:rule="!has(self.rateLimiting) || (has(self.sessionStorage) && self.sessionStorage.provider == 'redis')",message="rateLimiting requires sessionStorage with provider 'redis'"
// +kubebuilder:validation:XValidation:rule="!(has(self.rateLimiting) && has(self.rateLimiting.perUser)) || has(self.oidcConfig) || has(self.oidcConfigRef) || has(self.externalAuthConfigRef)",message="rateLimiting.perUser requires authentication (oidcConfig, oidcConfigRef, or externalAuthConfigRef)"
// +kubebuilder:validation:XValidation:rule="!has(self.rateLimiting) || !has(self.rateLimiting.tools) || self.rateLimiting.tools.all(t, !has(t.perUser)) || has(self.oidcConfig) || has(self.oidcConfigRef) || has(self.externalAuthConfigRef)",message="per-tool perUser rate limiting requires authentication (oidcConfig, oidcConfigRef, or externalAuthConfigRef)"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPServerSpec struct {
	// Image is the container image for the MCP server
	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// Transport is the transport method for the MCP server (stdio, streamable-http or sse)
	// +kubebuilder:validation:Enum=stdio;streamable-http;sse
	// +kubebuilder:default=stdio
	Transport string `json:"transport,omitempty"`

	// ProxyMode is the proxy mode for stdio transport (sse or streamable-http)
	// This setting is ONLY applicable when Transport is "stdio".
	// For direct transports (sse, streamable-http), this field is ignored.
	// The default value is applied by Kubernetes but will be ignored for non-stdio transports.
	// +kubebuilder:validation:Enum=sse;streamable-http
	// +kubebuilder:default=streamable-http
	// +optional
	ProxyMode string `json:"proxyMode,omitempty"`

	// ProxyPort is the port to expose the proxy runner on
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +kubebuilder:default=8080
	ProxyPort int32 `json:"proxyPort,omitempty"`

	// MCPPort is the port that MCP server listens to
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	MCPPort int32 `json:"mcpPort,omitempty"`

	// Args are additional arguments to pass to the MCP server
	// +listType=atomic
	// +optional
	Args []string `json:"args,omitempty"`

	// Env are environment variables to set in the MCP server container
	// +listType=map
	// +listMapKey=name
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// Volumes are volumes to mount in the MCP server container
	// +listType=map
	// +listMapKey=name
	// +optional
	Volumes []Volume `json:"volumes,omitempty"`

	// Resources defines the resource requirements for the MCP server container
	// +optional
	Resources ResourceRequirements `json:"resources,omitempty"`

	// Secrets are references to secrets to mount in the MCP server container
	// +listType=map
	// +listMapKey=name
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
	// This field accepts a PodTemplateSpec object as JSON/YAML.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`

	// ResourceOverrides allows overriding annotations and labels for resources created by the operator
	// +optional
	ResourceOverrides *ResourceOverrides `json:"resourceOverrides,omitempty"`

	// OIDCConfig defines OIDC authentication configuration for the MCP server.
	// Deprecated: Use OIDCConfigRef to reference a shared MCPOIDCConfig resource instead.
	// This field will be removed in v1beta1. OIDCConfig and OIDCConfigRef are mutually exclusive.
	// +optional
	OIDCConfig *OIDCConfigRef `json:"oidcConfig,omitempty"`

	// OIDCConfigRef references a shared MCPOIDCConfig resource for OIDC authentication.
	// The referenced MCPOIDCConfig must exist in the same namespace as this MCPServer.
	// Per-server overrides (audience, scopes) are specified here; shared provider config
	// lives in the MCPOIDCConfig resource. Mutually exclusive with oidcConfig.
	// +optional
	OIDCConfigRef *MCPOIDCConfigReference `json:"oidcConfigRef,omitempty"`

	// AuthzConfig defines authorization policy configuration for the MCP server
	// +optional
	AuthzConfig *AuthzConfigRef `json:"authzConfig,omitempty"`

	// Audit defines audit logging configuration for the MCP server
	// +optional
	Audit *AuditConfig `json:"audit,omitempty"`

	// ToolConfigRef references a MCPToolConfig resource for tool filtering and renaming.
	// The referenced MCPToolConfig must exist in the same namespace as this MCPServer.
	// Cross-namespace references are not supported for security and isolation reasons.
	// +optional
	ToolConfigRef *ToolConfigRef `json:"toolConfigRef,omitempty"`

	// ExternalAuthConfigRef references a MCPExternalAuthConfig resource for external authentication.
	// The referenced MCPExternalAuthConfig must exist in the same namespace as this MCPServer.
	// +optional
	ExternalAuthConfigRef *ExternalAuthConfigRef `json:"externalAuthConfigRef,omitempty"`

	// AuthServerRef optionally references a resource that configures an embedded
	// OAuth 2.0/OIDC authorization server to authenticate MCP clients.
	// Currently the only supported kind is MCPExternalAuthConfig (type: embeddedAuthServer).
	// +optional
	AuthServerRef *AuthServerRef `json:"authServerRef,omitempty"`

	// TelemetryConfigRef references an MCPTelemetryConfig resource for shared telemetry configuration.
	// The referenced MCPTelemetryConfig must exist in the same namespace as this MCPServer.
	// Cross-namespace references are not supported for security and isolation reasons.
	// +optional
	TelemetryConfigRef *MCPTelemetryConfigReference `json:"telemetryConfigRef,omitempty"`

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

	// GroupRef references the MCPGroup this server belongs to.
	// The referenced MCPGroup must be in the same namespace.
	// +optional
	GroupRef *MCPGroupRef `json:"groupRef,omitempty"`

	// SessionAffinity controls whether the Service routes repeated client connections to the same pod.
	// MCP protocols (SSE, streamable-http) are stateful, so ClientIP is the default.
	// Set to "None" for stateless servers or when using an external load balancer with its own affinity.
	// +kubebuilder:validation:Enum=ClientIP;None
	// +kubebuilder:default=ClientIP
	// +optional
	SessionAffinity string `json:"sessionAffinity,omitempty"`

	// Replicas is the desired number of proxy runner (thv run) pod replicas.
	// MCPServer creates two separate Deployments: one for the proxy runner and one
	// for the MCP server backend. This field controls the proxy runner Deployment.
	// When nil, the operator does not set Deployment.Spec.Replicas, leaving replica
	// management to an HPA or other external controller.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// BackendReplicas is the desired number of MCP server backend pod replicas.
	// This controls the backend Deployment (the MCP server container itself),
	// independent of the proxy runner controlled by Replicas.
	// When nil, the operator does not set Deployment.Spec.Replicas, leaving replica
	// management to an HPA or other external controller.
	// +kubebuilder:validation:Minimum=0
	// +optional
	BackendReplicas *int32 `json:"backendReplicas,omitempty"`

	// SessionStorage configures session storage for stateful horizontal scaling.
	// When nil, no session storage is configured.
	// +optional
	SessionStorage *SessionStorageConfig `json:"sessionStorage,omitempty"`

	// RateLimiting defines rate limiting configuration for the MCP server.
	// Requires Redis session storage to be configured for distributed rate limiting.
	// +optional
	RateLimiting *RateLimitConfig `json:"rateLimiting,omitempty"`
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
	// Use TOOLHIVE_DEBUG=true to enable debug logging in the proxy
	// +listType=map
	// +listMapKey=name
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// ImagePullSecrets allows specifying image pull secrets for the proxy runner
	// These are applied to both the Deployment and the ServiceAccount
	// +listType=atomic
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
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

// SessionStorageConfig defines session storage configuration for horizontal scaling.
//
// This is the CRD/K8s-aware surface: it uses SecretKeyRef for secret resolution.
// The reconciler resolves PasswordRef to a plain string and builds a
// session.RedisConfig (pkg/transport/session) for the actual storage backend.
// The operator also populates pkg/vmcp/config.SessionStorageConfig (without PasswordRef)
// into the vMCP ConfigMap so the vMCP process receives connection parameters at startup.
//
// +kubebuilder:validation:XValidation:rule="self.provider == 'redis' ? has(self.address) : true",message="address is required"
type SessionStorageConfig struct {
	// Provider is the session storage backend type
	// +kubebuilder:validation:Enum=memory;redis
	// +kubebuilder:validation:Required
	Provider string `json:"provider"`

	// Address is the Redis server address (required when provider is redis)
	// +kubebuilder:validation:MinLength=1
	// +optional
	Address string `json:"address,omitempty"`

	// DB is the Redis database number
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	// +optional
	DB int32 `json:"db,omitempty"`

	// KeyPrefix is an optional prefix for all Redis keys used by ToolHive
	// +optional
	KeyPrefix string `json:"keyPrefix,omitempty"`

	// PasswordRef is a reference to a Secret key containing the Redis password
	// +optional
	PasswordRef *SecretKeyRef `json:"passwordRef,omitempty"`
}

// RateLimitConfig defines rate limiting configuration for an MCP server.
// At least one of shared, perUser, or tools must be configured.
//
// +kubebuilder:validation:XValidation:rule="has(self.shared) || has(self.perUser) || (has(self.tools) && size(self.tools) > 0)",message="at least one of shared, perUser, or tools must be configured"
//
//nolint:lll // CEL validation rules exceed line length limit
type RateLimitConfig struct {
	// Shared is a token bucket shared across all users for the entire server.
	// +optional
	Shared *RateLimitBucket `json:"shared,omitempty"`

	// PerUser is a token bucket applied independently to each authenticated user
	// at the server level. Requires authentication to be enabled.
	// Each unique userID creates Redis keys that expire after 2x refillPeriod.
	// Memory formula: unique_users_per_TTL_window * (1 + num_tools_with_per_user_limits) keys.
	// +optional
	PerUser *RateLimitBucket `json:"perUser,omitempty"`

	// Tools defines per-tool rate limit overrides.
	// Each entry applies additional rate limits to calls targeting a specific tool name.
	// A request must pass both the server-level limit and the per-tool limit.
	// +listType=map
	// +listMapKey=name
	// +optional
	Tools []ToolRateLimitConfig `json:"tools,omitempty"`
}

// RateLimitBucket defines a token bucket configuration with a maximum capacity
// and a refill period. Used by both shared (global) and per-user rate limits.
type RateLimitBucket struct {
	// MaxTokens is the maximum number of tokens (bucket capacity).
	// This is also the burst size: the maximum number of requests that can be served
	// instantaneously before the bucket is depleted.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	MaxTokens int32 `json:"maxTokens"`

	// RefillPeriod is the duration to fully refill the bucket from zero to maxTokens.
	// The effective refill rate is maxTokens / refillPeriod tokens per second.
	// Format: Go duration string (e.g., "1m0s", "30s", "1h0m0s").
	// +kubebuilder:validation:Required
	RefillPeriod metav1.Duration `json:"refillPeriod"`
}

// ToolRateLimitConfig defines rate limits for a specific tool.
// At least one of shared or perUser must be configured.
//
// +kubebuilder:validation:XValidation:rule="has(self.shared) || has(self.perUser)",message="at least one of shared or perUser must be configured"
//
//nolint:lll // kubebuilder marker exceeds line length
type ToolRateLimitConfig struct {
	// Name is the MCP tool name this limit applies to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Shared token bucket for this specific tool.
	// +optional
	Shared *RateLimitBucket `json:"shared,omitempty"`

	// PerUser token bucket configuration for this tool.
	// +optional
	PerUser *RateLimitBucket `json:"perUser,omitempty"`
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
	// +listType=atomic
	// +optional
	Read []string `json:"read,omitempty"`

	// Write is a list of paths that the MCP server can write to
	// +listType=atomic
	// +optional
	Write []string `json:"write,omitempty"`

	// Network defines the network permissions for the MCP server
	// +optional
	Network *NetworkPermissions `json:"network,omitempty"`
}

// NetworkPermissions defines the network permissions for an MCP server
type NetworkPermissions struct {
	// Mode specifies the network mode for the container (e.g., "host", "bridge", "none")
	// When empty, the default container runtime network mode is used
	// +optional
	Mode string `json:"mode,omitempty"`

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
	// +listType=set
	// +optional
	AllowHost []string `json:"allowHost,omitempty"`

	// AllowPort is a list of ports to allow connections to
	// +listType=set
	// +optional
	AllowPort []int32 `json:"allowPort,omitempty"`
}

// OIDCConfigRef defines a reference to OIDC configuration
//
// +kubebuilder:validation:XValidation:rule="self.type == 'configMap' ? has(self.configMap) : !has(self.configMap)",message="configMap must be set when type is 'configMap', and must not be set otherwise"
// +kubebuilder:validation:XValidation:rule="self.type == 'inline' ? has(self.inline) : !has(self.inline)",message="inline must be set when type is 'inline', and must not be set otherwise"
// +kubebuilder:validation:XValidation:rule="self.type != 'kubernetes' ? !has(self.kubernetes) : true",message="kubernetes must not be set when type is not 'kubernetes'"
//
//nolint:lll // CEL validation rules exceed line length limit
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

	// CABundleRef references a ConfigMap containing the CA certificate bundle.
	// When specified, ToolHive auto-mounts the ConfigMap and auto-computes ThvCABundlePath.
	// If the ConfigMap data contains an explicit thvCABundlePath key, it takes precedence.
	// +optional
	CABundleRef *CABundleSource `json:"caBundleRef,omitempty"`
}

// CABundleSource defines a source for CA certificate bundles.
type CABundleSource struct {
	// ConfigMapRef references a ConfigMap containing the CA certificate bundle.
	// If Key is not specified, it defaults to "ca.crt".
	// +optional
	ConfigMapRef *corev1.ConfigMapKeySelector `json:"configMapRef,omitempty"`
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

	// ClientSecretRef is a reference to a Kubernetes Secret containing the client secret
	// +optional
	ClientSecretRef *SecretKeyRef `json:"clientSecretRef,omitempty"`

	// CABundleRef references a ConfigMap containing the CA certificate bundle.
	// When specified, ToolHive auto-mounts the ConfigMap and auto-computes the CA bundle path.
	// +optional
	CABundleRef *CABundleSource `json:"caBundleRef,omitempty"`

	// JWKSAuthTokenPath is the path to file containing bearer token for JWKS/OIDC requests
	// The file must be mounted into the pod (e.g., via Secret volume)
	// +optional
	JWKSAuthTokenPath string `json:"jwksAuthTokenPath,omitempty"`

	// JWKSAllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses.
	// Use with caution - only enable for trusted internal IDPs.
	// Note: at runtime, if either JWKSAllowPrivateIP or ProtectedResourceAllowPrivateIP
	// is true, private IPs are allowed for all OIDC HTTP requests (JWKS, discovery, introspection).
	// +kubebuilder:default=false
	// +optional
	JWKSAllowPrivateIP bool `json:"jwksAllowPrivateIP"`

	// ProtectedResourceAllowPrivateIP allows protected resource endpoint on private IP addresses.
	// Use with caution - only enable for trusted internal IDPs or testing.
	// Note: at runtime, if either ProtectedResourceAllowPrivateIP or JWKSAllowPrivateIP
	// is true, private IPs are allowed for all OIDC HTTP requests (JWKS, discovery, introspection).
	// +kubebuilder:default=false
	// +optional
	ProtectedResourceAllowPrivateIP bool `json:"protectedResourceAllowPrivateIP"`

	// InsecureAllowHTTP allows HTTP (non-HTTPS) OIDC issuers for development/testing
	// WARNING: This is insecure and should NEVER be used in production
	// Only enable for local development, testing, or trusted internal networks
	// +kubebuilder:default=false
	// +optional
	InsecureAllowHTTP bool `json:"insecureAllowHTTP"`

	// Scopes is the list of OAuth scopes to advertise in the well-known endpoint (RFC 9728)
	// If empty, defaults to ["openid"]
	// +listType=atomic
	// +optional
	Scopes []string `json:"scopes,omitempty"`
}

// AuthzConfigRef defines a reference to authorization configuration
//
// +kubebuilder:validation:XValidation:rule="self.type == 'configMap' ? has(self.configMap) : !has(self.configMap)",message="configMap must be set when type is 'configMap', and must not be set otherwise"
// +kubebuilder:validation:XValidation:rule="self.type == 'inline' ? has(self.inline) : !has(self.inline)",message="inline must be set when type is 'inline', and must not be set otherwise"
//
//nolint:lll // CEL validation rules exceed line length limit
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

// ExternalAuthConfigRef defines a reference to a MCPExternalAuthConfig resource.
// The referenced MCPExternalAuthConfig must be in the same namespace as the MCPServer.
type ExternalAuthConfigRef struct {
	// Name is the name of the MCPExternalAuthConfig resource
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// AuthServerRef defines a reference to a resource that configures an embedded
// OAuth 2.0/OIDC authorization server. Currently only MCPExternalAuthConfig is supported;
// the enum will be extended when a dedicated auth server CRD is introduced.
type AuthServerRef struct {
	// Kind identifies the type of the referenced resource.
	// +kubebuilder:validation:Enum=MCPExternalAuthConfig
	// +kubebuilder:default=MCPExternalAuthConfig
	Kind string `json:"kind"`

	// Name is the name of the referenced resource in the same namespace.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ToolConfigRef defines a reference to a MCPToolConfig resource.
// The referenced MCPToolConfig must be in the same namespace as the MCPServer.
type ToolConfigRef struct {
	// Name is the name of the MCPToolConfig resource in the same namespace
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// MCPGroupRef defines a reference to an MCPGroup resource.
// The referenced MCPGroup must be in the same namespace.
type MCPGroupRef struct {
	// Name is the name of the MCPGroup resource in the same namespace
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// GetName returns the name, or empty string if the receiver is nil.
func (r *MCPGroupRef) GetName() string {
	if r == nil {
		return ""
	}
	return r.Name
}

// InlineAuthzConfig contains direct authorization configuration
type InlineAuthzConfig struct {
	// Policies is a list of Cedar policy strings
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
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
	// +kubebuilder:validation:Pattern=`^(0(\.\d+)?|1(\.0+)?)$`
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
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration reflects the generation most recently observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ToolConfigHash stores the hash of the referenced ToolConfig for change detection
	// +optional
	ToolConfigHash string `json:"toolConfigHash,omitempty"`

	// ExternalAuthConfigHash is the hash of the referenced MCPExternalAuthConfig spec
	// +optional
	ExternalAuthConfigHash string `json:"externalAuthConfigHash,omitempty"`

	// AuthServerConfigHash is the hash of the referenced authServerRef spec,
	// used to detect configuration changes and trigger reconciliation.
	// +optional
	AuthServerConfigHash string `json:"authServerConfigHash,omitempty"`

	// OIDCConfigHash is the hash of the referenced MCPOIDCConfig spec for change detection
	// +optional
	OIDCConfigHash string `json:"oidcConfigHash,omitempty"`

	// TelemetryConfigHash is the hash of the referenced MCPTelemetryConfig spec for change detection
	// +optional
	TelemetryConfigHash string `json:"telemetryConfigHash,omitempty"`

	// URL is the URL where the MCP server can be accessed
	// +optional
	URL string `json:"url,omitempty"`

	// Phase is the current phase of the MCPServer
	// +optional
	Phase MCPServerPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// ReadyReplicas is the number of ready proxy replicas
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
}

// MCPServerPhase is the phase of the MCPServer
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Terminating;Stopped
type MCPServerPhase string

const (
	// MCPServerPhasePending means the MCPServer is being created
	MCPServerPhasePending MCPServerPhase = "Pending"

	// MCPServerPhaseReady means the MCPServer is ready
	MCPServerPhaseReady MCPServerPhase = "Ready"

	// MCPServerPhaseFailed means the MCPServer failed to start
	MCPServerPhaseFailed MCPServerPhase = "Failed"

	// MCPServerPhaseTerminating means the MCPServer is being deleted
	MCPServerPhaseTerminating MCPServerPhase = "Terminating"

	// MCPServerPhaseStopped means the MCPServer is scaled to zero
	MCPServerPhaseStopped MCPServerPhase = "Stopped"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mcpserver;mcpservers,categories=toolhive
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
//+kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.readyReplicas"
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

// GetName returns the name of the MCPServer
func (m *MCPServer) GetName() string {
	return m.Name
}

// GetNamespace returns the namespace of the MCPServer
func (m *MCPServer) GetNamespace() string {
	return m.Namespace
}

// GetOIDCConfig returns the OIDC configuration reference
func (m *MCPServer) GetOIDCConfig() *OIDCConfigRef {
	return m.Spec.OIDCConfig
}

// GetProxyPort returns the proxy port of the MCPServer
func (m *MCPServer) GetProxyPort() int32 {
	if m.Spec.ProxyPort > 0 {
		return m.Spec.ProxyPort
	}
	return 8080
}

// GetMCPPort returns the MCP port of the MCPServer
func (m *MCPServer) GetMCPPort() int32 {
	if m.Spec.MCPPort > 0 {
		return m.Spec.MCPPort
	}
	return 8080
}

func init() {
	SchemeBuilder.Register(&MCPServer{}, &MCPServerList{})
}
