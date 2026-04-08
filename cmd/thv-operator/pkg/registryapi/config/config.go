// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package config provides management for the registry server configuration
package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

// ConfigManager provides methods to build registry server configuration from MCPRegistry resources
//
//nolint:revive
type ConfigManager interface {
	BuildConfig() (*Config, error)
	GetRegistryServerConfigMapName() string
}

// NewConfigManager creates a new instance of ConfigManager
func NewConfigManager(mcpRegistry *mcpv1alpha1.MCPRegistry) ConfigManager {
	return &configManager{
		mcpRegistry: mcpRegistry,
	}
}

type configManager struct {
	mcpRegistry *mcpv1alpha1.MCPRegistry
}

func (cm *configManager) GetRegistryServerConfigMapName() string {
	return fmt.Sprintf("%s-registry-server-config", cm.mcpRegistry.Name)
}

const (
	// SourceTypeGit is the type for registry data stored in Git repositories
	SourceTypeGit = "git"

	// SourceTypeAPI is the type for registry data fetched from API endpoints
	SourceTypeAPI = "api"

	// SourceTypeFile is the type for registry data stored in local files
	SourceTypeFile = "file"

	// RegistryJSONFilePath is the file path where the registry JSON file will be mounted
	RegistryJSONFilePath = "/config/registry"

	// RegistryJSONFileName is the name of the registry JSON file
	RegistryJSONFileName = "registry.json"

	// RegistryServerConfigFilePath is the file path where the registry server config file will be mounted
	RegistryServerConfigFilePath = "/config"

	// RegistryServerConfigFileName is the name of the registry server config file
	RegistryServerConfigFileName = "config.yaml"
)

// Config represents the root configuration structure (v2 format)
type Config struct {
	Sources    []SourceConfig   `yaml:"sources"`
	Registries []RegistryConfig `yaml:"registries,omitempty"`
	Database   *DatabaseConfig  `yaml:"database,omitempty"`
	Auth       *AuthConfig      `yaml:"auth,omitempty"`
	Telemetry  *TelemetryConfig `yaml:"telemetry,omitempty"`
}

// DatabaseConfig defines PostgreSQL database configuration
// Uses two-user security model: separate users for operations and migrations
type DatabaseConfig struct {
	// Host is the database server hostname
	Host string `yaml:"host"`

	// Port is the database server port
	Port int32 `yaml:"port"`

	// User is the application user (limited privileges: SELECT, INSERT, UPDATE, DELETE)
	// Credentials provided via pgpass file
	User string `yaml:"user"`

	// MigrationUser is the migration user (elevated privileges: CREATE, ALTER, DROP)
	// Used for running database schema migrations
	// Credentials provided via pgpass file
	MigrationUser string `yaml:"migrationUser"`

	// Database is the database name
	Database string `yaml:"database"`

	// SSLMode is the SSL mode for the connection
	SSLMode string `yaml:"sslMode"`

	// MaxOpenConns is the maximum number of open connections to the database
	MaxOpenConns int32 `yaml:"maxOpenConns"`

	// MaxIdleConns is the maximum number of idle connections in the pool
	MaxIdleConns int32 `yaml:"maxIdleConns"`

	// ConnMaxLifetime is the maximum amount of time a connection may be reused
	ConnMaxLifetime string `yaml:"connMaxLifetime"`

	// MaxMetaSize is the maximum allowed size in bytes for publisher-provided metadata extensions
	MaxMetaSize *int32 `yaml:"maxMetaSize,omitempty"`

	// DynamicAuth defines dynamic database authentication configuration
	DynamicAuth *DynamicAuthConfig `yaml:"dynamicAuth,omitempty"`
}

// DynamicAuthConfig defines dynamic database authentication configuration
type DynamicAuthConfig struct {
	AWSRDSIAM *DynamicAuthAWSRDSIAM `yaml:"awsRdsIam,omitempty"`
}

// DynamicAuthAWSRDSIAM defines AWS RDS IAM authentication configuration
type DynamicAuthAWSRDSIAM struct {
	Region string `yaml:"region,omitempty"`
}

// SourceConfig defines a single data source configuration (v2 format)
type SourceConfig struct {
	Name       string            `yaml:"name"`
	Format     string            `yaml:"format,omitempty"`
	Claims     map[string]any    `yaml:"claims,omitempty"`
	Git        *GitConfig        `yaml:"git,omitempty"`
	API        *APIConfig        `yaml:"api,omitempty"`
	File       *FileConfig       `yaml:"file,omitempty"`
	Managed    *ManagedConfig    `yaml:"managed,omitempty"`
	Kubernetes *KubernetesConfig `yaml:"kubernetes,omitempty"`
	SyncPolicy *SyncPolicyConfig `yaml:"syncPolicy,omitempty"`
	Filter     *FilterConfig     `yaml:"filter,omitempty"`
}

