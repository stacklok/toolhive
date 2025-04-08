// Package config contains the definition of the application config structure
// and logic required to load and update it.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path"

	"github.com/adrg/xdg"
	"gopkg.in/yaml.v3"

	"github.com/StacklokLabs/toolhive/pkg/logger"
	"github.com/StacklokLabs/toolhive/pkg/secrets"
)

// Config represents the configuration of the application.
type Config struct {
	Secrets Secrets `yaml:"secrets"`
	Clients Clients `yaml:"clients"`
}

// Secrets contains the settings for secrets management.
type Secrets struct {
	ProviderType string `yaml:"provider_type"`
}

// Clients contains settings for client configuration.
type Clients struct {
	AutoDiscovery     bool     `yaml:"auto_discovery"`
	RegisteredClients []string `yaml:"registered_clients"`
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
			Clients: Clients{
				AutoDiscovery: true,
			},
		}

		// Prompt user explicitly for auto discovery behaviour.
		logger.Log.Info("Would you like to enable auto discovery and configuraion of MCP clients? (y/n) [n]: ")
		reader := bufio.NewReader(os.Stdin)
		input, err := reader.ReadString('\n')
		if err != nil {
			logger.Log.Info("Unable to read input, defaulting to No.")
		}
		// Treat anything except y/Y as n.
		if input == "y\n" || input == "Y\n" {
			config.Clients.AutoDiscovery = true
		} else {
			config.Clients.AutoDiscovery = false
		}

		// Persist the new default to disk.
		logger.Log.Info("initializing configuration file at %s", configPath)
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
	return xdg.ConfigFile("toolhive/config.yaml")
}
