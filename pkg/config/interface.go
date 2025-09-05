package config

import (
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// Provider defines the interface for configuration operations
type Provider interface {
	GetConfig() *Config
	UpdateConfig(updateFn func(*Config)) error
	LoadOrCreateConfig() (*Config, error)

	// Registry operations
	SetRegistryURL(registryURL string, allowPrivateRegistryIp bool) error
	SetRegistryFile(registryPath string) error
	UnsetRegistry() error
	GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string)
}

// DefaultProvider implements Provider using the default XDG config path
type DefaultProvider struct{}

// NewDefaultProvider creates a new default config provider
func NewDefaultProvider() *DefaultProvider {
	return &DefaultProvider{}
}

// GetConfig returns the singleton config (for backward compatibility)
func (*DefaultProvider) GetConfig() *Config {
	return getSingletonConfig()
}

// UpdateConfig updates the config using the default path
func (*DefaultProvider) UpdateConfig(updateFn func(*Config)) error {
	return UpdateConfigAtPath("", updateFn)
}

// LoadOrCreateConfig loads or creates config using the default path
func (*DefaultProvider) LoadOrCreateConfig() (*Config, error) {
	return LoadOrCreateConfigWithDefaultPath()
}

// SetRegistryURL validates and sets a registry URL
func (d *DefaultProvider) SetRegistryURL(registryURL string, allowPrivateRegistryIp bool) error {
	return setRegistryURL(d, registryURL, allowPrivateRegistryIp)
}

// SetRegistryFile validates and sets a local registry file
func (d *DefaultProvider) SetRegistryFile(registryPath string) error {
	return setRegistryFile(d, registryPath)
}

// UnsetRegistry resets registry configuration to defaults
func (d *DefaultProvider) UnsetRegistry() error {
	return unsetRegistry(d)
}

// GetRegistryConfig returns current registry configuration
func (d *DefaultProvider) GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string) {
	return getRegistryConfig(d)
}

// PathProvider implements Provider using a specific config path
type PathProvider struct {
	configPath string
}

// NewPathProvider creates a new config provider with a specific path
func NewPathProvider(configPath string) *PathProvider {
	return &PathProvider{configPath: configPath}
}

// GetConfig loads and returns the config from the specific path
func (p *PathProvider) GetConfig() *Config {
	config, err := LoadOrCreateConfigWithPath(p.configPath)
	if err != nil {
		// Return default config on error, similar to singleton behavior
		defaultConfig := createNewConfigWithDefaults()
		return &defaultConfig
	}
	return config
}

// UpdateConfig updates the config at the specific path
func (p *PathProvider) UpdateConfig(updateFn func(*Config)) error {
	return UpdateConfigAtPath(p.configPath, updateFn)
}

// LoadOrCreateConfig loads or creates config at the specific path
func (p *PathProvider) LoadOrCreateConfig() (*Config, error) {
	return LoadOrCreateConfigWithPath(p.configPath)
}

// SetRegistryURL validates and sets a registry URL
func (p *PathProvider) SetRegistryURL(registryURL string, allowPrivateRegistryIp bool) error {
	return setRegistryURL(p, registryURL, allowPrivateRegistryIp)
}

// SetRegistryFile validates and sets a local registry file
func (p *PathProvider) SetRegistryFile(registryPath string) error {
	return setRegistryFile(p, registryPath)
}

// UnsetRegistry resets registry configuration to defaults
func (p *PathProvider) UnsetRegistry() error {
	return unsetRegistry(p)
}

// GetRegistryConfig returns current registry configuration
func (p *PathProvider) GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string) {
	return getRegistryConfig(p)
}

// KubernetesProvider is a no-op implementation of Provider for Kubernetes environments.
// In Kubernetes, configuration is managed by the cluster, not by local files.
type KubernetesProvider struct{}

// NewKubernetesProvider creates a new no-op config provider for Kubernetes environments
func NewKubernetesProvider() *KubernetesProvider {
	return &KubernetesProvider{}
}

// GetConfig returns a default config for Kubernetes environments
func (*KubernetesProvider) GetConfig() *Config {
	config := createNewConfigWithDefaults()
	return &config
}

// UpdateConfig is a no-op for Kubernetes environments
func (*KubernetesProvider) UpdateConfig(_ func(*Config)) error {
	return nil
}

// LoadOrCreateConfig returns a default config for Kubernetes environments
func (*KubernetesProvider) LoadOrCreateConfig() (*Config, error) {
	config := createNewConfigWithDefaults()
	return &config, nil
}

// SetRegistryURL is a no-op for Kubernetes environments
func (*KubernetesProvider) SetRegistryURL(_ string, _ bool) error {
	return nil
}

// SetRegistryFile is a no-op for Kubernetes environments
func (*KubernetesProvider) SetRegistryFile(_ string) error {
	return nil
}

// UnsetRegistry is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetRegistry() error {
	return nil
}

// GetRegistryConfig returns empty registry configuration for Kubernetes environments
func (*KubernetesProvider) GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string) {
	return "", "", false, ""
}

// NewProvider creates the appropriate config provider based on the runtime environment
func NewProvider() Provider {
	if runtime.IsKubernetesRuntime() {
		return NewKubernetesProvider()
	}
	return NewDefaultProvider()
}
