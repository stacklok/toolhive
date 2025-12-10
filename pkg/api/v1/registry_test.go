package v1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/logger"
)

func CreateTestConfigProvider(t *testing.T, cfg *config.Config) (config.Provider, func()) {
	t.Helper()

	// Create a temporary directory for the test
	tempDir := t.TempDir()

	// Create the config directory structure
	configDir := filepath.Join(tempDir, "toolhive")
	err := os.MkdirAll(configDir, 0755)
	require.NoError(t, err)

	// Set up the config file path
	configPath := filepath.Join(configDir, "config.yaml")

	// Create a path-based config provider
	provider := config.NewPathProvider(configPath)

	// Write the config file if one is provided
	if cfg != nil {
		err = provider.UpdateConfig(func(c *config.Config) { *c = *cfg })
		require.NoError(t, err)
	}

	return provider, func() {
		// Cleanup is handled by t.TempDir()
	}
}

func TestRegistryRouter(t *testing.T) {
	t.Parallel()

	logger.Initialize()

	// Create a test config provider to avoid using the singleton
	provider, _ := CreateTestConfigProvider(t, nil)
	routes := NewRegistryRoutesWithProvider(provider)
	assert.NotNil(t, routes)
}

//nolint:paralleltest // Cannot use t.Parallel() with t.Setenv() in Go 1.24+
func TestGetRegistryInfo(t *testing.T) {
	t.Parallel()
	logger.Initialize()

	tests := []struct {
		name           string
		config         *config.Config
		expectedType   RegistryType
		expectedSource string
	}{
		{
			name: "default registry",
			config: &config.Config{
				RegistryUrl:       "",
				LocalRegistryPath: "",
			},
			expectedType:   RegistryTypeDefault,
			expectedSource: "",
		},
		{
			name: "URL registry",
			config: &config.Config{
				RegistryUrl:            "https://test.com/registry.json",
				AllowPrivateRegistryIp: false,
				LocalRegistryPath:      "",
			},
			expectedType:   RegistryTypeURL,
			expectedSource: "https://test.com/registry.json",
		},
		{
			name: "file registry",
			config: &config.Config{
				RegistryUrl:       "",
				LocalRegistryPath: "/tmp/test-registry.json",
			},
			expectedType:   RegistryTypeFile,
			expectedSource: "/tmp/test-registry.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			configProvider, cleanup := CreateTestConfigProvider(t, tt.config)
			defer cleanup()

			registryType, source := getRegistryInfoWithProvider(configProvider)
			assert.Equal(t, tt.expectedType, registryType, "Registry type should match expected")
			assert.Equal(t, tt.expectedSource, source, "Registry source should match expected")
		})
	}
}
