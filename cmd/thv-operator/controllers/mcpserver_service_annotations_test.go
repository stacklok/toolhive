// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

// TestMCPServerReconcile_PreservesExternalServiceAnnotations is a regression test for
// #5730: when a genuine operator-owned change triggers the inline Service update in
// Reconcile, annotations written by an external controller (e.g. GKE NEG) must be
// merged, not stripped, otherwise the operator hot-loops Update against the concurrent
// writer.
func TestMCPServerReconcile_PreservesExternalServiceAnnotations(t *testing.T) {
	t.Parallel()

	const name = "ext-annot-server"
	namespace := testNamespaceDefault

	mcpServer := v1beta1test.NewMCPServer(name, namespace, v1beta1test.WithTransport("sse"))

	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		Build()
	r := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
	// Drive to steady state; successive passes create the Deployment and Service.
	for range 6 {
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
	}

	svcName := ctrlutil.CreateProxyServiceName(name)
	svcKey := types.NamespacedName{Name: svcName, Namespace: namespace}

	// An external controller (GKE NEG) writes an annotation, and an operator-owned field
	// drifts (empty session affinity) so serviceNeedsUpdate fires on the next reconcile.
	svc := &corev1.Service{}
	require.NoError(t, fakeClient.Get(t.Context(), svcKey, svc))
	if svc.Annotations == nil {
		svc.Annotations = map[string]string{}
	}
	svc.Annotations["cloud.google.com/neg-status"] = `{"network_endpoint_groups":{"8080":"k8s1-abc"}}`
	svc.Spec.SessionAffinity = ""
	require.NoError(t, fakeClient.Update(t.Context(), svc))

	// Reconcile again; the inline Service update path merges rather than replaces.
	for range 3 {
		_, err := r.Reconcile(t.Context(), req)
		require.NoError(t, err)
	}

	updated := &corev1.Service{}
	require.NoError(t, fakeClient.Get(t.Context(), svcKey, updated))
	// External annotation preserved...
	assert.Equal(t, `{"network_endpoint_groups":{"8080":"k8s1-abc"}}`,
		updated.Annotations["cloud.google.com/neg-status"],
		"external annotation must survive the operator Service update")
	// ...and the operator-owned field corrected.
	assert.Equal(t, corev1.ServiceAffinityClientIP, updated.Spec.SessionAffinity,
		"operator-owned session affinity should be reconciled back to the default")
}
