// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Registry formats
const (
	// RegistryFormatToolHive is the native ToolHive registry format
	RegistryFormatToolHive = "toolhive"
	// RegistryFormatUpstream is the standard MCP registry format
	RegistryFormatUpstream = "upstream"
)

// MCPRegistrySpec defines the desired state of MCPRegistry
type MCPRegistrySpec struct {
	// ============================================================
	// New decoupled config fields
	// ============================================================

	// ConfigYAML is the complete registry server config.yaml content.
	// The operator creates a ConfigMap from this string and mounts it
	// at /config/config.yaml in the registry-api container.
	// The operator does NOT parse, validate, or transform this content.
	//
	// Mutually exclusive with the legacy typed fields (Sources, Registries,
	// DatabaseConfig, AuthConfig, TelemetryConfig). When set, the operator
	// uses the decoupled code path — volumes and mounts must be provided
	// via the Volumes and VolumeMounts fields below.
	//
	// +optional
	ConfigYAML string `json:"configYAML,omitempty"`

	// Volumes defines additional volumes to add to the registry API pod.
	// Each entry is a standard Kubernetes Volume object (JSON/YAML).
	// The operator appends them to the pod spec alongside its own config volume.
	// Only used when configYAML is set.
	//
	// Use these to mount:
	//   - Secrets (git auth tokens, OAuth client secrets, CA certs)
	//   - ConfigMaps (registry data files)
	//   - PersistentVolumeClaims (registry data on persistent storage)
	//   - Any other volume type the registry server needs
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:pruning:PreserveUnknownFields
	Volumes []apiextensionsv1.JSON `json:"volumes,omitempty"`

	// VolumeMounts defines additional volume mounts for the registry-api container.
	// Each entry is a standard Kubernetes VolumeMount object (JSON/YAML).
	// The operator appends them to the container's volume mounts alongside the config mount.
	// Only used when configYAML is set.
	//
	// Mount paths must match the file paths referenced in configYAML.
	// For example, if configYAML references passwordFile: /secrets/git-creds/token,
	// a corresponding volume mount must exist with mountPath: /secrets/git-creds.
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:pruning:PreserveUnknownFields
	VolumeMounts []apiextensionsv1.JSON `json:"volumeMounts,omitempty"`

	// PGPassSecretRef references a Secret containing a pre-created pgpass file.
	// Only used when configYAML is set. Mutually exclusive with DatabaseConfig.
	//
	// Why this is a dedicated field instead of a regular volume/volumeMount:
	// PostgreSQL's libpq rejects pgpass files that aren't mode 0600. Kubernetes
	// secret volumes mount files as root-owned, and the registry-api container
	// runs as non-root (UID 65532). A root-owned 0600 file is unreadable by
	// UID 65532, and using fsGroup changes permissions to 0640 which libpq also
	// rejects. The only solution is an init container that copies the file to an
	// emptyDir as the app user and runs chmod 0600. This cannot be expressed
	// through volumes/volumeMounts alone — it requires an init container, two
	// extra volumes (secret + emptyDir), a subPath mount, and an environment
	// variable, all wired together correctly.
	//
	// When specified, the operator generates all of that plumbing invisibly.
	// The user creates the Secret with pgpass-formatted content; the operator
	// handles only the Kubernetes permission mechanics.
	//
	// Example Secret:
	//
	//	apiVersion: v1
	//	kind: Secret
	//	metadata:
	//	  name: my-pgpass
	//	stringData:
	//	  .pgpass: |
	//	    postgres:5432:registry:db_app:mypassword
	//	    postgres:5432:registry:db_migrator:otherpassword
	//
	// Then reference it:
	//
	//	pgpassSecretRef:
	//	  name: my-pgpass
	//	  key: .pgpass
	//
	// +optional
	PGPassSecretRef *corev1.SecretKeySelector `json:"pgpassSecretRef,omitempty"`

	// ============================================================
	// Shared fields — used by both config paths
	// ============================================================

	// DisplayName is a human-readable name for the registry.
	// Works with both the new configYAML path and the legacy typed path.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// EnforceServers indicates whether MCPServers in this namespace must have their images
	// present in at least one registry in the namespace. When any registry in the namespace
	// has this field set to true, enforcement is enabled for the entire namespace.
	// MCPServers with images not found in any registry will be rejected.
	// When false (default), MCPServers can be deployed regardless of registry presence.
	// +kubebuilder:default=false
	// +optional
	EnforceServers bool `json:"enforceServers,omitempty"`

	// PodTemplateSpec defines the pod template to use for the registry API server.
	// This allows for customizing the pod configuration beyond what is provided by the other fields.
	// Note that to modify the specific container the registry API server runs in, you must specify
	// the `registry-api` container name in the PodTemplateSpec.
	// This field accepts a PodTemplateSpec object as JSON/YAML.
	// Works with both the new configYAML path and the legacy typed path.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	PodTemplateSpec *runtime.RawExtension `json:"podTemplateSpec,omitempty"`

	// ============================================================
	// Deprecated legacy fields
	// Deprecated: Use configYAML, volumes, volumeMounts, and
	// pgpassSecretRef instead. These fields will be removed in a
	// future release.
	// ============================================================

	// Sources defines the data source configurations for the registry.
	// Each source defines where registry data comes from (Git, API, ConfigMap, URL, Managed, or Kubernetes).
	// Deprecated: Use configYAML with volumes/volumeMounts instead.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	// +listType=map
	// +listMapKey=name
	Sources []MCPRegistrySourceConfig `json:"sources,omitempty"`

	// Registries defines lightweight registry views that aggregate one or more sources.
	// Each registry references sources by name and can optionally gate access via claims.
	// Deprecated: Use configYAML with volumes/volumeMounts instead.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	// +listType=map
	// +listMapKey=name
	Registries []MCPRegistryViewConfig `json:"registries,omitempty"`

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
	//
	// Deprecated: Put database config in configYAML and use pgpassSecretRef.
	// +optional
	DatabaseConfig *MCPRegistryDatabaseConfig `json:"databaseConfig,omitempty"`

	// AuthConfig defines the authentication configuration for the registry API server.
	// If not specified, defaults to anonymous authentication.
	// Deprecated: Put auth config in configYAML instead.
	// +optional
	AuthConfig *MCPRegistryAuthConfig `json:"authConfig,omitempty"`

	// TelemetryConfig defines OpenTelemetry configuration for the registry API server.
	// When enabled, the server exports traces and metrics via OTLP.
	// Deprecated: Put telemetry config in configYAML instead.
	// +optional
	TelemetryConfig *MCPRegistryTelemetryConfig `json:"telemetryConfig,omitempty"`
}