// RegistryConfig defines a lightweight registry view that aggregates sources (v2 format)
type RegistryConfig struct {
	Name    string         `yaml:"name"`
	Sources []string       `yaml:"sources"`
	Claims  map[string]any `yaml:"claims,omitempty"`
}

// ManagedConfig defines configuration for managed sources.
// Managed sources are directly manipulated via API and do not sync from external sources.
type ManagedConfig struct{}

// TelemetryConfig defines OpenTelemetry configuration
type TelemetryConfig struct {
	Enabled        bool           `yaml:"enabled"`
	ServiceName    string         `yaml:"serviceName,omitempty"`
	ServiceVersion string         `yaml:"serviceVersion,omitempty"`
	Endpoint       string         `yaml:"endpoint,omitempty"`
	Insecure       bool           `yaml:"insecure,omitempty"`
	Tracing        *TracingConfig `yaml:"tracing,omitempty"`
	Metrics        *MetricsConfig `yaml:"metrics,omitempty"`
}

// TracingConfig defines tracing-specific configuration
type TracingConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Sampling *float64 `yaml:"sampling,omitempty"`
}

// MetricsConfig defines metrics-specific configuration
type MetricsConfig struct {
	Enabled bool `yaml:"enabled"`
}

// AuthMode represents the authentication mode
type AuthMode string

const (
	// AuthModeAnonymous allows unauthenticated access
	AuthModeAnonymous AuthMode = "anonymous"

	// AuthModeOAuth enables OAuth/OIDC authentication
	AuthModeOAuth AuthMode = "oauth"
)

// AuthConfig defines authentication configuration for the registry server
type AuthConfig struct {
	// Mode specifies the authentication mode (anonymous or oauth)
	// Defaults to "oauth" if not specified (security-by-default).
	// Use "anonymous" to explicitly disable authentication for development.
	Mode AuthMode `yaml:"mode,omitempty"`

	// PublicPaths defines additional paths that bypass authentication
	PublicPaths []string `yaml:"publicPaths,omitempty"`

	// OAuth defines OAuth/OIDC specific authentication settings
	// Only used when Mode is "oauth"
	OAuth *OAuthConfig `yaml:"oauth,omitempty"`

	// Authz defines authorization configuration for role-based access control
	Authz *AuthzConfig `yaml:"authz,omitempty"`
}

// AuthzConfig defines authorization configuration for role-based access control
type AuthzConfig struct {
	Roles RolesConfig `yaml:"roles,omitempty"`
}

// RolesConfig defines role-based authorization rules
type RolesConfig struct {
	SuperAdmin       []map[string]any `yaml:"superAdmin,omitempty"`
	ManageSources    []map[string]any `yaml:"manageSources,omitempty"`
	ManageRegistries []map[string]any `yaml:"manageRegistries,omitempty"`
	ManageEntries    []map[string]any `yaml:"manageEntries,omitempty"`
}

// OAuthConfig defines OAuth/OIDC specific authentication settings
type OAuthConfig struct {
	// ResourceURL is the URL identifying this protected resource (RFC 9728)
	// Used in the /.well-known/oauth-protected-resource endpoint
	ResourceURL string `yaml:"resourceUrl,omitempty"`

	// Providers defines the OAuth/OIDC providers for authentication
	// Multiple providers can be configured (e.g., Kubernetes + external IDP)
	Providers []OAuthProviderConfig `yaml:"providers,omitempty"`

	// ScopesSupported defines the OAuth scopes supported by this resource (RFC 9728)
	// Defaults to ["mcp-registry:read", "mcp-registry:write"] if not specified
	ScopesSupported []string `yaml:"scopesSupported,omitempty"`

	// Realm is the protection space identifier for WWW-Authenticate header (RFC 7235)
	// Defaults to "mcp-registry" if not specified
	Realm string `yaml:"realm,omitempty"`
}

