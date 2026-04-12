// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		initial       []RegistrySource
		source        RegistrySource
		expectedLen   int
		expectedNames []string
	}{
		{
			name:    "add to empty list",
			initial: nil,
			source: RegistrySource{
				Name:     "remote",
				Type:     RegistrySourceTypeURL,
				Location: "https://example.com/registry.json",
			},
			expectedLen:   1,
			expectedNames: []string{"remote"},
		},
		{
			name: "add new registry",
			initial: []RegistrySource{
				{Name: "existing", Type: RegistrySourceTypeFile, Location: "/tmp/reg.json"},
			},
			source: RegistrySource{
				Name:     "remote",
				Type:     RegistrySourceTypeURL,
				Location: "https://example.com/registry.json",
			},
			expectedLen:   2,
			expectedNames: []string{"existing", "remote"},
		},
		{
			name: "replace existing registry",
			initial: []RegistrySource{
				{Name: "remote", Type: RegistrySourceTypeURL, Location: "https://old.com/registry.json"},
			},
			source: RegistrySource{
				Name:     "remote",
				Type:     RegistrySourceTypeURL,
				Location: "https://new.com/registry.json",
			},
			expectedLen:   1,
			expectedNames: []string{"remote"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			provider := NewPathProvider(configPath)

			// Initialize config
			_, err := provider.LoadOrCreateConfig()
			require.NoError(t, err)

			// Seed initial registries
			if len(tt.initial) > 0 {
				err = provider.UpdateConfig(func(c *Config) {
					c.Registries = tt.initial
				})
				require.NoError(t, err)
			}

			// Add the registry
			err = addRegistry(provider, tt.source)
			require.NoError(t, err)

			// Verify
			cfg := provider.GetConfig()
			assert.Len(t, cfg.Registries, tt.expectedLen)
			for i, expected := range tt.expectedNames {
				assert.Equal(t, expected, cfg.Registries[i].Name)
			}
		})
	}
}

func TestRemoveRegistry(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	provider := NewPathProvider(configPath)

	// Initialize config with registries
	_, err := provider.LoadOrCreateConfig()
	require.NoError(t, err)
	err = provider.UpdateConfig(func(c *Config) {
		c.Registries = []RegistrySource{
			{Name: "remote", Type: RegistrySourceTypeURL, Location: "https://example.com/registry.json"},
			{Name: "local", Type: RegistrySourceTypeFile, Location: "/tmp/reg.json"},
		}
		c.DefaultRegistry = "remote"
	})
	require.NoError(t, err)

	// Remove "remote" which is the default
	err = removeRegistry(provider, "remote")
	require.NoError(t, err)

	cfg := provider.GetConfig()
	assert.Len(t, cfg.Registries, 1)
	assert.Equal(t, "local", cfg.Registries[0].Name)
	assert.Empty(t, cfg.DefaultRegistry, "default should be cleared when removed registry was default")
}

func TestSetDefaultRegistry(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	provider := NewPathProvider(configPath)

	_, err := provider.LoadOrCreateConfig()
	require.NoError(t, err)

	err = setDefaultRegistry(provider, "my-registry")
	require.NoError(t, err)

	cfg := provider.GetConfig()
	assert.Equal(t, "my-registry", cfg.DefaultRegistry)
}

func TestConfigFindRegistry(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Registries: []RegistrySource{
			{Name: "remote", Type: RegistrySourceTypeURL, Location: "https://example.com/registry.json"},
			{Name: "local", Type: RegistrySourceTypeFile, Location: "/tmp/reg.json"},
		},
	}

	t.Run("found", func(t *testing.T) {
		t.Parallel()
		src := cfg.FindRegistry("remote")
		require.NotNil(t, src)
		assert.Equal(t, "remote", src.Name)
		assert.Equal(t, "https://example.com/registry.json", src.Location)
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		src := cfg.FindRegistry("nonexistent")
		assert.Nil(t, src)
	})
}

func TestConfigEffectiveDefaultRegistry(t *testing.T) {
	t.Parallel()

	t.Run("explicit default", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{DefaultRegistry: "custom"}
		assert.Equal(t, "custom", cfg.EffectiveDefaultRegistry())
	})

	t.Run("fallback to embedded", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		assert.Equal(t, "embedded", cfg.EffectiveDefaultRegistry())
	})
}
