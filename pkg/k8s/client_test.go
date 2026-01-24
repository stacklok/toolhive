// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

// createTestConfig creates a valid kubeconfig file and returns the config
func createTestConfig(t *testing.T) *rest.Config {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")
	err := os.WriteFile(configPath, []byte(validKubeconfigYAML), 0600)
	require.NoError(t, err)
	config, err := getConfigFromKubeconfigFile(configPath)
	require.NoError(t, err)
	return config
}

// createTestScheme creates a runtime scheme with standard types
func createTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	return scheme
}

func TestNewClientWithConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *rest.Config
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid config",
			config:      &rest.Config{Host: "https://localhost:6443", BearerToken: "fake-token"},
			expectError: false,
		},
		{
			name:        "invalid host URL",
			config:      &rest.Config{Host: "://invalid-url"},
			expectError: true,
			errorMsg:    "failed to create kubernetes client",
		},
		{
			name:        "nil config",
			config:      nil,
			expectError: true,
			errorMsg:    "config cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clientset, err := NewClientWithConfig(tt.config)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, clientset)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, clientset)
			}
		})
	}
}

func TestNewControllerRuntimeClientWithConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		scheme      *runtime.Scheme
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid scheme",
			scheme:      createTestScheme(),
			expectError: false,
		},
		{
			name:        "nil scheme",
			scheme:      nil,
			expectError: true,
			errorMsg:    "scheme cannot be nil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			config := createTestConfig(t)
			client, err := newControllerRuntimeClientWithConfig(config, tt.scheme)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, client)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}

func TestNewDynamicClientWithConfig(t *testing.T) {
	t.Parallel()

	t.Run("creates dynamic client from valid config", func(t *testing.T) {
		t.Parallel()

		config := createTestConfig(t)
		client, err := newDynamicClientWithConfig(config)

		assert.NoError(t, err)
		assert.NotNil(t, client)
	})
}

func TestClientTypeCompatibility(t *testing.T) {
	t.Parallel()

	t.Run("standard client implements kubernetes.Interface", func(t *testing.T) {
		t.Parallel()

		config := createTestConfig(t)
		clientset, err := NewClientWithConfig(config)

		require.NoError(t, err)
		require.NotNil(t, clientset)
		assert.NotNil(t, clientset.CoreV1())
		assert.NotNil(t, clientset.AppsV1())
		assert.NotNil(t, clientset.BatchV1())
	})
}

func TestIsAvailableInternal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		inClusterError  error
		rulesError      error
		expectAvailable bool
	}{
		{
			name:            "available when config loads",
			inClusterError:  errors.New("not in cluster"),
			rulesError:      nil,
			expectAvailable: true,
		},
		{
			name:            "not available when config fails",
			inClusterError:  errors.New("not in cluster"),
			rulesError:      errors.New("no kubeconfig"),
			expectAvailable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			loader := &mockConfigLoader{
				inClusterError: tt.inClusterError,
				rulesError:     tt.rulesError,
				rulesConfig:    &rest.Config{Host: "https://test:6443"},
			}

			_, err := getConfigWithLoader(loader)

			if tt.expectAvailable {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
