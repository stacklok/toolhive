package sources

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestNewConfigMapSourceHandler(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	handler := NewConfigMapSourceHandler(fakeClient)
	assert.NotNil(t, handler)
	assert.IsType(t, &ConfigMapSourceHandler{}, handler)
}

func TestNewConfigMapSourceHandler_Validate(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	handler := NewConfigMapSourceHandler(fakeClient)

	tests := []struct {
		name          string
		source        *mcpv1alpha1.MCPRegistrySource
		expectError   bool
		errorContains string
	}{
		{
			name: "valid configmap source",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
				ConfigMap: &mcpv1alpha1.ConfigMapSource{
					Name: "test-config",
					Key:  ConfigMapSourceDataKey,
				},
			},
			expectError: false,
		},
		{
			name: "valid configmap source with default key",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
				ConfigMap: &mcpv1alpha1.ConfigMapSource{
					Name: "test-config",
					// Key will be set to default
				},
			},
			expectError: false,
		},
		{
			name: "invalid source type",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: "invalid",
				ConfigMap: &mcpv1alpha1.ConfigMapSource{
					Name: "test-config",
				},
			},
			expectError:   true,
			errorContains: "invalid source type: expected configmap",
		},
		{
			name: "missing configmap configuration",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
				// ConfigMap is nil
			},
			expectError:   true,
			errorContains: "configMap configuration is required",
		},
		{
			name: "empty configmap name",
			source: &mcpv1alpha1.MCPRegistrySource{
				Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
				ConfigMap: &mcpv1alpha1.ConfigMapSource{
					Name: "",
				},
			},
			expectError:   true,
			errorContains: "configMap name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := handler.Validate(tt.source)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				// Check that default key is set
				if tt.source.ConfigMap.Key == "" {
					assert.Equal(t, ConfigMapSourceDataKey, tt.source.ConfigMap.Key)
				}
			}
		})
	}
}

func TestConfigMapSourceHandler_FetchRegistry(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name                string
		registry            *mcpv1alpha1.MCPRegistry
		configMaps          []corev1.ConfigMap
		expectError         bool
		errorContains       string
		expectedServerCount int
	}{
		{
			name: "successful sync with toolhive format",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "test-config",
							Key:  ConfigMapSourceDataKey,
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						ConfigMapSourceDataKey: `{
							"version": "1.0.0",
							"last_updated": "2025-01-15T10:30:00Z",
							"servers": {
								"server1": {
									"name": "Test Server 1",
									"description": "A test server for validation - Server 1",
									"image": "test/server1:latest",
									"tier": "Community",
									"status": "Active",
									"transport": "stdio",
									"tools": ["test_tool"]
								},
								"server2": {
									"name": "Test Server 2", 
									"description": "A test server for validation - Server 2",
									"image": "test/server2:latest",
									"tier": "Community",
									"status": "Active",
									"transport": "stdio",
									"tools": ["test_tool"]
								}
							}
						}`,
					},
				},
			},
			expectError:         false,
			expectedServerCount: 2,
		},
		{
			name: "failed sync with upstream format",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatUpstream,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "test-config",
							Key:  ConfigMapSourceDataKey,
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						ConfigMapSourceDataKey: `[
							{
								"server": {
									"name": "Server 1",
									"description": "A test server for validation - Server 1",
									"status": "Active",
									"version_detail": {
										"version": "1.0.0"
									},
									"packages": [{
										"registry_name": "docker",
										"name": "test/server1",
										"version": "latest"
									}]
								},
								"x-publisher": {
									"x-dev.toolhive": {
										"tier": "Community",
										"transport": "stdio",
										"tools": ["test_tool"]
									}
								}
							},
							{
								"server": {
									"name": "Server 2", 
									"description": "A test server for validation - Server 2",
									"status": "Active",
									"version_detail": {
										"version": "1.0.0"
									},
									"packages": [{
										"registry_name": "docker",
										"name": "test/server2",
										"version": "latest"
									}]
								},
								"x-publisher": {
									"x-dev.toolhive": {
										"tier": "Community",
										"transport": "stdio",
										"tools": ["test_tool"]
									}
								}
							},
							{
								"server": {
									"name": "Server 3",
									"description": "A test server for validation - Server 3",
									"status": "Active",
									"version_detail": {
										"version": "1.0.0"
									},
									"packages": [{
										"registry_name": "docker",
										"name": "test/server3",
										"version": "latest"
									}]
								},
								"x-publisher": {
									"x-dev.toolhive": {
										"tier": "Community",
										"transport": "stdio",
										"tools": ["test_tool"]
									}
								}
							}
						]`,
					},
				},
			},
			expectError:   true,
			errorContains: "upstream registry format is not yet supported",
		},
		{
			name: "successful sync with default key",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "test-config",
							// Key not specified, should default
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						ConfigMapSourceDataKey: `{
							"version": "1.0.0",
							"last_updated": "2025-01-15T10:30:00Z",
							"servers": {
								"server1": {
									"name": "server1",
									"description": "A test server for validation - Server 1",
									"image": "test/server1:latest",
									"tier": "Community",
									"status": "Active",
									"transport": "stdio",
									"tools": ["test_tool"]
								}
							}
						}`,
					},
				},
			},
			expectError:         false,
			expectedServerCount: 1,
		},
		{
			name: "successful sync with same namespace",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "registry-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type:   mcpv1alpha1.RegistrySourceTypeConfigMap,
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "test-config",
							Key:  ConfigMapSourceDataKey,
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "registry-namespace",
					},
					Data: map[string]string{
						ConfigMapSourceDataKey: `{
							"version": "1.0.0",
							"last_updated": "2025-01-15T10:30:00Z",
							"servers": {}
						}`,
					},
				},
			},
			expectError:         false,
			expectedServerCount: 0,
		},
		{
			name: "configmap not found",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "missing-config",
						},
					},
				},
			},
			configMaps:    []corev1.ConfigMap{},
			expectError:   true,
			errorContains: "failed to get ConfigMap",
		},
		{
			name: "key not found in configmap",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "test-config",
							Key:  "missing-key",
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						ConfigMapSourceDataKey: `{
							"version": "1.0.0",
							"last_updated": "2025-01-15T10:30:00Z",
							"servers": {}
						}`,
					},
				},
			},
			expectError:   true,
			errorContains: "key missing-key not found",
		},
		{
			name: "invalid json data",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapSource{
							Name: "test-config",
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
<<<<<<< HEAD
						ConfigMapSourceDataKey: string(InvalidJSON()),
=======
						ConfigMapSourceDataKey: `invalid json`,
>>>>>>> eca8e428 (Refactor ConfigMap handling to use a constant key for registry data)
					},
				},
			},
			expectError:   true,
			errorContains: "unsupported format:",
		},
		{
			name: "invalid source configuration",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Type: mcpv1alpha1.RegistrySourceTypeConfigMap,
						// ConfigMap is nil
					},
				},
			},
			configMaps:    []corev1.ConfigMap{},
			expectError:   true,
			errorContains: "source validation failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []runtime.Object{tt.registry}
			for i := range tt.configMaps {
				objects = append(objects, &tt.configMaps[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			handler := NewConfigMapSourceHandler(fakeClient)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			result, err := handler.FetchRegistry(ctx, tt.registry)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.expectedServerCount, result.ServerCount)
				assert.NotEmpty(t, result.Hash)
				assert.NotEmpty(t, result.Registry)
			}
		})
	}
}

