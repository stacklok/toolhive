// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
)

func TestEnsureWorkloadStatefulSet_BouncesWhenMissingAndProxyReady(t *testing.T) {
	t.Parallel()
	scheme := testutil.NewScheme(t)

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "fetch", Namespace: "default", UID: "uid-fetch"},
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "fetch", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer, deployment).Build()
	r := &MCPServerReconciler{Client: k8sClient, Scheme: scheme}

	result, err := r.ensureWorkloadStatefulSet(context.Background(), mcpServer, deployment)
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, result.RequeueAfter)

	updated := &appsv1.Deployment{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "fetch", Namespace: "default"}, updated))
	require.NotEmpty(t, updated.Spec.Template.Annotations[RestartedAtAnnotationKey],
		"missing StatefulSet with a ready proxy must bounce the Deployment")
}

func TestEnsureWorkloadStatefulSet_DoesNotBounceBeforeProxyIsAvailable(t *testing.T) {
	t.Parallel()
	scheme := testutil.NewScheme(t)

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "fetch", Namespace: "default"},
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "fetch", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 0},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer, deployment).Build()
	r := &MCPServerReconciler{Client: k8sClient, Scheme: scheme}

	_, err := r.ensureWorkloadStatefulSet(context.Background(), mcpServer, deployment)
	require.NoError(t, err)

	updated := &appsv1.Deployment{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "fetch", Namespace: "default"}, updated))
	assert.Empty(t, updated.Spec.Template.Annotations[RestartedAtAnnotationKey],
		"first boot must not bounce the proxy before it has created the StatefulSet")
}

func TestEnsureWorkloadStatefulSet_AdoptsExisting(t *testing.T) {
	t.Parallel()
	scheme := testutil.NewScheme(t)

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "fetch", Namespace: "default", UID: "uid-fetch"},
	}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fetch",
			Namespace: "default",
			Labels:    map[string]string{"toolhive": "true", "toolhive-name": "fetch"},
		},
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "fetch", Namespace: "default"},
		Status:     appsv1.DeploymentStatus{AvailableReplicas: 1},
	}

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mcpServer, deployment, sts).Build()
	r := &MCPServerReconciler{Client: k8sClient, Scheme: scheme}

	result, err := r.ensureWorkloadStatefulSet(context.Background(), mcpServer, deployment)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter)

	updated := &appsv1.StatefulSet{}
	require.NoError(t, k8sClient.Get(context.Background(), types.NamespacedName{Name: "fetch", Namespace: "default"}, updated))
	require.Len(t, updated.OwnerReferences, 1)
	assert.Equal(t, mcpServer.UID, updated.OwnerReferences[0].UID)
	assert.Equal(t, ptr.To(true), updated.OwnerReferences[0].Controller)
}

func TestMapStatefulSetToMCPServer(t *testing.T) {
	t.Parallel()
	r := &MCPServerReconciler{}
	reqs := r.mapStatefulSetToMCPServer(context.Background(), &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fetch",
			Namespace: "tools",
			Labels:    map[string]string{"toolhive-name": "fetch"},
		},
	})
	require.Len(t, reqs, 1)
	assert.Equal(t, "fetch", reqs[0].Name)
	assert.Equal(t, "tools", reqs[0].Namespace)
}

func TestRecentlyBounced(t *testing.T) {
	t.Parallel()
	assert.False(t, recentlyBounced(&appsv1.Deployment{}))

	fresh := &appsv1.Deployment{}
	fresh.Spec.Template.Annotations = map[string]string{
		RestartedAtAnnotationKey: time.Now().UTC().Format(time.RFC3339),
	}
	assert.True(t, recentlyBounced(fresh))

	stale := &appsv1.Deployment{}
	stale.Spec.Template.Annotations = map[string]string{
		RestartedAtAnnotationKey: time.Now().UTC().Add(-3 * time.Minute).Format(time.RFC3339),
	}
	assert.False(t, recentlyBounced(stale))
}
