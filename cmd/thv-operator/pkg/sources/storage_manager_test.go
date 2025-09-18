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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestNewConfigMapStorageManager(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	manager := NewConfigMapStorageManager(fakeClient, scheme)
	assert.NotNil(t, manager)
	assert.IsType(t, &ConfigMapStorageManager{}, manager)
}

func TestConfigMapStorageManager_Store(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name              string
		registry          *mcpv1alpha1.MCPRegistry
		registryData      *registry.Registry
		existingConfigMap *corev1.ConfigMap
		expectError       bool
		errorContains     string
	}{
		{
			name: "store new registry data",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Format: mcpv1alpha1.RegistryFormatToolHive,
					},
				},
			},
			registryData: &registry.Registry{
				Version:       "1.0.0",
				Servers:       make(map[string]*registry.ImageMetadata),
				RemoteServers: make(map[string]*registry.RemoteServerMetadata),
			},
			expectError: false,
		},
		{
			name: "update existing registry data",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Format: mcpv1alpha1.RegistryFormatToolHive,
					},
				},
			},
			registryData: &registry.Registry{
				Version: "1.0.0",
				Servers: map[string]*registry.ImageMetadata{
					"server1": {},
				},
				RemoteServers: make(map[string]*registry.RemoteServerMetadata),
			},
			existingConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry-registry-storage",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					ConfigMapStorageDataKey: `{"version": "1.0.0", "servers": {}}`,
				},
			},
			expectError: false,
		},
		{
			name: "store with different namespace",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "different-namespace",
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Format: mcpv1alpha1.RegistryFormatToolHive,
					},
				},
			},
			registryData: &registry.Registry{
				Version: "1.0",
				Servers: map[string]*registry.ImageMetadata{
					"test-server": {},
				},
				RemoteServers: make(map[string]*registry.RemoteServerMetadata),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []runtime.Object{tt.registry}
			if tt.existingConfigMap != nil {
				objects = append(objects, tt.existingConfigMap)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			manager := NewConfigMapStorageManager(fakeClient, scheme)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := manager.Store(ctx, tt.registry, tt.registryData)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)

				// Verify the ConfigMap was created/updated
				configMap := &corev1.ConfigMap{}
				err = fakeClient.Get(ctx, types.NamespacedName{
					Name:      "test-registry-registry-storage",
					Namespace: tt.registry.Namespace,
				}, configMap)

				require.NoError(t, err)

				// Verify the stored data is valid JSON
				assert.NotEmpty(t, configMap.Data[ConfigMapStorageDataKey])

				// Verify annotations
				assert.Equal(t, tt.registry.Name, configMap.Annotations["toolhive.stacklok.dev/registry-name"])
				assert.Equal(t, string(tt.registry.Spec.Source.Format), configMap.Annotations["toolhive.stacklok.dev/registry-format"])

				// Verify labels
				assert.Equal(t, "toolhive-operator", configMap.Labels["app.kubernetes.io/name"])
				assert.Equal(t, RegistryStorageComponent, configMap.Labels["app.kubernetes.io/component"])
				assert.Equal(t, tt.registry.Name, configMap.Labels["toolhive.stacklok.dev/registry"])

				// Verify owner reference
				assert.Len(t, configMap.OwnerReferences, 1)
				assert.Equal(t, tt.registry.Name, configMap.OwnerReferences[0].Name)
			}
		})
	}
}

func TestConfigMapStorageManager_Get(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name             string
		registry         *mcpv1alpha1.MCPRegistry
		configMap        *corev1.ConfigMap
		expectedRegistry *registry.Registry
		expectError      bool
		errorContains    string
	}{
		{
			name: "get existing registry data",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry-registry-storage",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					ConfigMapStorageDataKey: `{"version": "1.0.0", "servers": {"server1": {}}, "remoteServers": {}}`,
				},
			},
			expectedRegistry: &registry.Registry{
				Version: "1.0.0",
				Servers: map[string]*registry.ImageMetadata{
					"server1": {},
				},
				RemoteServers: nil, // JSON unmarshaling creates nil for empty objects
			},
			expectError: false,
		},
		{
			name: "configmap not found",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
			},
			configMap:     nil,
			expectError:   true,
			errorContains: "failed to get storage ConfigMap",
		},
		{
			name: "missing registry data key",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry-registry-storage",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					"other-key": "some data",
				},
			},
			expectError:   true,
			errorContains: "registry data not found in ConfigMap",
		},
		{
			name: "invalid JSON data",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
			},
			configMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry-registry-storage",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					ConfigMapStorageDataKey: "invalid json",
				},
			},
			expectError:   true,
			errorContains: "failed to parse registry data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []runtime.Object{tt.registry}
			if tt.configMap != nil {
				objects = append(objects, tt.configMap)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			manager := NewConfigMapStorageManager(fakeClient, scheme)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			registryData, err := manager.Get(ctx, tt.registry)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, registryData)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedRegistry, registryData)
			}
		})
	}
}

func TestConfigMapStorageManager_Delete(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name              string
		registry          *mcpv1alpha1.MCPRegistry
		existingConfigMap *corev1.ConfigMap
		expectError       bool
		errorContains     string
	}{
		{
			name: "delete existing configmap",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
			},
			existingConfigMap: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry-registry-storage",
					Namespace: "test-namespace",
				},
				Data: map[string]string{
					ConfigMapStorageDataKey: `{"version": "1.0.0", "servers": {}}`,
				},
			},
			expectError: false,
		},
		{
			name: "delete non-existent configmap",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
				},
			},
			existingConfigMap: nil,
			expectError:       false, // Delete should not error if ConfigMap doesn't exist
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []runtime.Object{tt.registry}
			if tt.existingConfigMap != nil {
				objects = append(objects, tt.existingConfigMap)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			manager := NewConfigMapStorageManager(fakeClient, scheme)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := manager.Delete(ctx, tt.registry)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)

				// Verify the ConfigMap was deleted (if it existed)
				if tt.existingConfigMap != nil {
					configMap := &corev1.ConfigMap{}
					err = fakeClient.Get(ctx, types.NamespacedName{
						Name:      "test-registry-registry-storage",
						Namespace: tt.registry.Namespace,
					}, configMap)

					// Should get a not found error
					assert.Error(t, err)
				}
			}
		})
	}
}

func TestConfigMapStorageManager_GetStorageReference(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	manager := NewConfigMapStorageManager(fakeClient, scheme)

	mcpRegistry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "test-namespace",
		},
	}

	ref := manager.GetStorageReference(mcpRegistry)

	assert.NotNil(t, ref)
	assert.Equal(t, "configmap", ref.Type)
	assert.NotNil(t, ref.ConfigMapRef)
	assert.Equal(t, "test-registry-registry-storage", ref.ConfigMapRef.Name)
}
