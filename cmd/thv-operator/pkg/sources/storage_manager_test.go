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
		data              []byte
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
					UID:       types.UID("test-uid"),
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Format: mcpv1alpha1.RegistryFormatToolHive,
					},
				},
			},
			data:        []byte(`{"version": "1.0.0", "servers": {}}`),
			expectError: false,
		},
		{
			name: "update existing registry data",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid"),
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Format: mcpv1alpha1.RegistryFormatToolHive,
					},
				},
			},
			data: []byte(`{"version": "1.0.0", "servers": {"server1": {}}}`),
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
			name: "store with upstream format",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "upstream-registry",
					Namespace: "test-namespace",
					UID:       types.UID("test-uid-2"),
				},
				Spec: mcpv1alpha1.MCPRegistrySpec{
					Source: mcpv1alpha1.MCPRegistrySource{
						Format: mcpv1alpha1.RegistryFormatUpstream,
					},
				},
			},
			data:        []byte(`[{"server": {"name": "test-server"}}]`),
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

			err := manager.Store(ctx, tt.registry, tt.data)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)

				// Verify the ConfigMap was created/updated
				configMapName := tt.registry.Name + "-registry-storage"
				configMap := &corev1.ConfigMap{}
				err = fakeClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: tt.registry.Namespace,
				}, configMap)

				require.NoError(t, err)
				assert.Equal(t, string(tt.data), configMap.Data[ConfigMapStorageDataKey])

				// Verify annotations
				assert.Equal(t, tt.registry.Name, configMap.Annotations["toolhive.stacklok.dev/registry-name"])
				assert.Equal(t, string(tt.registry.Spec.Source.Format), configMap.Annotations["toolhive.stacklok.dev/registry-format"])

				// Verify labels
				assert.Equal(t, "toolhive-operator", configMap.Labels["app.kubernetes.io/name"])
				assert.Equal(t, "registry-storage", configMap.Labels["app.kubernetes.io/component"])
				assert.Equal(t, tt.registry.Name, configMap.Labels["toolhive.stacklok.dev/registry"])

				// Verify owner reference
				assert.Len(t, configMap.OwnerReferences, 1)
				assert.Equal(t, tt.registry.Name, configMap.OwnerReferences[0].Name)
				assert.Equal(t, tt.registry.UID, configMap.OwnerReferences[0].UID)
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
		name          string
		registry      *mcpv1alpha1.MCPRegistry
		configMap     *corev1.ConfigMap
		expectedData  []byte
		expectError   bool
		errorContains string
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
					ConfigMapStorageDataKey: `{"version": "1.0.0", "servers": {"server1": {}}}`,
				},
			},
			expectedData: []byte(`{"version": "1.0.0", "servers": {"server1": {}}}`),
			expectError:  false,
		},
		{
			name: "configmap not found",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "missing-registry",
					Namespace: "test-namespace",
				},
			},
			expectError:   true,
			errorContains: "failed to get storage ConfigMap",
		},
		{
			name: "registry data key not found",
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
					"other-key": "some-data",
				},
			},
			expectError:   true,
			errorContains: "registry data not found in ConfigMap",
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

			data, err := manager.Get(ctx, tt.registry)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, data)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedData, data)
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
		name          string
		registry      *mcpv1alpha1.MCPRegistry
		configMap     *corev1.ConfigMap
		expectError   bool
		errorContains string
	}{
		{
			name: "delete existing configmap",
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
					ConfigMapStorageDataKey: `{"version": "1.0.0", "servers": {}}`,
				},
			},
			expectError: false,
		},
		{
			name: "delete non-existent configmap",
			registry: &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "missing-registry",
					Namespace: "test-namespace",
				},
			},
			expectError:   true,
			errorContains: "failed to delete storage ConfigMap",
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

			err := manager.Delete(ctx, tt.registry)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)

				// Verify the ConfigMap was deleted
				configMapName := tt.registry.Name + "-registry-storage"
				configMap := &corev1.ConfigMap{}
				err = fakeClient.Get(ctx, types.NamespacedName{
					Name:      configMapName,
					Namespace: tt.registry.Namespace,
				}, configMap)

				assert.Error(t, err) // Should not be found
			}
		})
	}
}

func TestConfigMapStorageManager_GetStorageReference(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	manager := NewConfigMapStorageManager(fakeClient, scheme)

	registry := &mcpv1alpha1.MCPRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-registry",
			Namespace: "test-namespace",
		},
	}

	ref := manager.GetStorageReference(registry)

	assert.NotNil(t, ref)
	assert.Equal(t, "configmap", ref.Type)
	assert.NotNil(t, ref.ConfigMapRef)
	assert.Equal(t, "test-registry-registry-storage", ref.ConfigMapRef.Name)
}

func TestConfigMapStorageManager_getConfigMapName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		registryName string
		expected     string
	}{
		{"simple", "simple-registry-storage"},
		{"my-registry", "my-registry-registry-storage"},
		{"test123", "test123-registry-storage"},
	}

	scheme := runtime.NewScheme()
	fakeClient := fake.NewClientBuilder().Build()
	manager := &ConfigMapStorageManager{client: fakeClient, scheme: scheme}

	for _, tt := range tests {
		t.Run(tt.registryName, func(t *testing.T) {
			t.Parallel()

			registry := &mcpv1alpha1.MCPRegistry{
				ObjectMeta: metav1.ObjectMeta{
					Name: tt.registryName,
				},
			}

			result := manager.getConfigMapName(registry)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestStorageError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		operation    string
		registryName string
		reason       string
		wrappedErr   error
		expectedMsg  string
	}{
		{
			name:         "error with wrapped error",
			operation:    "create",
			registryName: "test-registry",
			reason:       "permission denied",
			wrappedErr:   assert.AnError,
			expectedMsg:  "storage operation 'create' failed for registry 'test-registry': permission denied: assert.AnError general error for testing",
		},
		{
			name:         "error without wrapped error",
			operation:    "get",
			registryName: "missing-registry",
			reason:       "not found",
			wrappedErr:   nil,
			expectedMsg:  "storage operation 'get' failed for registry 'missing-registry': not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := NewStorageError(tt.operation, tt.registryName, tt.reason, tt.wrappedErr)

			assert.Equal(t, tt.operation, err.Operation)
			assert.Equal(t, tt.registryName, err.RegistryName)
			assert.Equal(t, tt.reason, err.Reason)
			assert.Equal(t, tt.wrappedErr, err.Err)
			assert.Equal(t, tt.expectedMsg, err.Error())

			if tt.wrappedErr != nil {
				assert.Equal(t, tt.wrappedErr, err.Unwrap())
			} else {
				assert.Nil(t, err.Unwrap())
			}
		})
	}
}

func TestStorageError_Is(t *testing.T) {
	t.Parallel()

	err1 := NewStorageError("create", "registry1", "reason1", nil)
	err2 := NewStorageError("create", "registry1", "reason2", nil)
	err3 := NewStorageError("update", "registry1", "reason1", nil)
	err4 := NewStorageError("create", "registry2", "reason1", nil)

	assert.True(t, err1.Is(err2))            // Same operation and registry
	assert.False(t, err1.Is(err3))           // Different operation
	assert.False(t, err1.Is(err4))           // Different registry
	assert.False(t, err1.Is(assert.AnError)) // Different error type
}
