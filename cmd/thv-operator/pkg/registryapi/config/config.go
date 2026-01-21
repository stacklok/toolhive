// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package config provides management for the registry server configuration
package config

import (
	"fmt"
	"path/filepath"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
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

// Config represents the root configuration structure
type Config struct {
	// RegistryName is the name/identifier for this registry instance
	// Defaults to "default" if not specified
	RegistryName string           `yaml:"registryName,omitempty"`
	Registries   []RegistryConfig `yaml:"registries"`
	Database     *DatabaseConfig  `yaml:"database,omitempty"`
	Auth         *AuthConfig      `yaml:"auth,omitempty"`
}

// DatabaseConfig defines PostgreSQL database configuration
// Uses two-user security model: separate users for operations and migrations
type DatabaseConfig struct {
	// Host is the database server hostname
	Host string `yaml:"host"`

	// Port is the database server port
	Port int `yaml:"port"`

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
	MaxOpenConns int `yaml:"maxOpenConns"`

	// MaxIdleConns is the maximum number of idle connections in the pool
	MaxIdleConns int `yaml:"maxIdleConns"`

	// ConnMaxLifetime is the maximum amount of time a connection may be reused
	ConnMaxLifetime string `yaml:"connMaxLifetime"`
}

// RegistryConfig defines the configuration for a registry data source
type RegistryConfig struct {
	// Name is a unique identifier for this registry configuration
	Name       string            `yaml:"name"`
	Format     string            `yaml:"format"`
	Git        *GitConfig        `yaml:"git,omitempty"`
	API        *APIConfig        `yaml:"api,omitempty"`
	File       *FileConfig       `yaml:"file,omitempty"`
	Kubernetes *KubernetesConfig `yaml:"kubernetes,omitempty"`
	SyncPolicy *SyncPolicyConfig `yaml:"syncPolicy,omitempty"`
	Filter     *FilterConfig     `yaml:"filter,omitempty"`
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

	// OAuth defines OAuth/OIDC specific authentication settings
	// Only used when Mode is "oauth"
	OAuth *OAuthConfig `yaml:"oauth,omitempty"`
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

// KubernetesConfig defines a Kubernetes-based registry source where data is discovered
// from MCPServer resources in the cluster. This is the default type for the built-in "default" registry.
type KubernetesConfig struct {
	// Empty struct - presence indicates this is a Kubernetes registry
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

// FileConfig defines local file source configuration
type FileConfig struct {
	// Path is the path to the registry.json file on the local filesystem
	// Can be absolute or relative to the working directory
	Path string `yaml:"path"`
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
			Name:      fmt.Sprintf("%s-registry-server-config", c.RegistryName),
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

// DefaultRegistryName is the name of the default managed registry
const DefaultRegistryName = "default"

func (cm *configManager) BuildConfig() (*Config, error) {
	config := Config{}

	mcpRegistry := cm.mcpRegistry

	if mcpRegistry.Name == "" {
		return nil, fmt.Errorf("registry name is required")
	}

	config.RegistryName = mcpRegistry.Name

	if len(mcpRegistry.Spec.Registries) == 0 {
		return nil, fmt.Errorf("at least one registry must be specified")
	}

	// Validate registry names are unique
	if err := validateRegistryNames(mcpRegistry.Spec.Registries); err != nil {
		return nil, fmt.Errorf("invalid registry configuration: %w", err)
	}

	// Build registry configs from user-specified registries
	userRegistries := make([]RegistryConfig, 0, len(mcpRegistry.Spec.Registries))
	for _, registrySpec := range mcpRegistry.Spec.Registries {
		registryConfig, err := buildRegistryConfig(&registrySpec)
		if err != nil {
			return nil, fmt.Errorf("failed to build registry configuration for %q: %w", registrySpec.Name, err)
		}
		userRegistries = append(userRegistries, *registryConfig)
	}

	// Prepend the default kubernetes registry as the first entry
	defaultRegistry := RegistryConfig{
		Name:       DefaultRegistryName,
		Format:     mcpv1alpha1.RegistryFormatUpstream,
		Kubernetes: &KubernetesConfig{},
	}
	config.Registries = append([]RegistryConfig{defaultRegistry}, userRegistries...)

	// Build database configuration from CRD spec or use defaults
	config.Database = buildDatabaseConfig(mcpRegistry.Spec.DatabaseConfig)

	// Build authentication configuration from CRD spec or use defaults
	authConfig, err := buildAuthConfig(mcpRegistry.Spec.AuthConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build authentication configuration: %w", err)
	}
	config.Auth = authConfig

	return &config, nil
}

// validateRegistryNames ensures all registry names are unique
func validateRegistryNames(registries []mcpv1alpha1.MCPRegistryConfig) error {
	seen := make(map[string]bool)
	for _, registry := range registries {
		if registry.Name == "" {
			return fmt.Errorf("registry name is required")
		}
		if seen[registry.Name] {
			return fmt.Errorf("duplicate registry name: %q", registry.Name)
		}
		seen[registry.Name] = true
	}
	return nil
}

func buildFilePath(registryName string) *FileConfig {
	return buildFilePathWithCustomName(registryName, RegistryJSONFileName)
}

func buildFilePathWithCustomName(registryName string, filename string) *FileConfig {
	return &FileConfig{
		Path: filepath.Join(RegistryJSONFilePath, registryName, filename),
	}
}

//nolint:gocyclo // Complexity is acceptable for handling multiple source types
func buildRegistryConfig(registrySpec *mcpv1alpha1.MCPRegistryConfig) (*RegistryConfig, error) {
	if registrySpec.Name == "" {
		return nil, fmt.Errorf("registry name is required")
	}

	registryConfig := RegistryConfig{
		Name:   registrySpec.Name,
		Format: registrySpec.Format,
	}

	if registrySpec.Format == "" {
		registryConfig.Format = mcpv1alpha1.RegistryFormatToolHive
	}

	// Determine source type and build appropriate config
	sourceCount := 0
	if registrySpec.ConfigMapRef != nil {
		sourceCount++
		// we use the file source config for configmap sources
		// because the configmap will be mounted as a file in the registry server container.
		// this stops the registry server worrying about configmap sources when all it has to do
		// is read the file on startup
		registryConfig.File = buildFilePath(registrySpec.Name)
	}
	if registrySpec.Git != nil {
		sourceCount++
		gitConfig, err := buildGitSourceConfig(registrySpec.Git)
		if err != nil {
			return nil, fmt.Errorf("failed to build Git source configuration: %w", err)
		}
		registryConfig.Git = gitConfig
	}
	if registrySpec.API != nil {
		sourceCount++
		apiConfig, err := buildAPISourceConfig(registrySpec.API)
		if err != nil {
			return nil, fmt.Errorf("failed to build API source configuration: %w", err)
		}
		registryConfig.API = apiConfig
	}
	if registrySpec.PVCRef != nil {
		sourceCount++
		// PVC sources are mounted at /config/registry/{registryName}/
		// File path: /config/registry/{registryName}/{pvcRef.path}
		// Multiple registries can share the same PVC by mounting it at different paths
		pvcPath := RegistryJSONFileName
		if registrySpec.PVCRef.Path != "" {
			pvcPath = registrySpec.PVCRef.Path
		}
		registryConfig.File = buildFilePathWithCustomName(registrySpec.Name, pvcPath)
	}

	if sourceCount == 0 {
		return nil, fmt.Errorf("exactly one source type (ConfigMapRef, Git, API, or PVCRef) must be specified")
	}
	if sourceCount > 1 {
		return nil, fmt.Errorf("only one source type (ConfigMapRef, Git, API, or PVCRef) can be specified")
	}

	// Build sync policy
	if registrySpec.SyncPolicy != nil {
		if registrySpec.SyncPolicy.Interval == "" {
			return nil, fmt.Errorf("sync policy interval is required")
		}
		registryConfig.SyncPolicy = &SyncPolicyConfig{
			Interval: registrySpec.SyncPolicy.Interval,
		}
	}

	// Build filter
	if registrySpec.Filter != nil {
		filterConfig := &FilterConfig{}
		if registrySpec.Filter.NameFilters != nil {
			filterConfig.Names = &NameFilterConfig{
				Include: registrySpec.Filter.NameFilters.Include,
				Exclude: registrySpec.Filter.NameFilters.Exclude,
			}
		}
		if registrySpec.Filter.Tags != nil {
			filterConfig.Tags = &TagFilterConfig{
				Include: registrySpec.Filter.Tags.Include,
				Exclude: registrySpec.Filter.Tags.Exclude,
			}
		}
		registryConfig.Filter = filterConfig
	}

	return &registryConfig, nil
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

	return &serverGitConfig, nil
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

	// Build OAuth config if mode is oauth and OAuth config is provided
	if config.Mode == AuthModeOAuth && authConfig.OAuth != nil {
		oauthConfig, err := buildOAuthConfig(authConfig.OAuth)
		if err != nil {
			return nil, fmt.Errorf("failed to build OAuth configuration: %w", err)
		}
		config.OAuth = oauthConfig
	}

	return config, nil
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