// OAuthProviderConfig defines configuration for an OAuth/OIDC provider
type OAuthProviderConfig struct {
	// Name is a unique identifier for this provider (e.g., "kubernetes", "keycloak")
	Name string `yaml:"name"`

	// IssuerURL is the OIDC issuer URL (e.g., https://accounts.google.com)
	// The JWKS URL will be discovered automatically from .well-known/openid-configuration
	// unless JwksUrl is explicitly specified
	IssuerURL string `yaml:"issuerUrl"`

	// JwksUrl is the URL to fetch the JSON Web Key Set (JWKS) from
	// If specified, OIDC discovery is skipped and this URL is used directly
	// Example: https://kubernetes.default.svc/openid/v1/jwks
	JwksUrl string `yaml:"jwksUrl,omitempty"`

	// Audience is the expected audience claim in the token (REQUIRED)
	// Per RFC 6749 Section 4.1.3, tokens must be validated against expected audience
	// For Kubernetes, this is typically the API server URL
	Audience string `yaml:"audience"`

	// ClientID is the OAuth client ID for token introspection (optional)
	ClientID string `yaml:"clientId,omitempty"`

	// ClientSecretFile is the path to a file containing the client secret
	// The file should contain only the secret with optional trailing whitespace
	ClientSecretFile string `yaml:"clientSecretFile,omitempty"`

	// CACertPath is the path to a CA certificate bundle for verifying the provider's TLS certificate
	// Required for Kubernetes in-cluster authentication or self-signed certificates
	CACertPath string `yaml:"caCertPath,omitempty"`

	// AuthTokenFile is the path to a file containing a bearer token for authenticating to OIDC/JWKS endpoints
	// Useful when the OIDC discovery or JWKS endpoint requires authentication
	// Example: /var/run/secrets/kubernetes.io/serviceaccount/token
	AuthTokenFile string `yaml:"authTokenFile,omitempty"`

	// IntrospectionURL is the OAuth 2.0 Token Introspection endpoint (RFC 7662)
	// Used for validating opaque (non-JWT) tokens
	// If not specified, only JWT tokens can be validated via JWKS
	IntrospectionURL string `yaml:"introspectionUrl,omitempty"`

	// AllowPrivateIP allows JWKS/OIDC endpoints on private IP addresses
	// Required when the OAuth provider (e.g., Kubernetes API server) is running on a private network
	// Example: Set to true when using https://kubernetes.default.svc as the issuer URL
	AllowPrivateIP bool `yaml:"allowPrivateIP,omitempty"`
}

// KubernetesConfig defines a Kubernetes-based source where data is discovered
// from MCPServer resources in the cluster.
type KubernetesConfig struct {
	// Namespaces is a list of Kubernetes namespaces to watch for MCP servers.
	// If empty, watches the operator's configured namespace.
	Namespaces []string `yaml:"namespaces,omitempty"`
}

// GitConfig defines Git source settings
type GitConfig struct {
	// Repository is the Git repository URL (HTTP/HTTPS/SSH)
	Repository string `yaml:"repository"`

	// Branch is the Git branch to use (mutually exclusive with Tag and Commit)
	Branch string `yaml:"branch,omitempty"`

	// Tag is the Git tag to use (mutually exclusive with Branch and Commit)
	Tag string `yaml:"tag,omitempty"`

	// Commit is the Git commit SHA to use (mutually exclusive with Branch and Tag)
	Commit string `yaml:"commit,omitempty"`

	// Path is the path to the registry file within the repository
	Path string `yaml:"path,omitempty"`

	// Auth contains optional authentication for private repositories
	Auth *GitAuthConfig `yaml:"auth,omitempty"`
}

// GitAuthConfig defines authentication settings for Git repositories
type GitAuthConfig struct {
	// Username is the Git username for HTTP Basic authentication
	Username string `yaml:"username,omitempty"`

	// PasswordFile is the path to a file containing the Git password/token
	PasswordFile string `yaml:"passwordFile,omitempty"`
}

// APIConfig defines API source configuration for ToolHive Registry APIs
type APIConfig struct {
	// Endpoint is the base API URL (without path)
	// The source handler will append the appropriate paths, for instance:
	//   - /v0/servers - List all servers (single response, no pagination)
	//   - /v0/servers/{name} - Get specific server (future)
	//   - /v0/info - Get registry metadata (future)
	// Example: "http://my-registry-api.default.svc.cluster.local/api"
	Endpoint string `yaml:"endpoint"`
}

// FileConfig defines file source configuration
type FileConfig struct {
	// Path is the path to the registry.json file on the local filesystem
	// Can be absolute or relative to the working directory
	Path string `yaml:"path,omitempty"`

	// URL is the HTTP/HTTPS URL to fetch the registry file from
	URL string `yaml:"url,omitempty"`

	// Data is the inline registry data as a JSON string
	Data string `yaml:"data,omitempty"`

	// Timeout is the timeout for HTTP requests when using URL
	Timeout string `yaml:"timeout,omitempty"`
}

// SyncPolicyConfig defines synchronization settings
type SyncPolicyConfig struct {
	Interval string `yaml:"interval"`
}

// FilterConfig defines filtering rules for registry entries
type FilterConfig struct {
	Names *NameFilterConfig `yaml:"names,omitempty"`
	Tags  *TagFilterConfig  `yaml:"tags,omitempty"`
}

// NameFilterConfig defines name-based filtering
type NameFilterConfig struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

// TagFilterConfig defines tag-based filtering
type TagFilterConfig struct {
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

// ToConfigMapWithContentChecksum converts the Config to a ConfigMap with a content checksum annotation
func (c *Config) ToConfigMapWithContentChecksum(mcpRegistry *mcpv1alpha1.MCPRegistry) (*corev1.ConfigMap, error) {
	yamlData, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config to YAML: %w", err)
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-registry-server-config", mcpRegistry.Name),
			Namespace: mcpRegistry.Namespace,
			Annotations: map[string]string{
				checksum.ContentChecksumAnnotation: ctrlutil.CalculateConfigHash(yamlData),
			},
		},
		Data: map[string]string{
			RegistryServerConfigFileName: string(yamlData),
		},
	}
	return configMap, nil
}

