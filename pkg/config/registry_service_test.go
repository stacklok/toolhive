// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/config"
)

func TestRegistryConfigService_SetRegistryFromInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		input              string
		allowPrivateIP     bool
		expectedType       string
		expectError        bool
		setupFunc          func(t *testing.T) string // Returns path to test file if needed
		cleanupFunc        func(path string)
		expectedMessagePat string // Pattern to match in message
	}{
		{
			name:               "set local registry file",
			allowPrivateIP:     false,
			expectedType:       config.RegistryTypeFile,
			expectError:        false,
			expectedMessagePat: "Successfully set local registry file",
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile := filepath.Join(t.TempDir(), "test-registry.json")
				content := []byte(`{
					"version": "0.1",
					"servers": {
						"test": {
							"command": ["test"],
							"args": []
						}
					}
				}`)
				require.NoError(t, os.WriteFile(tmpFile, content, 0600))
				return tmpFile
			},
		},
		{
			name:           "invalid local file - missing",
			allowPrivateIP: false,
			expectError:    true,
			setupFunc: func(_ *testing.T) string {
				return "/tmp/non-existent-file-xyz123.json"
			},
		},
		{
			name:           "invalid local file - wrong structure",
			allowPrivateIP: false,
			expectError:    true,
			setupFunc: func(t *testing.T) string {
				t.Helper()
				tmpFile := filepath.Join(t.TempDir(), "invalid-registry.json")
				content := []byte(`{"invalid": "structure"}`)
				require.NoError(t, os.WriteFile(tmpFile, content, 0600))
				return tmpFile
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a test config provider
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			provider := config.NewPathProvider(configPath)
			service := config.NewRegistryConfigServiceWithProvider(provider)

			// Setup test data if needed
			var input string
			if tt.setupFunc != nil {
				input = tt.setupFunc(t)
			} else {
				input = tt.input
			}

			// Call the service
			registryType, message, err := service.SetRegistryFromInput(input, tt.allowPrivateIP)

			// Check results
			if tt.expectError {
				assert.Error(t, err, "Expected an error")
			} else {
				assert.NoError(t, err, "Should not return error")
				assert.Equal(t, tt.expectedType, registryType, "Registry type should match")
				if tt.expectedMessagePat != "" {
					assert.Contains(t, message, tt.expectedMessagePat, "Message should contain expected pattern")
				}
			}
		})
	}
}

func TestRegistryConfigService_UnsetRegistry(t *testing.T) {
	t.Parallel()

	// Create a test config provider with a registry set
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	tmpFile := filepath.Join(tmpDir, "test-registry.json")

	// Create a valid registry file
	content := []byte(`{
		"version": "0.1",
		"servers": {
			"test": {
				"command": ["test"],
				"args": []
			}
		}
	}`)
	require.NoError(t, os.WriteFile(tmpFile, content, 0600))

	provider := config.NewPathProvider(configPath)
	service := config.NewRegistryConfigServiceWithProvider(provider)

	// First, set a registry
	_, _, err := service.SetRegistryFromInput(tmpFile, false)
	require.NoError(t, err, "Should be able to set registry")

	// Verify it's set
	registryType, source := service.GetRegistryInfo()
	assert.Equal(t, config.RegistryTypeFile, registryType, "Registry type should be file")
	assert.NotEmpty(t, source, "Source should not be empty")

	// Now unset it
	message, err := service.UnsetRegistry()
	assert.NoError(t, err, "Should be able to unset registry")
	assert.Contains(t, message, "Successfully removed", "Message should indicate removal")
	assert.Contains(t, message, "built-in registry", "Message should mention built-in registry")

	// Verify it's unset
	registryType, source = service.GetRegistryInfo()
	assert.Equal(t, config.RegistryTypeDefault, registryType, "Registry type should be default")
	assert.Empty(t, source, "Source should be empty")
}

func TestRegistryConfigService_GetRegistryInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		setupFunc      func(t *testing.T, service config.RegistryConfigService)
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
			setupFunc: func(t *testing.T, service config.RegistryConfigService) {
				t.Helper()
				tmpFile := filepath.Join(t.TempDir(), "test-registry.json")
				content := []byte(`{
					"version": "0.1",
					"servers": {
						"test": {
							"command": ["test"],
							"args": []
						}
					}
				}`)
				require.NoError(t, os.WriteFile(tmpFile, content, 0600))
				_, _, err := service.SetRegistryFromInput(tmpFile, false)
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
			service := config.NewRegistryConfigServiceWithProvider(provider)

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
