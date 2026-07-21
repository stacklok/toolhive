// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ratelimittypes "github.com/stacklok/toolhive/pkg/ratelimit/types"
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

const (
	// ConditionReasonSecretEnvRejected indicates an untrusted-flagged workload
	// attempted to source backend (mcp) container env from a Secret or ConfigMap,
	// or mount a Secret volume — rejected by the untrusted env gate.
	ConditionReasonSecretEnvRejected = "SecretEnvRejected"
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

	// ConditionAuthzConfigRefValidated indicates whether the AuthzConfigRef is valid
	ConditionAuthzConfigRefValidated = "AuthzConfigRefValidated"
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
	// ConditionReasonAuthzConfigRefValid indicates the referenced MCPAuthzConfig is valid and ready
	ConditionReasonAuthzConfigRefValid = "AuthzConfigRefValid"

	// ConditionReasonAuthzConfigRefNotFound indicates the referenced MCPAuthzConfig was not found
	ConditionReasonAuthzConfigRefNotFound = "AuthzConfigRefNotFound"

	// ConditionReasonAuthzConfigRefNotValid indicates the referenced MCPAuthzConfig is not valid
	ConditionReasonAuthzConfigRefNotValid = "AuthzConfigRefNotValid"

	// ConditionReasonAuthzConfigRefError indicates an error occurred validating the AuthzConfigRef
	ConditionReasonAuthzConfigRefError = "AuthzConfigRefError"
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

	// ConditionTypeWebhookConfigValidated indicates whether the WebhookConfig is valid
	ConditionTypeWebhookConfigValidated = "WebhookConfigValidated"

	// ConditionTypeAuthzPrimaryUpstreamProviderIgnored is an advisory condition set
	// when spec.authzConfig.inline.primaryUpstreamProvider is non-empty on a CR type
	// that has no embedded auth server (MCPServer / MCPRemoteProxy). The field has
	// no effect on those resources and is documented as VirtualMCPServer-only.
	//
	// Tied to the deprecated InlineAuthzConfig.PrimaryUpstreamProvider field
	// (see mcpserver_types.go). When that field is removed at end of the
	// deprecation cycle, this condition and ConditionReasonAuthzPrimaryUpstreamProviderIgnored
	// below should be removed in the same change: there is no other path that
	// fires this advisory.
	ConditionTypeAuthzPrimaryUpstreamProviderIgnored = "AuthzPrimaryUpstreamProviderIgnored"
)