func (cm *configManager) BuildConfig() (*Config, error) {
	config := Config{}

	mcpRegistry := cm.mcpRegistry

	if mcpRegistry.Name == "" {
		return nil, fmt.Errorf("registry name is required")
	}

	if len(mcpRegistry.Spec.Sources) == 0 {
		return nil, fmt.Errorf("at least one source must be specified")
	}

	if len(mcpRegistry.Spec.Registries) == 0 {
		return nil, fmt.Errorf("at least one registry must be specified")
	}

	// Validate source names are unique
	if err := validateSourceNames(mcpRegistry.Spec.Sources); err != nil {
		return nil, fmt.Errorf("invalid source configuration: %w", err)
	}

	// Validate registry names are unique
	if err := validateRegistryViewNames(mcpRegistry.Spec.Registries); err != nil {
		return nil, fmt.Errorf("invalid registry configuration: %w", err)
	}

	// Build source configs
	sources := make([]SourceConfig, 0, len(mcpRegistry.Spec.Sources))
	for _, sourceSpec := range mcpRegistry.Spec.Sources {
		sourceConfig, err := buildSourceConfig(&sourceSpec)
		if err != nil {
			return nil, fmt.Errorf("failed to build source configuration for %q: %w", sourceSpec.Name, err)
		}
		sources = append(sources, *sourceConfig)
	}
	config.Sources = sources

	// Build source name set for validation
	sourceNames := make(map[string]bool, len(config.Sources))
	for _, s := range config.Sources {
		sourceNames[s.Name] = true
	}

	// Build registry view configs
	registries := make([]RegistryConfig, 0, len(mcpRegistry.Spec.Registries))
	for _, regSpec := range mcpRegistry.Spec.Registries {
		regConfig, err := buildRegistryViewConfig(&regSpec, sourceNames)
		if err != nil {
			return nil, fmt.Errorf("failed to build registry configuration for %q: %w", regSpec.Name, err)
		}
		registries = append(registries, *regConfig)
	}
	config.Registries = registries

	// Build database configuration from CRD spec or use defaults
	config.Database = buildDatabaseConfig(mcpRegistry.Spec.DatabaseConfig)

	// Build authentication configuration from CRD spec or use defaults
	authConfig, err := buildAuthConfig(mcpRegistry.Spec.AuthConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build authentication configuration: %w", err)
	}
	config.Auth = authConfig

	// Build telemetry configuration from CRD spec
	telemetryConfig, err := buildTelemetryConfig(mcpRegistry.Spec.TelemetryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build telemetry configuration: %w", err)
	}
	config.Telemetry = telemetryConfig

	return &config, nil
}

// validateSourceNames ensures all source names are unique
func validateSourceNames(sources []mcpv1alpha1.MCPRegistrySourceConfig) error {
	seen := make(map[string]bool)
	for _, source := range sources {
		if source.Name == "" {
			return fmt.Errorf("source name is required")
		}
		if seen[source.Name] {
			return fmt.Errorf("duplicate source name: %q", source.Name)
		}
		seen[source.Name] = true
	}
	return nil
}

// validateRegistryViewNames ensures all registry view names are unique
func validateRegistryViewNames(registries []mcpv1alpha1.MCPRegistryViewConfig) error {
	seen := make(map[string]bool)
	for _, reg := range registries {
		if reg.Name == "" {
			return fmt.Errorf("registry name is required")
		}
		if seen[reg.Name] {
			return fmt.Errorf("duplicate registry name: %q", reg.Name)
		}
		seen[reg.Name] = true
	}
	return nil
}

func buildFilePath(sourceName string) *FileConfig {
	return &FileConfig{
		Path: filepath.Join(RegistryJSONFilePath, sourceName, RegistryJSONFileName),
	}
}

