// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package config contains the definition of the application config structure
// and logic required to load and update it.
package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"time"

	"github.com/adrg/xdg"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/container/templates"
	"github.com/stacklok/toolhive/pkg/lockfile"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// lockTimeout is the maximum time to wait for a file lock
const lockTimeout = 1 * time.Second

// Config represents the configuration of the application.
type Config struct {
	Secrets                      Secrets                             `yaml:"secrets"`
	Clients                      Clients                             `yaml:"clients"`
	RegistryUrl                  string                              `yaml:"registry_url"`
	RegistryApiUrl               string                              `yaml:"registry_api_url"`
	LocalRegistryPath            string                              `yaml:"local_registry_path"`
	AllowPrivateRegistryIp       bool                                `yaml:"allow_private_registry_ip"`
	CACertificatePath            string                              `yaml:"ca_certificate_path,omitempty"`
	OTEL                         OpenTelemetryConfig                 `yaml:"otel,omitempty"`
	DefaultGroupMigration        bool                                `yaml:"default_group_migration,omitempty"`
	TelemetryConfigMigration     bool                                `yaml:"telemetry_config_migration,omitempty"`
	MiddlewareTelemetryMigration bool                                `yaml:"middleware_telemetry_migration,omitempty"`
	DisableUsageMetrics          bool                                `yaml:"disable_usage_metrics,omitempty"`
	BuildEnv                     map[string]string                   `yaml:"build_env,omitempty"`
	BuildEnvFromSecrets          map[string]string                   `yaml:"build_env_from_secrets,omitempty"`
	BuildEnvFromShell            []string                            `yaml:"build_env_from_shell,omitempty"`
	BuildAuthFiles               map[string]string                   `yaml:"build_auth_files,omitempty"`
	RuntimeConfigs               map[string]*templates.RuntimeConfig `yaml:"runtime_configs,omitempty"`
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
	case string(secrets.EnvironmentType):
		return secrets.EnvironmentType, nil
	default:
		return "", fmt.Errorf("invalid secrets provider type: %s (valid types: %s, %s, %s)",
			provider,
			string(secrets.EncryptedType),
			string(secrets.OnePasswordType),
			string(secrets.EnvironmentType),
		)
	}
}

// GetProviderType returns the secrets provider type from the environment variable or application config.
// It first checks the TOOLHIVE_SECRETS_PROVIDER environment variable (allowing Kubernetes deployments
// to override without local setup), and falls back to the config file.
// Returns ErrSecretsNotSetup only if the environment variable is not set and secrets have not been configured.
func (s *Secrets) GetProviderType() (secrets.ProviderType, error) {
	return s.GetProviderTypeWithEnv(&env.OSReader{})
}

