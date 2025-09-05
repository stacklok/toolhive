package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/stacklok/toolhive/pkg/logger"
)

func TestNewDefaultProvider(t *testing.T) {
	t.Parallel()
	provider := NewDefaultProvider()
	assert.NotNil(t, provider)
	assert.IsType(t, &DefaultProvider{}, provider)
}

func TestNewPathProvider(t *testing.T) {
	t.Parallel()
	configPath := "/test/path/config.yaml"
	provider := NewPathProvider(configPath)
	assert.NotNil(t, provider)
	assert.IsType(t, &PathProvider{}, provider)
	assert.Equal(t, configPath, provider.configPath)
}

func TestNewKubernetesProvider(t *testing.T) {
	t.Parallel()
	provider := NewKubernetesProvider()
	assert.NotNil(t, provider)
	assert.IsType(t, &KubernetesProvider{}, provider)
}

func TestDefaultProvider(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	t.Run("GetConfig", func(t *testing.T) {
		t.Parallel()

		// Use PathProvider instead to avoid singleton issues in parallel tests
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "config.yaml")
		pathProvider := NewPathProvider(configPath)

		config := pathProvider.GetConfig()
		assert.NotNil(t, config)
		assert.IsType(t, &Config{}, config)
	})

	t.Run("LoadOrCreateConfig", func(t *testing.T) {
		t.Parallel()

		// Use PathProvider instead to avoid singleton issues in parallel tests
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "config.yaml")
		pathProvider := NewPathProvider(configPath)

		config, err := pathProvider.LoadOrCreateConfig()
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.FileExists(t, configPath)
	})

	t.Run("UpdateConfig", func(t *testing.T) {
		t.Parallel()

		// Use PathProvider instead to avoid singleton issues in parallel tests
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "config.yaml")
		pathProvider := NewPathProvider(configPath)

		// Create initial config
		_, err := pathProvider.LoadOrCreateConfig()
		require.NoError(t, err)

		// Update config
		err = pathProvider.UpdateConfig(func(c *Config) {
			c.RegistryUrl = "https://example.com"
		})
		assert.NoError(t, err)

		// Verify update - just check that we can load the config
		_, err = LoadOrCreateConfigFromPath(configPath)
		assert.NoError(t, err)
	})
}

func TestPathProvider(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	provider := NewPathProvider(configPath)

	t.Run("GetConfig_NewFile", func(t *testing.T) {
		t.Parallel()
		config := provider.GetConfig()
		assert.NotNil(t, config)
		assert.IsType(t, &Config{}, config)
		assert.FileExists(t, configPath)
	})

	t.Run("GetConfig_ExistingFile", func(t *testing.T) {
		t.Parallel()
		// Create a config with specific content
		testConfig := &Config{
			RegistryUrl: "https://test.com",
			Secrets: Secrets{
				ProviderType:   "encrypted",
				SetupCompleted: true,
			},
		}
		configBytes, err := yaml.Marshal(testConfig)
		require.NoError(t, err)

		configPath2 := filepath.Join(tempDir, "config2.yaml")
		err = os.WriteFile(configPath2, configBytes, 0600)
		require.NoError(t, err)

		provider2 := NewPathProvider(configPath2)
		config := provider2.GetConfig()
		assert.NotNil(t, config)
		assert.Equal(t, "https://test.com", config.RegistryUrl)
		assert.Equal(t, "encrypted", config.Secrets.ProviderType)
	})

	t.Run("GetConfig_ErrorFallback", func(t *testing.T) {
		t.Parallel()
		// Use a path that will cause an error (directory instead of file)
		dirPath := filepath.Join(tempDir, "dir")
		err := os.MkdirAll(dirPath, 0755)
		require.NoError(t, err)

		provider := NewPathProvider(dirPath)
		config := provider.GetConfig()
		assert.NotNil(t, config)
		// Should return default config on error
		assert.Equal(t, "", config.RegistryUrl)
		assert.False(t, config.Secrets.SetupCompleted)
	})

	t.Run("LoadOrCreateConfig", func(t *testing.T) {
		t.Parallel()
		configPath3 := filepath.Join(tempDir, "config3.yaml")
		provider := NewPathProvider(configPath3)

		config, err := provider.LoadOrCreateConfig()
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.FileExists(t, configPath3)
	})

	t.Run("UpdateConfig", func(t *testing.T) {
		t.Parallel()
		configPath4 := filepath.Join(tempDir, "config4.yaml")
		provider := NewPathProvider(configPath4)

		// Create initial config
		_, err := provider.LoadOrCreateConfig()
		require.NoError(t, err)

		// Update config
		err = provider.UpdateConfig(func(c *Config) {
			c.RegistryUrl = "https://updated.com"
		})
		assert.NoError(t, err)

		// Verify update
		config, err := LoadOrCreateConfigFromPath(configPath4)
		require.NoError(t, err)
		assert.Equal(t, "https://updated.com", config.RegistryUrl)
	})
}

