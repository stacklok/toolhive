package config

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
	return UpdateConfig(updateFn)
}

// LoadOrCreateConfig loads or creates config using the default path
func (*DefaultProvider) LoadOrCreateConfig() (*Config, error) {
	return LoadOrCreateConfig()
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
