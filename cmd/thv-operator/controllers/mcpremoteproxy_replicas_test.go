// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

// replicasTestScheme registers the types the replica tests seed into the fake
// client: the CRDs plus core and apps (Deployments).
func replicasTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	return scheme
}

func replicasTestProxy(replicas *int32) *mcpv1beta1.MCPRemoteProxy {
	return &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "replicas-proxy", Namespace: "default"},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.example.com",
			ProxyPort: 8080,
			Replicas:  replicas,
		},
	}
}

// TestDeploymentForMCPRemoteProxyReplicas verifies spec.replicas flows into
// the generated Deployment: nil stays nil (apiserver defaults to 1, and an
// HPA can manage the count thereafter), non-nil is set verbatim.
func TestDeploymentForMCPRemoteProxyReplicas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		replicas *int32
	}{
		{name: "nil replicas leaves Deployment.Spec.Replicas unset", replicas: nil},
		{name: "replicas 3 is set on the Deployment", replicas: int32Ptr(3)},
		{name: "replicas 0 is set on the Deployment", replicas: int32Ptr(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := replicasTestScheme(t)
			r := &MCPRemoteProxyReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
				Scheme: scheme,
			}

			dep := r.deploymentForMCPRemoteProxy(context.Background(), replicasTestProxy(tt.replicas), "test-checksum")
			require.NotNil(t, dep)

			if tt.replicas == nil {
				assert.Nil(t, dep.Spec.Replicas)
			} else {
				require.NotNil(t, dep.Spec.Replicas)
				assert.Equal(t, *tt.replicas, *dep.Spec.Replicas)
			}
		})
	}
}

// TestMCPRemoteProxyDeploymentNeedsUpdateReplicas pins the drift semantics:
// non-nil spec.replicas is operator-owned and any divergence triggers an
// update; nil spec.replicas is hands-off so external scaling (HPA) is never
// fought by the reconciler.
func TestMCPRemoteProxyDeploymentNeedsUpdateReplicas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		specReplicas *int32
		liveReplicas *int32
		wantUpdate   bool
	}{
		{name: "nil spec ignores externally scaled count", specReplicas: nil, liveReplicas: int32Ptr(5), wantUpdate: false},
		{name: "set spec reconciles drifted count", specReplicas: int32Ptr(3), liveReplicas: int32Ptr(1), wantUpdate: true},
		{name: "set spec matches live count", specReplicas: int32Ptr(3), liveReplicas: int32Ptr(3), wantUpdate: false},
		{name: "set spec with nil live count", specReplicas: int32Ptr(2), liveReplicas: nil, wantUpdate: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := replicasTestScheme(t)
			r := &MCPRemoteProxyReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
				Scheme: scheme,
			}

			proxy := replicasTestProxy(tt.specReplicas)
			// Generate the desired deployment so all non-replica comparisons
			// in deploymentNeedsUpdate see identical state.
			deployment := r.deploymentForMCPRemoteProxy(context.Background(), proxy, "test-checksum")
			require.NotNil(t, deployment)
			deployment.Spec.Replicas = tt.liveReplicas

			assert.Equal(t, tt.wantUpdate,
				r.deploymentNeedsUpdate(context.Background(), deployment, proxy, "test-checksum"))
		})
	}
}

// TestMCPRemoteProxyEnsureDeploymentReplicaSync drives ensureDeployment
// against a fake cluster to verify the update path end to end: a live count
// scaled by an external controller survives when spec.replicas is nil, and is
// overwritten when spec.replicas is set.
func TestMCPRemoteProxyEnsureDeploymentReplicaSync(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		specReplicas *int32
		liveReplicas int32
		wantReplicas int32
	}{
		{name: "nil spec preserves HPA-scaled count", specReplicas: nil, liveReplicas: 5, wantReplicas: 5},
		{name: "set spec overrides drifted count", specReplicas: int32Ptr(3), liveReplicas: 5, wantReplicas: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := replicasTestScheme(t)
			proxy := replicasTestProxy(tt.specReplicas)

			// Seed the RunConfig ConfigMap so getRunConfigChecksum resolves the
			// same checksum the test passes to the deployment generator.
			runConfigCM := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:        proxy.Name + "-runconfig",
					Namespace:   proxy.Namespace,
					Annotations: map[string]string{checksum.ContentChecksumAnnotation: "test-checksum"},
				},
			}

			seedReconciler := &MCPRemoteProxyReconciler{
				Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
				Scheme: scheme,
			}
			liveDeployment := seedReconciler.deploymentForMCPRemoteProxy(context.Background(), proxy, "test-checksum")
			require.NotNil(t, liveDeployment)
			liveDeployment.Spec.Replicas = int32Ptr(tt.liveReplicas)
			// Strip the generated owner reference: the fake client rejects
			// owner refs to objects it has not assigned a UID.
			liveDeployment.OwnerReferences = nil

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(proxy, runConfigCM, liveDeployment).
				Build()
			r := &MCPRemoteProxyReconciler{Client: fakeClient, Scheme: scheme}

			_, err := r.ensureDeployment(context.Background(), proxy)
			require.NoError(t, err)

			got := &appsv1.Deployment{}
			require.NoError(t, fakeClient.Get(context.Background(),
				types.NamespacedName{Name: proxy.Name, Namespace: proxy.Namespace}, got))
			require.NotNil(t, got.Spec.Replicas)
			assert.Equal(t, tt.wantReplicas, *got.Spec.Replicas)
		})
	}
}

// TestValidateSessionStorageForReplicasRemoteProxy mirrors the MCPServer and
// VirtualMCPServer validators: the SessionStorageWarning condition is True
// only when replicas > 1 without Redis-backed session storage.
func TestValidateSessionStorageForReplicasRemoteProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		replicas   *int32
		storage    *mcpv1beta1.SessionStorageConfig
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name:       "nil replicas is not applicable",
			replicas:   nil,
			wantStatus: metav1.ConditionFalse,
			wantReason: mcpv1beta1.ConditionReasonSessionStorageNotApplicable,
		},
		{
			name:       "single replica is not applicable",
			replicas:   int32Ptr(1),
			wantStatus: metav1.ConditionFalse,
			wantReason: mcpv1beta1.ConditionReasonSessionStorageNotApplicable,
		},
		{
			name:       "multiple replicas without session storage warns",
			replicas:   int32Ptr(3),
			wantStatus: metav1.ConditionTrue,
			wantReason: mcpv1beta1.ConditionReasonSessionStorageMissing,
		},
		{
			name:       "multiple replicas with memory provider warns",
			replicas:   int32Ptr(3),
			storage:    &mcpv1beta1.SessionStorageConfig{Provider: "memory"},
			wantStatus: metav1.ConditionTrue,
			wantReason: mcpv1beta1.ConditionReasonSessionStorageMissing,
		},
		{
			name:     "multiple replicas with redis provider is configured",
			replicas: int32Ptr(3),
			storage: &mcpv1beta1.SessionStorageConfig{
				Provider: mcpv1beta1.SessionStorageProviderRedis,
				Address:  "redis:6379",
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: mcpv1beta1.ConditionReasonSessionStorageConfigured,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			proxy := replicasTestProxy(tt.replicas)
			proxy.Spec.SessionStorage = tt.storage

			r := &MCPRemoteProxyReconciler{}
			r.validateSessionStorageForReplicas(proxy)

			cond := meta.FindStatusCondition(proxy.Status.Conditions, mcpv1beta1.ConditionSessionStorageWarning)
			require.NotNil(t, cond, "SessionStorageWarning condition must always be set")
			assert.Equal(t, tt.wantStatus, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
		})
	}
}