// MCPRegistrySourceConfig defines a data source configuration for the registry.
// Exactly one source type must be specified (ConfigMapRef, Git, API, URL, Managed, or Kubernetes).
//
// +kubebuilder:validation:XValidation:rule="(has(self.configMapRef) ? 1 : 0) + (has(self.git) ? 1 : 0) + (has(self.api) ? 1 : 0) + (has(self.url) ? 1 : 0) + (has(self.managed) ? 1 : 0) + (has(self.kubernetes) ? 1 : 0) == 1",message="exactly one source type must be specified"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPRegistrySourceConfig struct {
	// Name is a unique identifier for this source within the MCPRegistry
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Format is the data format (toolhive, upstream)
	// +kubebuilder:validation:Enum=toolhive;upstream
	// +kubebuilder:default=toolhive
	Format string `json:"format,omitempty"`

	// Claims are key-value pairs attached to this source for authorization purposes.
	// All entries from this source inherit these claims. Values must be string or []string.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Claims *apiextensionsv1.JSON `json:"claims,omitempty"`

	// ConfigMapRef defines the ConfigMap source configuration
	// Mutually exclusive with Git, API, URL, Managed, and Kubernetes
	// +optional
	ConfigMapRef *corev1.ConfigMapKeySelector `json:"configMapRef,omitempty"`

	// Git defines the Git repository source configuration
	// Mutually exclusive with ConfigMapRef, API, URL, Managed, and Kubernetes
	// +optional
	Git *GitSource `json:"git,omitempty"`

	// API defines the API source configuration
	// Mutually exclusive with ConfigMapRef, Git, URL, Managed, and Kubernetes
	// +optional
	API *APISource `json:"api,omitempty"`

	// URL defines a URL-hosted file source configuration.
	// The registry server fetches the registry data from the specified HTTP/HTTPS URL.
	// Mutually exclusive with ConfigMapRef, Git, API, Managed, and Kubernetes
	// +optional
	URL *URLSource `json:"url,omitempty"`

	// Managed defines a managed source that is directly manipulated via the registry API.
	// Managed sources do not sync from external sources.
	// At most one managed source is allowed per MCPRegistry.
	// Mutually exclusive with ConfigMapRef, Git, API, URL, and Kubernetes
	// +optional
	Managed *ManagedSource `json:"managed,omitempty"`

	// Kubernetes defines a source that discovers MCP servers from running Kubernetes resources.
	// Mutually exclusive with ConfigMapRef, Git, API, URL, and Managed
	// +optional
	Kubernetes *KubernetesSource `json:"kubernetes,omitempty"`

	// SyncPolicy defines the automatic synchronization behavior for this source.
	// If specified, enables automatic synchronization at the given interval.
	// Manual synchronization is always supported via annotation-based triggers
	// regardless of this setting.
	// Not applicable for Managed and Kubernetes sources (will be ignored).
	// +optional
	SyncPolicy *SyncPolicy `json:"syncPolicy,omitempty"`

	// Filter defines include/exclude patterns for registry content.
	// Not applicable for Managed and Kubernetes sources (will be ignored).
	// +optional
	Filter *RegistryFilter `json:"filter,omitempty"`
}