//nolint:gocyclo // Complexity is acceptable for handling multiple source types
func buildSourceConfig(sourceSpec *mcpv1alpha1.MCPRegistrySourceConfig) (*SourceConfig, error) {
	if sourceSpec.Name == "" {
		return nil, fmt.Errorf("source name is required")
	}

	sourceConfig := SourceConfig{
		Name:   sourceSpec.Name,
		Format: sourceSpec.Format,
	}

	if sourceSpec.Format == "" {
		sourceConfig.Format = mcpv1alpha1.RegistryFormatToolHive
	}

	// Deserialize claims if present
	if sourceSpec.Claims != nil {
		claims, err := deserializeClaims(sourceSpec.Claims)
		if err != nil {
			return nil, fmt.Errorf("invalid claims: %w", err)
		}
		sourceConfig.Claims = claims
	}

	// Determine source type and build appropriate config
	sourceCount := 0
	if sourceSpec.ConfigMapRef != nil {
		sourceCount++
		sourceConfig.File = buildFilePath(sourceSpec.Name)
	}
	if sourceSpec.Git != nil {
		sourceCount++
		gitConfig, err := buildGitSourceConfig(sourceSpec.Git)
		if err != nil {
			return nil, fmt.Errorf("failed to build Git source configuration: %w", err)
		}
		sourceConfig.Git = gitConfig
	}
	if sourceSpec.API != nil {
		sourceCount++
		apiConfig, err := buildAPISourceConfig(sourceSpec.API)
		if err != nil {
			return nil, fmt.Errorf("failed to build API source configuration: %w", err)
		}
		sourceConfig.API = apiConfig
	}
	if sourceSpec.URL != nil {
		sourceCount++
		sourceConfig.File = &FileConfig{
			URL:     sourceSpec.URL.Endpoint,
			Timeout: sourceSpec.URL.Timeout,
		}
	}
	if sourceSpec.Managed != nil {
		sourceCount++
		sourceConfig.Managed = &ManagedConfig{}
	}
	if sourceSpec.Kubernetes != nil {
		sourceCount++
		sourceConfig.Kubernetes = &KubernetesConfig{
			Namespaces: sourceSpec.Kubernetes.Namespaces,
		}
	}

	if sourceCount == 0 {
		return nil, fmt.Errorf(
			"exactly one source type must be specified")
	}
	if sourceCount > 1 {
		return nil, fmt.Errorf(
			"only one source type can be specified")
	}

	// Build sync policy (not applicable for managed/kubernetes sources)
	if sourceSpec.SyncPolicy != nil && sourceSpec.Managed == nil && sourceSpec.Kubernetes == nil {
		if sourceSpec.SyncPolicy.Interval == "" {
			return nil, fmt.Errorf("sync policy interval is required")
		}
		sourceConfig.SyncPolicy = &SyncPolicyConfig{
			Interval: sourceSpec.SyncPolicy.Interval,
		}
	}

	// Build filter (not applicable for managed/kubernetes sources)
	if sourceSpec.Filter != nil && sourceSpec.Managed == nil && sourceSpec.Kubernetes == nil {
		filterConfig := &FilterConfig{}
		if sourceSpec.Filter.NameFilters != nil {
			filterConfig.Names = &NameFilterConfig{
				Include: sourceSpec.Filter.NameFilters.Include,
				Exclude: sourceSpec.Filter.NameFilters.Exclude,
			}
		}
		if sourceSpec.Filter.Tags != nil {
			filterConfig.Tags = &TagFilterConfig{
				Include: sourceSpec.Filter.Tags.Include,
				Exclude: sourceSpec.Filter.Tags.Exclude,
			}
		}
		sourceConfig.Filter = filterConfig
	}

	return &sourceConfig, nil
}

// buildRegistryViewConfig builds a RegistryConfig from a CRD MCPRegistryViewConfig
func buildRegistryViewConfig(
	regSpec *mcpv1alpha1.MCPRegistryViewConfig,
	validSourceNames map[string]bool,
) (*RegistryConfig, error) {
	if regSpec.Name == "" {
		return nil, fmt.Errorf("registry name is required")
	}

	if len(regSpec.Sources) == 0 {
		return nil, fmt.Errorf("at least one source reference is required")
	}

	// Validate all source references exist
	for _, srcName := range regSpec.Sources {
		if !validSourceNames[srcName] {
			return nil, fmt.Errorf("registry %q references unknown source %q", regSpec.Name, srcName)
		}
	}

	regConfig := &RegistryConfig{
		Name:    regSpec.Name,
		Sources: regSpec.Sources,
	}

	// Deserialize claims if present
	if regSpec.Claims != nil {
		claims, err := deserializeClaims(regSpec.Claims)
		if err != nil {
			return nil, fmt.Errorf("invalid claims: %w", err)
		}
		regConfig.Claims = claims
	}

	return regConfig, nil
}

// deserializeClaims converts apiextensionsv1.JSON to map[string]any and validates
// that all values are string or []string.
func deserializeClaims(raw *apiextensionsv1.JSON) (map[string]any, error) {
	if raw == nil || raw.Raw == nil {
		return nil, nil
	}

	var claims map[string]any
	if err := json.Unmarshal(raw.Raw, &claims); err != nil {
		return nil, fmt.Errorf("claims must be a JSON object: %w", err)
	}

	// Validate that values are string or []string
	for key, val := range claims {
		switch v := val.(type) {
		case string:
			// OK
		case []any:
			// Convert to []string and validate
			strs := make([]string, 0, len(v))
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("claim %q: array values must be strings, got %T", key, item)
				}
				strs = append(strs, s)
			}
			claims[key] = strs
		default:
			return nil, fmt.Errorf("claim %q: value must be string or []string, got %T", key, val)
		}
	}

	return claims, nil
}

