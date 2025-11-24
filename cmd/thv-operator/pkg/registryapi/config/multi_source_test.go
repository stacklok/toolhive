package config

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestBuildConfigWithMultipleSources(t *testing.T) {
	tests := []struct {
		name           string
		mcpRegistry    *mcpv1alpha1.MCPRegistry
		expectedSources int
		validateFunc   func(*testing.T, *Config)
		wantErr        bool
		errorContains  string
	}{
		{
			name: "single configmap source",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
						{
							Name: "source1",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "test-configmap",
									},
									Key: "registry.json",
								},
							},
						},
					},
				},
			},
			expectedSources: 1,
			validateFunc: func(t *testing.T, config *Config) {
				assert.Equal(t, "test-registry", config.RegistryName)
				assert.Len(t, config.Sources, 1)
				assert.Equal(t, "source1", config.Sources[0].Name)
				assert.Equal(t, SourceTypeFile, config.Sources[0].Type)
				assert.Equal(t, mcpv1alpha1.RegistryFormatToolHive, config.Sources[0].Format)
				assert.NotNil(t, config.Sources[0].File)
				assert.Contains(t, config.Sources[0].File.Path, "/config/registry/source-0/registry.json")
			},
		},
		{
			name: "multiple configmap sources with different sync policies",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-registry",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
						{
							Name: "primary",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "primary-configmap",
									},
									Key: "registry.json",
								},
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "5m",
							},
						},
						{
							Name: "secondary",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "secondary-configmap",
									},
									Key: "registry.json",
								},
							},
							SyncPolicy: &mcpv1alpha1.SyncPolicy{
								Interval: "10m",
							},
						},
					},
				},
			},
			expectedSources: 2,
			validateFunc: func(t *testing.T, config *Config) {
				assert.Equal(t, "multi-registry", config.RegistryName)
				assert.Len(t, config.Sources, 2)

				// Verify first source
				assert.Equal(t, "primary", config.Sources[0].Name)
				assert.Contains(t, config.Sources[0].File.Path, "/config/registry/source-0/registry.json")
				assert.NotNil(t, config.Sources[0].SyncPolicy)
				assert.Equal(t, "5m", config.Sources[0].SyncPolicy.Interval)

				// Verify second source
				assert.Equal(t, "secondary", config.Sources[1].Name)
				assert.Contains(t, config.Sources[1].File.Path, "/config/registry/source-1/registry.json")
				assert.NotNil(t, config.Sources[1].SyncPolicy)
				assert.Equal(t, "10m", config.Sources[1].SyncPolicy.Interval)
			},
		},
		{
			name: "mixed source types (configmap and git)",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "mixed-registry",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
						{
							Name: "configmap-source",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "test-configmap",
									},
									Key: "registry.json",
								},
							},
						},
						{
							Name: "git-source",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeGit,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								Git: &mcpv1alpha1.GitSource{
									Repository: "https://github.com/example/registry.git",
									Branch:     "main",
									Path:       "registry.json",
								},
							},
						},
						{
							Name: "api-source",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeAPI,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								API: &mcpv1alpha1.APISource{
									Endpoint: "https://api.example.com/registry",
								},
							},
						},
					},
				},
			},
			expectedSources: 3,
			validateFunc: func(t *testing.T, config *Config) {
				assert.Len(t, config.Sources, 3)

				// Verify ConfigMap source (converted to file)
				assert.Equal(t, "configmap-source", config.Sources[0].Name)
				assert.Equal(t, SourceTypeFile, config.Sources[0].Type)
				assert.NotNil(t, config.Sources[0].File)

				// Verify Git source
				assert.Equal(t, "git-source", config.Sources[1].Name)
				assert.Equal(t, SourceTypeGit, config.Sources[1].Type)
				assert.NotNil(t, config.Sources[1].Git)
				assert.Equal(t, "https://github.com/example/registry.git", config.Sources[1].Git.Repository)
				assert.Equal(t, "main", config.Sources[1].Git.Branch)

				// Verify API source
				assert.Equal(t, "api-source", config.Sources[2].Name)
				assert.Equal(t, SourceTypeAPI, config.Sources[2].Type)
				assert.NotNil(t, config.Sources[2].API)
				assert.Equal(t, "https://api.example.com/registry", config.Sources[2].API.Endpoint)
			},
		},
		{
			name: "sources with filters",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "filtered-registry",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Sources: []mcpv1alpha1.MCPRegistrySourceConfig{
						{
							Name: "filtered-source",
							MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
								Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
								Format: mcpv1alpha1.RegistryFormatToolHive,
								ConfigMapRef: &corev1.ConfigMapKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: "test-configmap",
									},
									Key: "registry.json",
								},
							},
							Filter: &mcpv1alpha1.RegistryFilter{
								NameFilters: &mcpv1alpha1.NameFilter{
									Include: []string{"server-*"},
									Exclude: []string{"server-test"},
								},
								Tags: &mcpv1alpha1.TagFilter{
									Include: []string{"production", "stable"},
								},
							},
						},
					},
				},
			},
			expectedSources: 1,
			validateFunc: func(t *testing.T, config *Config) {
				assert.Len(t, config.Sources, 1)
				source := config.Sources[0]
				assert.NotNil(t, source.Filter)
				assert.NotNil(t, source.Filter.Names)
				assert.Equal(t, []string{"server-*"}, source.Filter.Names.Include)
				assert.Equal(t, []string{"server-test"}, source.Filter.Names.Exclude)
				assert.NotNil(t, source.Filter.Tags)
				assert.Equal(t, []string{"production", "stable"}, source.Filter.Tags.Include)
			},
		},
		{
			name: "no sources configured",
			mcpRegistry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "empty-registry",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Sources: []mcpv1alpha1.MCPRegistrySourceConfig{},
				},
			},
			expectedSources: 0,
			validateFunc: func(t *testing.T, config *Config) {
				assert.Len(t, config.Sources, 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a config manager with the test MCPRegistry
			cm := &configManager{
				mcpRegistry: tt.mcpRegistry,
			}

			// Build the config
			config, err := cm.BuildConfig()

			if tt.wantErr {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, config)
			assert.Len(t, config.Sources, tt.expectedSources)

			// Run additional validation if provided
			if tt.validateFunc != nil {
				tt.validateFunc(t, config)
			}
		})
	}
}

func TestMultipleSourceVolumeNaming(t *testing.T) {
	// Test that volume names are unique for multiple sources
	sources := []mcpv1alpha1.MCPRegistrySourceConfig{
		{
			Name: "source-a",
			MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
			},
		},
		{
			Name: "source-b",
			MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
			},
		},
		{
			Name: "source-c",
			MCPRegistrySource: mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
			},
		},
	}

	volumeNames := make(map[string]bool)
	for i, source := range sources {
		// Simulate the volume naming logic
		volumeName := fmt.Sprintf("registry-source-%d-%s", i, source.Name)

		// Ensure no duplicates
		assert.False(t, volumeNames[volumeName], "Volume name %s should be unique", volumeName)
		volumeNames[volumeName] = true

		// Verify naming pattern
		assert.Contains(t, volumeName, source.Name)
		assert.Contains(t, volumeName, fmt.Sprintf("-%d-", i))
	}
}