// MCPRegistryViewConfig defines a lightweight registry view that aggregates one or more sources.
type MCPRegistryViewConfig struct {
	// Name is a unique identifier for this registry view
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Sources is an ordered list of source names that feed this registry.
	// Each name must reference a source defined in spec.sources.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	Sources []string `json:"sources"`

	// Claims are key-value pairs that gate access to this registry view.
	// Only requests with matching claims can access this registry. Values must be string or []string.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	Claims *apiextensionsv1.JSON `json:"claims,omitempty"`
}

// URLSource defines a URL-hosted file source configuration.
// The registry server fetches registry data from the specified HTTP/HTTPS URL.
type URLSource struct {
	// Endpoint is the HTTP/HTTPS URL to fetch the registry file from.
	// HTTPS is required unless the host is localhost.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^https?://.*"
	Endpoint string `json:"endpoint"`

	// Timeout is the timeout for HTTP requests (Go duration format).
	// Defaults to "30s" if not specified.
	// +kubebuilder:validation:Pattern=^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$
	// +optional
	Timeout string `json:"timeout,omitempty"`
}

// ManagedSource defines a managed source that is directly manipulated via the registry API.
// Managed sources do not sync from external sources.
type ManagedSource struct {
	// Empty — presence indicates this is a managed (internal) source for publishing
}