// deserializeRoleEntry converts apiextensionsv1.JSON to map[string]any for a role entry.
// Values are validated to be string or []string, matching the claims validation contract.
func deserializeRoleEntry(raw apiextensionsv1.JSON) (map[string]any, error) {
	if raw.Raw == nil {
		return nil, nil
	}

	var entry map[string]any
	if err := json.Unmarshal(raw.Raw, &entry); err != nil {
		return nil, fmt.Errorf("role entry must be a JSON object: %w", err)
	}

	for key, val := range entry {
		switch v := val.(type) {
		case string:
			// OK
		case []any:
			strs := make([]string, 0, len(v))
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("role entry %q: array values must be strings, got %T", key, item)
				}
				strs = append(strs, s)
			}
			entry[key] = strs
		default:
			return nil, fmt.Errorf("role entry %q: value must be string or []string, got %T", key, val)
		}
	}

	return entry, nil
}

func buildGitSourceConfig(git *mcpv1alpha1.GitSource) (*GitConfig, error) {
	if git == nil {
		return nil, fmt.Errorf("git source configuration is required")
	}

	if git.Repository == "" {
		return nil, fmt.Errorf("git repository is required")
	}

	if git.Path == "" {
		return nil, fmt.Errorf("git path is required")
	}

	serverGitConfig := GitConfig{
		Repository: git.Repository,
		Path:       git.Path,
	}

	switch {
	case git.Branch != "":
		serverGitConfig.Branch = git.Branch
	case git.Tag != "":
		serverGitConfig.Tag = git.Tag
	case git.Commit != "":
		serverGitConfig.Commit = git.Commit
	default:
		return nil, fmt.Errorf("git branch, tag, and commit are mutually exclusive, please provide only one of them")
	}

	// Build auth config if specified
	if git.Auth != nil {
		authConfig, err := buildGitAuthConfig(git.Auth)
		if err != nil {
			return nil, fmt.Errorf("failed to build git auth configuration: %w", err)
		}
		serverGitConfig.Auth = authConfig
	}

	return &serverGitConfig, nil
}

// buildGitAuthConfig creates a GitAuthConfig from the CRD spec.
// It validates that required fields are present and constructs the password file path.
func buildGitAuthConfig(auth *mcpv1alpha1.GitAuthConfig) (*GitAuthConfig, error) {
	if auth == nil {
		return nil, nil
	}

	if auth.Username == "" {
		return nil, fmt.Errorf("git auth username is required")
	}

	if auth.PasswordSecretRef.Name == "" {
		return nil, fmt.Errorf("git auth password secret reference name is required")
	}

	if auth.PasswordSecretRef.Key == "" {
		return nil, fmt.Errorf("git auth password secret reference key is required")
	}

	return &GitAuthConfig{
		Username:     auth.Username,
		PasswordFile: buildGitPasswordFilePath(&auth.PasswordSecretRef),
	}, nil
}

// buildGitPasswordFilePath constructs the file path where a git password secret will be mounted.
// The secretRef must have both Name and Key set (validated by buildGitAuthConfig).
func buildGitPasswordFilePath(secretRef *corev1.SecretKeySelector) string {
	if secretRef == nil {
		return ""
	}
	return fmt.Sprintf("/secrets/%s/%s", secretRef.Name, secretRef.Key)
}

func buildAPISourceConfig(api *mcpv1alpha1.APISource) (*APIConfig, error) {
	if api == nil {
		return nil, fmt.Errorf("api source configuration is required")
	}

	if api.Endpoint == "" {
		return nil, fmt.Errorf("api endpoint is required")
	}

	return &APIConfig{
		Endpoint: api.Endpoint,
	}, nil
}

// buildDatabaseConfig creates a DatabaseConfig from the CRD spec.
// If the spec is nil or fields are empty, sensible defaults are used.
func buildDatabaseConfig(dbConfig *mcpv1alpha1.MCPRegistryDatabaseConfig) *DatabaseConfig {
	// Default values
	config := &DatabaseConfig{
		Host:            "postgres",
		Port:            5432,
		User:            "db_app",
		MigrationUser:   "db_migrator",
		Database:        "registry",
		SSLMode:         "prefer",
		MaxOpenConns:    10,
		MaxIdleConns:    2,
		ConnMaxLifetime: "30m",
	}

	// If no database config specified, return defaults
	if dbConfig == nil {
		return config
	}

	// Override defaults with values from CRD spec if provided
	if dbConfig.Host != "" {
		config.Host = dbConfig.Host
	}
	if dbConfig.Port != 0 {
		config.Port = dbConfig.Port
	}
	if dbConfig.User != "" {
		config.User = dbConfig.User
	}
	if dbConfig.MigrationUser != "" {
		config.MigrationUser = dbConfig.MigrationUser
	}
	if dbConfig.Database != "" {
		config.Database = dbConfig.Database
	}
	if dbConfig.SSLMode != "" {
		config.SSLMode = dbConfig.SSLMode
	}
	if dbConfig.MaxOpenConns != 0 {
		config.MaxOpenConns = dbConfig.MaxOpenConns
	}
	if dbConfig.MaxIdleConns != 0 {
		config.MaxIdleConns = dbConfig.MaxIdleConns
	}
	if dbConfig.ConnMaxLifetime != "" {
		config.ConnMaxLifetime = dbConfig.ConnMaxLifetime
	}
	if dbConfig.MaxMetaSize != nil {
		config.MaxMetaSize = dbConfig.MaxMetaSize
	}
	if dbConfig.DynamicAuth != nil && dbConfig.DynamicAuth.AWSRDSIAM != nil {
		config.DynamicAuth = &DynamicAuthConfig{
			AWSRDSIAM: &DynamicAuthAWSRDSIAM{
				Region: dbConfig.DynamicAuth.AWSRDSIAM.Region,
			},
		}
	}

	return config
}

