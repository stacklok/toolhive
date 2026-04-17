// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestReplicaBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		transport        string
		currentReplicas  int32
		expectedReplicas int32
		expectRequeue    bool
		description      string
	}{
		{
			name:             "SSE transport allows scaling to 3",
			transport:        "sse",
			currentReplicas:  3,
			expectedReplicas: 3,
			expectRequeue:    false,
			description:      "Non-stdio transports should not have replicas reverted",
		},
		{
			name:             "streamable-http transport allows scaling to 5",
			transport:        "streamable-http",
			currentReplicas:  5,
			expectedReplicas: 5,
			expectRequeue:    false,
			description:      "Non-stdio transports should not have replicas reverted",
		},
		{
			name:             "stdio transport caps at 1 when scaled to 3",
			transport:        "stdio",
			currentReplicas:  3,
			expectedReplicas: 1,
			expectRequeue:    true,
			description:      "stdio requires 1:1 proxy-to-backend connections",
		},
		{
			name:             "stdio transport stays at 1",
			transport:        "stdio",
			currentReplicas:  1,
			expectedReplicas: 1,
			expectRequeue:    false,
			description:      "stdio at 1 replica should not trigger an update",
		},
		{
			name:             "SSE transport allows scale to 0",
			transport:        "sse",
			currentReplicas:  0,
			expectedReplicas: 0,
			expectRequeue:    false,
			description:      "Scale-to-zero should be allowed for any transport",
		},
		{
			name:             "stdio transport allows scale to 0",
			transport:        "stdio",
			currentReplicas:  0,
			expectedReplicas: 0,
			expectRequeue:    false,
			description:      "Scale-to-zero should be allowed even for stdio",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			name := "replica-test"
			namespace := testNamespaceDefault

			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     "test-image:latest",
					Transport: tt.transport,
					ProxyPort: 8080,
				},
			}

			testScheme := createTestScheme()

			// Create a deployment with the desired replica count
			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(tt.currentReplicas),
					Selector: &metav1.LabelSelector{
						MatchLabels: labelsForMCPServer(name),
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: labelsForMCPServer(name),
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "mcp",
									Image: "test-image:latest",
								},
							},
						},
					},
				},
			}

			// Create a service so reconcile doesn't bail early
			service := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("mcp-%s-proxy", name),
					Namespace: namespace,
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{Port: 8080},
					},
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithObjects(mcpServer, deployment, service).
				WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

			result, err := reconciler.Reconcile(t.Context(), ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      name,
					Namespace: namespace,
				},
			})
			require.NoError(t, err)

			if tt.expectRequeue {
				//nolint:staticcheck // Requeue is what the controller actually returns
				assert.True(t, result.Requeue, tt.description)
			}

			// Verify the deployment replicas
			updatedDeployment := &appsv1.Deployment{}
			err = fakeClient.Get(t.Context(), types.NamespacedName{
				Name:      name,
				Namespace: namespace,
			}, updatedDeployment)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedReplicas, *updatedDeployment.Spec.Replicas, tt.description)
		})
	}
}

func TestConfigUpdatePreservesReplicas(t *testing.T) {
	t.Parallel()

	name := "config-update-test"
	namespace := testNamespaceDefault

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "new-image:v2", // Changed image triggers deployment update
			Transport: "sse",
			ProxyPort: 8080,
		},
	}

	testScheme := createTestScheme()

	// Create deployment with 3 replicas and an old image
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(3),
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForMCPServer(name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForMCPServer(name),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "mcp",
							Image: "old-runner-image:v1", // Different from current runner image
						},
					},
				},
			},
		},
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("mcp-%s-proxy", name),
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{Port: 8080},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer, deployment, service).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	})
	require.NoError(t, err)

	// Verify the deployment replicas are preserved
	updatedDeployment := &appsv1.Deployment{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, updatedDeployment)
	require.NoError(t, err)
	assert.Equal(t, int32(3), *updatedDeployment.Spec.Replicas,
		"Config update should preserve replicas set by external tools")
}

