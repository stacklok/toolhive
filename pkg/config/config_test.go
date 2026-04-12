// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive-core/env/mocks"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// SetupTestConfig creates a temporary config file and returns the config path
func SetupTestConfig(t *testing.T, configContent *Config) (string, string) {
	t.Helper()
	// Create a temporary directory
	tempDir := t.TempDir()

	// Create config directory
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	// Set up the config file path
	configPath := filepath.Join(configDir, "config.yaml")

	// If config content is provided, write it to the file
	if configContent != nil {
		configBytes, err := yaml.Marshal(configContent)
		require.NoError(t, err)

		err = os.WriteFile(configPath, configBytes, 0600)
		require.NoError(t, err)
	}

	return tempDir, configPath
}

func TestLoadOrCreateConfig(t *testing.T) {
	t.Parallel()

	t.Run("TestLoadOrCreateConfigWithMockConfig", func(t *testing.T) {
		t.Parallel()
		tempDir, configPath := SetupTestConfig(t, &Config{
			Secrets: Secrets{
				ProviderType: string(secrets.EncryptedType),
			},
			Clients: Clients{
				RegisteredClients: []string{"vscode", "cursor"},
			},
		})

		// Load the config
		config, err := LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)

		// Verify the loaded config matches our mock
		assert.Equal(t, string(secrets.EncryptedType), config.Secrets.ProviderType)
		assert.Equal(t, []string{"vscode", "cursor"}, config.Clients.RegisteredClients)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("TestLoadOrCreateConfigWithNewConfig", func(t *testing.T) {
		t.Parallel()
		// Create a temporary directory for the test
		tempDir, configPath := SetupTestConfig(t, nil)

		// Load the config - this should create a new one since none exists
		config, err := LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)

		// Verify the default values
		assert.Equal(t, "", config.Secrets.ProviderType) // Default is empty - requires explicit setup
		assert.False(t, config.Secrets.SetupCompleted)   // Setup not completed by default
		assert.Empty(t, config.Clients.RegisteredClients)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestSave(t *testing.T) {
	t.Parallel()

	t.Run("TestSave", func(t *testing.T) {
		t.Parallel()
		// Use the same pattern as other tests with proper mocking
		tempDir, configPath := SetupTestConfig(t, nil)

		// Create a config instance
		config := &Config{
			Secrets: Secrets{
				ProviderType: string(secrets.EncryptedType),
			},
			Clients: Clients{
				RegisteredClients: []string{
					"vscode", "cursor", "roo-code", "cline", "claude-code", "amp-cli", "amp-vscode", "amp-cursor",
				},
			},
		}

		// Write the config
		err := config.saveToPath(configPath)
		require.NoError(t, err)

		// Verify the file was created
		_, err = os.Stat(configPath)
		require.NoError(t, err)

		// Read the file and verify its contents
		data, err := os.ReadFile(configPath)
		require.NoError(t, err)

		// Load the config from the file
		loadedConfig := &Config{}
		err = yaml.Unmarshal(data, loadedConfig)
		require.NoError(t, err)

		// Verify the loaded config matches what we wrote
		assert.Equal(t, config.Secrets.ProviderType, loadedConfig.Secrets.ProviderType)
		assert.Equal(t, config.Clients.RegisteredClients, loadedConfig.Clients.RegisteredClients)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestRegistriesConfig(t *testing.T) {
	t.Parallel()

	t.Run("TestSetAndGetRegistries", func(t *testing.T) {
		t.Parallel()
		tempDir, configPath := SetupTestConfig(t, &Config{
			Secrets: Secrets{
				ProviderType: string(secrets.EncryptedType),
			},
		})

		// Test adding a registry
		err := UpdateConfigAtPath(configPath, func(c *Config) {
			c.Registries = []RegistrySource{
				{
					Name:     "remote",
					Type:     RegistrySourceTypeURL,
					Location: "https://example.com/registry.json",
				},
			}
		})
		require.NoError(t, err)

		// Load the config and verify
		config, err := LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)
		require.Len(t, config.Registries, 1)
		assert.Equal(t, "remote", config.Registries[0].Name)
		assert.Equal(t, RegistrySourceTypeURL, config.Registries[0].Type)

		// Test clearing registries
		err = UpdateConfigAtPath(configPath, func(c *Config) {
			c.Registries = nil
		})
		require.NoError(t, err)

		config, err = LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)
		assert.Empty(t, config.Registries)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("TestDefaultRegistryPersistence", func(t *testing.T) {
		t.Parallel()
		tempDir, configPath := SetupTestConfig(t, nil)

		err := UpdateConfigAtPath(configPath, func(c *Config) {
			c.DefaultRegistry = "my-custom-registry"
		})
		require.NoError(t, err)

		config, err := LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)
		assert.Equal(t, "my-custom-registry", config.DefaultRegistry)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestSecrets_GetProviderType_EnvironmentVariable(t *testing.T) {
	t.Parallel()

	t.Run("Environment variable takes precedence", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   string(secrets.OnePasswordType),
			SetupCompleted: true,
		}

		mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return(string(secrets.EncryptedType))
		got, err := s.GetProviderTypeWithEnv(mockEnv)
		require.NoError(t, err)
		assert.Equal(t, secrets.EncryptedType, got, "Environment variable should take precedence over config")
	})

	t.Run("Falls back to config when env var is unset", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   string(secrets.OnePasswordType),
			SetupCompleted: true,
		}

		mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return("")
		got, err := s.GetProviderTypeWithEnv(mockEnv)
		require.NoError(t, err)
		assert.Equal(t, secrets.OnePasswordType, got, "Should fallback to config value when env var is unset")
	})

	t.Run("Environment provider via environment variable", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   string(secrets.OnePasswordType),
			SetupCompleted: true,
		}

		mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return(string(secrets.EnvironmentType))
		got, err := s.GetProviderTypeWithEnv(mockEnv)
		require.NoError(t, err)
		assert.Equal(t, secrets.EnvironmentType, got, "Environment variable should support environment provider")
	})

	t.Run("Setup not completed returns error when env var not set", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   string(secrets.OnePasswordType),
			SetupCompleted: false,
		}

		mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return("")
		_, err := s.GetProviderTypeWithEnv(mockEnv)
		assert.Error(t, err, "Should return error when setup not completed and env var not set")
		assert.ErrorIs(t, err, secrets.ErrSecretsNotSetup)
	})

	t.Run("Environment variable bypasses SetupCompleted check", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   string(secrets.OnePasswordType),
			SetupCompleted: false,
		}

		mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return(string(secrets.EnvironmentType))
		got, err := s.GetProviderTypeWithEnv(mockEnv)
		require.NoError(t, err, "Should not return error when env var is set, even if setup not completed")
		assert.Equal(t, secrets.EnvironmentType, got, "Should return provider type from env var")
	})

	t.Run("Non-environment providers require SetupCompleted when set via env var", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   "",
			SetupCompleted: false,
		}

		mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return(string(secrets.EncryptedType))
		_, err := s.GetProviderTypeWithEnv(mockEnv)
		assert.Error(t, err, "Should return error when non-environment provider is set without setup")
		assert.Contains(t, err.Error(), "requires setup to be completed")
	})
}
