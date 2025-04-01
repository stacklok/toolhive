// Package config contains the definition of the application config structure
// and logic required to load and update it.
package config

import (
	"errors"
	"fmt"
	"os"
	"path"

	"github.com/adrg/xdg"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/vibetool/pkg/secrets"
)

// Config represents the configuration of the application.
type Config struct {
	Secrets Secrets `yaml:"secrets"`
}

// Secrets contains the configuration for secrets management.
type Secrets struct {
	ProviderType string `yaml:"provider_type"`
}

// LoadOrCreateConfig fetches the application configuration.
// If it does not already exist - it will create a new config file with default values.
func LoadOrCreateConfig() (*Config, error) {
	var config Config

	configPath, err := getConfigPath()
	if err != nil {
		return nil, fmt.Errorf("unable to fetch config path: %w", err)
	}

	// Check to see if the config file already exists.
	configPath = path.Clean(configPath)
	newConfig := false
	// #nosec G304: File path is not configurable at this time.
	_, err = os.Stat(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			newConfig = true
		} else {
			return nil, fmt.Errorf("failed to stat secrets file: %w", err)
		}
	}

	if newConfig {
		// Create a new config with default values.
		config = Config{
			Secrets: Secrets{
				ProviderType: string(secrets.BasicType),
			},
		}

		// Persist the new default to disk.
		fmt.Printf("initializing configuration file at %s\n", configPath)
		err = config.WriteConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to write default config: %w", err)
		}
	} else {
		// Load the existing config and decode.
		// #nosec G304: File path is not configurable at this time.
		configFile, err := os.ReadFile(configPath)
		if err != nil {
			return nil, fmt.Errorf("unable to read config file %s: %w", configPath, err)
		}
		err = yaml.Unmarshal(configFile, &config)
		if err != nil {
			return nil, fmt.Errorf("failed to parse config file yaml: %w", err)
		}
	}

	return &config, nil
}

// WriteConfig serializes the config struct and writes it to disk.
func (c *Config) WriteConfig() error {
	configPath, err := getConfigPath()
	if err != nil {
		return fmt.Errorf("unable to fetch config path: %w", err)
	}

	configBytes, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("error serializing config file: %w", err)
	}

	err = os.WriteFile(configPath, configBytes, 0600)
	if err != nil {
		return fmt.Errorf("error writing config file: %w", err)
	}
	return nil
}

// Consider making the config path configurable.
func getConfigPath() (string, error) {
	return xdg.ConfigFile("vibetool/config.yaml")
}
