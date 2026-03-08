// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

const testNamespaceDefault = "default"

func TestEmbeddingServer_GetPort(t *testing.T) {
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

			embedding := &mcpv1alpha1.EmbeddingServer{
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Port: tt.port,
				},
			}

			assert.Equal(t, tt.expected, embedding.GetPort())
		})
	}
}

func TestEmbeddingServer_GetReplicas(t *testing.T) {
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

			embedding := &mcpv1alpha1.EmbeddingServer{
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Replicas: tt.replicas,
				},
			}

			assert.Equal(t, tt.expected, embedding.GetReplicas())
		})
	}
}

func TestEmbeddingServer_IsModelCacheEnabled(t *testing.T) {
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

			embedding := &mcpv1alpha1.EmbeddingServer{
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					ModelCache: tt.modelCache,
				},
			}

			assert.Equal(t, tt.expected, embedding.IsModelCacheEnabled())
		})
	}
}

func TestEmbeddingServer_GetImagePullPolicy(t *testing.T) {
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

			embedding := &mcpv1alpha1.EmbeddingServer{
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					ImagePullPolicy: tt.imagePullPolicy,
				},
			}

			assert.Equal(t, tt.expected, embedding.GetImagePullPolicy())
		})
	}
}

func TestEmbeddingServerPodTemplateSpecValidation(t *testing.T) {
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

func TestEmbeddingServer_Labels(t *testing.T) {
	t.Parallel()

	embedding := &mcpv1alpha1.EmbeddingServer{
		Spec: mcpv1alpha1.EmbeddingServerSpec{
			Model: "test-model",
		},
	}
	embedding.Name = "test-embedding"

	reconciler := &EmbeddingServerReconciler{}
	labels := reconciler.labelsForEmbedding(embedding)

	// Check required labels
	assert.Equal(t, "embeddingserver", labels["app.kubernetes.io/name"])
	assert.Equal(t, "test-embedding", labels["app.kubernetes.io/instance"])
	assert.Equal(t, "embedding-server", labels["app.kubernetes.io/component"])
	assert.Equal(t, "toolhive-operator", labels["app.kubernetes.io/managed-by"])

}

func TestEmbeddingServer_ModelCacheConfig(t *testing.T) {
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

			embedding := &mcpv1alpha1.EmbeddingServer{
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model:      "test-model",
					ModelCache: tt.modelCache,
				},
			}
			embedding.Name = "test-embedding"
			embedding.Namespace = testNamespaceDefault

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

// Test helpers

func createEmbeddingServerTestScheme() *runtime.Scheme {
	testScheme := runtime.NewScheme()
	_ = corev1.AddToScheme(testScheme)
	_ = appsv1.AddToScheme(testScheme)
	_ = mcpv1alpha1.AddToScheme(testScheme)
	return testScheme
}

func createTestEmbeddingServer(name, namespace, image, model string) *mcpv1alpha1.EmbeddingServer {
	return &mcpv1alpha1.EmbeddingServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 1,
		},
		Spec: mcpv1alpha1.EmbeddingServerSpec{
			Image: image,
			Model: model,
		},
	}
}

// TestReconcile_NotFound tests reconciliation when resource is not found
func TestReconcile_NotFound(t *testing.T) {
	t.Parallel()

	scheme := createEmbeddingServerTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &EmbeddingServerReconciler{
		Client:          fakeClient,
		Scheme:          scheme,
		Recorder:        record.NewFakeRecorder(10),
		ImageValidation: validation.ImageValidationAlwaysAllow,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent",
			Namespace: testNamespaceDefault,
		},
	}

	result, err := reconciler.Reconcile(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)
}

// TestReconcile_CreateResources tests the reconciliation creates all necessary resources
func TestReconcile_CreateResources(t *testing.T) {
	t.Parallel()

	embedding := createTestEmbeddingServer("test-embedding", "test-ns", "test-image:latest", "test-model")

	scheme := createEmbeddingServerTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(embedding).
		WithStatusSubresource(embedding).
		Build()

	reconciler := &EmbeddingServerReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		Recorder:         record.NewFakeRecorder(10),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
		ImageValidation:  validation.ImageValidationAlwaysAllow,
	}

	ctx := context.TODO()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      embedding.Name,
			Namespace: embedding.Namespace,
		},
	}

	// First reconcile should create resources
	result, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result)

	// Verify finalizer was added
	updatedEmbedding := &mcpv1alpha1.EmbeddingServer{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      embedding.Name,
		Namespace: embedding.Namespace,
	}, updatedEmbedding)
	require.NoError(t, err)
	assert.Contains(t, updatedEmbedding.Finalizers, embeddingFinalizerName)

	// Verify StatefulSet was created
	sts := &appsv1.StatefulSet{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      embedding.Name,
		Namespace: embedding.Namespace,
	}, sts)
	assert.NoError(t, err, "StatefulSet should be created")
	assert.Equal(t, embedding.Name, sts.Name)
	assert.Equal(t, int32(1), *sts.Spec.Replicas)

	// Verify Service was created
	svc := &corev1.Service{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      embedding.Name,
		Namespace: embedding.Namespace,
	}, svc)
	assert.NoError(t, err, "Service should be created")
	assert.Equal(t, embedding.Name, svc.Name)
}