// GetProviderTypeWithEnv returns the secrets provider type using the provided environment reader.
// This method allows for dependency injection of environment variable access for testing.
//
// Precedence order:
//  1. Environment variable (TOOLHIVE_SECRETS_PROVIDER) - takes highest precedence
//  2. Config file (requires SetupCompleted to be true)
//
// Special handling when SetupCompleted is false:
//   - Only the "environment" provider can be set via env var when SetupCompleted is false
//   - Other providers (encrypted, 1password) require setup and will return ErrSecretsNotSetup
//   - This prevents confusing errors later when trying to create providers that need setup
//
// Why environment provider bypasses SetupCompleted:
//   - In Kubernetes environments, pods don't have config files set up
//   - The operator sets TOOLHIVE_SECRETS_PROVIDER=environment via env vars
//   - The environment provider doesn't require "setup" - it reads directly from env vars
//   - This allows the operator to work without requiring users to run 'thv secret setup'
//
// For CLI users:
//   - If they set TOOLHIVE_SECRETS_PROVIDER=environment, it works without setup
//   - If they set TOOLHIVE_SECRETS_PROVIDER=encrypted/1password without setup, it returns an error
//   - This prevents confusing errors when providers fail to initialize later
func (s *Secrets) GetProviderTypeWithEnv(envReader env.Reader) (secrets.ProviderType, error) {
	// First check the environment variable (takes precedence) - this allows Kubernetes deployments
	// to override the secrets provider without requiring local setup
	envVar := envReader.Getenv(secrets.ProviderEnvVar)
	if envVar != "" {
		providerType, err := validateProviderType(envVar)
		if err != nil {
			return "", err
		}

		// Special case: Only allow "environment" provider when SetupCompleted is false
		// Other providers (encrypted, 1password) require setup and will fail later when
		// trying to create them (keyring, password, 1Password CLI, etc.)
		if !s.SetupCompleted && providerType != secrets.EnvironmentType {
			return "", fmt.Errorf(
				"provider %q requires setup to be completed. "+
					"Only 'environment' provider can be used without setup. "+
					"Please run 'thv secret setup' or use TOOLHIVE_SECRETS_PROVIDER=environment",
				providerType,
			)
		}

		return providerType, nil
	}

	// Check if secrets setup has been completed (required for config file-based provider)
	// Only checked when environment variable is not set
	if !s.SetupCompleted {
		return "", secrets.ErrSecretsNotSetup
	}

	// Fall back to config file
	return validateProviderType(s.ProviderType)
}

// Clients contains settings for client configuration.
type Clients struct {
	RegisteredClients []string `yaml:"registered_clients"`
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
		RegistryUrl:                  "",
		RegistryApiUrl:               "",
		AllowPrivateRegistryIp:       false,
		DefaultGroupMigration:        false,
		TelemetryConfigMigration:     false,
		MiddlewareTelemetryMigration: false,
	}
}

// applyBackwardCompatibility applies backward compatibility fixes to existing configs
func applyBackwardCompatibility(config *Config) error {
	// Hack - if the secrets provider type is set to the old `basic` type,
	// just change it to `encrypted`.
	if config.Secrets.ProviderType == "basic" {
		slog.Debug("cleaning up basic secrets provider, migrating to encrypted type")
		// Attempt to cleanup path, treat errors as non fatal.
		oldPath, err := xdg.DataFile("toolhive/secrets")
		if err == nil {
			_ = os.Remove(oldPath)
		}
		config.Secrets.ProviderType = string(secrets.EncryptedType)
		err = config.save()
		if err != nil {
			return fmt.Errorf("error updating config: %w", err)
		}
	}

	// Handle backward compatibility: if provider is set but setup_completed is false,
	// consider it as setup completed (for existing users)
	if config.Secrets.ProviderType != "" && !config.Secrets.SetupCompleted {
		config.Secrets.SetupCompleted = true
		err := config.save()
		if err != nil {
			return fmt.Errorf("error updating config for backward compatibility: %w", err)
		}
	}

	return nil
}

// LoadOrCreateConfig fetches the application configuration.
// If it does not already exist - it will create a new config file with default values.
func LoadOrCreateConfig() (*Config, error) {
	provider := NewProvider()
	return provider.LoadOrCreateConfig()
}

// LoadOrCreateConfigWithDefaultPath is the internal implementation for loading config with the default path.
// This avoids circular dependency issues.
func LoadOrCreateConfigWithDefaultPath() (*Config, error) {
	configPath, err := getConfigPath()
	if err != nil {
		return nil, fmt.Errorf("unable to fetch config path: %w", err)
	}
	return LoadOrCreateConfigFromPath(configPath)
}

// LoadOrCreateConfigWithPath fetches the application configuration from a specific path.
// If configPath is empty, it uses the default path.
// If it does not already exist - it will create a new config file with default values.
func LoadOrCreateConfigWithPath(configPath string) (*Config, error) {
	if configPath == "" {
		// When no path is specified, use the provider pattern to handle runtime-specific behavior
		return LoadOrCreateConfig()
	}

	return LoadOrCreateConfigFromPath(configPath)
}

