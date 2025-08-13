package registry

import (
	"sync"

	"go.uber.org/zap"

	"github.com/stacklok/toolhive/pkg/config"
)

var (
	defaultProvider     Provider
	defaultProviderOnce sync.Once
	defaultProviderErr  error
)

// NewRegistryProvider creates a new registry provider based on the configuration
func NewRegistryProvider(cfg *config.Config) Provider {
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
func GetDefaultProvider(logger *zap.SugaredLogger) (Provider, error) {
	defaultProviderOnce.Do(func() {
		cfg, err := config.LoadOrCreateConfig(logger)
		if err != nil {
			defaultProviderErr = err
			return
		}
		defaultProvider = NewRegistryProvider(cfg)
	})

	return defaultProvider, defaultProviderErr
}

// ResetDefaultProvider clears the cached default provider instance
// This allows the provider to be recreated with updated configuration
func ResetDefaultProvider() {
	defaultProviderOnce = sync.Once{}
	defaultProvider = nil
	defaultProviderErr = nil
}
