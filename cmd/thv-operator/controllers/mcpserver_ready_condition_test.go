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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// findReadyCondition returns the Ready condition from the MCPServer status, or nil if not found.
func findReadyCondition(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == mcpv1alpha1.ConditionTypeReady {
			return &conditions[i]
		}
	}
	return nil
}

func TestReadyConditionRunning(t *testing.T) {
	t.Parallel()

	name := "ready-running-test"
	namespace := testNamespaceDefault

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  namespace,
			Generation: 2,
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
			Replicas: int32Ptr(1),
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

	runningPod := &corev1.Pod{
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

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer, deployment, runningPod).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	err := reconciler.updateMCPServerStatus(t.Context(), mcpServer)
	require.NoError(t, err)

	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updatedMCPServer)
	require.NoError(t, err)

	cond := findReadyCondition(updatedMCPServer.Status.Conditions)
	require.NotNil(t, cond, "Ready condition should be set")
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, mcpv1alpha1.ConditionReasonReady, cond.Reason)
	assert.Equal(t, int64(2), cond.ObservedGeneration, "ObservedGeneration should match the MCPServer generation")
}

func TestReadyConditionStopped(t *testing.T) {
	t.Parallel()

	name := "ready-stopped-test"
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
						{Name: "mcp", Image: "test-image:latest"},
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

	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updatedMCPServer)
	require.NoError(t, err)

	cond := findReadyCondition(updatedMCPServer.Status.Conditions)
	require.NotNil(t, cond, "Ready condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, mcpv1alpha1.ConditionReasonNotReady, cond.Reason)
	assert.Contains(t, cond.Message, "stopped", "Message should indicate the server is stopped")
}

func TestReadyConditionPending(t *testing.T) {
	t.Parallel()

	name := "ready-pending-test"
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

	// Deployment with 1 replica but no pods exist
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
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

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer, deployment).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	err := reconciler.updateMCPServerStatus(t.Context(), mcpServer)
	require.NoError(t, err)

	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updatedMCPServer)
	require.NoError(t, err)

	cond := findReadyCondition(updatedMCPServer.Status.Conditions)
	require.NotNil(t, cond, "Ready condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, mcpv1alpha1.ConditionReasonNotReady, cond.Reason)
	assert.Contains(t, cond.Message, "being created", "Message should indicate the server is being created")
}

func TestReadyConditionFailed(t *testing.T) {
	t.Parallel()

	name := "ready-failed-test"
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

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
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

	failedPod := &corev1.Pod{
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
			Phase: corev1.PodFailed,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer, deployment, failedPod).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	err := reconciler.updateMCPServerStatus(t.Context(), mcpServer)
	require.NoError(t, err)

	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updatedMCPServer)
	require.NoError(t, err)

	cond := findReadyCondition(updatedMCPServer.Status.Conditions)
	require.NotNil(t, cond, "Ready condition should be set")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, mcpv1alpha1.ConditionReasonNotReady, cond.Reason)
	assert.Contains(t, cond.Message, "failed", "Message should indicate the server has failed")
}

func TestReadyConditionInvalidPodTemplate(t *testing.T) {
	t.Parallel()

	name := "ready-invalid-podtemplate"
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
			PodTemplateSpec: &runtime.RawExtension{
				// Valid JSON but invalid PodTemplateSpec structure
				Raw: []byte(`{"spec": {"containers": "invalid-not-an-array"}}`),
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

	// First reconcile adds the finalizer
	_, err := reconciler.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
	require.NoError(t, err)

	// Second reconcile performs validation (finalizer is now present)
	_, err = reconciler.Reconcile(t.Context(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: namespace},
	})
	require.NoError(t, err)

	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updatedMCPServer)
	require.NoError(t, err)

	cond := findReadyCondition(updatedMCPServer.Status.Conditions)
	require.NotNil(t, cond, "Ready condition should be set when PodTemplateSpec is invalid")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, mcpv1alpha1.ConditionReasonNotReady, cond.Reason)
}

func TestReadyConditionTransitionsToTrue(t *testing.T) {
	t.Parallel()

	name := "ready-transition-test"
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

	// Deployment with 1 replica but no pods initially
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
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

	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(mcpServer, deployment).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, testScheme, kubernetes.PlatformKubernetes)

	// First call: no pods exist, should be Ready=False
	err := reconciler.updateMCPServerStatus(t.Context(), mcpServer)
	require.NoError(t, err)

	updatedMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updatedMCPServer)
	require.NoError(t, err)

	cond := findReadyCondition(updatedMCPServer.Status.Conditions)
	require.NotNil(t, cond, "Ready condition should be set after first update")
	assert.Equal(t, metav1.ConditionFalse, cond.Status, "Ready should be False when no pods exist")

	// Add a running pod to the fake client
	runningPod := &corev1.Pod{
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
	err = fakeClient.Create(t.Context(), runningPod)
	require.NoError(t, err)

	// Re-fetch the MCPServer to get the latest version (avoids conflict on status update)
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, updatedMCPServer)
	require.NoError(t, err)

	// Second call: running pod exists, should transition to Ready=True
	err = reconciler.updateMCPServerStatus(t.Context(), updatedMCPServer)
	require.NoError(t, err)

	finalMCPServer := &mcpv1alpha1.MCPServer{}
	err = fakeClient.Get(t.Context(), types.NamespacedName{Name: name, Namespace: namespace}, finalMCPServer)
	require.NoError(t, err)

	cond = findReadyCondition(finalMCPServer.Status.Conditions)
	require.NotNil(t, cond, "Ready condition should be set after second update")
	assert.Equal(t, metav1.ConditionTrue, cond.Status, "Ready should transition to True when pods are running")
	assert.Equal(t, mcpv1alpha1.ConditionReasonReady, cond.Reason)
}