const (
	// ConditionReasonExternalAuthConfigMultiUpstream indicates the ExternalAuthConfig has multiple upstreams,
	// which is not supported for MCPServer (use VirtualMCPServer for multi-upstream).
	ConditionReasonExternalAuthConfigMultiUpstream = "MultiUpstreamNotSupported"

	// ConditionReasonWebhookConfigInvalid indicates the referenced webhook config is invalid or missing
	ConditionReasonWebhookConfigInvalid = "WebhookConfigInvalid"

	// ConditionReasonAuthzPrimaryUpstreamProviderIgnored indicates that
	// primaryUpstreamProvider is set on a CR type without an embedded auth server,
	// where the field has no runtime effect.
	ConditionReasonAuthzPrimaryUpstreamProviderIgnored = "PrimaryUpstreamProviderIgnored"
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
// +kubebuilder:validation:XValidation:rule="!(has(self.authzConfig) && has(self.authzConfigRef))",message="authzConfig and authzConfigRef are mutually exclusive; use authzConfigRef to reference a shared MCPAuthzConfig"
// +kubebuilder:validation:XValidation:rule="!has(self.rateLimiting) || (has(self.sessionStorage) && self.sessionStorage.provider == 'redis')",message="rateLimiting requires sessionStorage with provider 'redis'"
// +kubebuilder:validation:XValidation:rule="!(has(self.rateLimiting) && has(self.rateLimiting.perUser)) || has(self.oidcConfigRef) || has(self.externalAuthConfigRef)",message="rateLimiting.perUser requires authentication (oidcConfigRef or externalAuthConfigRef)"
// +kubebuilder:validation:XValidation:rule="!has(self.rateLimiting) || !has(self.rateLimiting.tools) || self.rateLimiting.tools.all(t, !has(t.perUser)) || has(self.oidcConfigRef) || has(self.externalAuthConfigRef)",message="per-tool perUser rate limiting requires authentication (oidcConfigRef or externalAuthConfigRef)"
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

	// OIDCConfigRef references a shared MCPOIDCConfig resource for OIDC authentication.
	// The referenced MCPOIDCConfig must exist in the same namespace as this MCPServer.
	// Per-server overrides (audience, scopes) are specified here; shared provider config
	// lives in the MCPOIDCConfig resource.
	//
	// SECURITY: if this field is omitted and no other authentication source is configured,
	// the proxy runs UNAUTHENTICATED. It accepts every request that can reach its port and
	// forwards it to the MCP server under a synthetic local-user identity, with no token or
	// credential check. Set this field to enforce identity-based access control per request.
	// +optional
	OIDCConfigRef *MCPOIDCConfigReference `json:"oidcConfigRef,omitempty"`

	// AuthzConfig defines authorization policy configuration for the MCP server.
	// AuthzConfig and AuthzConfigRef are mutually exclusive.
	// +optional
	AuthzConfig *AuthzConfigRef `json:"authzConfig,omitempty"`

	// AuthzConfigRef references a shared MCPAuthzConfig resource for authorization.
	// The referenced MCPAuthzConfig must exist in the same namespace as this MCPServer.
	// Mutually exclusive with authzConfig.
	// +optional
	AuthzConfigRef *MCPAuthzConfigReference `json:"authzConfigRef,omitempty"`

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

	// WebhookConfigRef references a MCPWebhookConfig resource for webhook middleware configuration.
	// The referenced MCPWebhookConfig must exist in the same namespace as this MCPServer.
	// +optional
	WebhookConfigRef *WebhookConfigRef `json:"webhookConfigRef,omitempty"`

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
	RateLimiting *ratelimittypes.RateLimitConfig `json:"rateLimiting,omitempty"`
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
// +gendoc
type RateLimitConfig = ratelimittypes.RateLimitConfig

// RateLimitBucket defines a token bucket configuration with a maximum capacity
// and a refill period. Used by both shared and per-user rate limits.
// +gendoc
type RateLimitBucket = ratelimittypes.RateLimitBucket

// ToolRateLimitConfig defines rate limits for a specific tool.
// +gendoc
type ToolRateLimitConfig = ratelimittypes.ToolRateLimitConfig

// Permission profile types
const (
	// PermissionProfileTypeBuiltin is the type for built-in permission profiles
	PermissionProfileTypeBuiltin = "builtin"

	// PermissionProfileTypeConfigMap is the type for permission profiles stored in ConfigMaps
	PermissionProfileTypeConfigMap = "configmap"
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
// +gendoc
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

// CABundleSource defines a source for CA certificate bundles.
type CABundleSource struct {
	// ConfigMapRef references a ConfigMap containing the CA certificate bundle.
	// If Key is not specified, it defaults to "ca.crt".
	// +optional
	ConfigMapRef *corev1.ConfigMapKeySelector `json:"configMapRef,omitempty"`
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

	// GroupClaimName is the JWT claim key that contains group membership for the
	// principal. When set, takes priority over the well-known defaults
	// ("groups", "roles", "cognito:groups"). Use this for IDPs that place
	// groups under a URI-style claim (e.g. "https://example.com/groups"). When
	// Type is "configMap", a group_claim_name entry in the referenced ConfigMap
	// is overridden by this field if both are set.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	GroupClaimName string `json:"groupClaimName,omitempty"`

	// RoleClaimName is the JWT claim key that contains role membership for the
	// principal. When set, the claim is extracted separately from GroupClaimName
	// and both are mapped to the configured GroupEntityType. When Type is
	// "configMap", a role_claim_name entry in the referenced ConfigMap is
	// overridden by this field if both are set.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	RoleClaimName string `json:"roleClaimName,omitempty"`

	// GroupEntityType is the Cedar entity type name used for principal parent
	// UIDs synthesised from JWT group/role claims. Defaults to "THVGroup" when
	// empty. Must match the entity type used in the static entity store for
	// transitive `in` checks (e.g. `ClaimGroup → PlatformRole`) to resolve.
	// Namespaced names (`Foo::Bar`) are not yet supported. When Type is
	// "configMap", a group_entity_type entry in the referenced ConfigMap is
	// overridden by this field if both are set.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_]*$`
	GroupEntityType string `json:"groupEntityType,omitempty"`
}

// DeprecatedInlinePrimaryUpstreamProvider returns the legacy inline
// PrimaryUpstreamProvider value, or "" when the field or the AuthzConfigRef
// is nil. The field has moved to spec.authServerConfig.primaryUpstreamProvider
// on VirtualMCPServer; this accessor is the single read point for the
// deprecated location so callers can emit a deprecation warning when it
// returns a non-empty value.
func (r *AuthzConfigRef) DeprecatedInlinePrimaryUpstreamProvider() string {
	if r == nil || r.Inline == nil {
		return ""
	}
	return r.Inline.PrimaryUpstreamProvider
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

// WebhookConfigRef defines a reference to a MCPWebhookConfig resource.
// The referenced MCPWebhookConfig must be in the same namespace as the MCPServer.
type WebhookConfigRef struct {
	// Name is the name of the MCPWebhookConfig resource
	// +kubebuilder:validation:Required
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

// InlineAuthzConfig contains direct authorization configuration.
//
// Source-agnostic Cedar JWT-claim mapping settings (GroupClaimName,
// RoleClaimName, GroupEntityType) live on the parent AuthzConfigRef so they
// work the same way for inline and configMap-sourced authz.
type InlineAuthzConfig struct {
	// Policies is a list of Cedar policy strings
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	Policies []string `json:"policies"`

	// EntitiesJSON is a JSON string representing Cedar entities. Required when
	// transitive policies (e.g. `ClaimGroup → PlatformRole`) need a static
	// entity store; defaults to "[]".
	// +kubebuilder:default="[]"
	// +optional
	EntitiesJSON string `json:"entitiesJson,omitempty"`

	// PrimaryUpstreamProvider names the upstream IDP whose access token's
	// claims Cedar should evaluate.
	//
	// Deprecated: on VirtualMCPServer this field has moved to
	// spec.authServerConfig.primaryUpstreamProvider. The old location is
	// still read for one release for backward compatibility; the
	// VirtualMCPServer controller emits an AuthzPrimaryUpstreamProviderDeprecated
	// Warning event whenever it is consumed, and removal is planned for the
	// release after the deprecation cycle.
	//
	// On MCPServer and MCPRemoteProxy this field has always been a structural
	// no-op (those CRDs do not run an embedded auth server). Setting it
	// continues to surface the AuthzPrimaryUpstreamProviderIgnored advisory
	// condition; the deprecation does not change that behaviour.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	PrimaryUpstreamProvider string `json:"primaryUpstreamProvider,omitempty"`
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

	// AuthzConfigHash is the hash of the referenced MCPAuthzConfig spec for change detection
	// +optional
	AuthzConfigHash string `json:"authzConfigHash,omitempty"`

	// OIDCConfigHash is the hash of the referenced MCPOIDCConfig spec for change detection
	// +optional
	OIDCConfigHash string `json:"oidcConfigHash,omitempty"`

	// TelemetryConfigHash is the hash of the referenced MCPTelemetryConfig spec for change detection
	// +optional
	TelemetryConfigHash string `json:"telemetryConfigHash,omitempty"`

	// WebhookConfigHash is the hash of the referenced MCPWebhookConfig spec
	// +optional
	WebhookConfigHash string `json:"webhookConfigHash,omitempty"`

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
//+kubebuilder:storageversion
//+kubebuilder:subresource:status
//+kubebuilder:metadata:labels=toolhive.stacklok.dev/auto-migrate-storage-version=true
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
