// Package config contains the definition of the application config structure
// and logic required to load and update it.
package config

import (
	"context"
	"fmt"
	"os"

	"github.com/adrg/xdg"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/env"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// Config represents the configuration of the application.
type Config struct {
	Secrets                Secrets             `yaml:"secrets"`
	Clients                Clients             `yaml:"clients"`
	RegistryUrl            string              `yaml:"registry_url"`
	LocalRegistryPath      string              `yaml:"local_registry_path"`
	AllowPrivateRegistryIp bool                `yaml:"allow_private_registry_ip"`
	CACertificatePath      string              `yaml:"ca_certificate_path,omitempty"`
	OTEL                   OpenTelemetryConfig `yaml:"otel,omitempty"`
	DefaultGroupMigration  bool                `yaml:"default_group_migration,omitempty"`
}

// Secrets contains the settings for secrets management.
type Secrets struct {
	ProviderType   string `yaml:"provider_type"`
	SetupCompleted bool   `yaml:"setup_completed"`
}

// validateProviderType validates and returns the secrets provider type.
func validateProviderType(provider string) (secrets.ProviderType, error) {
	switch provider {
	case string(secrets.EncryptedType):
		return secrets.EncryptedType, nil
	case string(secrets.OnePasswordType):
		return secrets.OnePasswordType, nil
	case string(secrets.NoneType):
		return secrets.NoneType, nil
	default:
		return "", fmt.Errorf("invalid secrets provider type: %s (valid types: %s, %s, %s)",
			provider, string(secrets.EncryptedType), string(secrets.OnePasswordType), string(secrets.NoneType))
	}
}

// GetProviderType returns the secrets provider type from the environment variable or application config.
// It first checks the TOOLHIVE_SECRETS_PROVIDER environment variable, and falls back to the config file.
// Returns ErrSecretsNotSetup if secrets have not been configured yet.
func (s *Secrets) GetProviderType() (secrets.ProviderType, error) {
	return s.GetProviderTypeWithEnv(&env.OSReader{})
}

// GetProviderTypeWithEnv returns the secrets provider type using the provided environment reader.
// This method allows for dependency injection of environment variable access for testing.
func (s *Secrets) GetProviderTypeWithEnv(envReader env.Reader) (secrets.ProviderType, error) {
	// Check if secrets setup has been completed
	if !s.SetupCompleted {
		return "", secrets.ErrSecretsNotSetup
	}

	// First check the environment variable
	if envVar := envReader.Getenv(secrets.ProviderEnvVar); envVar != "" {
		return validateProviderType(envVar)
	}

	// Fall back to config file
	return validateProviderType(s.ProviderType)
}

// Clients contains settings for client configuration.
type Clients struct {
	RegisteredClients []string `yaml:"registered_clients"`
	AutoDiscovery     bool     `yaml:"auto_discovery"` // Deprecated: kept for migration purposes only
}

// defaultPathGenerator generates the default config path using xdg
var defaultPathGenerator = func() (string, error) {
	return xdg.ConfigFile("toolhive/config.yaml")
}

// getConfigPath is the current path generator, can be replaced in tests
var getConfigPath = defaultPathGenerator

// createNewConfigWithDefaults creates a new config with default values
func createNewConfigWithDefaults() Config {
	return Config{
		Secrets: Secrets{
			ProviderType:   "", // No default provider - user must run setup
			SetupCompleted: false,
		},
		RegistryUrl:            "",
		AllowPrivateRegistryIp: false,
		DefaultGroupMigration:  false,
	}
}

// applyBackwardCompatibility applies backward compatibility fixes to existing configs
func applyBackwardCompatibility(config *Config) error {
	// Hack - if the secrets provider type is set to the old `basic` type,
	// just change it to `encrypted`.
	if config.Secrets.ProviderType == "basic" {
		fmt.Println("cleaning up basic secrets provider")
		// Attempt to cleanup path, treat errors as non fatal.
		oldPath, err := xdg.DataFile("toolhive/secrets")
		if err == nil {
			_ = os.Remove(oldPath)
		}
		config.Secrets.ProviderType = string(secrets.EncryptedType)
		err = config.save()
		if err != nil {
			return fmt.Errorf("error updating config: %v", err)
		}
	}

	// Handle backward compatibility: if provider is set but setup_completed is false,
	// consider it as setup completed (for existing users)
	if config.Secrets.ProviderType != "" && !config.Secrets.SetupCompleted {
		config.Secrets.SetupCompleted = true
		err := config.save()
		if err != nil {
			return fmt.Errorf("error updating config for backward compatibility: %v", err)
		}
	}

	return nil
}

// LoadOrCreateConfig fetches the application configuration.
// If it does not already exist - it will create a new config file with default values.
func LoadOrCreateConfig() (*Config, error) {
	store, err := NewConfigStore()
	if err != nil {
		return nil, fmt.Errorf("failed to create config store: %w", err)
	}

	ctx := context.Background()
	return store.Load(ctx)
}

// LoadOrCreateConfigWithPath fetches the application configuration from a specific path.
// If configPath is empty, it uses the default path.
// If it does not already exist - it will create a new config file with default values.
func LoadOrCreateConfigWithPath(configPath string) (*Config, error) {
	store, err := NewConfigStoreWithDetector(configPath, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create config store: %w", err)
	}

	ctx := context.Background()
	return store.Load(ctx)
}

// Save serializes the config struct and writes it to disk.
func (c *Config) save() error {
	return c.saveToPath("")
}

// saveToPath serializes the config struct and writes it to a specific path.
// If configPath is empty, it uses the default path.
func (c *Config) saveToPath(configPath string) error {
	if configPath == "" {
		var err error
		configPath, err = getConfigPath()
		if err != nil {
			return fmt.Errorf("unable to fetch config path: %w", err)
		}
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

// UpdateConfig loads config from appropriate store, applies changes, and saves back
func UpdateConfig(updateFn func(*Config)) error {
	return UpdateConfigWithStore(nil, updateFn)
}

// UpdateConfigWithStore uses the provided store or creates a new one to update config
func UpdateConfigWithStore(store Store, updateFn func(*Config)) error {
	var err error
	if store == nil {
		store, err = NewConfigStore()
		if err != nil {
			return fmt.Errorf("failed to create config store: %w", err)
		}
	}

	ctx := context.Background()

	// Load current config
	config, err := store.Load(ctx)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Apply changes
	updateFn(config)

	// Save updated config
	err = store.Save(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Update singleton cache if this is the current config
	if appConfig != nil {
		lock.Lock()
		appConfig = config
		lock.Unlock()
	}

	return nil
}

// UpdateConfigAtPath loads config using appropriate store, applies changes, and saves back
// If configPath is empty, it uses the default path.
func UpdateConfigAtPath(configPath string, updateFn func(*Config)) error {
	store, err := NewConfigStoreWithDetector(configPath, nil)
	if err != nil {
		return fmt.Errorf("failed to create config store: %w", err)
	}

	ctx := context.Background()
	return store.Update(ctx, updateFn)
}

// OpenTelemetryConfig contains the settings for OpenTelemetry configuration.
type OpenTelemetryConfig struct {
	Endpoint     string   `yaml:"endpoint,omitempty"`
	SamplingRate float64  `yaml:"sampling-rate,omitempty"`
	EnvVars      []string `yaml:"env-vars,omitempty"`
}
