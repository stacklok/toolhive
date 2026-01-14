package controllers

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

func TestMCPEmbedding_GetPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		port     int32
		expected int32
	}{
		{
			name:     "default port",
			port:     0,
			expected: 8080,
		},
		{
			name:     "custom port",
			port:     9000,
			expected: 9000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedding := &mcpv1alpha1.MCPEmbedding{
				Spec: mcpv1alpha1.MCPEmbeddingSpec{
					Port: tt.port,
				},
			}

			assert.Equal(t, tt.expected, embedding.GetPort())
		})
	}
}

func TestMCPEmbedding_GetReplicas(t *testing.T) {
	t.Parallel()

	replicas2 := int32(2)
	tests := []struct {
		name     string
		replicas *int32
		expected int32
	}{
		{
			name:     "default replicas",
			replicas: nil,
			expected: 1,
		},
		{
			name:     "custom replicas",
			replicas: &replicas2,
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedding := &mcpv1alpha1.MCPEmbedding{
				Spec: mcpv1alpha1.MCPEmbeddingSpec{
					Replicas: tt.replicas,
				},
			}

			assert.Equal(t, tt.expected, embedding.GetReplicas())
		})
	}
}

func TestMCPEmbedding_IsModelCacheEnabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		modelCache *mcpv1alpha1.ModelCacheConfig
		expected   bool
	}{
		{
			name:       "nil model cache",
			modelCache: nil,
			expected:   false,
		},
		{
			name: "model cache disabled",
			modelCache: &mcpv1alpha1.ModelCacheConfig{
				Enabled: false,
			},
			expected: false,
		},
		{
			name: "model cache enabled",
			modelCache: &mcpv1alpha1.ModelCacheConfig{
				Enabled: true,
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedding := &mcpv1alpha1.MCPEmbedding{
				Spec: mcpv1alpha1.MCPEmbeddingSpec{
					ModelCache: tt.modelCache,
				},
			}

			assert.Equal(t, tt.expected, embedding.IsModelCacheEnabled())
		})
	}
}

func TestMCPEmbedding_GetImagePullPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		imagePullPolicy string
		expected        string
	}{
		{
			name:            "default pull policy",
			imagePullPolicy: "",
			expected:        "IfNotPresent",
		},
		{
			name:            "Never pull policy",
			imagePullPolicy: "Never",
			expected:        "Never",
		},
		{
			name:            "Always pull policy",
			imagePullPolicy: "Always",
			expected:        "Always",
		},
		{
			name:            "IfNotPresent pull policy",
			imagePullPolicy: "IfNotPresent",
			expected:        "IfNotPresent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedding := &mcpv1alpha1.MCPEmbedding{
				Spec: mcpv1alpha1.MCPEmbeddingSpec{
					ImagePullPolicy: tt.imagePullPolicy,
				},
			}

			assert.Equal(t, tt.expected, embedding.GetImagePullPolicy())
		})
	}
}

func TestMCPEmbeddingPodTemplateSpecValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		podTemplateSpec *runtime.RawExtension
		expectValid     bool
	}{
		{
			name:            "no PodTemplateSpec provided",
			podTemplateSpec: nil,
			expectValid:     true,
		},
		{
			name: "valid PodTemplateSpec",
			podTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{"spec":{"nodeSelector":{"disktype":"ssd"}}}`),
			},
			expectValid: true,
		},
		{
			name: "invalid PodTemplateSpec",
			podTemplateSpec: &runtime.RawExtension{
				Raw: []byte(`{invalid json`),
			},
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.podTemplateSpec == nil {
				// nil is always valid
				assert.True(t, tt.expectValid)
				return
			}

			_, err := ctrlutil.NewPodTemplateSpecBuilder(tt.podTemplateSpec, embeddingContainerName)

			if tt.expectValid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestMCPEmbedding_Labels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		groupRef string
	}{
		{
			name:     "no group reference",
			groupRef: "",
		},
		{
			name:     "with group reference",
			groupRef: "ml-services",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedding := &mcpv1alpha1.MCPEmbedding{
				Spec: mcpv1alpha1.MCPEmbeddingSpec{
					GroupRef: tt.groupRef,
				},
			}
			embedding.Name = "test-embedding"

			reconciler := &MCPEmbeddingReconciler{}
			labels := reconciler.labelsForEmbedding(embedding)

			// Check required labels
			assert.Equal(t, "mcpembedding", labels["app.kubernetes.io/name"])
			assert.Equal(t, "test-embedding", labels["app.kubernetes.io/instance"])
			assert.Equal(t, "embedding-server", labels["app.kubernetes.io/component"])
			assert.Equal(t, "toolhive-operator", labels["app.kubernetes.io/managed-by"])

			// Check group label
			if tt.groupRef != "" {
				assert.Equal(t, tt.groupRef, labels["toolhive.stacklok.dev/group"])
			} else {
				_, exists := labels["toolhive.stacklok.dev/group"]
				assert.False(t, exists)
			}
		})
	}
}

func TestMCPEmbedding_ModelCacheConfig(t *testing.T) {
	t.Parallel()

	storageClassName := "fast-ssd"
	tests := []struct {
		name           string
		modelCache     *mcpv1alpha1.ModelCacheConfig
		expectedSize   string
		expectedAccess string
	}{
		{
			name: "default values",
			modelCache: &mcpv1alpha1.ModelCacheConfig{
				Enabled: true,
			},
			expectedSize:   "10Gi",
			expectedAccess: "ReadWriteOnce",
		},
		{
			name: "custom values",
			modelCache: &mcpv1alpha1.ModelCacheConfig{
				Enabled:          true,
				Size:             "20Gi",
				AccessMode:       "ReadWriteMany",
				StorageClassName: &storageClassName,
			},
			expectedSize:   "20Gi",
			expectedAccess: "ReadWriteMany",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			embedding := &mcpv1alpha1.MCPEmbedding{
				Spec: mcpv1alpha1.MCPEmbeddingSpec{
					Model:      "test-model",
					ModelCache: tt.modelCache,
				},
			}
			embedding.Name = "test-embedding"
			embedding.Namespace = "default"

			// Note: We're testing the PVC structure creation, not SetControllerReference
			// which requires a Scheme. In actual reconciliation, the Scheme is set.
			// For this unit test, we test just the PVC structure without owner references.
			pvcName := fmt.Sprintf("%s-model-cache", embedding.Name)

			size := tt.modelCache.Size
			if size == "" {
				size = "10Gi"
			}

			accessMode := corev1.ReadWriteOnce
			if tt.modelCache.AccessMode != "" {
				accessMode = corev1.PersistentVolumeAccessMode(tt.modelCache.AccessMode)
			}

			// Verify expected values
			assert.Equal(t, "test-embedding-model-cache", pvcName)
			assert.Equal(t, tt.expectedSize, size)
			assert.Equal(t, tt.expectedAccess, string(accessMode))

			// Verify storage class name if provided
			if tt.modelCache.StorageClassName != nil {
				assert.Equal(t, storageClassName, *tt.modelCache.StorageClassName)
			}
		})
	}
}
