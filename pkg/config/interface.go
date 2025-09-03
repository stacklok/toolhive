package config

// Provider defines the interface for configuration operations
type Provider interface {
	GetConfig() *Config
	UpdateConfig(updateFn func(*Config)) error
	LoadOrCreateConfig() (*Config, error)
}

// DefaultProvider implements Provider using the default XDG config path
type DefaultProvider struct{}

// NewDefaultProvider creates a new default config provider
func NewDefaultProvider() *DefaultProvider {
	return &DefaultProvider{}
}

// GetConfig returns the singleton config (for backward compatibility)
func (*DefaultProvider) GetConfig() *Config {
	return GetConfig()
}

// UpdateConfig updates the config using the default path
func (*DefaultProvider) UpdateConfig(updateFn func(*Config)) error {
	return UpdateConfig(updateFn)
}

// LoadOrCreateConfig loads or creates config using the default path
func (*DefaultProvider) LoadOrCreateConfig() (*Config, error) {
	return LoadOrCreateConfig()
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
