// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const conditionTypeValid = "Valid"

func TestMCPOIDCConfigReconciler_calculateConfigHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec mcpv1alpha1.MCPOIDCConfigSpec
	}{
		{
			name: "kubernetesServiceAccount spec",
			spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
				KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
					Issuer: "https://kubernetes.default.svc",
				},
			},
		},
		{
			name: "inline spec",
			spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
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
		spec1 := mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "client1",
			},
		}
		spec2 := mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
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

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

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

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "test-client",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
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

	var afterInitial mcpv1alpha1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &afterInitial)
	require.NoError(t, err)
	initialHash := afterInitial.Status.ConfigHash
	initialRV := afterInitial.ResourceVersion

	// Third reconcile with no changes: should be a no-op
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	var afterSteady mcpv1alpha1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &afterSteady)
	require.NoError(t, err)
	assert.Equal(t, initialHash, afterSteady.Status.ConfigHash, "Hash should not change")
	assert.Equal(t, initialRV, afterSteady.ResourceVersion, "ResourceVersion should not change (no writes)")
}

func TestMCPOIDCConfigReconciler_ValidationRecovery(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Start with invalid config: type=inline but no inline config
	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "recovery-config",
			Namespace:  "default",
			Finalizers: []string{OIDCConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			// Missing Inline config — invalid
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
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

	// Reconcile invalid config — should set Valid=False
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var invalidConfig mcpv1alpha1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &invalidConfig)
	require.NoError(t, err)

	var foundFalse bool
	for _, cond := range invalidConfig.Status.Conditions {
		if cond.Type == conditionTypeValid {
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			foundFalse = true
		}
	}
	require.True(t, foundFalse, "Should have Valid=False condition")
	assert.Empty(t, invalidConfig.Status.ConfigHash, "Hash should not be set for invalid config")

	// Fix the config by adding the inline spec
	invalidConfig.Spec.Inline = &mcpv1alpha1.InlineOIDCSharedConfig{
		Issuer:   "https://accounts.google.com",
		ClientID: "test-client",
	}
	invalidConfig.Generation = 2
	err = fakeClient.Update(ctx, &invalidConfig)
	require.NoError(t, err)

	// Reconcile again — should set Valid=True and compute hash
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var recoveredConfig mcpv1alpha1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &recoveredConfig)
	require.NoError(t, err)

	var foundTrue bool
	for _, cond := range recoveredConfig.Status.Conditions {
		if cond.Type == conditionTypeValid {
			assert.Equal(t, metav1.ConditionTrue, cond.Status, "Valid condition should recover to True")
			assert.Equal(t, "ValidationSucceeded", cond.Reason)
			foundTrue = true
		}
	}
	assert.True(t, foundTrue, "Should have Valid=True condition after fix")
	assert.NotEmpty(t, recoveredConfig.Status.ConfigHash, "Hash should be set after recovery")
}

func TestMCPOIDCConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		oidcConfig             *mcpv1alpha1.MCPOIDCConfig
		expectFinalizerRemoved bool
	}{
		{
			name: "delete config removes finalizer",
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{OIDCConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
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

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

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

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "test-client",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
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
	var updatedConfig mcpv1alpha1.MCPOIDCConfig
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
	var finalConfig mcpv1alpha1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &finalConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, finalConfig.Status.ConfigHash, "Config hash should still be set")
	assert.NotEqual(t, firstHash, finalConfig.Status.ConfigHash, "Hash should change when spec changes")
	assert.Equal(t, int64(2), finalConfig.Status.ObservedGeneration, "ObservedGeneration should be updated")
}

func TestMCPOIDCConfigReconciler_ValidationFailureSetsCondition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Invalid config: type is inline but no inline config set
	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "invalid-config",
			Namespace:  "default",
			Finalizers: []string{OIDCConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			// Missing Inline config
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
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

	// Check that the Valid condition is set to False
	var updatedConfig mcpv1alpha1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)

	var foundCondition bool
	for _, cond := range updatedConfig.Status.Conditions {
		if cond.Type == conditionTypeValid {
			foundCondition = true
			assert.Equal(t, metav1.ConditionFalse, cond.Status, "Valid condition should be False")
			assert.Equal(t, "ValidationFailed", cond.Reason)
			break
		}
	}
	assert.True(t, foundCondition, "Should have a Valid condition")
}

func TestMCPOIDCConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *mcpv1alpha1.MCPOIDCConfig
		expectError bool
	}{
		{
			name: "valid kubernetesServiceAccount config",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: "test-sa",
						Issuer:         "https://kubernetes.default.svc",
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid inline config",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "test-client",
					},
				},
			},
			expectError: false,
		},
		{
			name: "invalid kubernetesServiceAccount set but type is inline",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: "test-sa",
					},
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
					},
				},
			},
			expectError: true,
		},
		{
			name: "invalid no config variant set",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
				},
			},
			expectError: true,
		},
		{
			name: "invalid multiple config variants set",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: "test-sa",
					},
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
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
