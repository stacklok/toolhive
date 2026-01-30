// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registryapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestNewManager(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		description string
	}{
		{
			name:        "successful manager creation",
			description: "Should create a new manager with all dependencies",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			scheme := runtime.NewScheme()

			// Create manager
			manager := NewManager(nil, scheme)

			// Verify manager is created
			assert.NotNil(t, manager)

			// Verify manager implements the interface
			var _ = manager
		})
	}
}

func TestReconcileAPIService(t *testing.T) {
	t.Parallel()

	t.Run("successful reconciliation creates configmap and returns no error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		// Create scheme and fake client
		scheme := runtime.NewScheme()
		_ = mcpv1alpha1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)
		_ = rbacv1.AddToScheme(scheme)

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		// Create test MCPRegistry
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-registry",
				Namespace: "test-namespace",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "10m",
						},
					},
				},
			},
		}

		// Create manager
		manager := NewManager(fakeClient, scheme)
		// Execute
		result := manager.ReconcileAPIService(context.Background(), mcpRegistry)

		// Verify - should succeed with no error
		assert.Nil(t, result, "Expected no error result from ReconcileAPIService")

		// Verify that the config ConfigMap was created
		configMapList := &corev1.ConfigMapList{}
		err := fakeClient.List(context.Background(), configMapList, client.InNamespace("test-namespace"))
		require.NoError(t, err, "Should be able to list ConfigMaps")

		// Find the registry server config ConfigMap
		var foundConfigMap *corev1.ConfigMap
		for _, cm := range configMapList.Items {
			if strings.Contains(cm.Name, "test-registry") && strings.Contains(cm.Name, "registry-server-config") {
				foundConfigMap = &cm
				break
			}
		}

		require.NotNil(t, foundConfigMap, "Registry server config ConfigMap should have been created")
		assert.Equal(t, "test-namespace", foundConfigMap.Namespace)
		assert.Contains(t, foundConfigMap.Name, "test-registry")

		// Verify ConfigMap has the expected data
		assert.Contains(t, foundConfigMap.Data, "config.yaml", "ConfigMap should have config.yaml key")
		configYAML := foundConfigMap.Data["config.yaml"]
		assert.NotEmpty(t, configYAML, "config.yaml should not be empty")

		// Verify the content includes expected configuration
		assert.Contains(t, configYAML, "registryName: test-registry")
		assert.Contains(t, configYAML, "format: toolhive")
		assert.Contains(t, configYAML, "interval: 10m")
	})

	t.Run("configmap upsert failure returns proper error", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		// Create scheme and a client that will fail on ConfigMap operations
		scheme := runtime.NewScheme()
		_ = mcpv1alpha1.AddToScheme(scheme)
		_ = appsv1.AddToScheme(scheme)
		_ = corev1.AddToScheme(scheme)

		// Create a fake client that will return an error when trying to create ConfigMaps
		err := errors.New("simulated ConfigMap operation failure")
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					// Simulate Update failure
					return err
				},
			}).
			Build()

		// Create test MCPRegistry
		mcpRegistry := &mcpv1alpha1.MCPRegistry{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-registry",
				Namespace: "test-namespace",
			},
			Spec: mcpv1alpha1.MCPRegistrySpec{
				Registries: []mcpv1alpha1.MCPRegistryConfig{
					{
						Name:   "default",
						Format: mcpv1alpha1.RegistryFormatToolHive,
						ConfigMapRef: &corev1.ConfigMapKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							Key: "registry.json",
						},
						SyncPolicy: &mcpv1alpha1.SyncPolicy{
							Interval: "10m",
						},
					},
				},
			},
		}

		// Create manager
		manager := NewManager(fakeClient, scheme)
		// Execute
		result := manager.ReconcileAPIService(context.Background(), mcpRegistry)

		// Verify that an error is returned
		assert.NotNil(t, result, "Expected an error when ConfigMap upsert fails")
		assert.Contains(t, result.Error(), "Failed to ensure registry server config config map",
			"Error should indicate registry server config ConfigMap failure")
		assert.Contains(t, result.Error(), "failed to upsert registry server config config map",
			"Error should indicate upsert operation failure")
		assert.Contains(t, result.Error(), "simulated ConfigMap operation failure",
			"Error should include the underlying client error")
	})
}

func TestManagerCheckAPIReadiness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		deployment  *appsv1.Deployment
		expected    bool
		description string
	}{
		{
			name:        "nil deployment",
			deployment:  nil,
			expected:    false,
			description: "Should return false for nil deployment",
		},
		{
			name: "deployment with ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 1,
				},
			},
			expected:    true,
			description: "Should return true when deployment has ready replicas",
		},
		{
			name: "deployment with no ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 0,
				},
			},
			expected:    false,
			description: "Should return false when deployment has no ready replicas",
		},
		{
			name: "deployment with partial ready replicas",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      3,
					ReadyReplicas: 1,
				},
			},
			expected:    true,
			description: "Should return true when deployment has at least one ready replica",
		},
		{
			name: "deployment with failed condition",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deployment",
					Namespace: "test-namespace",
				},
				Status: appsv1.DeploymentStatus{
					Replicas:      1,
					ReadyReplicas: 0,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:    appsv1.DeploymentProgressing,
							Status:  corev1.ConditionFalse,
							Reason:  "ProgressDeadlineExceeded",
							Message: "ReplicaSet has timed out progressing",
						},
					},
				},
			},
			expected:    false,
			description: "Should return false when deployment is not progressing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manager := &manager{}
			ctx := context.Background()

			result := manager.CheckAPIReadiness(ctx, tt.deployment)

			assert.Equal(t, tt.expected, result, tt.description)
		})
	}
}