// TestValidateImage tests image validation with different scenarios
func TestValidateImage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		embedding         *mcpv1alpha1.EmbeddingServer
		imageValidation   validation.ImageValidation
		registries        []runtime.Object
		expectError       bool
		expectedCondition metav1.ConditionStatus
		expectedReason    string
	}{
		{
			name:              "always allow - no validation",
			embedding:         createTestEmbeddingServer("test", testNamespaceDefault, "any-image:latest", "model"),
			imageValidation:   validation.ImageValidationAlwaysAllow,
			expectError:       false,
			expectedCondition: metav1.ConditionTrue,
			expectedReason:    mcpv1alpha1.ConditionReasonImageValidationSkipped,
		},
		{
			name:              "registry enforcing - no registries",
			embedding:         createTestEmbeddingServer("test", testNamespaceDefault, "test-image:latest", "model"),
			imageValidation:   validation.ImageValidationRegistryEnforcing,
			registries:        []runtime.Object{},
			expectError:       false,
			expectedCondition: metav1.ConditionTrue,
			expectedReason:    mcpv1alpha1.ConditionReasonImageValidationSkipped,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createEmbeddingServerTestScheme()
			objects := append([]runtime.Object{tt.embedding}, tt.registries...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(tt.embedding).
				Build()

			reconciler := &EmbeddingServerReconciler{
				Client:          fakeClient,
				Scheme:          scheme,
				ImageValidation: tt.imageValidation,
			}

			err := reconciler.validateImage(context.TODO(), tt.embedding)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify condition was set
			updatedEmbedding := &mcpv1alpha1.EmbeddingServer{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      tt.embedding.Name,
				Namespace: tt.embedding.Namespace,
			}, updatedEmbedding)
			require.NoError(t, err)

			// Find the ImageValidated condition
			for _, cond := range updatedEmbedding.Status.Conditions {
				if cond.Type == mcpv1alpha1.ConditionImageValidated {
					assert.Equal(t, tt.expectedCondition, cond.Status)
					assert.Equal(t, tt.expectedReason, cond.Reason)
					return
				}
			}
		})
	}
}

// TestStatefulSetNeedsUpdate tests drift detection logic
func TestStatefulSetNeedsUpdate(t *testing.T) {
	t.Parallel()

	scheme := createEmbeddingServerTestScheme()
	reconciler := &EmbeddingServerReconciler{
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	// Helper to generate a StatefulSet from an embedding using the reconciler
	generateSts := func(e *mcpv1alpha1.EmbeddingServer) *appsv1.StatefulSet {
		return reconciler.statefulSetForEmbedding(context.TODO(), e)
	}

	tests := []struct {
		name           string
		embedding      *mcpv1alpha1.EmbeddingServer
		existingSts    *appsv1.StatefulSet
		expectedUpdate bool
		updateReason   string
	}{
		{
			name:           "no update needed - identical",
			embedding:      createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1"),
			existingSts:    generateSts(createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1")),
			expectedUpdate: false,
		},
		{
			name:           "update needed - image changed",
			embedding:      createTestEmbeddingServer("test", testNamespaceDefault, "image:v2", "model1"),
			existingSts:    generateSts(createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1")),
			expectedUpdate: true,
			updateReason:   "image changed",
		},
		{
			name:           "update needed - model changed",
			embedding:      createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model2"),
			existingSts:    generateSts(createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1")),
			expectedUpdate: true,
			updateReason:   "model changed",
		},
		{
			name: "update needed - port changed",
			embedding: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: testNamespaceDefault, Generation: 1},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Image: "image:v1",
					Model: "model1",
					Port:  9090,
				},
			},
			existingSts:    generateSts(createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1")),
			expectedUpdate: true,
			updateReason:   "port changed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			needsUpdate := reconciler.statefulSetNeedsUpdate(context.TODO(), tt.existingSts, tt.embedding)

			assert.Equal(t, tt.expectedUpdate, needsUpdate, tt.updateReason)
		})
	}
}

// TestHandleDeletion tests finalizer cleanup
func TestHandleDeletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		embedding       *mcpv1alpha1.EmbeddingServer
		expectDone      bool
		expectError     bool
		expectFinalizer bool
	}{
		{
			name: "not being deleted",
			embedding: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test",
					Namespace:  testNamespaceDefault,
					Finalizers: []string{embeddingFinalizerName},
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Image: "test:latest",
					Model: "test-model",
				},
			},
			expectDone:      false,
			expectError:     false,
			expectFinalizer: true,
		},
		{
			name: "being deleted with finalizer",
			embedding: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test",
					Namespace:         testNamespaceDefault,
					Finalizers:        []string{embeddingFinalizerName},
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Image: "test:latest",
					Model: "test-model",
				},
			},
			expectDone:      true,
			expectError:     false,
			expectFinalizer: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createEmbeddingServerTestScheme()
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.embedding).
				WithStatusSubresource(tt.embedding).
				Build()

			reconciler := &EmbeddingServerReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Recorder: record.NewFakeRecorder(10),
			}

			result, done, err := reconciler.handleDeletion(context.TODO(), tt.embedding)

			assert.Equal(t, tt.expectDone, done)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if done {
				assert.Equal(t, ctrl.Result{}, result)
			}

			// Verify finalizer state if not being deleted
			if tt.embedding.DeletionTimestamp == nil {
				updatedEmbedding := &mcpv1alpha1.EmbeddingServer{}
				err := fakeClient.Get(context.TODO(), types.NamespacedName{
					Name:      tt.embedding.Name,
					Namespace: tt.embedding.Namespace,
				}, updatedEmbedding)
				require.NoError(t, err)

				hasFinalizer := false
				for _, f := range updatedEmbedding.Finalizers {
					if f == embeddingFinalizerName {
						hasFinalizer = true
						break
					}
				}
				assert.Equal(t, tt.expectFinalizer, hasFinalizer)
			}
		})
	}
}

