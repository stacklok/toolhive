// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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
			c.DefaultRegistry = "test-registry"
		})
		assert.NoError(t, err)

		// Verify update
		cfg, err := LoadOrCreateConfigFromPath(configPath)
		assert.NoError(t, err)
		assert.Equal(t, "test-registry", cfg.DefaultRegistry)
	})
}

func TestPathProvider(t *testing.T) {
	t.Parallel()

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
			Registries: []RegistrySource{
				{Name: "test", Type: RegistrySourceTypeURL, Location: "https://test.com"},
			},
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
		require.Len(t, config.Registries, 1)
		assert.Equal(t, "https://test.com", config.Registries[0].Location)
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
		assert.Empty(t, config.Registries)
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
			c.DefaultRegistry = "updated"
		})
		assert.NoError(t, err)

		// Verify update
		config, err := LoadOrCreateConfigFromPath(configPath4)
		require.NoError(t, err)
		assert.Equal(t, "updated", config.DefaultRegistry)
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
		assert.Empty(t, config.Registries)
		assert.False(t, config.Secrets.SetupCompleted)
	})

	t.Run("LoadOrCreateConfig", func(t *testing.T) {
		t.Parallel()
		config, err := provider.LoadOrCreateConfig()
		assert.NoError(t, err)
		assert.NotNil(t, config)
		assert.Empty(t, config.Registries)
		assert.False(t, config.Secrets.SetupCompleted)
	})

	t.Run("UpdateConfig", func(t *testing.T) {
		t.Parallel()
		err := provider.UpdateConfig(func(c *Config) {
			c.DefaultRegistry = "test"
		})
		assert.NoError(t, err) // Should be no-op
	})

	t.Run("AddRegistry", func(t *testing.T) {
		t.Parallel()
		err := provider.AddRegistry(RegistrySource{Name: "test"})
		assert.NoError(t, err) // Should be no-op
	})

	t.Run("RemoveRegistry", func(t *testing.T) {
		t.Parallel()
		err := provider.RemoveRegistry("test")
		assert.NoError(t, err) // Should be no-op
	})

	t.Run("SetDefaultRegistry", func(t *testing.T) {
		t.Parallel()
		err := provider.SetDefaultRegistry("test")
		assert.NoError(t, err) // Should be no-op
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

	t.Run("PathProvider_AddAndRemoveRegistry", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "config.yaml")
		provider := NewPathProvider(configPath)

		_, err := provider.LoadOrCreateConfig()
		require.NoError(t, err)

		// Add a registry
		err = provider.AddRegistry(RegistrySource{
			Name:     "test-remote",
			Type:     RegistrySourceTypeURL,
			Location: "https://example.com/registry.json",
		})
		require.NoError(t, err)

		cfg := provider.GetConfig()
		require.Len(t, cfg.Registries, 1)
		assert.Equal(t, "test-remote", cfg.Registries[0].Name)

		// Remove registry
		err = provider.RemoveRegistry("test-remote")
		require.NoError(t, err)

		cfg = provider.GetConfig()
		assert.Empty(t, cfg.Registries)
	})

	t.Run("PathProvider_SetDefaultRegistry", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "config.yaml")
		provider := NewPathProvider(configPath)

		_, err := provider.LoadOrCreateConfig()
		require.NoError(t, err)

		err = provider.SetDefaultRegistry("my-registry")
		require.NoError(t, err)

		cfg := provider.GetConfig()
		assert.Equal(t, "my-registry", cfg.DefaultRegistry)
	})
}

func TestProviderBuildEnvOperations(t *testing.T) {
	t.Parallel()

	t.Run("PathProvider_BuildEnvOperations", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "buildenv_config.yaml")
		provider := NewPathProvider(configPath)

		// Create initial config
		_, err := provider.LoadOrCreateConfig()
		require.NoError(t, err)

		// Test GetAllBuildEnv when empty
		envVars := provider.GetAllBuildEnv()
		assert.Empty(t, envVars)

		// Test GetBuildEnv when not set
		value, exists := provider.GetBuildEnv("NPM_CONFIG_REGISTRY")
		assert.False(t, exists)
		assert.Equal(t, "", value)

		// Test SetBuildEnv
		err = provider.SetBuildEnv("NPM_CONFIG_REGISTRY", "https://npm.corp.example.com")
		assert.NoError(t, err)

		// Test GetBuildEnv after setting
		value, exists = provider.GetBuildEnv("NPM_CONFIG_REGISTRY")
		assert.True(t, exists)
		assert.Equal(t, "https://npm.corp.example.com", value)

		// Test SetBuildEnv with multiple variables
		err = provider.SetBuildEnv("GOPROXY", "https://goproxy.corp.example.com")
		assert.NoError(t, err)

		// Test GetAllBuildEnv with multiple variables
		envVars = provider.GetAllBuildEnv()
		assert.Len(t, envVars, 2)
		assert.Equal(t, "https://npm.corp.example.com", envVars["NPM_CONFIG_REGISTRY"])
		assert.Equal(t, "https://goproxy.corp.example.com", envVars["GOPROXY"])

		// Test UnsetBuildEnv
		err = provider.UnsetBuildEnv("NPM_CONFIG_REGISTRY")
		assert.NoError(t, err)

		value, exists = provider.GetBuildEnv("NPM_CONFIG_REGISTRY")
		assert.False(t, exists)
		assert.Equal(t, "", value)

		// Verify GOPROXY still exists
		value, exists = provider.GetBuildEnv("GOPROXY")
		assert.True(t, exists)
		assert.Equal(t, "https://goproxy.corp.example.com", value)

		// Test UnsetAllBuildEnv
		err = provider.UnsetAllBuildEnv()
		assert.NoError(t, err)

		envVars = provider.GetAllBuildEnv()
		assert.Empty(t, envVars)
	})

	t.Run("PathProvider_BuildEnvValidation", func(t *testing.T) {
		t.Parallel()
		tempDir := t.TempDir()
		configPath := filepath.Join(tempDir, "buildenv_validation_config.yaml")
		provider := NewPathProvider(configPath)

		// Create initial config
		_, err := provider.LoadOrCreateConfig()
		require.NoError(t, err)

		// Test invalid key format
		err = provider.SetBuildEnv("invalid_key", "value")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid environment variable name")

		// Test reserved key
		err = provider.SetBuildEnv("PATH", "/usr/local/bin")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "reserved")

		// Test invalid value with shell metacharacters
		err = provider.SetBuildEnv("TEST_VAR", "$(whoami)")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "dangerous characters")
	})

	t.Run("KubernetesProvider_BuildEnvOperations", func(t *testing.T) {
		t.Parallel()
		provider := NewKubernetesProvider()

		// Test SetBuildEnv (should be no-op)
		err := provider.SetBuildEnv("NPM_CONFIG_REGISTRY", "https://npm.corp.example.com")
		assert.NoError(t, err)

		// Test GetBuildEnv (should return empty)
		value, exists := provider.GetBuildEnv("NPM_CONFIG_REGISTRY")
		assert.False(t, exists)
		assert.Equal(t, "", value)

		// Test GetAllBuildEnv (should return empty map)
		envVars := provider.GetAllBuildEnv()
		assert.Empty(t, envVars)

		// Test UnsetBuildEnv (should be no-op)
		err = provider.UnsetBuildEnv("NPM_CONFIG_REGISTRY")
		assert.NoError(t, err)

		// Test UnsetAllBuildEnv (should be no-op)
		err = provider.UnsetAllBuildEnv()
		assert.NoError(t, err)
	})
}