func TestConfigMapSourceHandler_ValidationWithBuilder(t *testing.T) {
	t.Parallel()

	handler := &ConfigMapSourceHandler{
		validator: NewSourceDataValidator(),
	}

	tests := []struct {
		name          string
		builder       func() []byte
		format        string
		expectedCount int
		expectError   bool
		errorContains string
	}{
		{
			name: "toolhive format with container servers",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatToolHive).
					WithServer("container-server1").
					WithServer("container-server2").
					BuildJSON()
			},
			format:        mcpv1alpha1.RegistryFormatToolHive,
			expectedCount: 2,
			expectError:   false,
		},
		{
			name: "toolhive format with servers and remote servers",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatToolHive).
					WithServer("container-server1").
					WithServer("container-server2").
					WithRemoteServerName("remote-server1", "https://remote-1.com").
					WithRemoteServerName("remote-server2", "https://remote-2.com").
					WithRemoteServerName("remote-server3", "https://remote-3.com").
					BuildJSON()
			},
			format:        mcpv1alpha1.RegistryFormatToolHive,
			expectedCount: 5, // 2 container servers + 3 remote servers
			expectError:   false,
		},
		{
			name: "toolhive format empty servers",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatToolHive).
					Empty().
					BuildJSON()
			},
			format:      mcpv1alpha1.RegistryFormatToolHive,
			expectError: false, // Empty is now valid for ToolHive format
		},
		{
			name: "toolhive format with only remote servers",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatToolHive).
					Empty().
					WithRemoteServerName("remote-only-1", "https://remote-only-1.com").
					WithRemoteServerName("remote-only-2", "https://remote-only-2.com").
					BuildJSON()
			},
			format:        mcpv1alpha1.RegistryFormatToolHive,
			expectedCount: 2, // 0 container servers + 2 remote servers
			expectError:   false,
		},
		{
			name: "upstream format with servers",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatUpstream).
					WithServerName("s1").
					WithServerName("s2").
					BuildJSON()
			},
			format:        mcpv1alpha1.RegistryFormatUpstream,
			expectedCount: 2,
			expectError:   false,
		},
		{
			name: "upstream format empty array",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatUpstream).
					Empty().
					BuildJSON()
			},
			format:        mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "upstream registry must contain at least one server",
		},
		{
			name: "unsupported format",
			builder: func() []byte {
				return NewTestRegistryBuilder(mcpv1alpha1.RegistryFormatToolHive).
					WithServer("s1").
					BuildJSON()
			},
			format:        "unsupported",
			expectError:   true,
			errorContains: "unsupported format: unsupported",
		},
		{
			name: "invalid toolhive json",
			builder: func() []byte {
				return InvalidJSON()
			},
			format:        mcpv1alpha1.RegistryFormatToolHive,
			expectError:   true,
			errorContains: "failed to parse registry data",
		},
		{
			name: "invalid upstream json",
			builder: func() []byte {
				return InvalidJSON()
			},
			format:        mcpv1alpha1.RegistryFormatUpstream,
			expectError:   true,
			errorContains: "invalid upstream format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := tt.builder()
			registry, err := handler.validator.ValidateData(data, tt.format)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, registry)
				if tt.expectedCount > 0 {
					actualCount := len(registry.Servers) + len(registry.RemoteServers)
					assert.Equal(t, tt.expectedCount, actualCount)
				}
			}
		})
	}
}
