package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRegistryProviderConfig_Validate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		config      *RegistryProviderConfig
		wantErr     bool
		errContains string
	}{
		{
			name: "valid configmap provider",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeConfigMap,
				ConfigMap: &ConfigMapProviderConfig{
					Name:      "test-cm",
					Namespace: "test-ns",
					Clientset: fake.NewSimpleClientset(),
				},
			},
			wantErr: false,
		},
		{
			name: "valid file provider",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeFile,
				File: &FileProviderConfig{
					FilePath: "/data/registry.json",
				},
			},
			wantErr: false,
		},
		{
			name: "empty provider type",
			config: &RegistryProviderConfig{
				Type: "",
			},
			wantErr:     true,
			errContains: "provider type is required",
		},
		{
			name: "configmap provider without config",
			config: &RegistryProviderConfig{
				Type:      RegistryProviderTypeConfigMap,
				ConfigMap: nil,
			},
			wantErr:     true,
			errContains: "configmap configuration required",
		},
		{
			name: "file provider without config",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeFile,
				File: nil,
			},
			wantErr:     true,
			errContains: "file configuration required",
		},
		{
			name: "unsupported provider type",
			config: &RegistryProviderConfig{
				Type: "unsupported",
			},
			wantErr:     true,
			errContains: "unsupported provider type",
		},
		{
			name: "configmap with missing clientset",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeConfigMap,
				ConfigMap: &ConfigMapProviderConfig{
					Name:      "test-cm",
					Namespace: "test-ns",
					Clientset: nil,
				},
			},
			wantErr:     true,
			errContains: "kubernetes clientset is required",
		},
		{
			name: "configmap with empty name",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeConfigMap,
				ConfigMap: &ConfigMapProviderConfig{
					Name:      "",
					Namespace: "test-ns",
					Clientset: fake.NewSimpleClientset(),
				},
			},
			wantErr:     true,
			errContains: "configmap name is required",
		},
		{
			name: "configmap with empty namespace",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeConfigMap,
				ConfigMap: &ConfigMapProviderConfig{
					Name:      "test-cm",
					Namespace: "",
					Clientset: fake.NewSimpleClientset(),
				},
			},
			wantErr:     true,
			errContains: "configmap namespace is required",
		},
		{
			name: "file with empty path",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeFile,
				File: &FileProviderConfig{
					FilePath: "",
				},
			},
			wantErr:     true,
			errContains: "file path is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.Validate()

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestDefaultRegistryProviderFactory_CreateProvider(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test-registry.json")

	// Create a test file
	err := os.WriteFile(tmpFile, []byte(`{
		"version": "1.0",
		"last_updated": "",
		"servers": {},
		"remote_servers": {}
	}`), 0644)
	require.NoError(t, err)

	tests := []struct {
		name        string
		config      *RegistryProviderConfig
		wantErr     bool
		errContains string
		checkType   func(*testing.T, RegistryDataProvider)
	}{
		{
			name: "create configmap provider",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeConfigMap,
				ConfigMap: &ConfigMapProviderConfig{
					Name:      "test-cm",
					Namespace: "test-ns",
					Clientset: fake.NewSimpleClientset(),
				},
			},
			wantErr: false,
			checkType: func(t *testing.T, provider RegistryDataProvider) {
				t.Helper()
				assert.IsType(t, &K8sRegistryDataProvider{}, provider)
				assert.Equal(t, "configmap:test-ns/test-cm", provider.GetSource())
			},
		},
		{
			name: "create file provider",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeFile,
				File: &FileProviderConfig{
					FilePath: tmpFile,
				},
			},
			wantErr: false,
			checkType: func(t *testing.T, provider RegistryDataProvider) {
				t.Helper()
				assert.IsType(t, &FileRegistryDataProvider{}, provider)
				assert.Equal(t, "file:"+tmpFile, provider.GetSource())
			},
		},
		{
			name:        "nil config",
			config:      nil,
			wantErr:     true,
			errContains: "registry provider config cannot be nil",
		},
		{
			name: "invalid config - missing configmap",
			config: &RegistryProviderConfig{
				Type:      RegistryProviderTypeConfigMap,
				ConfigMap: nil,
			},
			wantErr:     true,
			errContains: "configmap configuration required",
		},
		{
			name: "invalid config - missing file",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeFile,
				File: nil,
			},
			wantErr:     true,
			errContains: "file configuration required",
		},
		{
			name: "invalid config - unsupported type",
			config: &RegistryProviderConfig{
				Type: "unknown",
			},
			wantErr:     true,
			errContains: "unsupported registry provider type",
		},
		{
			name: "configmap with missing clientset",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeConfigMap,
				ConfigMap: &ConfigMapProviderConfig{
					Name:      "test-cm",
					Namespace: "test-ns",
					Clientset: nil,
				},
			},
			wantErr:     true,
			errContains: "kubernetes clientset is required",
		},
		{
			name: "file with empty path",
			config: &RegistryProviderConfig{
				Type: RegistryProviderTypeFile,
				File: &FileProviderConfig{
					FilePath: "",
				},
			},
			wantErr:     true,
			errContains: "file path is required",
		},
	}

	factory := NewRegistryProviderFactory()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := factory.CreateProvider(tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				assert.Nil(t, provider)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, provider)
				if tt.checkType != nil {
					tt.checkType(t, provider)
				}
			}
		})
	}
}

func TestNewRegistryProviderFactory(t *testing.T) {
	t.Parallel()
	factory := NewRegistryProviderFactory()
	assert.NotNil(t, factory)
	assert.IsType(t, &DefaultRegistryProviderFactory{}, factory)
}

func TestProviderTypes(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "configmap", string(RegistryProviderTypeConfigMap))
	assert.Equal(t, "file", string(RegistryProviderTypeFile))
}

func TestK8sRegistryDataProvider_GetRegistryName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		configMapName string
		namespace     string
		expected      string
	}{
		{
			name:          "simple configmap name",
			configMapName: "my-registry",
			namespace:     "default",
			expected:      "my-registry",
		},
		{
			name:          "hyphenated configmap name",
			configMapName: "my-registry-configmap",
			namespace:     "test-namespace",
			expected:      "my-registry-configmap",
		},
		{
			name:          "empty configmap name",
			configMapName: "",
			namespace:     "default",
			expected:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := NewK8sRegistryDataProvider(fake.NewSimpleClientset(), tt.configMapName, tt.namespace)
			result := provider.GetRegistryName()
			assert.Equal(t, tt.expected, result)
		})
	}
}
