package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/env/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
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
	logger.Initialize()

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
	logger.Initialize()

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

func TestRegistryURLConfig(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	t.Run("TestSetAndGetRegistryURL", func(t *testing.T) {
		t.Parallel()
		tempDir, configPath := SetupTestConfig(t, &Config{
			Secrets: Secrets{
				ProviderType: string(secrets.EncryptedType),
			},
			Clients: Clients{
				RegisteredClients: []string{},
			},
			RegistryUrl: "",
		})

		// Test setting a registry URL
		testURL := "https://example.com/registry.json"
		err := UpdateConfigAtPath(configPath, func(c *Config) {
			c.RegistryUrl = testURL
		})
		require.NoError(t, err)

		// Load the config and verify the URL was set
		config, err := LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)
		assert.Equal(t, testURL, config.RegistryUrl)

		// Test unsetting the registry URL
		err = UpdateConfigAtPath(configPath, func(c *Config) {
			c.RegistryUrl = ""
		})
		require.NoError(t, err)

		// Load the config and verify the URL was unset
		config, err = LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)
		assert.Equal(t, "", config.RegistryUrl)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("TestRegistryURLPersistence", func(t *testing.T) {
		t.Parallel()
		tempDir, configPath := SetupTestConfig(t, nil)

		testURL := "https://custom-registry.example.com/registry.json"

		// Set the registry URL
		err := UpdateConfigAtPath(configPath, func(c *Config) {
			c.RegistryUrl = testURL
		})
		require.NoError(t, err)

		// Load config again to verify persistence
		config, err := LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)
		assert.Equal(t, testURL, config.RegistryUrl)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("TestAllowPrivateRegistryIp", func(t *testing.T) {
		t.Parallel()
		tempDir, configPath := SetupTestConfig(t, &Config{
			Secrets: Secrets{
				ProviderType: string(secrets.EncryptedType),
			},
			Clients: Clients{
				RegisteredClients: []string{},
			},
			RegistryUrl:            "",
			AllowPrivateRegistryIp: false,
		})

		// Test enabling
		err := UpdateConfigAtPath(configPath, func(c *Config) {
			c.AllowPrivateRegistryIp = true
		})
		require.NoError(t, err)

		// Load the config and verify the setting was toggled to true
		config, err := LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)
		assert.Equal(t, true, config.AllowPrivateRegistryIp)

		// Test toggling setting to false
		err = UpdateConfigAtPath(configPath, func(c *Config) {
			c.AllowPrivateRegistryIp = false
		})
		require.NoError(t, err)

		// Load the config and verify the setting was toggled to false
		config, err = LoadOrCreateConfigWithPath(configPath)
		require.NoError(t, err)
		assert.Equal(t, false, config.AllowPrivateRegistryIp)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestSecrets_GetProviderType_EnvironmentVariable(t *testing.T) {
	t.Parallel()
	logger.Initialize()

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

	t.Run("None provider via environment variable", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   string(secrets.OnePasswordType),
			SetupCompleted: true,
		}

		mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return(string(secrets.NoneType))
		got, err := s.GetProviderTypeWithEnv(mockEnv)
		require.NoError(t, err)
		assert.Equal(t, secrets.NoneType, got, "Environment variable should support none provider")
	})

	t.Run("None provider via config", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   string(secrets.NoneType),
			SetupCompleted: true,
		}

		mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return("")
		got, err := s.GetProviderTypeWithEnv(mockEnv)
		require.NoError(t, err)
		assert.Equal(t, secrets.NoneType, got, "Config should support none provider")
	})

	t.Run("Invalid environment variable returns error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   string(secrets.OnePasswordType),
			SetupCompleted: true,
		}

		mockEnv.EXPECT().Getenv(secrets.ProviderEnvVar).Return("invalid")
		_, err := s.GetProviderTypeWithEnv(mockEnv)
		assert.Error(t, err, "Should return error for invalid environment variable")
		assert.Contains(t, err.Error(), "invalid secrets provider type", "Error should mention invalid provider type")
	})

	t.Run("Setup not completed returns error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mockEnv := mocks.NewMockReader(ctrl)
		s := &Secrets{
			ProviderType:   string(secrets.OnePasswordType),
			SetupCompleted: false,
		}

		// Mock call to TOOLHIVE_RUNTIME when SetupCompleted is false
		mockEnv.EXPECT().Getenv("TOOLHIVE_RUNTIME").Return("")

		_, err := s.GetProviderTypeWithEnv(mockEnv)
		assert.Error(t, err, "Should return error when setup not completed")
		assert.ErrorIs(t, err, secrets.ErrSecretsNotSetup, "Should return ErrSecretsNotSetup when setup not completed")
	})
}
