// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Registry formats
const (
	// RegistryFormatToolHive is the native ToolHive registry format
	RegistryFormatToolHive = "toolhive"
	RegistryFormatUpstream = "upstream"
)

// MCPRegistrySpec defines the desired state of MCPRegistry
type MCPRegistrySpec struct {
	// DisplayName is a human-readable name for the registry
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Registries defines the configuration for the registry data sources
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Registries []MCPRegistryConfig `json:"registries"`

	// EnforceServers indicates whether MCPServers in this namespace must have their images
	// present in at least one registry in the namespace. When any registry in the namespace
	// has this field set to true, enforcement is enabled for the entire namespace.
	// MCPServers with images not found in any registry will be rejected.
	// When false (default), MCPServers can be deployed regardless of registry presence.
	// +kubebuilder:default=false
	// +optional
	EnforceServers bool `json:"enforceServers,omitempty"`

	// PodTemplateSpec defines the pod template to use for the registry API server
	// This allows for customizing the pod configuration beyond what is provided by the other fields.
	// Note that to modify the specific container the registry API server runs in, you must specify
	// the `registry-api` container name in the PodTemplateSpec.
	// This field accepts a PodTemplateSpec object as JSON/YAML.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`

	// DatabaseConfig defines the PostgreSQL database configuration for the registry API server.
	// If not specified, defaults will be used:
	//   - Host: "postgres"
	//   - Port: 5432
	//   - User: "db_app"
	//   - MigrationUser: "db_migrator"
	//   - Database: "registry"
	//   - SSLMode: "prefer"
	//   - MaxOpenConns: 10
	//   - MaxIdleConns: 2
	//   - ConnMaxLifetime: "30m"
	// +optional
	DatabaseConfig *MCPRegistryDatabaseConfig `json:"databaseConfig,omitempty"`

	// AuthConfig defines the authentication configuration for the registry API server.
	// If not specified, defaults to anonymous authentication.
	// +optional
	AuthConfig *MCPRegistryAuthConfig `json:"authConfig,omitempty"`
}

// MCPRegistryConfig defines the configuration for a registry data source
type MCPRegistryConfig struct {
	// Name is a unique identifier for this registry configuration within the MCPRegistry
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Format is the data format (toolhive, upstream)
	// +kubebuilder:validation:Enum=toolhive;upstream
	// +kubebuilder:default=toolhive
	Format string `json:"format,omitempty"`

	// ConfigMapRef defines the ConfigMap source configuration
	// Mutually exclusive with Git, API, and PVCRef
	// +optional
	ConfigMapRef *corev1.ConfigMapKeySelector `json:"configMapRef,omitempty"`

	// Git defines the Git repository source configuration
	// Mutually exclusive with ConfigMapRef, API, and PVCRef
	// +optional
	Git *GitSource `json:"git,omitempty"`

	// API defines the API source configuration
	// Mutually exclusive with ConfigMapRef, Git, and PVCRef
	// +optional
	API *APISource `json:"api,omitempty"`

	// PVCRef defines the PersistentVolumeClaim source configuration
	// Mutually exclusive with ConfigMapRef, Git, and API
	// +optional
	PVCRef *PVCSource `json:"pvcRef,omitempty"`

	// SyncPolicy defines the automatic synchronization behavior for this registry.
	// If specified, enables automatic synchronization at the given interval.
	// Manual synchronization is always supported via annotation-based triggers
	// regardless of this setting.
	// +optional
	SyncPolicy *SyncPolicy `json:"syncPolicy,omitempty"`

	// Filter defines include/exclude patterns for registry content
	// +optional
	Filter *RegistryFilter `json:"filter,omitempty"`
}