// buildAuthConfig creates an AuthConfig from the CRD spec.
// If the spec is nil, defaults to anonymous authentication.
func buildAuthConfig(
	authConfig *mcpv1alpha1.MCPRegistryAuthConfig,
) (*AuthConfig, error) {
	config := &AuthConfig{}
	if authConfig == nil {
		// Note: we default to anonymous for backwards compatibility and
		// because we don't have active production deployments yet.
		// The plan is to remove this default and require the user to specify
		// the mode.
		// TODO: Remove this default once testing is complete.
		config.Mode = AuthModeAnonymous
		return config, nil
	}

	// Map the mode from CRD type to config type
	switch authConfig.Mode {
	case mcpv1alpha1.MCPRegistryAuthModeOAuth:
		config.Mode = AuthModeOAuth
	case mcpv1alpha1.MCPRegistryAuthModeAnonymous:
		config.Mode = AuthModeAnonymous
	default:
		// Default to anonymous if mode is empty or unrecognized
		config.Mode = AuthModeAnonymous
	}

	// Map public paths
	if len(authConfig.PublicPaths) > 0 {
		config.PublicPaths = authConfig.PublicPaths
	}

	// Build OAuth config if mode is oauth and OAuth config is provided
	if config.Mode == AuthModeOAuth && authConfig.OAuth != nil {
		oauthConfig, err := buildOAuthConfig(authConfig.OAuth)
		if err != nil {
			return nil, fmt.Errorf("failed to build OAuth configuration: %w", err)
		}
		config.OAuth = oauthConfig
	}

	// Build authz config if provided
	if authConfig.Authz != nil {
		authzConfig, err := buildAuthzConfig(authConfig.Authz)
		if err != nil {
			return nil, fmt.Errorf("failed to build authorization configuration: %w", err)
		}
		config.Authz = authzConfig
	}

	return config, nil
}

// buildAuthzConfig creates an AuthzConfig from the CRD spec.
func buildAuthzConfig(authzConfig *mcpv1alpha1.MCPRegistryAuthzConfig) (*AuthzConfig, error) {
	if authzConfig == nil {
		return nil, nil
	}

	config := &AuthzConfig{}

	roles := &authzConfig.Roles

	var err error
	config.Roles.SuperAdmin, err = deserializeRoleEntries(roles.SuperAdmin)
	if err != nil {
		return nil, fmt.Errorf("invalid superAdmin roles: %w", err)
	}

	config.Roles.ManageSources, err = deserializeRoleEntries(roles.ManageSources)
	if err != nil {
		return nil, fmt.Errorf("invalid manageSources roles: %w", err)
	}

	config.Roles.ManageRegistries, err = deserializeRoleEntries(roles.ManageRegistries)
	if err != nil {
		return nil, fmt.Errorf("invalid manageRegistries roles: %w", err)
	}

	config.Roles.ManageEntries, err = deserializeRoleEntries(roles.ManageEntries)
	if err != nil {
		return nil, fmt.Errorf("invalid manageEntries roles: %w", err)
	}

	return config, nil
}

// deserializeRoleEntries converts a slice of apiextensionsv1.JSON to []map[string]any
func deserializeRoleEntries(entries []apiextensionsv1.JSON) ([]map[string]any, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	result := make([]map[string]any, 0, len(entries))
	for i, entry := range entries {
		m, err := deserializeRoleEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		if m != nil {
			result = append(result, m)
		}
	}

	return result, nil
}

// buildOAuthConfig creates an OAuthConfig from the CRD spec.
func buildOAuthConfig(
	oauthConfig *mcpv1alpha1.MCPRegistryOAuthConfig,
) (*OAuthConfig, error) {
	if oauthConfig == nil {
		return nil, fmt.Errorf("OAuth configuration is required")
	}

	// TODO: Uncomment this after testing intra-cluster authentication
	if len(oauthConfig.Providers) == 0 {
		return nil, fmt.Errorf("at least one OAuth provider is required")
	}

	config := &OAuthConfig{
		ResourceURL:     oauthConfig.ResourceURL,
		ScopesSupported: oauthConfig.ScopesSupported,
		Realm:           oauthConfig.Realm,
		Providers:       make([]OAuthProviderConfig, 0, len(oauthConfig.Providers)),
	}

	// Build provider configs
	for _, providerSpec := range oauthConfig.Providers {
		provider, err := buildOAuthProviderConfig(&providerSpec)
		if err != nil {
			return nil, fmt.Errorf("failed to build OAuth provider configuration: %w", err)
		}
		config.Providers = append(config.Providers, *provider)
	}

	return config, nil
}

