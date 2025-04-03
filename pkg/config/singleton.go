package config

import (
	"fmt"
	"os"
)

// Singleton value - should only be written to by the init function.
var appConfig *Config

// GetConfig returns the application configuration.
// This can only be called after it is initialized in the init function.
func GetConfig() *Config {
	if appConfig == nil {
		panic("configuration is not initialized")
	}
	return appConfig
}

func init() {
	// Initialize the application configuration.
	var err error
	appConfig, err = LoadOrCreateConfig()
	if err != nil {
		fmt.Printf("error loading configuration: %v\n", err)
		os.Exit(1)
	}
}