// KubernetesSource defines a source that discovers MCP servers from running Kubernetes resources.
// Per-entry claims can be set on CRDs via the toolhive.stacklok.dev/authz-claims JSON annotation.
type KubernetesSource struct {
	// Namespaces is a list of Kubernetes namespaces to watch for MCP servers.
	// If empty, watches the operator's configured namespace.
	// +listType=atomic
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`
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
	// +listType=atomic
	// +optional
	Include []string `json:"include,omitempty"`

	// Exclude is a list of glob patterns to exclude
	// +listType=atomic
	// +optional
	Exclude []string `json:"exclude,omitempty"`
}

// TagFilter defines tag-based filtering
type TagFilter struct {
	// Include is a list of tags to include
	// +listType=atomic
	// +optional
	Include []string `json:"include,omitempty"`

	// Exclude is a list of tags to exclude
	// +listType=atomic
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
	Port int32 `json:"port,omitempty"`

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
	MaxOpenConns int32 `json:"maxOpenConns,omitempty"`

	// MaxIdleConns is the maximum number of idle connections in the pool
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxIdleConns int32 `json:"maxIdleConns,omitempty"`

	// ConnMaxLifetime is the maximum amount of time a connection may be reused (Go duration format)
	// Examples: "30m", "1h", "24h"
	// +kubebuilder:validation:Pattern=^([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$
	// +kubebuilder:default="30m"
	// +optional
	ConnMaxLifetime string `json:"connMaxLifetime,omitempty"`

	// MaxMetaSize is the maximum allowed size in bytes for publisher-provided
	// metadata extensions (_meta). Must be greater than zero.
	// Defaults to 262144 (256KB) if not specified.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxMetaSize *int32 `json:"maxMetaSize,omitempty"`

	// DynamicAuth defines dynamic database authentication configuration.
	// When set, the registry server authenticates to the database using
	// short-lived credentials instead of static passwords.
	// +optional
	DynamicAuth *MCPRegistryDynamicAuthConfig `json:"dynamicAuth,omitempty"`

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

// MCPRegistryDynamicAuthConfig defines dynamic database authentication configuration.
type MCPRegistryDynamicAuthConfig struct {
	// AWSRDSIAM enables AWS RDS IAM authentication for database connections.
	// +optional
	AWSRDSIAM *MCPRegistryAWSRDSIAMConfig `json:"awsRdsIam,omitempty"`
}

// MCPRegistryAWSRDSIAMConfig defines AWS RDS IAM authentication configuration.
type MCPRegistryAWSRDSIAMConfig struct {
	// Region is the AWS region for RDS IAM authentication.
	// Use "detect" to automatically detect the region from instance metadata.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Region string `json:"region,omitempty"`
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
//
// +kubebuilder:validation:XValidation:rule="self.mode != 'anonymous' || !has(self.authz)",message="authz configuration has no effect when auth mode is anonymous"
//
//nolint:lll // CEL validation rules exceed line length limit
type MCPRegistryAuthConfig struct {
	// Mode specifies the authentication mode (anonymous or oauth)
	// Defaults to "anonymous" if not specified.
	// Use "oauth" to enable OAuth/OIDC authentication.
	// +kubebuilder:validation:Enum=anonymous;oauth
	// +kubebuilder:default="anonymous"
	// +optional
	Mode MCPRegistryAuthMode `json:"mode,omitempty"`

	// PublicPaths defines additional paths that bypass authentication.
	// These extend the default public paths (health, docs, swagger, well-known).
	// Each path must start with "/". Do not add API data paths here.
	// Example: ["/custom/public", "/metrics"]
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:Pattern="^/"
	// +listType=atomic
	// +optional
	PublicPaths []string `json:"publicPaths,omitempty"`

	// OAuth defines OAuth/OIDC specific authentication settings
	// Only used when Mode is "oauth"
	// +optional
	OAuth *MCPRegistryOAuthConfig `json:"oauth,omitempty"`

	// Authz defines authorization configuration for role-based access control.
	// +optional
	Authz *MCPRegistryAuthzConfig `json:"authz,omitempty"`
}

// MCPRegistryAuthzConfig defines authorization configuration for role-based access control
type MCPRegistryAuthzConfig struct {
	// Roles defines the role-based authorization rules.
	// Each role is a list of claim matchers (JSON objects with string or []string values).
	// +optional
	Roles MCPRegistryRolesConfig `json:"roles,omitempty"`
}

// MCPRegistryRolesConfig defines role-based authorization rules.
// Each role is a list of claim matchers — a request matching any entry in the list is granted the role.
type MCPRegistryRolesConfig struct {
	// SuperAdmin grants full administrative access to the registry.
	// +optional
	// +listType=atomic
	SuperAdmin []apiextensionsv1.JSON `json:"superAdmin,omitempty"`

	// ManageSources grants permission to create, update, and delete sources.
	// +optional
	// +listType=atomic
	ManageSources []apiextensionsv1.JSON `json:"manageSources,omitempty"`

	// ManageRegistries grants permission to create, update, and delete registries.
	// +optional
	// +listType=atomic
	ManageRegistries []apiextensionsv1.JSON `json:"manageRegistries,omitempty"`

	// ManageEntries grants permission to create, update, and delete registry entries.
	// +optional
	// +listType=atomic
	ManageEntries []apiextensionsv1.JSON `json:"manageEntries,omitempty"`
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
	// +listType=map
	// +listMapKey=name
	// +optional
	Providers []MCPRegistryOAuthProviderConfig `json:"providers,omitempty"`

	// ScopesSupported defines the OAuth scopes supported by this resource (RFC 9728)
	// Defaults to ["mcp-registry:read", "mcp-registry:write"] if not specified
	// +listType=atomic
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

// MCPRegistryTelemetryConfig defines OpenTelemetry configuration for the registry API server.
type MCPRegistryTelemetryConfig struct {
	// Enabled controls whether telemetry is enabled globally.
	// When false, no telemetry providers are initialized.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// ServiceName is the name of the service for telemetry identification.
	// Defaults to "thv-registry-api" if not specified.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// ServiceVersion is the version of the service for telemetry identification.
	// +optional
	ServiceVersion string `json:"serviceVersion,omitempty"`

	// Endpoint is the OTLP collector endpoint (host:port).
	// Defaults to "localhost:4318" if not specified.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Insecure allows HTTP connections instead of HTTPS to the OTLP endpoint.
	// Should only be true for development/testing environments.
	// +kubebuilder:default=false
	// +optional
	Insecure bool `json:"insecure,omitempty"`

	// Tracing defines tracing-specific configuration.
	// +optional
	Tracing *MCPRegistryTracingConfig `json:"tracing,omitempty"`

	// Metrics defines metrics-specific configuration.
	// +optional
	Metrics *MCPRegistryMetricsConfig `json:"metrics,omitempty"`
}

// MCPRegistryTracingConfig defines tracing-specific configuration.
type MCPRegistryTracingConfig struct {
	// Enabled controls whether tracing is enabled.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Sampling controls the trace sampling rate (0.0 to 1.0, exclusive of 0.0).
	// 1.0 means sample all traces, 0.5 means sample 50%.
	// Defaults to 0.05 (5%) if not specified.
	// +optional
	Sampling *string `json:"sampling,omitempty"`
}

// MCPRegistryMetricsConfig defines metrics-specific configuration.
type MCPRegistryMetricsConfig struct {
	// Enabled controls whether metrics collection is enabled.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// MCPRegistryStatus defines the observed state of MCPRegistry
type MCPRegistryStatus struct {
	// Conditions represent the latest available observations of the MCPRegistry's state
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration reflects the generation most recently observed by the controller
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase represents the current overall phase of the MCPRegistry
	// +optional
	Phase MCPRegistryPhase `json:"phase,omitempty"`

	// Message provides additional information about the current phase
	// +optional
	Message string `json:"message,omitempty"`

	// URL is the URL where the registry API can be accessed
	// +optional
	URL string `json:"url,omitempty"`

	// ReadyReplicas is the number of ready registry API replicas
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
}

// MCPRegistryPhase represents the phase of the MCPRegistry
// +kubebuilder:validation:Enum=Pending;Ready;Failed;Terminating
type MCPRegistryPhase string

const (
	// MCPRegistryPhasePending means the MCPRegistry is being initialized
	MCPRegistryPhasePending MCPRegistryPhase = "Pending"

	// MCPRegistryPhaseReady means the MCPRegistry is ready and operational
	MCPRegistryPhaseReady MCPRegistryPhase = "Ready"

	// MCPRegistryPhaseFailed means the MCPRegistry has failed
	MCPRegistryPhaseFailed MCPRegistryPhase = "Failed"

	// MCPRegistryPhaseTerminating means the MCPRegistry is being deleted
	MCPRegistryPhaseTerminating MCPRegistryPhase = "Terminating"
)

// Condition reasons for MCPRegistry
const (
	// ConditionReasonRegistryReady indicates the MCPRegistry is ready
	ConditionReasonRegistryReady = "Ready"

	// ConditionReasonRegistryNotReady indicates the MCPRegistry is not ready
	ConditionReasonRegistryNotReady = "NotReady"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
//+kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.readyReplicas"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
//+kubebuilder:resource:shortName=mcpreg;registry,scope=Namespaced,categories=toolhive
//nolint:lll
//+kubebuilder:validation:XValidation:rule="size(self.spec.configYAML) > 0 || (has(self.spec.sources) && size(self.spec.sources) > 0)",message="either configYAML or sources must be specified"
//+kubebuilder:validation:XValidation:rule="!has(self.spec.sources) || self.spec.sources.filter(s, has(s.managed)).size() <= 1",message="at most one managed source is allowed"

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
func (r *MCPRegistry) GetDatabasePort() int32 {
	if r.Spec.DatabaseConfig == nil || r.Spec.DatabaseConfig.Port == 0 {
		return 5432
	}
	return r.Spec.DatabaseConfig.Port
}

// ParseVolumes deserializes the raw JSON Volumes into typed corev1.Volume objects.
// Returns an empty slice if Volumes is nil or empty.
func (s *MCPRegistrySpec) ParseVolumes() ([]corev1.Volume, error) {
	volumes := make([]corev1.Volume, 0, len(s.Volumes))
	for i, raw := range s.Volumes {
		var vol corev1.Volume
		if err := json.Unmarshal(raw.Raw, &vol); err != nil {
			return nil, fmt.Errorf("failed to unmarshal volumes[%d]: %w", i, err)
		}
		volumes = append(volumes, vol)
	}
	return volumes, nil
}

// ParseVolumeMounts deserializes the raw JSON VolumeMounts into typed corev1.VolumeMount objects.
// Returns an empty slice if VolumeMounts is nil or empty.
func (s *MCPRegistrySpec) ParseVolumeMounts() ([]corev1.VolumeMount, error) {
	mounts := make([]corev1.VolumeMount, 0, len(s.VolumeMounts))
	for i, raw := range s.VolumeMounts {
		var mount corev1.VolumeMount
		if err := json.Unmarshal(raw.Raw, &mount); err != nil {
			return nil, fmt.Errorf("failed to unmarshal volumeMounts[%d]: %w", i, err)
		}
		mounts = append(mounts, mount)
	}
	return mounts, nil
}
