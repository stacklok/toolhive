// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// mockConfigLoader is a test implementation of configLoader
type mockConfigLoader struct {
	inClusterConfig *rest.Config
	inClusterError  error
	rulesConfig     *rest.Config
	rulesError      error
}

func (m *mockConfigLoader) InClusterConfig() (*rest.Config, error) {
	if m.inClusterError != nil {
		return nil, m.inClusterError
	}
	return m.inClusterConfig, nil
}

func (m *mockConfigLoader) LoadFromRules(_ *clientcmd.ClientConfigLoadingRules) (*rest.Config, error) {
	if m.rulesError != nil {
		return nil, m.rulesError
	}
	return m.rulesConfig, nil
}

func TestGetConfigWithLoader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		inClusterConfig *rest.Config
		inClusterError  error
		rulesConfig     *rest.Config
		rulesError      error
		expectError     bool
		expectedHost    string
	}{
		{
			name:            "uses in-cluster config when available",
			inClusterConfig: &rest.Config{Host: "https://in-cluster:6443"},
			rulesConfig:     &rest.Config{Host: "https://kubeconfig:6443"},
			expectError:     false,
			expectedHost:    "https://in-cluster:6443",
		},
		{
			name:           "falls back to kubeconfig when in-cluster fails",
			inClusterError: errors.New("not in cluster"),
			rulesConfig:    &rest.Config{Host: "https://kubeconfig:6443"},
			expectError:    false,
			expectedHost:   "https://kubeconfig:6443",
		},
		{
			name:           "returns error when both fail",
			inClusterError: errors.New("not in cluster"),
			rulesError:     errors.New("no kubeconfig"),
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			loader := &mockConfigLoader{
				inClusterConfig: tt.inClusterConfig,
				inClusterError:  tt.inClusterError,
				rulesConfig:     tt.rulesConfig,
				rulesError:      tt.rulesError,
			}

			config, err := getConfigWithLoader(loader)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, config)
				assert.Equal(t, tt.expectedHost, config.Host)
			}
		})
	}
}
