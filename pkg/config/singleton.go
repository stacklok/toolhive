package config

import (
	"fmt"
	"os"
	"sync"

	"github.com/StacklokLabs/toolhive/pkg/logger"
)

// Singleton value - should only be written to by the GetConfig function.
var appConfig *Config

var lock = &sync.Mutex{}

// GetConfig is a Singleton that returns the application configuration.
func GetConfig() *Config {
	if appConfig == nil {
		lock.Lock()
		defer lock.Unlock()
		if appConfig == nil {
			appConfig, err := LoadOrCreateConfig()
			if err != nil {
				logger.Log.Error(fmt.Sprintf("error loading configuration: %v", err))
				os.Exit(1)
			}

			return appConfig
		}
	}
	return appConfig
}
