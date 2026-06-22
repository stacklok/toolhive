// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestMCPServerReconciler_handleOIDCConfig(t *testing.T) {
	t.Parallel()

	// validOIDCCondition is a helper to build a Ready=True condition slice.
	validOIDCCondition := []metav1.Condition{{
		Type: mcpv1beta1.ConditionTypeOIDCConfigValid, Status: metav1.ConditionTrue, Reason: mcpv1beta1.ConditionReasonOIDCConfigValid,
	}}

	tests := []struct {
		name                  string
		mcpServer             *mcpv1beta1.MCPServer
		oidcConfig            *mcpv1beta1.MCPOIDCConfig
		expectError           bool
		expectErrorContains   string
		expectHash            string
		expectHashCleared     bool
		expectConditionStatus *metav1.ConditionStatus
		expectConditionReason string
	}{
		{
			name: "no ref clears previously stored hash",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec:       mcpv1beta1.MCPServerSpec{Image: "img"},
				Status:     mcpv1beta1.MCPServerStatus{OIDCConfigHash: "old"},
			},
			expectHashCleared: true,
		},
		{
			name: "referenced config not found sets NotFound condition",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:         "img",
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: "missing", Audience: "aud"},
				},
			},
			expectError:           true,
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1beta1.ConditionReasonOIDCConfigRefNotFound,
		},
		{
			name: "config with Valid=False sets NotValid condition",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:         "img",
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: "bad", Audience: "aud"},
				},
			},
			oidcConfig: &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "default"},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://x"},
				},
				Status: mcpv1beta1.MCPOIDCConfigStatus{
					Conditions: []metav1.Condition{{
						Type: mcpv1beta1.ConditionTypeOIDCConfigValid, Status: metav1.ConditionFalse, Reason: mcpv1beta1.ConditionReasonOIDCConfigInvalid,
						Message: "missing fields",
					}},
				},
			},
			expectError:           true,
			expectErrorContains:   "not valid",
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1beta1.ConditionReasonOIDCConfigRefNotValid,
		},
		{
			name: "valid config sets hash, condition, and referencing server",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:         "img",
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: "ok", Audience: "aud"},
				},
			},
			oidcConfig: &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "default"},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://x", ClientID: "c"},
				},
				Status: mcpv1beta1.MCPOIDCConfigStatus{
					ConfigHash: "hash-123",
					Conditions: validOIDCCondition,
				},
			},
			expectHash:            "hash-123",
			expectConditionStatus: conditionStatusPtr(metav1.ConditionTrue),
			expectConditionReason: mcpv1beta1.ConditionReasonOIDCConfigRefValid,
		},
		{
			name: "detects config hash change",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:         "img",
					OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: "cfg", Audience: "aud"},
				},
				Status: mcpv1beta1.MCPServerStatus{OIDCConfigHash: "old-hash"},
			},
			oidcConfig: &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1beta1.KubernetesServiceAccountOIDCConfig{
						Issuer: "https://kubernetes.default.svc",
					},
				},
				Status: mcpv1beta1.MCPOIDCConfigStatus{
					ConfigHash: "new-hash",
					Conditions: validOIDCCondition,
				},
			},
			expectHash:            "new-hash",
			expectConditionStatus: conditionStatusPtr(metav1.ConditionTrue),
			expectConditionReason: mcpv1beta1.ConditionReasonOIDCConfigRefValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := testutil.NewScheme(t)

			objs := []runtime.Object{tt.mcpServer}
			if tt.oidcConfig != nil {
				objs = append(objs, tt.oidcConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(
					&mcpv1beta1.MCPServer{},
					&mcpv1beta1.MCPOIDCConfig{},
				).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			err := reconciler.handleOIDCConfig(ctx, tt.mcpServer)

			if tt.expectError {
				assert.Error(t, err)
				if tt.expectErrorContains != "" {
					assert.Contains(t, err.Error(), tt.expectErrorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			if tt.expectHash != "" {
				assert.Equal(t, tt.expectHash, tt.mcpServer.Status.OIDCConfigHash)
			}
			if tt.expectHashCleared {
				assert.Empty(t, tt.mcpServer.Status.OIDCConfigHash)
			}

			if tt.expectConditionStatus != nil {
				var found bool
				for _, cond := range tt.mcpServer.Status.Conditions {
					if cond.Type == mcpv1beta1.ConditionOIDCConfigRefValidated {
						found = true
						assert.Equal(t, string(*tt.expectConditionStatus), string(cond.Status))
						assert.Equal(t, tt.expectConditionReason, cond.Reason)
						break
					}
				}
				assert.True(t, found, "expected %s condition", mcpv1beta1.ConditionOIDCConfigRefValidated)
			}
		})
	}
}

// TestMCPServerReconciler_handleOIDCConfig_ConditionPersistedOnRecovery verifies that the
// OIDCConfigRefValidated condition is actually persisted to the API server (not just set
// in memory) when recovering from a transient error with an unchanged config hash (#4511).
func TestMCPServerReconciler_handleOIDCConfig_ConditionPersistedOnRecovery(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	validOIDCCondition := []metav1.Condition{{
		Type: mcpv1beta1.ConditionTypeOIDCConfigValid, Status: metav1.ConditionTrue, Reason: mcpv1beta1.ConditionReasonOIDCConfigValid,
	}}

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:         "img",
			OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: "cfg", Audience: "aud"},
		},
		Status: mcpv1beta1.MCPServerStatus{
			// Hash is already current — only the condition is stale (simulating recovery).
			OIDCConfigHash: "same-hash",
			Conditions: []metav1.Condition{{
				Type:   mcpv1beta1.ConditionOIDCConfigRefValidated,
				Status: metav1.ConditionFalse,
				Reason: mcpv1beta1.ConditionReasonOIDCConfigRefNotFound,
			}},
		},
	}
	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://x", ClientID: "c"},
		},
		Status: mcpv1beta1.MCPOIDCConfigStatus{
			ConfigHash: "same-hash",
			Conditions: validOIDCCondition,
		},
	}

	scheme := testutil.NewScheme(t)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(mcpServer, oidcConfig).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}, &mcpv1beta1.MCPOIDCConfig{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)
	require.NoError(t, reconciler.handleOIDCConfig(ctx, mcpServer))

	// Re-read from the fake client to verify the condition was actually persisted,
	// not just set in the in-memory Go struct.
	var persisted mcpv1beta1.MCPServer
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(mcpServer), &persisted))

	cond := meta.FindStatusCondition(persisted.Status.Conditions, mcpv1beta1.ConditionOIDCConfigRefValidated)
	require.NotNil(t, cond, "OIDCConfigRefValidated condition must be persisted")
	assert.Equal(t, metav1.ConditionTrue, cond.Status, "condition should be True after recovery")
	assert.Equal(t, mcpv1beta1.ConditionReasonOIDCConfigRefValid, cond.Reason)
	assert.Equal(t, "same-hash", persisted.Status.OIDCConfigHash, "hash should remain unchanged")
}

