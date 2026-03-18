// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"fmt"
	"testing"

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

	assert.Equal(t, mcpv1alpha1.MCPServerPhaseRunning, updatedMCPServer.Status.Phase)
	assert.Equal(t, int32(2), updatedMCPServer.Status.ReadyReplicas,
		"ReadyReplicas should match the number of running pods")
}

func TestDefaultCreationHasOneReplica(t *testing.T) {
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

	// Verify the deployment was created with 1 replica
	deployment := &appsv1.Deployment{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, deployment)
	require.NoError(t, err)
	assert.Equal(t, int32(1), *deployment.Spec.Replicas,
		"Default deployment should start with 1 replica")
}
