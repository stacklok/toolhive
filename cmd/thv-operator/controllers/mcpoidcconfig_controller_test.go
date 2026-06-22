// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
)

func TestMCPOIDCConfigReconciler_calculateConfigHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec mcpv1beta1.MCPOIDCConfigSpec
	}{
		{
			name: "kubernetesServiceAccount spec",
			spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type: mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount,
				KubernetesServiceAccount: &mcpv1beta1.KubernetesServiceAccountOIDCConfig{
					Issuer: "https://kubernetes.default.svc",
				},
			},
		},
		{
			name: "inline spec",
			spec: mcpv1beta1.MCPOIDCConfigSpec{
				Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1beta1.InlineOIDCSharedConfig{
					Issuer:   "https://accounts.google.com",
					ClientID: "test-client",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &MCPOIDCConfigReconciler{}

			hash1 := r.calculateConfigHash(tt.spec)
			hash2 := r.calculateConfigHash(tt.spec)

			assert.Equal(t, hash1, hash2, "Hash should be consistent for same spec")
			assert.NotEmpty(t, hash1, "Hash should not be empty")
		})
	}

	t.Run("different specs produce different hashes", func(t *testing.T) {
		t.Parallel()
		r := &MCPOIDCConfigReconciler{}
		spec1 := mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "client1",
			},
		}
		spec2 := mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "client2",
			},
		}

		hash1 := r.calculateConfigHash(spec1)
		hash2 := r.calculateConfigHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different specs should produce different hashes")
	})
}

func TestMCPOIDCConfigReconciler_ReconcileNotFound(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	// Empty client — no objects exist
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &MCPOIDCConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent",
			Namespace: "default",
		},
	}

	result, err := r.Reconcile(ctx, req)
	assert.NoError(t, err, "Reconciling a missing resource should not return error")
	assert.Equal(t, time.Duration(0), result.RequeueAfter, "Should not requeue")
}

func TestMCPOIDCConfigReconciler_SteadyStateNoOp(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "test-client",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()

	r := &MCPOIDCConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      oidcConfig.Name,
			Namespace: oidcConfig.Namespace,
		},
	}

	// First reconcile: add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	// Second reconcile: set hash and condition
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var afterInitial mcpv1beta1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &afterInitial)
	require.NoError(t, err)
	initialHash := afterInitial.Status.ConfigHash
	initialRV := afterInitial.ResourceVersion

	// Third reconcile with no changes: should be a no-op
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	var afterSteady mcpv1beta1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &afterSteady)
	require.NoError(t, err)
	assert.Equal(t, initialHash, afterSteady.Status.ConfigHash, "Hash should not change")
	assert.Equal(t, initialRV, afterSteady.ResourceVersion, "ResourceVersion should not change (no writes)")
}

func TestMCPOIDCConfigReconciler_ValidationRecovery(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	// Start with invalid config: type=inline but no inline config
	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "recovery-config",
			Namespace:  "default",
			Finalizers: []string{OIDCConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			// Missing Inline config — invalid
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()

	r := &MCPOIDCConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      oidcConfig.Name,
			Namespace: oidcConfig.Namespace,
		},
	}

	// Reconcile invalid config — should set Ready=False
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var invalidConfig mcpv1beta1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &invalidConfig)
	require.NoError(t, err)

	var foundFalse bool
	for _, cond := range invalidConfig.Status.Conditions {
		if cond.Type == mcpv1beta1.ConditionTypeOIDCConfigValid {
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			foundFalse = true
		}
	}
	require.True(t, foundFalse, "Should have Ready=False condition")
	assert.Empty(t, invalidConfig.Status.ConfigHash, "Hash should not be set for invalid config")

	// Fix the config by adding the inline spec
	invalidConfig.Spec.Inline = &mcpv1beta1.InlineOIDCSharedConfig{
		Issuer:   "https://accounts.google.com",
		ClientID: "test-client",
	}
	invalidConfig.Generation = 2
	err = fakeClient.Update(ctx, &invalidConfig)
	require.NoError(t, err)

	// Reconcile again — should set Ready=True and compute hash
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var recoveredConfig mcpv1beta1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &recoveredConfig)
	require.NoError(t, err)

	var foundTrue bool
	for _, cond := range recoveredConfig.Status.Conditions {
		if cond.Type == mcpv1beta1.ConditionTypeOIDCConfigValid {
			assert.Equal(t, metav1.ConditionTrue, cond.Status, "Valid condition should recover to True")
			assert.Equal(t, mcpv1beta1.ConditionReasonOIDCConfigValid, cond.Reason)
			foundTrue = true
		}
	}
	assert.True(t, foundTrue, "Should have Ready=True condition after fix")
	assert.NotEmpty(t, recoveredConfig.Status.ConfigHash, "Hash should be set after recovery")
}

func TestMCPOIDCConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		oidcConfig             *mcpv1beta1.MCPOIDCConfig
		expectFinalizerRemoved bool
	}{
		{
			name: "delete config removes finalizer",
			oidcConfig: &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{OIDCConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
					},
				},
			},
			expectFinalizerRemoved: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := testutil.NewScheme(t)

			objs := []client.Object{tt.oidcConfig}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			r := &MCPOIDCConfigReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			result, err := r.handleDeletion(ctx, tt.oidcConfig)

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(0), result.RequeueAfter)

			if tt.expectFinalizerRemoved {
				assert.NotContains(t, tt.oidcConfig.Finalizers, OIDCConfigFinalizerName,
					"Finalizer should be removed")
			}
		})
	}
}

func TestMCPOIDCConfigReconciler_ConfigChangeTriggersHashUpdate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "test-client",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()

	r := &MCPOIDCConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      oidcConfig.Name,
			Namespace: oidcConfig.Namespace,
		},
	}

	// First reconciliation - add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0), "Should requeue after adding finalizer")

	// Second reconciliation - calculate hash
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	// Get updated config and check hash was set
	var updatedConfig mcpv1beta1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedConfig.Status.ConfigHash, "Config hash should be set")
	firstHash := updatedConfig.Status.ConfigHash

	// Update the config spec (simulate a change)
	updatedConfig.Spec.Inline.ClientID = "new-client-id"
	updatedConfig.Generation = 2
	err = fakeClient.Update(ctx, &updatedConfig)
	require.NoError(t, err)

	// Third reconciliation - should detect change and update hash
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Get final config and verify hash changed
	var finalConfig mcpv1beta1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &finalConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, finalConfig.Status.ConfigHash, "Config hash should still be set")
	assert.NotEqual(t, firstHash, finalConfig.Status.ConfigHash, "Hash should change when spec changes")
	assert.Equal(t, int64(2), finalConfig.Status.ObservedGeneration, "ObservedGeneration should be updated")
}

func TestMCPOIDCConfigReconciler_ValidationFailureSetsCondition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	// Invalid config: type is inline but no inline config set
	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "invalid-config",
			Namespace:  "default",
			Finalizers: []string{OIDCConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			// Missing Inline config
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()

	r := &MCPOIDCConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      oidcConfig.Name,
			Namespace: oidcConfig.Namespace,
		},
	}

	// Reconcile should not return error (validation failures are not requeued)
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Check that the Ready condition is set to False
	var updatedConfig mcpv1beta1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)

	var foundCondition bool
	for _, cond := range updatedConfig.Status.Conditions {
		if cond.Type == mcpv1beta1.ConditionTypeOIDCConfigValid {
			foundCondition = true
			assert.Equal(t, metav1.ConditionFalse, cond.Status, "Valid condition should be False")
			assert.Equal(t, mcpv1beta1.ConditionReasonOIDCConfigInvalid, cond.Reason)
			break
		}
	}
	assert.True(t, foundCondition, "Should have a Ready condition")
}

func TestMCPOIDCConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *mcpv1beta1.MCPOIDCConfig
		expectError bool
	}{
		{
			name: "valid kubernetesServiceAccount config",
			config: &mcpv1beta1.MCPOIDCConfig{
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1beta1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: "test-sa",
						Issuer:         "https://kubernetes.default.svc",
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid inline config",
			config: &mcpv1beta1.MCPOIDCConfig{
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "test-client",
					},
				},
			},
			expectError: false,
		},
		{
			name: "invalid kubernetesServiceAccount set but type is inline",
			config: &mcpv1beta1.MCPOIDCConfig{
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeInline,
					KubernetesServiceAccount: &mcpv1beta1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: "test-sa",
					},
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
					},
				},
			},
			expectError: true,
		},
		{
			name: "invalid no config variant set",
			config: &mcpv1beta1.MCPOIDCConfig{
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeInline,
				},
			},
			expectError: true,
		},
		{
			name: "invalid multiple config variants set",
			config: &mcpv1beta1.MCPOIDCConfig{
				Spec: mcpv1beta1.MCPOIDCConfigSpec{
					Type: mcpv1beta1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1beta1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: "test-sa",
					},
					Inline: &mcpv1beta1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMCPOIDCConfigReconciler_ReferenceCountUpdatedWithWorkloads(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://issuer.example.com",
				ClientID: "test-client",
			},
		},
	}
	mcpServer := v1beta1test.NewMCPServer("server-to-track", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithOIDCConfigRef("test-config", ""),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig, mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()
	reconciler := &MCPOIDCConfigReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: oidcConfig.Name, Namespace: oidcConfig.Namespace}}

	result, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	var updatedConfig mcpv1beta1.MCPOIDCConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &updatedConfig))
	assert.Contains(t, updatedConfig.Status.ReferencingWorkloads,
		mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: "server-to-track"})
	assert.EqualValues(t, 1, updatedConfig.Status.ReferenceCount)

	require.NoError(t, fakeClient.Delete(ctx, mcpServer))

	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &updatedConfig))
	assert.Empty(t, updatedConfig.Status.ReferencingWorkloads)
	assert.EqualValues(t, 0, updatedConfig.Status.ReferenceCount)
}

// TestMCPOIDCConfigReconciler_ReconcileKeepsExistingForeignCondition verifies
// that when the controller observes a foreign-owned condition already on the
// object and then writes its own Valid=True, it folds its condition into the
// existing set rather than dropping the foreign one. It catches the
// mutate-outside-the-closure bug: if a condition were set before
// MutateAndPatchStatus took its snapshot, the diff would be empty and the
// controller-owned Valid condition would never land (the bottom assertion
// catches that).
//
// This case does NOT prove the merge-patch-vs-PUT distinction on its own —
// under the fake client there is no concurrent writer, so a full PUT of the
// in-memory object (which still carries the foreign condition loaded at Get)
// would also persist it. The concurrent-writer guarantee is exercised by
// TestMCPOIDCConfigReconciler_ConcurrentForeignConditionSurvivesMergePatch.
func TestMCPOIDCConfigReconciler_ReconcileKeepsExistingForeignCondition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default", Generation: 1},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "test-client",
			},
		},
		Status: mcpv1beta1.MCPOIDCConfigStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "ForeignControllerSays",
					Status:             metav1.ConditionTrue,
					Reason:             "ExternallySet",
					Message:            "set by a hypothetical sibling owner of this resource",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		Build()
	r := &MCPOIDCConfigReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: oidcConfig.Name, Namespace: oidcConfig.Namespace}}

	// First reconcile adds the finalizer; second runs the success path and
	// writes Valid=True without touching any foreign condition.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var after mcpv1beta1.MCPOIDCConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &after))

	foreign := meta.FindStatusCondition(after.Status.Conditions, "ForeignControllerSays")
	require.NotNil(t, foreign,
		"foreign condition must survive an MCPOIDCConfig reconcile — the controller must fold its own condition into the existing set, not replace it")
	assert.Equal(t, metav1.ConditionTrue, foreign.Status, "foreign condition value must not be modified")
	assert.Equal(t, "ExternallySet", foreign.Reason)

	// And our own Valid=True landed.
	own := meta.FindStatusCondition(after.Status.Conditions, mcpv1beta1.ConditionTypeOIDCConfigValid)
	require.NotNil(t, own, "controller-owned Valid condition must land")
	assert.Equal(t, metav1.ConditionTrue, own.Status)
}