func TestMCPOIDCConfigReconciler_handleDeletion_BlocksWhenReferenced(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	now := metav1.Now()
	cfg := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "default",
			Finalizers: []string{OIDCConfigFinalizerName}, DeletionTimestamp: &now,
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://x"},
		},
	}
	server := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "referencing", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:         "img",
			OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: "cfg", Audience: "aud"},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cfg, server).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).Build()
	r := &MCPOIDCConfigReconciler{Client: fc, Scheme: scheme}

	result, err := r.handleDeletion(ctx, cfg)
	require.NoError(t, err)

	assert.Greater(t, result.RequeueAfter, time.Duration(0), "should requeue while referenced")
	assert.Contains(t, cfg.Finalizers, OIDCConfigFinalizerName, "finalizer must remain")

	// The DeletionBlocked condition is written through MutateAndPatchStatus;
	// re-fetch to confirm it (and the referencing-workload bookkeeping) was
	// persisted rather than only mutated in memory.
	var after mcpv1beta1.MCPOIDCConfig
	require.NoError(t, fc.Get(ctx, client.ObjectKeyFromObject(cfg), &after))
	blocked := meta.FindStatusCondition(after.Status.Conditions, mcpv1beta1.ConditionTypeDeletionBlocked)
	require.NotNil(t, blocked, "DeletionBlocked condition must be set while referenced")
	assert.Equal(t, metav1.ConditionTrue, blocked.Status)
	assert.Equal(t, "ReferencedByWorkloads", blocked.Reason)
	assert.EqualValues(t, 1, after.Status.ReferenceCount, "referencing workload must be recorded")
}

