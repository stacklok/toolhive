package config

import (
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// Provider defines the interface for configuration operations
//
//go:generate mockgen -destination=mocks/mock_provider.go -package=mocks -source=interface.go Provider
type Provider interface {
	GetConfig() *Config
	UpdateConfig(updateFn func(*Config)) error
	LoadOrCreateConfig() (*Config, error)

	// Registry operations
	SetRegistryURL(registryURL string, allowPrivateRegistryIp bool) error
	SetRegistryAPI(apiURL string, allowPrivateRegistryIp bool) error
	SetRegistryFile(registryPath string) error
	UnsetRegistry() error
	GetRegistryConfig() (url, localPath string, allowPrivateIP bool, registryType string)

	// CA certificate operations
	SetCACert(certPath string) error
	GetCACert() (certPath string, exists bool, accessible bool)
	UnsetCACert() error
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

// SetRegistryAPI validates and sets an MCP Registry API endpoint
func (d *DefaultProvider) SetRegistryAPI(apiURL string, allowPrivateRegistryIp bool) error {
	return setRegistryAPI(d, apiURL, allowPrivateRegistryIp)
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

// SetCACert validates and sets the CA certificate path
func (d *DefaultProvider) SetCACert(certPath string) error {
	return setCACert(d, certPath)
}

// GetCACert returns the currently configured CA certificate path and its accessibility status
func (d *DefaultProvider) GetCACert() (certPath string, exists bool, accessible bool) {
	return getCACert(d)
}

// UnsetCACert removes the CA certificate configuration
func (d *DefaultProvider) UnsetCACert() error {
	return unsetCACert(d)
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

// SetRegistryAPI validates and sets an MCP Registry API endpoint
func (p *PathProvider) SetRegistryAPI(apiURL string, allowPrivateRegistryIp bool) error {
	return setRegistryAPI(p, apiURL, allowPrivateRegistryIp)
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

// SetCACert validates and sets the CA certificate path
func (p *PathProvider) SetCACert(certPath string) error {
	return setCACert(p, certPath)
}

// GetCACert returns the currently configured CA certificate path and its accessibility status
func (p *PathProvider) GetCACert() (certPath string, exists bool, accessible bool) {
	return getCACert(p)
}

// UnsetCACert removes the CA certificate configuration
func (p *PathProvider) UnsetCACert() error {
	return unsetCACert(p)
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

// SetRegistryAPI is a no-op for Kubernetes environments
func (*KubernetesProvider) SetRegistryAPI(_ string, _ bool) error {
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

// SetCACert is a no-op for Kubernetes environments
func (*KubernetesProvider) SetCACert(_ string) error {
	return nil
}

// GetCACert returns empty CA cert configuration for Kubernetes environments
func (*KubernetesProvider) GetCACert() (certPath string, exists bool, accessible bool) {
	return "", false, false
}

// UnsetCACert is a no-op for Kubernetes environments
func (*KubernetesProvider) UnsetCACert() error {
	return nil
}

// NewProvider creates the appropriate config provider based on the runtime environment
func NewProvider() Provider {
	if runtime.IsKubernetesRuntime() {
		return NewKubernetesProvider()
	}
	return NewDefaultProvider()
}