func TestUpdateMCPServerStatusScaledToZero(t *testing.T) {
	t.Parallel()

	name := "stopped-test"
	namespace := testNamespaceDefault

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			ProxyPort: 8080,
		},
	}

	testScheme := createTestScheme()

	// Create deployment scaled to zero
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(0),
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForMCPServer(name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForMCPServer(name),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "mcp",
							Image: "test-image:latest",
						},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer, deployment).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	err := reconciler.updateMCPServerStatus(t.Context(), mcpServer)
	require.NoError(t, err)

	// Fetch the updated MCPServer
	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, updatedMCPServer)
	require.NoError(t, err)

	assert.Equal(t, mcpv1alpha1.MCPServerPhaseStopped, updatedMCPServer.Status.Phase)
	assert.Equal(t, "MCP server is stopped (scaled to zero)", updatedMCPServer.Status.Message)
	assert.Equal(t, int32(0), updatedMCPServer.Status.ReadyReplicas)
}

func TestUpdateMCPServerStatusReadyReplicas(t *testing.T) {
	t.Parallel()

	name := "ready-replicas-test"
	namespace := testNamespaceDefault

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			ProxyPort: 8080,
		},
	}

	testScheme := createTestScheme()

	// Create deployment with 3 replicas
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(3),
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForMCPServer(name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForMCPServer(name),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "mcp",
							Image: "test-image:latest",
						},
					},
				},
			},
		},
	}

	// Create 2 running pods and 1 pending
	runningPod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pod-0", name),
			Namespace: namespace,
			Labels:    labelsForMCPServer(name),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "mcp", Image: "test-image:latest"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	runningPod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pod-1", name),
			Namespace: namespace,
			Labels:    labelsForMCPServer(name),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "mcp", Image: "test-image:latest"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	pendingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pod-2", name),
			Namespace: namespace,
			Labels:    labelsForMCPServer(name),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "mcp", Image: "test-image:latest"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer, deployment, runningPod1, runningPod2, pendingPod).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	err := reconciler.updateMCPServerStatus(t.Context(), mcpServer)
	require.NoError(t, err)

	// Fetch the updated MCPServer
	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, updatedMCPServer)
	require.NoError(t, err)

	assert.Equal(t, mcpv1alpha1.MCPServerPhaseReady, updatedMCPServer.Status.Phase)
	assert.Equal(t, int32(2), updatedMCPServer.Status.ReadyReplicas,
		"ReadyReplicas should match the number of running pods")
}

func TestDefaultCreationHasNilReplicas(t *testing.T) {
	t.Parallel()

	name := "default-creation"
	namespace := testNamespaceDefault

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			ProxyPort: 8080,
		},
	}

	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	// First reconcile creates the deployment
	result, err := reconciler.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		},
	})
	require.NoError(t, err)
	//nolint:staticcheck // Requeue is what the controller actually returns
	assert.True(t, result.Requeue, "First reconcile should requeue after creating deployment")

	// Verify the deployment was created with nil replicas (nil-passthrough for HPA compatibility)
	deployment := &appsv1.Deployment{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, deployment)
	require.NoError(t, err)
	assert.Nil(t, deployment.Spec.Replicas,
		"Default deployment should have nil replicas (hands-off mode for HPA/KEDA)")
}

// --- resolveDeploymentReplicas unit tests ---

func TestResolveDeploymentReplicasNil(t *testing.T) {
	t.Parallel()
	result := resolveDeploymentReplicas("sse", nil)
	assert.Nil(t, result, "nil spec.replicas should return nil (hands-off mode)")
}

func TestResolveDeploymentReplicas1(t *testing.T) {
	t.Parallel()
	result := resolveDeploymentReplicas("sse", int32Ptr(1))
	require.NotNil(t, result)
	assert.Equal(t, int32(1), *result)
}

func TestResolveDeploymentReplicas3SSE(t *testing.T) {
	t.Parallel()
	result := resolveDeploymentReplicas("sse", int32Ptr(3))
	require.NotNil(t, result)
	assert.Equal(t, int32(3), *result)
}

func TestResolveDeploymentReplicasStdioCap(t *testing.T) {
	t.Parallel()
	result := resolveDeploymentReplicas("stdio", int32Ptr(3))
	require.NotNil(t, result)
	assert.Equal(t, int32(1), *result, "stdio transport must be capped at 1")
}

