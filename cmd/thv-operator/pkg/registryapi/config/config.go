package config

import (
	"context"
	"fmt"
	"path/filepath"

	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

// ConfigManager provides methods to build registry server configuration from MCPRegistry resources
// and its persistence into a ConfigMap
//
//nolint:revive
type ConfigManager interface {
	BuildConfig() (*Config, error)
	UpsertConfigMap(ctx context.Context,
		mcpRegistry *mcpv1alpha1.MCPRegistry,
		desired *corev1.ConfigMap,
	) error
	GetRegistryServerConfigMapName() string
}

// NewConfigManager creates a new instance of ConfigManager with required dependencies
func NewConfigManager(
	k8sClient client.Client,
	scheme *runtime.Scheme,
	checksumManager checksum.RunConfigConfigMapChecksum,
	mcpRegistry *mcpv1alpha1.MCPRegistry,
) (ConfigManager, error) {
	if k8sClient == nil {
		return nil, fmt.Errorf("k8sClient is required and cannot be nil")
	}
	if scheme == nil {
		return nil, fmt.Errorf("scheme is required and cannot be nil")
	}
	if checksumManager == nil {
		return nil, fmt.Errorf("checksumManager is required and cannot be nil")
	}

	return &configManager{
		client:      k8sClient,
		scheme:      scheme,
		checksum:    checksumManager,
		mcpRegistry: mcpRegistry,
	}, nil
}

// NewConfigManagerForTesting creates a ConfigManager for testing purposes only.
// WARNING: This manager will panic if methods requiring dependencies are called.
// Only use this for testing BuildConfig or other methods that don't use k8s client.
func NewConfigManagerForTesting(mcpRegistry *mcpv1alpha1.MCPRegistry) ConfigManager {
	return &configManager{
		mcpRegistry: mcpRegistry,
	}
}

type configManager struct {
	client      client.Client
	scheme      *runtime.Scheme
	checksum    checksum.RunConfigConfigMapChecksum
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
}

// RegistryConfig defines the configuration for a registry data source
type RegistryConfig struct {
	// Name is a unique identifier for this registry configuration
	Name       string            `yaml:"name"`
	Format     string            `yaml:"format"`
	Git        *GitConfig        `yaml:"git,omitempty"`
	API        *APIConfig        `yaml:"api,omitempty"`
	File       *FileConfig       `yaml:"file,omitempty"`
	SyncPolicy *SyncPolicyConfig `yaml:"syncPolicy,omitempty"`
	Filter     *FilterConfig     `yaml:"filter,omitempty"`
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

	// Build registry configs
	config.Registries = make([]RegistryConfig, 0, len(mcpRegistry.Spec.Registries))
	for _, registrySpec := range mcpRegistry.Spec.Registries {
		registryConfig, err := buildRegistryConfig(&registrySpec)
		if err != nil {
			return nil, fmt.Errorf("failed to build registry configuration for %q: %w", registrySpec.Name, err)
		}
		config.Registries = append(config.Registries, *registryConfig)
	}

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
		registryConfig.File = &FileConfig{
			Path: filepath.Join(RegistryJSONFilePath, RegistryJSONFileName),
		}
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

	if sourceCount == 0 {
		return nil, fmt.Errorf("exactly one source type (ConfigMapRef, Git, or API) must be specified")
	}
	if sourceCount > 1 {
		return nil, fmt.Errorf("only one source type (ConfigMapRef, Git, or API) can be specified")
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

	serverGitConfig := GitConfig{
		Repository: git.Repository,
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