// buildOAuthProviderConfig creates an OAuthProviderConfig from the CRD spec.
func buildOAuthProviderConfig(
	providerSpec *mcpv1alpha1.MCPRegistryOAuthProviderConfig,
) (*OAuthProviderConfig, error) {
	if providerSpec == nil {
		return nil, fmt.Errorf("provider specification is required")
	}

	if providerSpec.Name == "" {
		return nil, fmt.Errorf("provider name is required")
	}
	if providerSpec.IssuerURL == "" {
		return nil, fmt.Errorf("provider issuer URL is required")
	}
	if providerSpec.Audience == "" {
		return nil, fmt.Errorf("provider audience is required")
	}

	config := &OAuthProviderConfig{
		Name:           providerSpec.Name,
		IssuerURL:      providerSpec.IssuerURL,
		Audience:       providerSpec.Audience,
		AllowPrivateIP: providerSpec.AllowPrivateIP,
	}

	// JwksUrl is optional - if specified, OIDC discovery is skipped
	if providerSpec.JwksUrl != "" {
		config.JwksUrl = providerSpec.JwksUrl
	}

	// ClientID is optional, so we only set it if provided
	if providerSpec.ClientID != "" {
		config.ClientID = providerSpec.ClientID
	}

	// IntrospectionURL is optional - used for validating opaque tokens
	if providerSpec.IntrospectionURL != "" {
		config.IntrospectionURL = providerSpec.IntrospectionURL
	}

	// For ClientSecretRef, CACertRef, and AuthTokenRef, we store the path where the secret/configmap
	// will be mounted. The actual mounting is handled by the pod template spec builder.
	// The registry server will read the file at runtime.
	if providerSpec.ClientSecretRef != nil {
		// Secret will be mounted at /secrets/{secretName}/{key}
		config.ClientSecretFile = buildSecretFilePath(providerSpec.ClientSecretRef)
	}

	// CaCertPath can be set directly or via CACertRef
	// Direct path takes precedence over reference
	if providerSpec.CaCertPath != "" {
		config.CACertPath = providerSpec.CaCertPath
	} else if providerSpec.CACertRef != nil {
		// ConfigMap will be mounted at /config/certs/{configMapName}/{key}
		config.CACertPath = buildCACertFilePath(providerSpec.CACertRef)
	}

	// AuthTokenFile can be set directly or via AuthTokenRef
	// Direct path takes precedence over reference
	if providerSpec.AuthTokenFile != "" {
		config.AuthTokenFile = providerSpec.AuthTokenFile
	} else if providerSpec.AuthTokenRef != nil {
		// Secret will be mounted at /secrets/{secretName}/{key}
		config.AuthTokenFile = buildSecretFilePath(providerSpec.AuthTokenRef)
	}

	return config, nil
}

// buildSecretFilePath constructs the file path where a secret will be mounted
func buildSecretFilePath(secretRef *corev1.SecretKeySelector) string {
	if secretRef == nil {
		return ""
	}
	key := secretRef.Key
	if key == "" {
		key = "clientSecret"
	}
	return fmt.Sprintf("/secrets/%s/%s", secretRef.Name, key)
}

// buildTelemetryConfig creates a TelemetryConfig from the CRD spec.
// Returns nil if the spec is nil (telemetry disabled).
func buildTelemetryConfig(telConfig *mcpv1alpha1.MCPRegistryTelemetryConfig) (*TelemetryConfig, error) {
	if telConfig == nil {
		return nil, nil
	}

	config := &TelemetryConfig{
		Enabled:        telConfig.Enabled,
		ServiceName:    telConfig.ServiceName,
		ServiceVersion: telConfig.ServiceVersion,
		Endpoint:       telConfig.Endpoint,
		Insecure:       telConfig.Insecure,
	}

	if telConfig.Tracing != nil {
		tracingConfig := &TracingConfig{
			Enabled: telConfig.Tracing.Enabled,
		}
		if telConfig.Tracing.Sampling != nil {
			val, err := strconv.ParseFloat(*telConfig.Tracing.Sampling, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid tracing sampling value %q: %w", *telConfig.Tracing.Sampling, err)
			}
			tracingConfig.Sampling = &val
		}
		config.Tracing = tracingConfig
	}

	if telConfig.Metrics != nil {
		config.Metrics = &MetricsConfig{
			Enabled: telConfig.Metrics.Enabled,
		}
	}

	return config, nil
}

// buildCACertFilePath constructs the file path where a CA cert configmap will be mounted
func buildCACertFilePath(configMapRef *corev1.ConfigMapKeySelector) string {
	if configMapRef == nil {
		return ""
	}
	key := configMapRef.Key
	if key == "" {
		key = "ca.crt"
	}
	return fmt.Sprintf("/config/certs/%s/%s", configMapRef.Name, key)
}