// TestMCPOIDCConfigReconciler_ConcurrentForeignConditionSurvivesMergePatch
// proves the property the MutateAndPatchStatus migration actually buys over a
// full r.Status().Update: a condition written by a disjoint owner that lands
// between the reconciler's Get and its status patch survives, because the
// reconciler sends a JSON merge-patch carrying only the fields it changed
// (here referencingWorkloads/referenceCount) rather than sending a full PUT of
// its stale view of the whole Status.Conditions array.
//
// The merge-patch-vs-PUT behaviour lives entirely in the shared
// ctrlutil.MutateAndPatchStatus helper that all three config controllers use,
// so this single end-to-end proof guards the mechanism for the OIDC,
// ExternalAuth, and Authz reconcilers alike.
//
// A WithInterceptorFuncs Get hook simulates the concurrent writer: once armed,
// it injects a foreign condition into the backing store immediately after the
// reconciler reads the object. The reconcile that follows changes only the
// reference list — the spec, and hence the generation and the Valid
// condition's ObservedGeneration, is unchanged — so the merge-patch body omits
// the conditions array and the foreign entry is preserved. A regression to
// r.Status().Update would PUT conditions=[Valid] and erase it.
func TestMCPOIDCConfigReconciler_ConcurrentForeignConditionSurvivesMergePatch(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "concurrent-config", Namespace: "default", Generation: 1},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "test-client",
			},
		},
	}
	key := client.ObjectKeyFromObject(oidcConfig)

	foreign := metav1.Condition{
		Type:               "ForeignControllerSays",
		Status:             metav1.ConditionTrue,
		Reason:             "ExternallySet",
		Message:            "written by a concurrent owner between Get and Patch",
		LastTransitionTime: metav1.Now(),
	}

	var armed atomic.Bool
	inject := interceptor.Funcs{
		Get: func(ctx context.Context, cl client.WithWatch, k client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if err := cl.Get(ctx, k, obj, opts...); err != nil {
				return err
			}
			// Simulate a disjoint owner writing a condition into the store
			// right after the reconciler's read of the config object. cl is the
			// inner (non-intercepted) client, so this does not recurse.
			if k == key && armed.CompareAndSwap(true, false) {
				cur := &mcpv1beta1.MCPOIDCConfig{}
				if err := cl.Get(ctx, k, cur); err != nil {
					return err
				}
				meta.SetStatusCondition(&cur.Status.Conditions, foreign)
				if err := cl.Status().Update(ctx, cur); err != nil {
					return err
				}
			}
			return nil
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1beta1.MCPOIDCConfig{}).
		WithInterceptorFuncs(inject).
		Build()
	r := &MCPOIDCConfigReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: key}

	// Reach steady state: finalizer added, then Valid=True + ConfigHash set.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Introduce a referencing workload so the next reconcile patches only
	// referencingWorkloads/referenceCount — not the conditions array, and
	// without bumping the config's generation.
	server := v1beta1test.NewMCPServer("referencing-server", "default",
		v1beta1test.WithImage("img"),
		v1beta1test.WithOIDCConfigRef(oidcConfig.Name, "aud"),
	)
	require.NoError(t, fakeClient.Create(ctx, server))

	// Arm the concurrent writer and run the reference-refresh reconcile.
	armed.Store(true)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.False(t, armed.Load(), "interceptor must have fired on the config Get")

	var after mcpv1beta1.MCPOIDCConfig
	require.NoError(t, fakeClient.Get(ctx, key, &after))

	foreignAfter := meta.FindStatusCondition(after.Status.Conditions, "ForeignControllerSays")
	require.NotNil(t, foreignAfter,
		"a concurrently-written foreign condition must survive the reconciler's merge-patch; a full PUT would erase it")
	assert.Equal(t, metav1.ConditionTrue, foreignAfter.Status)

	// The reconciler's own work still landed in the same patch cycle.
	own := meta.FindStatusCondition(after.Status.Conditions, mcpv1beta1.ConditionTypeOIDCConfigValid)
	require.NotNil(t, own, "controller-owned Valid condition must remain")
	assert.Equal(t, metav1.ConditionTrue, own.Status)
	assert.EqualValues(t, 1, after.Status.ReferenceCount, "reference refresh must have been applied")
}