// --- deploymentForMCPServer unit tests ---

func TestTerminationGracePeriodSet(t *testing.T) {
	t.Parallel()

	name := "tgp-test"
	namespace := testNamespaceDefault

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			ProxyPort: 8080,
		},
	}

	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)
	dep := reconciler.deploymentForMCPServer(t.Context(), mcpServer, "")
	require.NotNil(t, dep)
	require.NotNil(t, dep.Spec.Template.Spec.TerminationGracePeriodSeconds)
	assert.Equal(t, int64(30), *dep.Spec.Template.Spec.TerminationGracePeriodSeconds)
}

func TestSpecDrivenReplicasNil(t *testing.T) {
	t.Parallel()

	name := "nil-replicas-test"
	namespace := testNamespaceDefault

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			ProxyPort: 8080,
			Replicas:  nil,
		},
	}

	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)
	dep := reconciler.deploymentForMCPServer(t.Context(), mcpServer, "")
	require.NotNil(t, dep)
	assert.Nil(t, dep.Spec.Replicas, "nil spec.replicas should produce nil Deployment.Spec.Replicas")
}

func TestSpecDrivenReplicas3(t *testing.T) {
	t.Parallel()

	name := "three-replicas-test"
	namespace := testNamespaceDefault
	replicas := int32(3)

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			ProxyPort: 8080,
			Replicas:  &replicas,
		},
	}

	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)
	dep := reconciler.deploymentForMCPServer(t.Context(), mcpServer, "")
	require.NotNil(t, dep)
	require.NotNil(t, dep.Spec.Replicas)
	assert.Equal(t, int32(3), *dep.Spec.Replicas)
}

// --- reconciler-level condition tests ---

func TestStdioCapConditionSet(t *testing.T) {
	t.Parallel()

	name := "stdio-cap-test"
	namespace := testNamespaceDefault
	replicas := int32(3)

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "stdio",
			ProxyPort: 8080,
			Replicas:  &replicas,
		},
	}

	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	// First reconcile creates the deployment
	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
	require.NoError(t, err)

	// Read back the MCPServer to check conditions
	updated := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updated)
	require.NoError(t, err)

	var found bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == mcpv1alpha1.ConditionStdioReplicaCapped {
			found = true
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
			assert.Equal(t, mcpv1alpha1.ConditionReasonStdioReplicaCapped, cond.Reason)
		}
	}
	assert.True(t, found, "ConditionStdioReplicaCapped condition should be set")
}

func TestSessionStorageWarningSet(t *testing.T) {
	t.Parallel()

	name := "session-storage-warning-test"
	namespace := testNamespaceDefault
	replicas := int32(2)

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			ProxyPort: 8080,
			Replicas:  &replicas,
			// No SessionStorage configured
		},
	}

	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
	require.NoError(t, err)

	updated := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updated)
	require.NoError(t, err)

	var found bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == mcpv1alpha1.ConditionSessionStorageWarning {
			found = true
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
			assert.Equal(t, mcpv1alpha1.ConditionReasonSessionStorageMissing, cond.Reason)
		}
	}
	assert.True(t, found, "ConditionSessionStorageWarning condition should be set")
}

func TestSessionStorageWarningCleared(t *testing.T) {
	t.Parallel()

	name := "session-storage-ok-test"
	namespace := testNamespaceDefault
	replicas := int32(2)

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			ProxyPort: 8080,
			Replicas:  &replicas,
			SessionStorage: &mcpv1alpha1.SessionStorageConfig{
				Provider: mcpv1alpha1.SessionStorageProviderRedis,
				Address:  "redis:6379",
			},
		},
	}

	testScheme := createTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
	require.NoError(t, err)

	updated := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updated)
	require.NoError(t, err)

	var found bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == mcpv1alpha1.ConditionSessionStorageWarning {
			found = true
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			assert.Equal(t, mcpv1alpha1.ConditionReasonSessionStorageConfigured, cond.Reason)
		}
	}
	assert.True(t, found, "ConditionSessionStorageWarning condition should be set to False when Redis is configured")
}