// LoadOrCreateConfigFromPath is the core implementation for loading/creating config from a specific path
func LoadOrCreateConfigFromPath(configPath string) (*Config, error) {
	var config Config
	var err error

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
		config = createNewConfigWithDefaults()

		// Persist the new default to disk using the specific path
		//nolint:gosec // G706: config path is validated and cleaned before use
		slog.Debug("initializing configuration file", "path", configPath)
		err = config.saveToPath(configPath)
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

		// Apply backward compatibility fixes
		err = applyBackwardCompatibility(&config)
		if err != nil {
			return nil, fmt.Errorf("failed to apply backward compatibility fixes: %w", err)
		}
	}

	return &config, nil
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

// UpdateConfig locks a separate lock file, reads from disk, applies the changes
// from the anonymous function, writes to disk and unlocks the file.
func UpdateConfig(updateFn func(*Config)) error {
	provider := NewProvider()
	return provider.UpdateConfig(updateFn)
}

// UpdateConfigAtPath locks a separate lock file, reads from disk, applies the changes
// from the anonymous function, writes to disk and unlocks the file.
// If configPath is empty, it uses the default path.
func UpdateConfigAtPath(configPath string, updateFn func(*Config)) error {
	if configPath == "" {
		var err error
		configPath, err = getConfigPath()
		if err != nil {
			return fmt.Errorf("unable to fetch config path: %w", err)
		}
	}

	// Use a separate lock file for cross-platform compatibility
	lockPath := configPath + ".lock"
	fileLock := lockfile.NewTrackedLock(lockPath)
	ctx, cancel := context.WithTimeout(context.Background(), lockTimeout)
	defer cancel()

	// Try and acquire a file lock.
	locked, err := fileLock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to acquire lock: %w", err)
	}
	if !locked {
		return fmt.Errorf("failed to acquire lock: timeout after %v", lockTimeout)
	}
	defer lockfile.ReleaseTrackedLock(lockPath, fileLock)

	// Load the config after acquiring the lock to avoid race conditions
	c, err := LoadOrCreateConfigWithPath(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config from disk: %w", err)
	}

	// Apply changes to the config file.
	updateFn(c)

	// Write the updated config to disk.
	err = c.saveToPath(configPath)
	if err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Lock is released automatically when the function returns.
	return nil
}

// OpenTelemetryConfig contains the settings for OpenTelemetry configuration.
type OpenTelemetryConfig struct {
	Endpoint                    string   `yaml:"endpoint,omitempty"`
	SamplingRate                float64  `yaml:"sampling-rate,omitempty"`
	EnvVars                     []string `yaml:"env-vars,omitempty"`
	MetricsEnabled              bool     `yaml:"metrics-enabled,omitempty"`
	TracingEnabled              bool     `yaml:"tracing-enabled,omitempty"`
	Insecure                    bool     `yaml:"insecure,omitempty"`
	EnablePrometheusMetricsPath bool     `yaml:"enable-prometheus-metrics-path,omitempty"`
	UseLegacyAttributes         *bool    `yaml:"use-legacy-attributes"`
}

// getRuntimeConfig returns the runtime configuration for a given transport type
func getRuntimeConfig(provider Provider, transportType string) (*templates.RuntimeConfig, error) {
	config := provider.GetConfig()
	if config.RuntimeConfigs == nil {
		return nil, nil
	}

	runtimeConfig, exists := config.RuntimeConfigs[transportType]
	if !exists {
		return nil, nil
	}

	return runtimeConfig, nil
}

// setRuntimeConfig sets the runtime configuration for a given transport type
func setRuntimeConfig(provider Provider, transportType string, runtimeConfig *templates.RuntimeConfig) error {
	return provider.UpdateConfig(func(c *Config) {
		if c.RuntimeConfigs == nil {
			c.RuntimeConfigs = make(map[string]*templates.RuntimeConfig)
		}
		c.RuntimeConfigs[transportType] = runtimeConfig
	})
}