// GitSource defines Git repository source configuration
type GitSource struct {
	// Repository is the Git repository URL (HTTP/HTTPS/SSH)
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^(file:///|https?://|git@|ssh://|git://).*"
	Repository string `json:"repository"`

	// Branch is the Git branch to use (mutually exclusive with Tag and Commit)
	// +kubebuilder:validation:MinLength=1
	// +optional
	Branch string `json:"branch,omitempty"`

	// Tag is the Git tag to use (mutually exclusive with Branch and Commit)
	// +kubebuilder:validation:MinLength=1
	// +optional
	Tag string `json:"tag,omitempty"`

	// Commit is the Git commit SHA to use (mutually exclusive with Branch and Tag)
	// +kubebuilder:validation:MinLength=1
	// +optional
	Commit string `json:"commit,omitempty"`

	// Path is the path to the registry file within the repository
	// +kubebuilder:validation:Pattern=^.*\.json$
	// +kubebuilder:default=registry.json
	// +optional
	Path string `json:"path,omitempty"`

	// Auth defines optional authentication for private Git repositories.
	// When specified, enables HTTP Basic authentication using the provided
	// username and password/token from a Kubernetes Secret.
	// +optional
	Auth *GitAuthConfig `json:"auth,omitempty"`
}

// GitAuthConfig defines authentication settings for private Git repositories.
// Uses HTTP Basic authentication with username and password/token.
// The password is stored in a Kubernetes Secret and mounted as a file
// for the registry server to read.
type GitAuthConfig struct {
	// Username is the Git username for HTTP Basic authentication.
	// For GitHub/GitLab token-based auth, this is typically the literal string "git"
	// or the token itself depending on the provider.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Username string `json:"username"`

	// PasswordSecretRef references a Kubernetes Secret containing the password or token
	// for Git authentication. The secret value will be mounted as a file and its path
	// passed to the registry server via the git.auth.passwordFile configuration.
	//
	// Example secret:
	//   apiVersion: v1
	//   kind: Secret
	//   metadata:
	//     name: git-credentials
	//   stringData:
	//     token: <github token>
	//
	// Then reference it as:
	//   passwordSecretRef:
	//     name: git-credentials
	//     key: token
	//
	// +kubebuilder:validation:Required
	PasswordSecretRef corev1.SecretKeySelector `json:"passwordSecretRef"`
}

// APISource defines API source configuration for ToolHive Registry APIs
// Phase 1: Supports ToolHive API endpoints (no pagination)
// Phase 2: Will add support for upstream MCP Registry API with pagination
type APISource struct {
	// Endpoint is the base API URL (without path)
	// The controller will append the appropriate paths:
	// Phase 1 (ToolHive API):
	//   - /v0/servers - List all servers (single response, no pagination)
	//   - /v0/servers/{name} - Get specific server (future)
	//   - /v0/info - Get registry metadata (future)
	// Example: "http://my-registry-api.default.svc.cluster.local/api"
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^https?://.*"
	Endpoint string `json:"endpoint"`
}

// PVCSource defines PersistentVolumeClaim source configuration
type PVCSource struct {
	// ClaimName is the name of the PersistentVolumeClaim
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	ClaimName string `json:"claimName"`

	// Path is the relative path to the registry file within the PVC.
	// The PVC is mounted at /config/registry/{registryName}/.
	// The full file path becomes: /config/registry/{registryName}/{path}
	//
	// This design:
	// - Each registry gets its own mount point (consistent with ConfigMap sources)
	// - Multiple registries can share the same PVC by mounting it at different paths
	// - Users control PVC organization freely via the path field
	//
	// Examples:
	//   Registry "production" using PVC "shared-data" with path "prod/registry.json":
	//     PVC contains /prod/registry.json → accessed at /config/registry/production/prod/registry.json
	//
	//   Registry "development" using SAME PVC "shared-data" with path "dev/registry.json":
	//     PVC contains /dev/registry.json → accessed at /config/registry/development/dev/registry.json
	//     (Same PVC, different mount path)
	//
	//   Registry "staging" using DIFFERENT PVC "other-pvc" with path "registry.json":
	//     PVC contains /registry.json → accessed at /config/registry/staging/registry.json
	//     (Different PVC, independent mount)
	//
	//   Registry "team-a" with path "v1/servers.json":
	//     PVC contains /v1/servers.json → accessed at /config/registry/team-a/v1/servers.json
	//     (Subdirectories allowed in path)
	// +kubebuilder:validation:Pattern=^.*\.json$
	// +kubebuilder:default=registry.json
	// +optional
	Path string `json:"path,omitempty"`
}

// SyncPolicy defines automatic synchronization behavior.
// When specified, enables automatic synchronization at the given interval.
// Manual synchronization via annotation-based triggers is always available
// regardless of this policy setting.
type SyncPolicy struct {
	// Interval is the sync interval for automatic synchronization (Go duration format)
	// Examples: "1h", "30m", "24h"
	// +kubebuilder:validation:Pattern=^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$
	// +kubebuilder:validation:Required
	Interval string `json:"interval"`
}

// RegistryFilter defines include/exclude patterns for registry content
type RegistryFilter struct {
	// NameFilters defines name-based filtering
	// +optional
	NameFilters *NameFilter `json:"names,omitempty"`

	// Tags defines tag-based filtering
	// +optional
	Tags *TagFilter `json:"tags,omitempty"`
}

// NameFilter defines name-based filtering
type NameFilter struct {
	// Include is a list of glob patterns to include
	// +optional
	Include []string `json:"include,omitempty"`

	// Exclude is a list of glob patterns to exclude
	// +optional
	Exclude []string `json:"exclude,omitempty"`
}

// TagFilter defines tag-based filtering
type TagFilter struct {
	// Include is a list of tags to include
	// +optional
	Include []string `json:"include,omitempty"`

	// Exclude is a list of tags to exclude
	// +optional
	Exclude []string `json:"exclude,omitempty"`
}

// MCPRegistryDatabaseConfig defines PostgreSQL database configuration for the registry API server.
// Uses a two-user security model: separate users for operations and migrations.
type MCPRegistryDatabaseConfig struct {
	// Host is the database server hostname
	// +kubebuilder:default="postgres"
	// +optional
	Host string `json:"host,omitempty"`

	// Port is the database server port
	// +kubebuilder:default=5432
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port int `json:"port,omitempty"`

	// User is the application user (limited privileges: SELECT, INSERT, UPDATE, DELETE)
	// Credentials should be provided via pgpass file or environment variables
	// +kubebuilder:default="db_app"
	// +optional
	User string `json:"user,omitempty"`

	// MigrationUser is the migration user (elevated privileges: CREATE, ALTER, DROP)
	// Used for running database schema migrations
	// Credentials should be provided via pgpass file or environment variables
	// +kubebuilder:default="db_migrator"
	// +optional
	MigrationUser string `json:"migrationUser,omitempty"`

	// Database is the database name
	// +kubebuilder:default="registry"
	// +optional
	Database string `json:"database,omitempty"`

	// SSLMode is the SSL mode for the connection
	// Valid values: disable, allow, prefer, require, verify-ca, verify-full
	// +kubebuilder:validation:Enum=disable;allow;prefer;require;verify-ca;verify-full
	// +kubebuilder:default="prefer"
	// +optional
	SSLMode string `json:"sslMode,omitempty"`

	// MaxOpenConns is the maximum number of open connections to the database
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxOpenConns int `json:"maxOpenConns,omitempty"`

	// MaxIdleConns is the maximum number of idle connections in the pool
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxIdleConns int `json:"maxIdleConns,omitempty"`

	// ConnMaxLifetime is the maximum amount of time a connection may be reused (Go duration format)
	// Examples: "30m", "1h", "24h"
	// +kubebuilder:validation:Pattern=^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$
	// +kubebuilder:default="30m"
	// +optional
	ConnMaxLifetime string `json:"connMaxLifetime,omitempty"`

	// DBAppUserPasswordSecretRef references a Kubernetes Secret containing the password for the application database user.
	// The operator will use this password along with DBMigrationUserPasswordSecretRef to generate a pgpass file
	// that is mounted to the registry API container.
	//
	// +kubebuilder:validation:Required
	DBAppUserPasswordSecretRef corev1.SecretKeySelector `json:"dbAppUserPasswordSecretRef"`

	// DBMigrationUserPasswordSecretRef references a Kubernetes Secret containing the password for the migration database user.
	// The operator will use this password along with DBAppUserPasswordSecretRef to generate a pgpass file
	// that is mounted to the registry API container.
	//
	// +kubebuilder:validation:Required
	DBMigrationUserPasswordSecretRef corev1.SecretKeySelector `json:"dbMigrationUserPasswordSecretRef"`
}

// MCPRegistryAuthMode represents the authentication mode for the registry API server
type MCPRegistryAuthMode string

const (
	// MCPRegistryAuthModeAnonymous allows unauthenticated access
	MCPRegistryAuthModeAnonymous MCPRegistryAuthMode = "anonymous"

	// MCPRegistryAuthModeOAuth enables OAuth/OIDC authentication
	MCPRegistryAuthModeOAuth MCPRegistryAuthMode = "oauth"
)

// MCPRegistryAuthConfig defines authentication configuration for the registry API server.
type MCPRegistryAuthConfig struct {
	// Mode specifies the authentication mode (anonymous or oauth)
	// Defaults to "anonymous" if not specified.
	// Use "oauth" to enable OAuth/OIDC authentication.
	// +kubebuilder:validation:Enum=anonymous;oauth
	// +kubebuilder:default="anonymous"
	// +optional
	Mode MCPRegistryAuthMode `json:"mode,omitempty"`

	// OAuth defines OAuth/OIDC specific authentication settings
	// Only used when Mode is "oauth"
	// +optional
	OAuth *MCPRegistryOAuthConfig `json:"oauth,omitempty"`
}

// MCPRegistryOAuthConfig defines OAuth/OIDC specific authentication settings
type MCPRegistryOAuthConfig struct {
	// ResourceURL is the URL identifying this protected resource (RFC 9728)
	// Used in the /.well-known/oauth-protected-resource endpoint
	// +optional
	ResourceURL string `json:"resourceUrl,omitempty"`

	// Providers defines the OAuth/OIDC providers for authentication
	// Multiple providers can be configured (e.g., Kubernetes + external IDP)
	// +kubebuilder:validation:MinItems=1
	// +optional
	Providers []MCPRegistryOAuthProviderConfig `json:"providers,omitempty"`

	// ScopesSupported defines the OAuth scopes supported by this resource (RFC 9728)
	// Defaults to ["mcp-registry:read", "mcp-registry:write"] if not specified
	// +optional
	ScopesSupported []string `json:"scopesSupported,omitempty"`

	// Realm is the protection space identifier for WWW-Authenticate header (RFC 7235)
	// Defaults to "mcp-registry" if not specified
	// +optional
	Realm string `json:"realm,omitempty"`
}

// MCPRegistryOAuthProviderConfig defines configuration for an OAuth/OIDC provider
type MCPRegistryOAuthProviderConfig struct {
	// Name is a unique identifier for this provider (e.g., "kubernetes", "keycloak")
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// IssuerURL is the OIDC issuer URL (e.g., https://accounts.google.com)
	// The JWKS URL will be discovered automatically from .well-known/openid-configuration
	// unless JwksUrl is explicitly specified
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^https?://.*"
	IssuerURL string `json:"issuerUrl"`

	// JwksUrl is the URL to fetch the JSON Web Key Set (JWKS) from
	// If specified, OIDC discovery is skipped and this URL is used directly
	// Example: https://kubernetes.default.svc/openid/v1/jwks
	// +kubebuilder:validation:Pattern="^https?://.*"
	// +optional
	JwksUrl string `json:"jwksUrl,omitempty"`

	// Audience is the expected audience claim in the token (REQUIRED)
	// Per RFC 6749 Section 4.1.3, tokens must be validated against expected audience
	// For Kubernetes, this is typically the API server URL
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Audience string `json:"audience"`

	// ClientID is the OAuth client ID for token introspection (optional)
	// +optional
	ClientID string `json:"clientId,omitempty"`

	// ClientSecretRef is a reference to a Secret containing the client secret
	// The secret should have a key "clientSecret" containing the secret value
	// +optional
	ClientSecretRef *corev1.SecretKeySelector `json:"clientSecretRef,omitempty"`

	// CACertRef is a reference to a ConfigMap containing the CA certificate bundle
	// for verifying the provider's TLS certificate.
	// Required for Kubernetes in-cluster authentication or self-signed certificates
	// +optional
	CACertRef *corev1.ConfigMapKeySelector `json:"caCertRef,omitempty"`

	// CaCertPath is the path to the CA certificate bundle for verifying the provider's TLS certificate.
	// Required for Kubernetes in-cluster authentication or self-signed certificates
	// +optional
	CaCertPath string `json:"caCertPath,omitempty"`

	// AuthTokenRef is a reference to a Secret containing a bearer token for authenticating
	// to OIDC/JWKS endpoints. Useful when the OIDC discovery or JWKS endpoint requires authentication.
	// Example: ServiceAccount token for Kubernetes API server
	// +optional
	AuthTokenRef *corev1.SecretKeySelector `json:"authTokenRef,omitempty"`

	// AuthTokenFile is the path to a file containing a bearer token for authenticating to OIDC/JWKS endpoints.
	// Useful when the OIDC discovery or JWKS endpoint requires authentication.
	// Example: /var/run/secrets/kubernetes.io/serviceaccount/token
	// +optional
	AuthTokenFile string `json:"authTokenFile,omitempty"`

	// IntrospectionURL is the OAuth 2.0 Token Introspection endpoint (RFC 7662)
	// Used for validating opaque (non-JWT) tokens
	// If not specified, only JWT tokens can be validated via JWKS
	// +kubebuilder:validation:Pattern="^https?://.*"
	// +optional
	IntrospectionURL string `json:"introspectionUrl,omitempty"`

	// AllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses
	// Required when the OAuth provider (e.g., Kubernetes API server) is running on a private network
	// Example: Set to true when using https://kubernetes.default.svc as the issuer URL
	// +kubebuilder:default=false
	// +optional
	AllowPrivateIP bool `json:"allowPrivateIP,omitempty"`
}

// MCPRegistryStatus defines the observed state of MCPRegistry
type MCPRegistryStatus struct {
	// Phase represents the current overall phase of the MCPRegistry
	// Derived from sync and API status
	// +optional
	Phase MCPRegistryPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// SyncStatus provides detailed information about data synchronization
	// +optional
	SyncStatus *SyncStatus `json:"syncStatus,omitempty"`

	// APIStatus provides detailed information about the API service
	// +optional
	APIStatus *APIStatus `json:"apiStatus,omitempty"`

	// LastAppliedFilterHash is the hash of the last applied filter
	// +optional
	LastAppliedFilterHash string `json:"lastAppliedFilterHash,omitempty"`

	// StorageRef is a reference to the internal storage location
	// +optional
	StorageRef *StorageReference `json:"storageRef,omitempty"`

	// LastManualSyncTrigger tracks the last processed manual sync annotation value
	// Used to detect new manual sync requests via toolhive.stacklok.dev/sync-trigger annotation
	// +optional
	LastManualSyncTrigger string `json:"lastManualSyncTrigger,omitempty"`

	// Conditions represent the latest available observations of the MCPRegistry's state
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// SyncStatus provides detailed information about data synchronization
type SyncStatus struct {
	// Phase represents the current synchronization phase
	// +kubebuilder:validation:Enum=Syncing;Complete;Failed
	Phase SyncPhase `json:"phase"`

	// Message provides additional information about the sync status
	// +optional
	Message string `json:"message,omitempty"`

	// LastAttempt is the timestamp of the last sync attempt
	// +optional
	LastAttempt *metav1.Time `json:"lastAttempt,omitempty"`

	// AttemptCount is the number of sync attempts since last success
	// +optional
	// +kubebuilder:validation:Minimum=0
	AttemptCount int `json:"attemptCount,omitempty"`

	// LastSyncTime is the timestamp of the last successful sync
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// LastSyncHash is the hash of the last successfully synced data
	// Used to detect changes in source data
	// +optional
	LastSyncHash string `json:"lastSyncHash,omitempty"`

	// ServerCount is the total number of servers in the registry
	// +optional
	// +kubebuilder:validation:Minimum=0
	ServerCount int `json:"serverCount,omitempty"`
}

// APIStatus provides detailed information about the API service
type APIStatus struct {
	// Phase represents the current API service phase
	// +kubebuilder:validation:Enum=NotStarted;Deploying;Ready;Unhealthy;Error
	Phase APIPhase `json:"phase"`

	// Message provides additional information about the API status
	// +optional
	Message string `json:"message,omitempty"`

	// Endpoint is the URL where the API is accessible
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ReadySince is the timestamp when the API became ready
	// +optional
	ReadySince *metav1.Time `json:"readySince,omitempty"`
}

// SyncPhase represents the data synchronization state
// +kubebuilder:validation:Enum=Syncing;Complete;Failed
type SyncPhase string

const (
	// SyncPhaseSyncing means sync is currently in progress
	SyncPhaseSyncing SyncPhase = "Syncing"

	// SyncPhaseComplete means sync completed successfully
	SyncPhaseComplete SyncPhase = "Complete"

	// SyncPhaseFailed means sync failed
	SyncPhaseFailed SyncPhase = "Failed"
)

// APIPhase represents the API service state
// +kubebuilder:validation:Enum=NotStarted;Deploying;Ready;Unhealthy;Error
type APIPhase string

const (
	// APIPhaseNotStarted means API deployment has not been created
	APIPhaseNotStarted APIPhase = "NotStarted"

	// APIPhaseDeploying means API is being deployed
	APIPhaseDeploying APIPhase = "Deploying"

	// APIPhaseReady means API is ready to serve requests
	APIPhaseReady APIPhase = "Ready"

	// APIPhaseUnhealthy means API is deployed but not healthy
	APIPhaseUnhealthy APIPhase = "Unhealthy"

	// APIPhaseError means API deployment failed
	APIPhaseError APIPhase = "Error"
)

// StorageReference defines a reference to internal storage
type StorageReference struct {
	// Type is the storage type (configmap)
	// +kubebuilder:validation:Enum=configmap
	Type string `json:"type"`

	// ConfigMapRef is a reference to a ConfigMap storage
	// Only used when Type is "configmap"
	// +optional
	ConfigMapRef *corev1.LocalObjectReference `json:"configMapRef,omitempty"`
}

// MCPRegistryPhase represents the phase of the MCPRegistry
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Syncing;Terminating
type MCPRegistryPhase string

const (
	// MCPRegistryPhasePending means the MCPRegistry is being initialized
	MCPRegistryPhasePending MCPRegistryPhase = "Pending"

	// MCPRegistryPhaseReady means the MCPRegistry is ready and operational
	MCPRegistryPhaseReady MCPRegistryPhase = "Ready"

	// MCPRegistryPhaseFailed means the MCPRegistry has failed
	MCPRegistryPhaseFailed MCPRegistryPhase = "Failed"

	// MCPRegistryPhaseSyncing means the MCPRegistry is currently syncing data
	MCPRegistryPhaseSyncing MCPRegistryPhase = "Syncing"

	// MCPRegistryPhaseTerminating means the MCPRegistry is being deleted
	MCPRegistryPhaseTerminating MCPRegistryPhase = "Terminating"
)

// Condition types for MCPRegistry
const (
	// ConditionSourceAvailable indicates whether the source is available and accessible
	ConditionSourceAvailable = "SourceAvailable"

	// ConditionDataValid indicates whether the registry data is valid
	ConditionDataValid = "DataValid"

	// ConditionSyncSuccessful indicates whether the last sync was successful
	ConditionSyncSuccessful = "SyncSuccessful"

	// ConditionAPIReady indicates whether the registry API is ready
	ConditionAPIReady = "APIReady"

	// ConditionRegistryPodTemplateValid indicates whether the PodTemplateSpec is valid
	ConditionRegistryPodTemplateValid = "PodTemplateValid"
)

// Condition reasons for MCPRegistry PodTemplateSpec validation
const (
	// ConditionReasonRegistryPodTemplateValid indicates PodTemplateSpec validation succeeded
	ConditionReasonRegistryPodTemplateValid = "ValidPodTemplateSpec"

	// ConditionReasonRegistryPodTemplateInvalid indicates PodTemplateSpec validation failed
	ConditionReasonRegistryPodTemplateInvalid = "InvalidPodTemplateSpec"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Sync",type="string",JSONPath=".status.syncStatus.phase"
//+kubebuilder:printcolumn:name="API",type="string",JSONPath=".status.apiStatus.phase"
//+kubebuilder:printcolumn:name="Servers",type="integer",JSONPath=".status.syncStatus.serverCount"
//+kubebuilder:printcolumn:name="Last Sync",type="date",JSONPath=".status.syncStatus.lastSyncTime"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:resource:scope=Namespaced,categories=toolhive
//nolint:lll
//+kubebuilder:validation:XValidation:rule="size(self.spec.registries) > 0",message="at least one registry must be specified"

// MCPRegistry is the Schema for the mcpregistries API
type MCPRegistry struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MCPRegistrySpec   `json:"spec,omitempty"`
	Status MCPRegistryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPRegistryList contains a list of MCPRegistry
type MCPRegistryList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPRegistry `json:"items"`
}

// GetStorageName returns the name used for registry storage resources
func (r *MCPRegistry) GetStorageName() string {
	return fmt.Sprintf("%s-registry-storage", r.Name)
}

// GetAPIResourceName returns the base name for registry API resources (deployment, service)
func (r *MCPRegistry) GetAPIResourceName() string {
	return fmt.Sprintf("%s-api", r.Name)
}

// DeriveOverallPhase determines the overall MCPRegistry phase based on sync and API status
func (r *MCPRegistry) DeriveOverallPhase() MCPRegistryPhase {
	syncStatus := r.Status.SyncStatus
	apiStatus := r.Status.APIStatus

	// Default phases if status not set
	var syncPhase SyncPhase
	if syncStatus != nil {
		syncPhase = syncStatus.Phase
	}

	apiPhase := APIPhaseNotStarted
	if apiStatus != nil {
		apiPhase = apiStatus.Phase
	}

	// If sync failed, overall is Failed
	if syncPhase == SyncPhaseFailed {
		return MCPRegistryPhaseFailed
	}

	// If sync in progress, overall is Syncing
	if syncPhase == SyncPhaseSyncing {
		return MCPRegistryPhaseSyncing
	}

	// If sync is complete (no sync needed), check API status
	if syncPhase == SyncPhaseComplete {
		switch apiPhase {
		case APIPhaseReady:
			return MCPRegistryPhaseReady
		case APIPhaseError:
			return MCPRegistryPhaseFailed
		case APIPhaseNotStarted, APIPhaseDeploying, APIPhaseUnhealthy:
			return MCPRegistryPhasePending // API still starting/not healthy
		}
	}

	// Default to pending for initial states
	return MCPRegistryPhasePending
}

func init() {
	SchemeBuilder.Register(&MCPRegistry{}, &MCPRegistryList{})
}

// HasPodTemplateSpec returns true if the MCPRegistry has a PodTemplateSpec
func (r *MCPRegistry) HasPodTemplateSpec() bool {
	return r.Spec.PodTemplateSpec != nil
}

// GetPodTemplateSpecRaw returns the raw PodTemplateSpec
func (r *MCPRegistry) GetPodTemplateSpecRaw() *runtime.RawExtension {
	return r.Spec.PodTemplateSpec
}

// BuildPGPassSecretName returns the name of the generated pgpass secret for this registry
func (r *MCPRegistry) BuildPGPassSecretName() string {
	return fmt.Sprintf("%s-db-pgpass", r.Name)
}

// HasDatabaseConfig returns true if the MCPRegistry has a valid database configuration.
// A valid configuration requires:
// - DatabaseConfig to be non-nil
// - Host to be specified
// - Database to be specified
// - User to be specified
// - MigrationUser to be specified
// - DBAppUserPasswordSecretRef.Name to be specified
// - DBMigrationUserPasswordSecretRef.Name to be specified
func (r *MCPRegistry) HasDatabaseConfig() bool {
	if r.Spec.DatabaseConfig == nil {
		return false
	}

	dbConfig := r.Spec.DatabaseConfig

	// All required fields must be specified
	if dbConfig.Host == "" {
		return false
	}
	if dbConfig.Database == "" {
		return false
	}
	if dbConfig.User == "" {
		return false
	}
	if dbConfig.MigrationUser == "" {
		return false
	}
	if dbConfig.DBAppUserPasswordSecretRef.Name == "" {
		return false
	}
	if dbConfig.DBMigrationUserPasswordSecretRef.Name == "" {
		return false
	}

	return true
}

// GetDatabaseConfig returns the database configuration.
// Callers should check HasDatabaseConfig() before calling this method.
func (r *MCPRegistry) GetDatabaseConfig() *MCPRegistryDatabaseConfig {
	return r.Spec.DatabaseConfig
}

// GetDatabasePort returns the database port.
// If the port is not specified, it returns 5432.
// We do this because its likely to be 5432 due to
// it being the default port for PostgreSQL.
func (r *MCPRegistry) GetDatabasePort() int {
	if r.Spec.DatabaseConfig == nil || r.Spec.DatabaseConfig.Port == 0 {
		return 5432
	}
	return r.Spec.DatabaseConfig.Port
}