func TestCategorizePodStatusExcludesTerminatingPods(t *testing.T) {
	t.Parallel()

	now := metav1.NewTime(time.Now())

	tests := []struct {
		name            string
		pod             corev1.Pod
		expectedRunning int
		expectedPending int
		expectedFailed  int
	}{
		{
			name: "terminating pod with running containers is excluded",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &now,
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					},
				},
			},
			expectedRunning: 0,
			expectedPending: 0,
			expectedFailed:  0,
		},
		{
			name: "non-terminating running pod is counted",
			pod: corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
					},
				},
			},
			expectedRunning: 1,
			expectedPending: 0,
			expectedFailed:  0,
		},
		{
			name: "terminating pending pod is excluded",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &now,
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			},
			expectedRunning: 0,
			expectedPending: 0,
			expectedFailed:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			running, pending, failed, _ := categorizePodStatus(tt.pod)
			assert.Equal(t, tt.expectedRunning, running, "running count")
			assert.Equal(t, tt.expectedPending, pending, "pending count")
			assert.Equal(t, tt.expectedFailed, failed, "failed count")
		})
	}
}

func TestUpdateMCPServerStatusExcludesTerminatingPods(t *testing.T) {
	t.Parallel()

	name := "terminating-pods-test"
	namespace := testNamespaceDefault
	now := metav1.NewTime(time.Now())

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image:     "test-image:latest",
			Transport: "sse",
			ProxyPort: 8080,
		},
	}

	testScheme := createTestScheme()

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(2),
			Selector: &metav1.LabelSelector{
				MatchLabels: labelsForMCPServer(name),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labelsForMCPServer(name),
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "mcp", Image: "test-image:latest"},
					},
				},
			},
		},
	}

	// 2 running pods + 1 terminating-but-ready pod (old replica during rollout)
	runningPod1 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pod-0", name),
			Namespace: namespace,
			Labels:    labelsForMCPServer(name),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "mcp", Image: "test-image:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	runningPod2 := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-pod-1", name),
			Namespace: namespace,
			Labels:    labelsForMCPServer(name),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "mcp", Image: "test-image:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}
	terminatingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              fmt.Sprintf("%s-pod-old", name),
			Namespace:         namespace,
			Labels:            labelsForMCPServer(name),
			DeletionTimestamp: &now,
			Finalizers:        []string{"test-finalizer"}, // required for fake client with DeletionTimestamp
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "mcp", Image: "test-image:latest"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer, deployment, runningPod1, runningPod2, terminatingPod).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	err := reconciler.updateMCPServerStatus(t.Context(), mcpServer)
	require.NoError(t, err)

	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, updatedMCPServer)
	require.NoError(t, err)

	assert.Equal(t, mcpv1alpha1.MCPServerPhaseReady, updatedMCPServer.Status.Phase)
	assert.Equal(t, int32(2), updatedMCPServer.Status.ReadyReplicas,
		"ReadyReplicas should exclude terminating pods")
}

func TestRateLimitConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		spec         mcpv1alpha1.MCPServerSpec
		expectStatus metav1.ConditionStatus
		expectReason string
	}{
		{
			name: "no-rate-limiting",
			spec: mcpv1alpha1.MCPServerSpec{
				Image:     "test-image:latest",
				Transport: "sse",
				ProxyPort: 8080,
			},
			expectStatus: metav1.ConditionTrue,
			expectReason: mcpv1alpha1.ConditionReasonRateLimitNotApplicable,
		},
		{
			name: "peruser-with-auth",
			spec: mcpv1alpha1.MCPServerSpec{
				Image:     "test-image:latest",
				Transport: "sse",
				ProxyPort: 8080,
				SessionStorage: &mcpv1alpha1.SessionStorageConfig{
					Provider: mcpv1alpha1.SessionStorageProviderRedis,
					Address:  "redis:6379",
				},
				OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "test-oidc", Audience: "test"},
				RateLimiting: &mcpv1alpha1.RateLimitConfig{
					PerUser: &mcpv1alpha1.RateLimitBucket{
						MaxTokens:    100,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
			expectStatus: metav1.ConditionTrue,
			expectReason: mcpv1alpha1.ConditionReasonRateLimitConfigValid,
		},
		{
			name: "peruser-without-auth",
			spec: mcpv1alpha1.MCPServerSpec{
				Image:     "test-image:latest",
				Transport: "sse",
				ProxyPort: 8080,
				SessionStorage: &mcpv1alpha1.SessionStorageConfig{
					Provider: mcpv1alpha1.SessionStorageProviderRedis,
					Address:  "redis:6379",
				},
				RateLimiting: &mcpv1alpha1.RateLimitConfig{
					PerUser: &mcpv1alpha1.RateLimitBucket{
						MaxTokens:    100,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
			expectStatus: metav1.ConditionFalse,
			expectReason: mcpv1alpha1.ConditionReasonRateLimitPerUserRequiresAuth,
		},
		{
			name: "per-tool-peruser-without-auth",
			spec: mcpv1alpha1.MCPServerSpec{
				Image:     "test-image:latest",
				Transport: "sse",
				ProxyPort: 8080,
				SessionStorage: &mcpv1alpha1.SessionStorageConfig{
					Provider: mcpv1alpha1.SessionStorageProviderRedis,
					Address:  "redis:6379",
				},
				RateLimiting: &mcpv1alpha1.RateLimitConfig{
					Tools: []mcpv1alpha1.ToolRateLimitConfig{
						{
							Name: "search",
							PerUser: &mcpv1alpha1.RateLimitBucket{
								MaxTokens:    10,
								RefillPeriod: metav1.Duration{Duration: time.Minute},
							},
						},
					},
				},
			},
			expectStatus: metav1.ConditionFalse,
			expectReason: mcpv1alpha1.ConditionReasonRateLimitPerUserRequiresAuth,
		},
		{
			name: "shared-only-no-auth",
			spec: mcpv1alpha1.MCPServerSpec{
				Image:     "test-image:latest",
				Transport: "sse",
				ProxyPort: 8080,
				SessionStorage: &mcpv1alpha1.SessionStorageConfig{
					Provider: mcpv1alpha1.SessionStorageProviderRedis,
					Address:  "redis:6379",
				},
				RateLimiting: &mcpv1alpha1.RateLimitConfig{
					Shared: &mcpv1alpha1.RateLimitBucket{
						MaxTokens:    1000,
						RefillPeriod: metav1.Duration{Duration: time.Minute},
					},
				},
			},
			expectStatus: metav1.ConditionTrue,
			expectReason: mcpv1alpha1.ConditionReasonRateLimitConfigValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			name := "rl-" + tt.name
			namespace := testNamespaceDefault

			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
				},
				Spec: tt.spec,
			}

			testScheme := createTestScheme()
			clientBuilder := fake.NewClientBuilder().
				WithScheme(testScheme).
				WithObjects(mcpServer).
				WithStatusSubresource(&mcpv1alpha1.MCPServer{})

			// Add referenced MCPOIDCConfig to fake client if spec references one
			if mcpServer.Spec.OIDCConfigRef != nil {
				oidcCfg := &mcpv1alpha1.MCPOIDCConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      mcpServer.Spec.OIDCConfigRef.Name,
						Namespace: namespace,
					},
					Spec: mcpv1alpha1.MCPOIDCConfigSpec{
						Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
							Issuer: "https://auth.example.com",
						},
					},
				}
				oidcCfg.Status.Conditions = []metav1.Condition{
					{
						Type:               mcpv1alpha1.ConditionTypeValid,
						Status:             metav1.ConditionTrue,
						Reason:             "Valid",
						LastTransitionTime: metav1.Now(),
					},
				}
				clientBuilder = clientBuilder.
					WithObjects(oidcCfg).
					WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{})
			}

			fakeClient := clientBuilder.Build()

			reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

			_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
			})
			require.NoError(t, err)

			updated := &mcpv1alpha1.MCPServer{}
			err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updated)
			require.NoError(t, err)

			var found bool
			for _, cond := range updated.Status.Conditions {
				if cond.Type == mcpv1alpha1.ConditionRateLimitConfigValid {
					found = true
					assert.Equal(t, tt.expectStatus, cond.Status)
					assert.Equal(t, tt.expectReason, cond.Reason)
				}
			}
			assert.True(t, found, "ConditionRateLimitConfigValid condition should be set")
		})
	}
}