func TestKubernetesProvider(t *testing.T) {
	t.Parallel()
	provider := NewKubernetesProvider()

	t.Run("GetConfig", func(t *testing.T) {
		t.Parallel()
		config := provider.GetConfig()
		assert.NotNil(t, config)
		assert.IsType(t, &Config{}, config)
		// Should return default config
		assert.Equal(t, "", config.RegistryUrl)
		assert.False(t, config.Secrets.SetupCompleted)
	})

	t.Run("LoadOrCreateConfig", func(t *testing.T) {
		t.Parallel()
		config, err := provider.LoadOrCreateConfig()
		assert.NoError(t, err)
		assert.NotNil(t, config)
		// Should return default config
		assert.Equal(t, "", config.RegistryUrl)
		assert.False(t, config.Secrets.SetupCompleted)
	})

	t.Run("UpdateConfig", func(t *testing.T) {
		t.Parallel()
		err := provider.UpdateConfig(func(c *Config) {
			c.RegistryUrl = "https://example.com"
		})
		assert.NoError(t, err) // Should be no-op
	})

	t.Run("SetRegistryURL", func(t *testing.T) {
		t.Parallel()
		err := provider.SetRegistryURL("https://example.com", true)
		assert.NoError(t, err) // Should be no-op
	})

	t.Run("SetRegistryFile", func(t *testing.T) {
		t.Parallel()
		err := provider.SetRegistryFile("/path/to/registry.yaml")
		assert.NoError(t, err) // Should be no-op
	})

	t.Run("UnsetRegistry", func(t *testing.T) {
		t.Parallel()
		err := provider.UnsetRegistry()
		assert.NoError(t, err) // Should be no-op
	})

	t.Run("GetRegistryConfig", func(t *testing.T) {
		t.Parallel()
		url, localPath, allowPrivateIP, registryType := provider.GetRegistryConfig()
		assert.Equal(t, "", url)
		assert.Equal(t, "", localPath)
		assert.False(t, allowPrivateIP)
		assert.Equal(t, "", registryType)
	})
}

func TestNewProvider(t *testing.T) {
	t.Run("DefaultProvider", func(t *testing.T) {
		// Ensure no Kubernetes environment variables are set
		originalKubeEnv := os.Getenv("KUBERNETES_SERVICE_HOST")
		originalPodEnv := os.Getenv("KUBERNETES_SERVICE_PORT")
		if originalKubeEnv != "" {
			t.Setenv("KUBERNETES_SERVICE_HOST", "")
		}
		if originalPodEnv != "" {
			t.Setenv("KUBERNETES_SERVICE_PORT", "")
		}

		provider := NewProvider()
		assert.NotNil(t, provider)
		assert.IsType(t, &DefaultProvider{}, provider)
	})

	t.Run("KubernetesProvider", func(t *testing.T) {
		// Set Kubernetes environment variables
		t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
		t.Setenv("KUBERNETES_SERVICE_PORT", "443")

		provider := NewProvider()
		assert.NotNil(t, provider)
		assert.IsType(t, &KubernetesProvider{}, provider)
	})
}

func TestProviderRegistryOperations(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tempDir := t.TempDir()

	t.Run("DefaultProvider_RegistryOperations", func(t *testing.T) {
		t.Parallel()

		// Use PathProvider to avoid singleton issues in parallel tests
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "default_config.yaml")
		pathProvider := NewPathProvider(configPath)

		// Create initial config
		_, err := pathProvider.LoadOrCreateConfig()
		require.NoError(t, err)

		// Test SetRegistryURL
		err = pathProvider.SetRegistryURL("https://example.com", true)
		assert.NoError(t, err)

		// Test GetRegistryConfig after setting URL
		url, localPath, allowPrivateIP, registryType := pathProvider.GetRegistryConfig()
		assert.Equal(t, "https://example.com", url)
		assert.Equal(t, "", localPath)
		assert.True(t, allowPrivateIP)
		assert.Equal(t, "url", registryType)

		// Test SetRegistryFile (must be a JSON file)
		registryFilePath := filepath.Join(tempDir, "registry.json")
		err = os.WriteFile(registryFilePath, []byte(`{"test": "registry"}`), 0600)
		require.NoError(t, err)
		err = pathProvider.SetRegistryFile(registryFilePath)
		assert.NoError(t, err)

		// Test UnsetRegistry
		err = pathProvider.UnsetRegistry()
		assert.NoError(t, err)
	})

	t.Run("PathProvider_RegistryOperations", func(t *testing.T) {
		t.Parallel()
		configPath := filepath.Join(tempDir, "path_config.yaml")
		provider := NewPathProvider(configPath)

		// Create initial config
		_, err := provider.LoadOrCreateConfig()
		require.NoError(t, err)

		// Test SetRegistryURL
		err = provider.SetRegistryURL("https://path-example.com", false)
		assert.NoError(t, err)

		// Test GetRegistryConfig after setting URL
		url, localPath, allowPrivateIP, registryType := provider.GetRegistryConfig()
		assert.Equal(t, "https://path-example.com", url)
		assert.Equal(t, "", localPath)
		assert.False(t, allowPrivateIP)
		assert.Equal(t, "url", registryType)

		// Test SetRegistryFile (must be a JSON file)
		registryFilePath := filepath.Join(tempDir, "path_registry.json")
		err = os.WriteFile(registryFilePath, []byte(`{"test": "registry"}`), 0600)
		require.NoError(t, err)
		err = provider.SetRegistryFile(registryFilePath)
		assert.NoError(t, err)

		// Test GetRegistryConfig after setting file
		url, localPath, allowPrivateIP, registryType = provider.GetRegistryConfig()
		assert.Equal(t, "", url)
		assert.Equal(t, registryFilePath, localPath)
		assert.False(t, allowPrivateIP)
		assert.Equal(t, "file", registryType)

		// Test UnsetRegistry
		err = provider.UnsetRegistry()
		assert.NoError(t, err)
	})
}
