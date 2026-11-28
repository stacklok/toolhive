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
	"k8s.io/client-go/tools/clientcmd"
)

// createTestConfig creates a valid kubeconfig file and returns the config
func createTestConfig(t *testing.T) *rest.Config {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config")
	err := os.WriteFile(configPath, []byte(validKubeconfigYAML), 0600)
	require.NoError(t, err)
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: configPath},
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	require.NoError(t, err)
	return config
}

// createTestScheme creates a runtime scheme with standard types
func createTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	return scheme
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