func TestMCPOIDCConfigReconciler_handleDeletion_AllowsWhenNotReferenced(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	now := metav1.Now()
	cfg := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "default",
			Finalizers: []string{OIDCConfigFinalizerName}, DeletionTimestamp: &now,
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://x"},
		},
	}
	// Unrelated server -- does NOT reference this config
	unrelated := v1beta1test.NewMCPServer("other", "default", v1beta1test.WithImage("img"))

	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cfg, unrelated).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).Build()
	r := &MCPOIDCConfigReconciler{Client: fc, Scheme: scheme}

	result, err := r.handleDeletion(ctx, cfg)
	require.NoError(t, err)

	assert.Equal(t, time.Duration(0), result.RequeueAfter, "should not requeue")
	assert.NotContains(t, cfg.Finalizers, OIDCConfigFinalizerName, "finalizer should be removed")
}

func TestMCPOIDCConfigReconciler_handleDeletion_IgnoresCrossNamespaceRef(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	now := metav1.Now()
	cfg := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cfg", Namespace: "ns-a",
			Finalizers: []string{OIDCConfigFinalizerName}, DeletionTimestamp: &now,
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://x"},
		},
	}
	// Server in a DIFFERENT namespace referencing same config name
	crossNS := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns-b"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:         "img",
			OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: "cfg", Audience: "aud"},
		},
	}

	fc := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cfg, crossNS).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).Build()
	r := &MCPOIDCConfigReconciler{Client: fc, Scheme: scheme}

	result, err := r.handleDeletion(ctx, cfg)
	require.NoError(t, err)

	assert.Equal(t, time.Duration(0), result.RequeueAfter)
	assert.NotContains(t, cfg.Finalizers, OIDCConfigFinalizerName,
		"cross-namespace refs should not block deletion")
}

// conditionStatusPtr returns a pointer to a metav1.ConditionStatus value.
func conditionStatusPtr(s metav1.ConditionStatus) *metav1.ConditionStatus {
	return &s
}

// TestMCPServerReconciler_handleOIDCConfig_DoesNotWriteConfigStatus guards the
// trust boundary consolidated in #5511: the MCPServer controller may read the
// referenced MCPOIDCConfig but must never write its status. The MCPOIDCConfig
// controller is the sole owner of that status (conditions and
// ReferencingWorkloads). This unit test is the actual enforcement of that
// boundary — RBAC does not enforce it, because the operator runs as a single
// ServiceAccount whose aggregated role still grants mcpoidcconfigs/status write
// for the MCPOIDCConfig controller's own use. A future reintroduction of a
// cross-controller status write would flip the ResourceVersion and fail here.
func TestMCPServerReconciler_handleOIDCConfig_DoesNotWriteConfigStatus(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	// Seed a config whose status carries a condition and a ReferencingWorkloads
	// entry owned by the MCPOIDCConfig controller — neither must be touched.
	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://x", ClientID: "c"},
		},
		Status: mcpv1beta1.MCPOIDCConfigStatus{
			ConfigHash: "hash-123",
			Conditions: []metav1.Condition{
				{
					Type: mcpv1beta1.ConditionTypeOIDCConfigValid, Status: metav1.ConditionTrue,
					Reason: mcpv1beta1.ConditionReasonOIDCConfigValid,
				},
				{
					Type: "ForeignControllerSays", Status: metav1.ConditionTrue,
					Reason: "ExternallySet", LastTransitionTime: metav1.Now(),
				},
			},
			ReferencingWorkloads: []mcpv1beta1.WorkloadReference{{Kind: "MCPServer", Name: "someone-else"}},
			ReferenceCount:       1,
		},
	}
	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"},
		Spec: mcpv1beta1.MCPServerSpec{
			Image:         "img",
			OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{Name: "cfg", Audience: "aud"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig, mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}, &mcpv1beta1.MCPOIDCConfig{}).
		Build()
	r := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	var before mcpv1beta1.MCPOIDCConfig
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(oidcConfig), &before))

	require.NoError(t, r.handleOIDCConfig(ctx, mcpServer))

	var after mcpv1beta1.MCPOIDCConfig
	require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(oidcConfig), &after))

	assert.Equal(t, before.ResourceVersion, after.ResourceVersion,
		"MCPServer reconcile must not write the MCPOIDCConfig — its status is owned by the MCPOIDCConfig controller")
	assert.Equal(t, before.Status.ReferencingWorkloads, after.Status.ReferencingWorkloads,
		"MCPServer reconcile must not touch the config's ReferencingWorkloads")
	assert.NotNil(t, meta.FindStatusCondition(after.Status.Conditions, "ForeignControllerSays"),
		"config-owned conditions must remain untouched")
}
