// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestConfigurator_SetRegistryFromInput(t *testing.T) {
	t.Parallel()

	const validAPIResponse = `{"servers":[],"metadata":{"count":0}}`

	tests := []struct {
		name           string
		input          string
		allowPrivateIP bool
		hasAuth        bool
		expectedType   string
		expectError    bool
		setupFunc      func(t *testing.T) string
		handler        http.HandlerFunc
	}{
		{
			name:           "set local registry file",
			allowPrivateIP: false,
			expectedType:   config.RegistryTypeFile,
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile := filepath.Join(t.TempDir(), "test-registry.json")
				content := []byte(`{
					"$schema": "https://example.com/schema.json",
					"version": "0.1",
					"meta": {"last_updated": "2025-01-01T00:00:00Z"},
					"data": {"servers": [{"name": "io.example.test"}]}
				}`)
				require.NoError(t, os.WriteFile(tmpFile, content, 0600))
				return tmpFile
			},
		},
		{
			name:         "invalid local file - missing",
			expectedType: config.RegistryTypeFile,
			expectError:  true,
			setupFunc: func(_ *testing.T) string {
				return "/tmp/non-existent-file-xyz123.json"
			},
		},
		{
			name:         "invalid local file - wrong structure",
			expectedType: config.RegistryTypeFile,
			expectError:  true,
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile := filepath.Join(t.TempDir(), "invalid-registry.json")
				content := []byte(`{"invalid": "structure"}`)
				require.NoError(t, os.WriteFile(tmpFile, content, 0600))
				return tmpFile
			},
		},
		{
			name:           "valid MCP registry API",
			allowPrivateIP: true,
			expectedType:   config.RegistryTypeAPI,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(validAPIResponse))
			},
		},
		{
			name:           "API registry returns 401 with auth provided",
			allowPrivateIP: true,
			hasAuth:        true,
			expectedType:   config.RegistryTypeAPI,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
		},
		{
			name:           "API registry returns 401 without auth",
			allowPrivateIP: true,
			hasAuth:        false,
			expectedType:   config.RegistryTypeAPI,
			expectError:    true,
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			provider := config.NewPathProvider(configPath)
			service := registry.NewConfiguratorWithProvider(provider)

			var input string
			if tt.handler != nil {
				server := httptest.NewServer(tt.handler)
				t.Cleanup(server.Close)
				input = server.URL
			} else if tt.setupFunc != nil {
				input = tt.setupFunc(t)
			} else {
				input = tt.input
			}

			registryType, err := service.SetRegistryFromInput(input, tt.allowPrivateIP, tt.hasAuth)

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

	// Create a test config provider with a registry set
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	tmpFile := filepath.Join(tmpDir, "test-registry.json")

	// Create a valid registry file
	content := []byte(`{
		"$schema": "https://example.com/schema.json",
		"version": "0.1",
		"meta": {"last_updated": "2025-01-01T00:00:00Z"},
		"data": {"servers": [{"name": "io.example.test"}]}
	}`)
	require.NoError(t, os.WriteFile(tmpFile, content, 0600))

	provider := config.NewPathProvider(configPath)
	service := registry.NewConfiguratorWithProvider(provider)

	// First, set a registry
	_, err := service.SetRegistryFromInput(tmpFile, false, false)
	require.NoError(t, err, "Should be able to set registry")

	// Verify it's set
	registryType, source := service.GetRegistryInfo()
	assert.Equal(t, config.RegistryTypeFile, registryType, "Registry type should be file")
	assert.NotEmpty(t, source, "Source should not be empty")

	// Now unset it
	err = service.UnsetRegistry()
	assert.NoError(t, err, "Should be able to unset registry")

	// Verify it's unset
	registryType, source = service.GetRegistryInfo()
	assert.Equal(t, config.RegistryTypeDefault, registryType, "Registry type should be default")
	assert.Empty(t, source, "Source should be empty")
}


func TestConfigurator_GetRegistryInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupFunc      func(t *testing.T, service registry.Configurator)
		expectedType   string
		expectedSource string // Empty means we don't check it
	}{
		{
			name:           "default registry",
			setupFunc:      nil, // No setup, should be default
			expectedType:   config.RegistryTypeDefault,
			expectedSource: "",
		},
		{
			name: "local file registry",
			setupFunc: func(t *testing.T, service registry.Configurator) {
				t.Helper()
				tmpFile := filepath.Join(t.TempDir(), "test-registry.json")
				content := []byte(`{
					"$schema": "https://example.com/schema.json",
					"version": "0.1",
					"meta": {"last_updated": "2025-01-01T00:00:00Z"},
					"data": {"servers": [{"name": "io.example.test"}]}
				}`)
				require.NoError(t, os.WriteFile(tmpFile, content, 0600))
				_, err := service.SetRegistryFromInput(tmpFile, false, false)
				require.NoError(t, err)
			},
			expectedType: config.RegistryTypeFile,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a test config provider
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			provider := config.NewPathProvider(configPath)
			service := registry.NewConfiguratorWithProvider(provider)

			// Setup if needed
			if tt.setupFunc != nil {
				tt.setupFunc(t, service)
			}

			// Get registry info
			registryType, source := service.GetRegistryInfo()

			// Check results
			assert.Equal(t, tt.expectedType, registryType, "Registry type should match")
			if tt.expectedSource != "" {
				assert.Equal(t, tt.expectedSource, source, "Source should match")
			}
		})
	}
}
