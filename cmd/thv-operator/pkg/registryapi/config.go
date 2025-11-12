package registryapi

import (
	"fmt"

	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

// ConfigManager provides methods to build registry server configuration from MCPRegistry resources
type ConfigManager interface {
	BuildConfig(mcpRegistry *mcpv1alpha1.MCPRegistry) (*Config, error)
}

// NewConfigManager creates a new instance of ConfigManager
func NewConfigManager() ConfigManager {
	return &configManager{}
}

type configManager struct{}

const (
	// SourceTypeGit is the type for registry data stored in Git repositories
	SourceTypeGit = "git"

	// SourceTypeAPI is the type for registry data fetched from API endpoints
	SourceTypeAPI = "api"

	// SourceTypeFile is the type for registry data stored in local files
	SourceTypeFile = "file"

	// RegistryJSONFilePath is the file path where the registry JSON file will be mounted
	RegistryJSONFilePath = "/etc/registry/registry.json"
)

// Config represents the root configuration structure
type Config struct {
	// RegistryName is the name/identifier for this registry instance
	// Defaults to "default" if not specified
	RegistryName string            `yaml:"registryName,omitempty"`
	Source       *SourceConfig     `yaml:"source"`
	SyncPolicy   *SyncPolicyConfig `yaml:"syncPolicy,omitempty"`
	Filter       *FilterConfig     `yaml:"filter,omitempty"`
}

// SourceConfig defines the data source configuration
type SourceConfig struct {
	Type   string      `yaml:"type"`
	Format string      `yaml:"format"`
	Git    *GitConfig  `yaml:"git,omitempty"`
	API    *APIConfig  `yaml:"api,omitempty"`
	File   *FileConfig `yaml:"file,omitempty"`
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
			Name:      fmt.Sprintf("%s-configmap", c.RegistryName),
			Namespace: mcpRegistry.Namespace,
			Annotations: map[string]string{
				"toolhive.dev/content-checksum": ctrlutil.CalculateConfigHash(yamlData),
			},
		},
		Data: map[string]string{
			"config.yaml": string(yamlData),
		},
	}
	return configMap, nil
}

func (*configManager) BuildConfig(mcpRegistry *mcpv1alpha1.MCPRegistry) (*Config, error) {
	config := Config{}

	if mcpRegistry.Name == "" {
		return nil, fmt.Errorf("registry name is required")
	}

	config.RegistryName = mcpRegistry.Name

	if err := buildSourceConfig(&mcpRegistry.Spec.Source, &config); err != nil {
		return nil, fmt.Errorf("failed to build source configuration: %w", err)
	}

	if err := buildSyncPolicyConfig(mcpRegistry.Spec.SyncPolicy, &config); err != nil {
		return nil, fmt.Errorf("failed to build sync policy configuration: %w", err)
	}

	buildFilterConfig(mcpRegistry.Spec.Filter, &config)

	return &config, nil
}

func buildFilterConfig(filter *mcpv1alpha1.RegistryFilter, config *Config) {
	if filter == nil {
		return
	}

	// Initialize Filter if needed
	if config.Filter == nil {
		config.Filter = &FilterConfig{}
	}

	if filter.NameFilters != nil {
		config.Filter.Names = &NameFilterConfig{
			Include: filter.NameFilters.Include,
			Exclude: filter.NameFilters.Exclude,
		}
	}

	if filter.Tags != nil {
		config.Filter.Tags = &TagFilterConfig{
			Include: filter.Tags.Include,
			Exclude: filter.Tags.Exclude,
		}
	}
}

func buildSyncPolicyConfig(syncPolicy *mcpv1alpha1.SyncPolicy, config *Config) error {
	if syncPolicy == nil {
		return fmt.Errorf("sync policy configuration is required")
	}

	if syncPolicy.Interval == "" {
		return fmt.Errorf("sync policy interval is required")
	}

	config.SyncPolicy = &SyncPolicyConfig{
		Interval: syncPolicy.Interval,
	}

	return nil
}

func buildSourceConfig(source *mcpv1alpha1.MCPRegistrySource, config *Config) error {
	if source == nil || source.Type == "" {
		return fmt.Errorf("source type is required")
	}

	sourceConfig := SourceConfig{}

	if source.Format == "" {
		return fmt.Errorf("source format is required")
	}
	sourceConfig.Format = source.Format

	switch source.Type {
	case mcpv1alpha1.RegistrySourceTypeConfigMap:
		// we use the file source config for configmap sources
		// because the configmap will be mounted as a file in the registry server container.
		// this stops the registry server worrying about configmap sources when all it has to do
		// is read the file on startup
		sourceConfig.File = &FileConfig{
			Path: RegistryJSONFilePath,
		}
		sourceConfig.Type = SourceTypeFile
	case mcpv1alpha1.RegistrySourceTypeGit:
		gitConfig, err := buildGitSourceConfig(source.Git)
		if err != nil {
			return fmt.Errorf("failed to build Git source configuration: %w", err)
		}
		sourceConfig.Git = gitConfig
		sourceConfig.Type = SourceTypeGit
	case mcpv1alpha1.RegistrySourceTypeAPI:
		apiConfig, err := buildAPISourceConfig(source.API)
		if err != nil {
			return fmt.Errorf("failed to build API source configuration: %w", err)
		}
		sourceConfig.API = apiConfig
		sourceConfig.Type = SourceTypeAPI
	default:
		return fmt.Errorf("unsupported source type: %s", source.Type)
	}

	config.Source = &sourceConfig
	return nil
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
