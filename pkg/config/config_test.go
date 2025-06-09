package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/secrets"
)

// MockConfigPath replaces the getConfigPath function with a mock that returns a specified path
func MockConfigPath(configPath string) func() {
	original := getConfigPath

	// Replace the function with our mock
	getConfigPath = func() (string, error) {
		return configPath, nil
	}

	// Return a cleanup function to restore the original
	return func() {
		getConfigPath = original
	}
}

// SetupTestConfig creates a temporary config file and mocks the config path
func SetupTestConfig(t *testing.T, configContent *Config) (string, func()) {
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

	// Mock the config path function
	cleanup := MockConfigPath(configPath)

	return tempDir, cleanup
}

func TestLoadOrCreateConfig(t *testing.T) {
	logger.Initialize()

	t.Run("TestLoadOrCreateConfigWithMockConfig", func(t *testing.T) {
		tempDir, cleanup := SetupTestConfig(t, &Config{
			Secrets: Secrets{
				ProviderType: "encrypted",
			},
			Clients: Clients{
				AutoDiscovery:     true,
				RegisteredClients: []string{"vscode", "cursor"},
			},
		})
		defer cleanup()

		// Load the config
		config, err := LoadOrCreateConfig()
		require.NoError(t, err)

		// Verify the loaded config matches our mock
		assert.Equal(t, "encrypted", config.Secrets.ProviderType)
		assert.True(t, config.Clients.AutoDiscovery)
		assert.Equal(t, []string{"vscode", "cursor"}, config.Clients.RegisteredClients)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("TestLoadOrCreateConfigWithNewConfig", func(t *testing.T) {
		// Create a temporary directory for the test
		tempDir, cleanup := SetupTestConfig(t, nil)
		defer cleanup()

		// Load the config - this should create a new one since none exists
		config, err := LoadOrCreateConfig()
		require.NoError(t, err)

		// Verify the default values
		assert.Equal(t, "encrypted", config.Secrets.ProviderType)
		assert.False(t, config.Clients.AutoDiscovery) // Default is false when no input is provided
		assert.Empty(t, config.Clients.RegisteredClients)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestSave(t *testing.T) {
	logger.Initialize()

	t.Run("TestSave", func(t *testing.T) {
		// Use the same pattern as other tests with proper mocking
		tempDir, cleanup := SetupTestConfig(t, nil)
		defer cleanup()

		// Create a config instance
		config := &Config{
			Secrets: Secrets{
				ProviderType: "encrypted",
			},
			Clients: Clients{
				AutoDiscovery:     true,
				RegisteredClients: []string{"vscode", "cursor", "roo-code", "cline", "claude-code"},
			},
		}

		// Write the config
		err := config.save()
		require.NoError(t, err)

		// Verify the file was created
		configPath, err := getConfigPath()
		require.NoError(t, err)

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
		assert.Equal(t, config.Clients.AutoDiscovery, loadedConfig.Clients.AutoDiscovery)
		assert.Equal(t, config.Clients.RegisteredClients, loadedConfig.Clients.RegisteredClients)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestRegistryURLConfig(t *testing.T) {
	logger.Initialize()

	t.Run("TestSetAndGetRegistryURL", func(t *testing.T) {
		tempDir, cleanup := SetupTestConfig(t, &Config{
			Secrets: Secrets{
				ProviderType: "encrypted",
			},
			Clients: Clients{
				AutoDiscovery:     false,
				RegisteredClients: []string{},
			},
			RegistryUrl: "",
		})
		defer cleanup()

		// Test setting a registry URL
		testURL := "https://example.com/registry.json"
		err := UpdateConfig(func(c *Config) {
			c.RegistryUrl = testURL
		})
		require.NoError(t, err)

		// Load the config and verify the URL was set
		config, err := LoadOrCreateConfig()
		require.NoError(t, err)
		assert.Equal(t, testURL, config.RegistryUrl)

		// Test unsetting the registry URL
		err = UpdateConfig(func(c *Config) {
			c.RegistryUrl = ""
		})
		require.NoError(t, err)

		// Load the config and verify the URL was unset
		config, err = LoadOrCreateConfig()
		require.NoError(t, err)
		assert.Equal(t, "", config.RegistryUrl)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})

	t.Run("TestRegistryURLPersistence", func(t *testing.T) {
		tempDir, cleanup := SetupTestConfig(t, nil)
		defer cleanup()

		testURL := "https://custom-registry.example.com/registry.json"

		// Set the registry URL
		err := UpdateConfig(func(c *Config) {
			c.RegistryUrl = testURL
		})
		require.NoError(t, err)

		// Load config again to verify persistence
		config, err := LoadOrCreateConfig()
		require.NoError(t, err)
		assert.Equal(t, testURL, config.RegistryUrl)

		t.Cleanup(func() {
			if err := os.RemoveAll(tempDir); err != nil {
				t.Logf("Failed to remove temp dir: %v", err)
			}
		})
	})
}

func TestSecrets_GetProviderType_EnvironmentVariable(t *testing.T) {
	logger.Initialize()

	// Save original env value and restore at the end
	originalEnv := os.Getenv(secrets.ProviderEnvVar)
	defer func() {
		if originalEnv != "" {
			os.Setenv(secrets.ProviderEnvVar, originalEnv)
		} else {
			os.Unsetenv(secrets.ProviderEnvVar)
		}
	}()

	s := &Secrets{
		ProviderType: "1password", // Config says 1password
	}

	// Test 1: Environment variable takes precedence
	os.Setenv(secrets.ProviderEnvVar, "encrypted")
	got, err := s.GetProviderType()
	require.NoError(t, err)
	assert.Equal(t, secrets.EncryptedType, got, "Environment variable should take precedence over config")

	// Test 2: Falls back to config when env var is unset
	os.Unsetenv(secrets.ProviderEnvVar)
	got, err = s.GetProviderType()
	require.NoError(t, err)
	assert.Equal(t, secrets.OnePasswordType, got, "Should fallback to config value when env var is unset")

	// Test 3: Invalid environment variable returns error
	os.Setenv(secrets.ProviderEnvVar, "invalid")
	_, err = s.GetProviderType()
	assert.Error(t, err, "Should return error for invalid environment variable")
	assert.Contains(t, err.Error(), "invalid secrets provider type", "Error should mention invalid provider type")
}
