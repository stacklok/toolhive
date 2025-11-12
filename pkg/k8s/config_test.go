package k8s

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestGetConfigFromKubeconfigFile(t *testing.T) {
	t.Parallel()

	// Helper to create kubeconfig file
	writeKubeconfig := func(t *testing.T, content string) string {
		t.Helper()
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config")
		err := os.WriteFile(configPath, []byte(content), 0600)
		require.NoError(t, err)
		return configPath
	}

	kubeconfigNoContext := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://localhost:6443
  name: test-cluster
`

	kubeconfigWithCA := `apiVersion: v1
kind: Config
current-context: test-context
clusters:
- cluster:
    server: https://custom-server:6443
    certificate-authority-data: LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSURCVENDQWUyZ0F3SUJBZ0lJZHZhdzZYRGRaVVV3RFFZSktvWklodmNOQVFFTEJRQXdGVEVUTUJFR0ExVUUKQXhNS2EzVmlaWEp1WlhSbGN6QWVGdzB5TkRBeE1ERXhNVEkyTWpkYUZ3MHpOREF4TVRBeE1USXhNamRhTUJVeApFekFSQmdOVkJBTVRDbXQxWW1WeWJtVjBaWE13Z2dFaU1BMEdDU3FHU0liM0RRRUJBUVVBQTRJQkR3QXdnZ0VLCkFvSUJBUUNrNzFvaGlnPT0KLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo=
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: test-user
  name: test-context
users:
- name: test-user
  user:
    token: custom-token
`

	tests := []struct {
		name         string
		content      string
		useNonExist  bool
		expectError  bool
		expectedHost string
		expectedCA   bool
	}{
		{
			name:         "valid kubeconfig",
			content:      validKubeconfigYAML,
			expectError:  false,
			expectedHost: "https://localhost:6443",
		},
		{
			name:        "non-existent file",
			useNonExist: true,
			expectError: true,
		},
		{
			name:        "invalid YAML",
			content:     `this is not valid yaml: {{}`,
			expectError: true,
		},
		{
			name:        "missing current-context",
			content:     kubeconfigNoContext,
			expectError: true,
		},
		{
			name:         "with certificate authority data",
			content:      kubeconfigWithCA,
			expectError:  false,
			expectedHost: "https://custom-server:6443",
			expectedCA:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var configPath string
			if tt.useNonExist {
				configPath = filepath.Join(t.TempDir(), "nonexistent")
			} else {
				configPath = writeKubeconfig(t, tt.content)
			}

			config, err := getConfigFromKubeconfigFile(configPath)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				require.NotNil(t, config)
				assert.Equal(t, tt.expectedHost, config.Host)
				if tt.expectedCA {
					assert.NotNil(t, config.CAData)
				}
			}
		})
	}
}
