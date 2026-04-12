// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestConfigurator_SetRegistryFromInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          string
		allowPrivateIP bool
		expectedType   string
		expectError    bool
	}{
		{
			name:           "set local file",
			input:          "/tmp/test-registry.json",
			allowPrivateIP: false,
			expectedType:   string(config.RegistrySourceTypeFile),
			expectError:    false,
		},
		{
			name:           "set URL ending in .json",
			input:          "https://example.com/registry.json",
			allowPrivateIP: false,
			expectedType:   string(config.RegistrySourceTypeURL),
			expectError:    false,
		},
		{
			name:           "set API URL",
			input:          "https://registry.example.com",
			allowPrivateIP: false,
			expectedType:   string(config.RegistrySourceTypeAPI),
			expectError:    false,
		},
		{
			name:           "set file:// path",
			input:          "file:///path/to/registry.json",
			allowPrivateIP: false,
			expectedType:   string(config.RegistrySourceTypeFile),
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			provider := config.NewPathProvider(configPath)
			service := registry.NewConfiguratorWithProvider(provider)

			registryType, err := service.SetRegistryFromInput(tt.input, tt.allowPrivateIP)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedType, registryType)
		})
	}
}

func TestConfigurator_UnsetRegistry(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	provider := config.NewPathProvider(configPath)
	service := registry.NewConfiguratorWithProvider(provider)

	// Set a registry
	_, err := service.SetRegistryFromInput("/tmp/test-registry.json", false)
	require.NoError(t, err)

	// Verify it's set
	registryType, source := service.GetRegistryInfo()
	assert.Equal(t, string(config.RegistrySourceTypeFile), registryType)
	assert.NotEmpty(t, source)

	// Unset it
	err = service.UnsetRegistry()
	require.NoError(t, err)

	// Verify it's unset
	registryType, source = service.GetRegistryInfo()
	assert.Equal(t, "default", registryType)
	assert.Empty(t, source)
}

func TestConfigurator_GetRegistryInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupFunc      func(t *testing.T, service registry.Configurator)
		expectedType   string
		expectedSource string
	}{
		{
			name:           "default registry",
			setupFunc:      nil,
			expectedType:   "default",
			expectedSource: "",
		},
		{
			name: "file registry",
			setupFunc: func(t *testing.T, service registry.Configurator) {
				t.Helper()
				_, err := service.SetRegistryFromInput("/tmp/test.json", false)
				require.NoError(t, err)
			},
			expectedType:   string(config.RegistrySourceTypeFile),
			expectedSource: "/tmp/test.json",
		},
		{
			name: "URL registry",
			setupFunc: func(t *testing.T, service registry.Configurator) {
				t.Helper()
				_, err := service.SetRegistryFromInput("https://example.com/registry.json", false)
				require.NoError(t, err)
			},
			expectedType:   string(config.RegistrySourceTypeURL),
			expectedSource: "https://example.com/registry.json",
		},
		{
			name: "API registry",
			setupFunc: func(t *testing.T, service registry.Configurator) {
				t.Helper()
				_, err := service.SetRegistryFromInput("https://registry.example.com", false)
				require.NoError(t, err)
			},
			expectedType:   string(config.RegistrySourceTypeAPI),
			expectedSource: "https://registry.example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			provider := config.NewPathProvider(configPath)
			service := registry.NewConfiguratorWithProvider(provider)

			if tt.setupFunc != nil {
				tt.setupFunc(t, service)
			}

			registryType, source := service.GetRegistryInfo()
			assert.Equal(t, tt.expectedType, registryType)
			if tt.expectedSource != "" {
				assert.Equal(t, tt.expectedSource, source)
			}
		})
	}
}
