package registry

import (
	"sync"

	"github.com/stacklok/toolhive/pkg/config"
)

var (
	defaultProvider     Provider
	defaultProviderOnce sync.Once
	defaultProviderErr  error
	// defaultProviderMu protects the ResetDefaultProvider operation
	// to prevent race conditions when resetting the sync.Once.
	// The mutex is NOT needed for GetDefaultProviderWithConfig since
	// sync.Once already provides thread-safety for initialization.
	defaultProviderMu sync.Mutex
)

// NewRegistryProvider creates a new registry provider based on the configuration
func NewRegistryProvider(cfg *config.Config) Provider {
	// Priority order:
	// 1. API URL (if configured) - for live MCP Registry API queries
	// 2. Remote URL (if configured) - for static JSON over HTTP
	// 3. Local file path (if configured) - for local JSON file
	// 4. Default - embedded registry data

	if cfg != nil && len(cfg.RegistryApiUrl) > 0 {
		provider, err := NewAPIRegistryProvider(cfg.RegistryApiUrl, cfg.AllowPrivateRegistryIp)
		if err != nil {
			// Log error but fall back to default provider
			// This prevents application from failing if API is temporarily unavailable
			return NewLocalRegistryProvider()
		}
		return provider
	}
	if cfg != nil && len(cfg.RegistryUrl) > 0 {
		return NewRemoteRegistryProvider(cfg.RegistryUrl, cfg.AllowPrivateRegistryIp)
	}
	if cfg != nil && len(cfg.LocalRegistryPath) > 0 {
		return NewLocalRegistryProvider(cfg.LocalRegistryPath)
	}
	return NewLocalRegistryProvider()
}

// GetDefaultProvider returns the default registry provider instance
// This maintains backward compatibility with the existing singleton pattern
func GetDefaultProvider() (Provider, error) {
	return GetDefaultProviderWithConfig(config.NewDefaultProvider())
}

// GetDefaultProviderWithConfig returns a registry provider using the given config provider
// This allows tests to inject their own config provider
func GetDefaultProviderWithConfig(configProvider config.Provider) (Provider, error) {
	defaultProviderOnce.Do(func() {
		cfg, err := configProvider.LoadOrCreateConfig()
		if err != nil {
			defaultProviderErr = err
			return
		}
		defaultProvider = NewRegistryProvider(cfg)
	})

	return defaultProvider, defaultProviderErr
}

// ResetDefaultProvider clears the cached default provider instance
// This allows the provider to be recreated with updated configuration.
// This function is thread-safe and can be called concurrently.
// The mutex is required here because we're modifying the sync.Once itself,
// which is not a thread-safe operation.
func ResetDefaultProvider() {
	defaultProviderMu.Lock()
	defer defaultProviderMu.Unlock()

	// Reset the sync.Once to allow re-initialization
	defaultProviderOnce = sync.Once{}
	defaultProvider = nil
	defaultProviderErr = nil
}