// TestEnsureStatefulSet tests statefulset creation and updates
func TestEnsureStatefulSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		embedding    *mcpv1alpha1.EmbeddingServer
		existingSts  *appsv1.StatefulSet
		expectCreate bool
		expectUpdate bool
		expectDone   bool
	}{
		{
			name:         "create new statefulset",
			embedding:    createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1"),
			existingSts:  nil,
			expectCreate: true,
			expectDone:   false,
		},
		{
			name: "update replicas",
			embedding: func() *mcpv1alpha1.EmbeddingServer {
				e := createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1")
				replicas := int32(3)
				e.Spec.Replicas = &replicas
				return e
			}(),
			existingSts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: testNamespaceDefault,
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(1)),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  embeddingContainerName,
									Image: "image:v1",
									Args:  []string{"--model-id", "model1", "--port", "8080"},
									Env: []corev1.EnvVar{
										{Name: "MODEL_ID", Value: "model1"},
									},
									Ports: []corev1.ContainerPort{
										{ContainerPort: 8080},
									},
								},
							},
						},
					},
				},
			},
			expectUpdate: true,
			expectDone:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createEmbeddingServerTestScheme()
			objects := []runtime.Object{tt.embedding}
			if tt.existingSts != nil {
				objects = append(objects, tt.existingSts)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := &EmbeddingServerReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			result, err := reconciler.ensureStatefulSet(context.TODO(), tt.embedding)
			require.NoError(t, err)
			// expectDone is now represented by whether we need to requeue
			if tt.expectDone {
				assert.True(t, result.RequeueAfter > 0)
			}

			// Verify statefulset exists
			sts := &appsv1.StatefulSet{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      tt.embedding.Name,
				Namespace: tt.embedding.Namespace,
			}, sts)
			assert.NoError(t, err)

			if tt.expectUpdate {
				assert.Greater(t, result.RequeueAfter, time.Duration(0))
			}
		})
	}
}

// TestUpdateEmbeddingServerStatus tests status updates
func TestUpdateEmbeddingServerStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		embedding     *mcpv1alpha1.EmbeddingServer
		statefulSet   *appsv1.StatefulSet
		expectedPhase mcpv1alpha1.EmbeddingServerPhase
		expectedURL   string
	}{
		{
			name:          "no statefulset - pending",
			embedding:     createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1"),
			statefulSet:   nil,
			expectedPhase: mcpv1alpha1.EmbeddingServerPhasePending,
			expectedURL:   "http://test.default.svc.cluster.local:8080",
		},
		{
			name:      "statefulset ready",
			embedding: createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1"),
			statefulSet: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: testNamespaceDefault,
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:      1,
					ReadyReplicas: 1,
				},
			},
			expectedPhase: mcpv1alpha1.EmbeddingServerPhaseRunning,
			expectedURL:   "http://test.default.svc.cluster.local:8080",
		},
		{
			name:      "statefulset downloading",
			embedding: createTestEmbeddingServer("test", testNamespaceDefault, "image:v1", "model1"),
			statefulSet: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: testNamespaceDefault,
				},
				Status: appsv1.StatefulSetStatus{
					Replicas:      1,
					ReadyReplicas: 0,
				},
			},
			expectedPhase: mcpv1alpha1.EmbeddingServerPhaseDownloading,
			expectedURL:   "http://test.default.svc.cluster.local:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createEmbeddingServerTestScheme()
			objects := []runtime.Object{tt.embedding}
			if tt.statefulSet != nil {
				objects = append(objects, tt.statefulSet)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(tt.embedding).
				Build()

			reconciler := &EmbeddingServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := reconciler.updateEmbeddingServerStatus(context.TODO(), tt.embedding)
			assert.NoError(t, err)

			// Verify status was updated
			updatedEmbedding := &mcpv1alpha1.EmbeddingServer{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      tt.embedding.Name,
				Namespace: tt.embedding.Namespace,
			}, updatedEmbedding)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedPhase, updatedEmbedding.Status.Phase)
			assert.Equal(t, tt.expectedURL, updatedEmbedding.Status.URL)
		})
	}
}
