package config

import (
	"os"
	"sync"

	"github.com/stacklok/toolhive/pkg/logger"
)

// Singleton value - should only be written to by the getSingletonConfig function.
var appConfig *Config

var lock = &sync.RWMutex{}

// SetSingletonConfig allows tests to pre-initialize the singleton with test data
// This prevents the singleton from loading the real config file during tests
func SetSingletonConfig(cfg *Config) {
	lock.Lock()
	defer lock.Unlock()
	appConfig = cfg
}

// ResetSingleton clears the singleton - useful for test cleanup
func ResetSingleton() {
	lock.Lock()
	defer lock.Unlock()
	appConfig = nil
}

// getSingletonConfig is a Singleton that returns the application configuration.
// This is only used internally by the DefaultProvider
func getSingletonConfig() *Config {
	// First check with read lock for performance
	lock.RLock()
	if appConfig != nil {
		defer lock.RUnlock()
		return appConfig
	}
	lock.RUnlock()

	// If config is nil, acquire write lock and double-check
	lock.Lock()
	defer lock.Unlock()
	if appConfig == nil {
		config, err := LoadOrCreateConfig()
		if err != nil {
			logger.Errorf("error loading configuration: %v", err)
			os.Exit(1)
		}
		appConfig = config
	}
	return appConfig
}
