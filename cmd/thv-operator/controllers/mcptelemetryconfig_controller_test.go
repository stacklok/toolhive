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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestMCPTelemetryConfigReconciler_calculateConfigHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec mcpv1alpha1.MCPTelemetryConfigSpec
	}{
		{
			name: "basic telemetry spec",
			spec: newTelemetrySpec("https://otel-collector:4317", true, false),
		},
		{
			name: "telemetry spec with headers",
			spec: func() mcpv1alpha1.MCPTelemetryConfigSpec {
				s := newTelemetrySpec("https://otel-collector:4317", true, true)
				s.OpenTelemetry.Headers = map[string]string{"X-Team": "platform"}
				return s
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &MCPTelemetryConfigReconciler{}

			hash1 := r.calculateConfigHash(tt.spec)
			hash2 := r.calculateConfigHash(tt.spec)

			assert.Equal(t, hash1, hash2, "Hash should be consistent for same spec")
			assert.NotEmpty(t, hash1, "Hash should not be empty")
		})
	}

	t.Run("different specs produce different hashes", func(t *testing.T) {
		t.Parallel()
		r := &MCPTelemetryConfigReconciler{}
		spec1 := newTelemetrySpec("https://collector-a:4317", true, false)
		spec2 := newTelemetrySpec("https://collector-b:4317", true, false)

		hash1 := r.calculateConfigHash(spec1)
		hash2 := r.calculateConfigHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different specs should produce different hashes")
	})
}

func TestMCPTelemetryConfigReconciler_ReconcileNotFound(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	// Empty client — no objects exist
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &MCPTelemetryConfigReconciler{
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

func TestMCPTelemetryConfigReconciler_SteadyStateNoOp(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: newTelemetrySpec("https://otel-collector:4317", true, true),
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(telemetryConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPTelemetryConfig{}).
		Build()

	r := &MCPTelemetryConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      telemetryConfig.Name,
			Namespace: telemetryConfig.Namespace,
		},
	}

	// First reconcile: add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	// Second reconcile: set hash and condition
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var afterInitial mcpv1alpha1.MCPTelemetryConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &afterInitial)
	require.NoError(t, err)
	initialHash := afterInitial.Status.ConfigHash
	initialRV := afterInitial.ResourceVersion

	// Third reconcile with no changes: should be a no-op
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	var afterSteady mcpv1alpha1.MCPTelemetryConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &afterSteady)
	require.NoError(t, err)
	assert.Equal(t, initialHash, afterSteady.Status.ConfigHash, "Hash should not change")
	assert.Equal(t, initialRV, afterSteady.ResourceVersion, "ResourceVersion should not change (no writes)")
}

func TestMCPTelemetryConfigReconciler_ValidationRecovery(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Start with invalid config: empty sensitive header name
	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "recovery-config",
			Namespace:  "default",
			Finalizers: []string{TelemetryConfigFinalizerName},
			Generation: 1,
		},
		Spec: func() mcpv1alpha1.MCPTelemetryConfigSpec {
			s := newTelemetrySpec("https://otel-collector:4317", true, false)
			s.OpenTelemetry.SensitiveHeaders = []mcpv1alpha1.SensitiveHeader{{
				Name:         "",
				SecretKeyRef: mcpv1alpha1.SecretKeyRef{Name: "s", Key: "k"},
			}}
			return s
		}(),
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(telemetryConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPTelemetryConfig{}).
		Build()

	r := &MCPTelemetryConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      telemetryConfig.Name,
			Namespace: telemetryConfig.Namespace,
		},
	}

	// Reconcile invalid config — should set Valid=False
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var invalidConfig mcpv1alpha1.MCPTelemetryConfig
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

	// Fix the config by removing invalid sensitive headers
	invalidConfig.Spec.OpenTelemetry.SensitiveHeaders = nil
	invalidConfig.Generation = 2
	err = fakeClient.Update(ctx, &invalidConfig)
	require.NoError(t, err)

	// Reconcile again — should set Valid=True and compute hash
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var recoveredConfig mcpv1alpha1.MCPTelemetryConfig
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

func TestMCPTelemetryConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		telemetryConfig        *mcpv1alpha1.MCPTelemetryConfig
		expectFinalizerRemoved bool
	}{
		{
			name: "delete config removes finalizer",
			telemetryConfig: &mcpv1alpha1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{TelemetryConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: newTelemetrySpec("https://otel-collector:4317", true, true),
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

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.telemetryConfig).
				Build()

			r := &MCPTelemetryConfigReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			result, err := r.handleDeletion(ctx, tt.telemetryConfig)

			assert.NoError(t, err)
			assert.Equal(t, time.Duration(0), result.RequeueAfter)

			if tt.expectFinalizerRemoved {
				assert.NotContains(t, tt.telemetryConfig.Finalizers, TelemetryConfigFinalizerName,
					"Finalizer should be removed")
			}
		})
	}
}

func TestMCPTelemetryConfigReconciler_ConfigChangeTriggersHashUpdate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: newTelemetrySpec("https://otel-collector:4317", true, false),
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(telemetryConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPTelemetryConfig{}).
		Build()

	r := &MCPTelemetryConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      telemetryConfig.Name,
			Namespace: telemetryConfig.Namespace,
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
	var updatedConfig mcpv1alpha1.MCPTelemetryConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedConfig.Status.ConfigHash, "Config hash should be set")
	firstHash := updatedConfig.Status.ConfigHash

	// Update the config spec (simulate a change)
	updatedConfig.Spec.OpenTelemetry.Endpoint = "https://new-collector:4317"
	updatedConfig.Generation = 2
	err = fakeClient.Update(ctx, &updatedConfig)
	require.NoError(t, err)

	// Third reconciliation - should detect change and update hash
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Get final config and verify hash changed
	var finalConfig mcpv1alpha1.MCPTelemetryConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &finalConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, finalConfig.Status.ConfigHash, "Config hash should still be set")
	assert.NotEqual(t, firstHash, finalConfig.Status.ConfigHash, "Hash should change when spec changes")
	assert.Equal(t, int64(2), finalConfig.Status.ObservedGeneration, "ObservedGeneration should be updated")
}

func TestMCPTelemetryConfigReconciler_ValidationFailureSetsCondition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Invalid config: empty sensitive header name
	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "invalid-config",
			Namespace:  "default",
			Finalizers: []string{TelemetryConfigFinalizerName},
			Generation: 1,
		},
		Spec: func() mcpv1alpha1.MCPTelemetryConfigSpec {
			s := newTelemetrySpec("https://otel-collector:4317", true, false)
			s.OpenTelemetry.SensitiveHeaders = []mcpv1alpha1.SensitiveHeader{{
				Name:         "",
				SecretKeyRef: mcpv1alpha1.SecretKeyRef{Name: "s", Key: "k"},
			}}
			return s
		}(),
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(telemetryConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPTelemetryConfig{}).
		Build()

	r := &MCPTelemetryConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      telemetryConfig.Name,
			Namespace: telemetryConfig.Namespace,
		},
	}

	// Reconcile should not return error (validation failures are not requeued)
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Check that the Valid condition is set to False
	var updatedConfig mcpv1alpha1.MCPTelemetryConfig
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

func TestMCPTelemetryConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *mcpv1alpha1.MCPTelemetryConfig
		expectError bool
	}{
		{
			name: "valid basic config",
			config: &mcpv1alpha1.MCPTelemetryConfig{
				Spec: newTelemetrySpec("https://otel-collector:4317", false, true),
			},
			expectError: false,
		},
		{
			name: "valid config with sensitive headers",
			config: &mcpv1alpha1.MCPTelemetryConfig{
				Spec: func() mcpv1alpha1.MCPTelemetryConfigSpec {
					s := newTelemetrySpec("https://otel-collector:4317", true, false)
					s.OpenTelemetry.SensitiveHeaders = []mcpv1alpha1.SensitiveHeader{
						{
							Name: "Authorization",
							SecretKeyRef: mcpv1alpha1.SecretKeyRef{
								Name: "otel-secret",
								Key:  "auth-token",
							},
						},
					}
					return s
				}(),
			},
			expectError: false,
		},
		{
			name: "invalid overlapping headers",
			config: &mcpv1alpha1.MCPTelemetryConfig{
				Spec: func() mcpv1alpha1.MCPTelemetryConfigSpec {
					s := newTelemetrySpec("https://otel-collector:4317", true, false)
					s.OpenTelemetry.Headers = map[string]string{"Authorization": "Bearer token"}
					s.OpenTelemetry.SensitiveHeaders = []mcpv1alpha1.SensitiveHeader{
						{
							Name: "Authorization",
							SecretKeyRef: mcpv1alpha1.SecretKeyRef{
								Name: "otel-secret",
								Key:  "auth-token",
							},
						},
					}
					return s
				}(),
			},
			expectError: true,
		},
		{
			name: "invalid endpoint without tracing or metrics",
			config: &mcpv1alpha1.MCPTelemetryConfig{
				Spec: mcpv1alpha1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1alpha1.MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "otel-collector:4317",
						// No Tracing or Metrics configured
					},
				},
			},
			expectError: true,
		},
		{
			name: "invalid empty secret ref name",
			config: &mcpv1alpha1.MCPTelemetryConfig{
				Spec: func() mcpv1alpha1.MCPTelemetryConfigSpec {
					s := newTelemetrySpec("https://otel-collector:4317", true, false)
					s.OpenTelemetry.SensitiveHeaders = []mcpv1alpha1.SensitiveHeader{
						{
							Name: "Authorization",
							SecretKeyRef: mcpv1alpha1.SecretKeyRef{
								Name: "",
								Key:  "auth-token",
							},
						},
					}
					return s
				}(),
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

func TestMCPTelemetryConfigReconciler_ConditionOnlyUpdate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	spec := newTelemetrySpec("https://otel-collector:4317", true, true)

	// Pre-compute the hash the controller would produce
	r := &MCPTelemetryConfigReconciler{}
	precomputedHash := r.calculateConfigHash(spec)

	// Resource already has finalizer and correct hash, but no Valid condition
	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "condition-only-config",
			Namespace:  "default",
			Finalizers: []string{TelemetryConfigFinalizerName},
			Generation: 1,
		},
		Spec: spec,
		Status: mcpv1alpha1.MCPTelemetryConfigStatus{
			ConfigHash:         precomputedHash,
			ObservedGeneration: 1,
			// No conditions set — this is the key setup
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(telemetryConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPTelemetryConfig{}).
		Build()

	r.Client = fakeClient
	r.Scheme = scheme

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      telemetryConfig.Name,
			Namespace: telemetryConfig.Namespace,
		},
	}

	// Reconcile should detect condition is missing and write it
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var updated mcpv1alpha1.MCPTelemetryConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updated)
	require.NoError(t, err)

	// Hash should remain unchanged
	assert.Equal(t, precomputedHash, updated.Status.ConfigHash, "Hash should not change")

	// Valid=True condition should now be set
	var foundValid bool
	for _, cond := range updated.Status.Conditions {
		if cond.Type == conditionTypeValid {
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
			assert.Equal(t, "ValidationSucceeded", cond.Reason)
			foundValid = true
		}
	}
	assert.True(t, foundValid, "Should have Valid=True condition after condition-only update")
}

func TestMCPTelemetryConfigReconciler_ReferenceTracking(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "shared-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: newTelemetrySpec("https://otel-collector:4317", true, false),
	}

	// Two MCPServers reference this config, one does not
	server1 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-a",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
				Name: "shared-config",
			},
		},
	}
	server2 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-b",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
				Name: "shared-config",
			},
		},
	}
	server3 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-c",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			// No TelemetryConfigRef
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(telemetryConfig, server1, server2, server3).
		WithStatusSubresource(&mcpv1alpha1.MCPTelemetryConfig{}).
		Build()

	r := &MCPTelemetryConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      telemetryConfig.Name,
			Namespace: telemetryConfig.Namespace,
		},
	}

	// First reconcile: add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0), "Should requeue after adding finalizer")

	// Second reconcile: set hash, condition, and referencing servers
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var updated mcpv1alpha1.MCPTelemetryConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updated)
	require.NoError(t, err)

	// ReferencingWorkloads should list server-a and server-b (sorted), but not server-c
	assert.Equal(t, []mcpv1alpha1.WorkloadReference{
		{Kind: "MCPServer", Namespace: "default", Name: "server-a"},
		{Kind: "MCPServer", Namespace: "default", Name: "server-b"},
	}, updated.Status.ReferencingWorkloads)
}

func TestMCPTelemetryConfigReconciler_handleDeletion_BlocksWhenReferenced(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	now := metav1.Now()
	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "referenced-config",
			Namespace:         "default",
			Finalizers:        []string{TelemetryConfigFinalizerName},
			DeletionTimestamp: &now,
			Generation:        1,
		},
		Spec: newTelemetrySpec("https://otel-collector:4317", true, false),
	}

	// MCPServer that references this config
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "referencing-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			TelemetryConfigRef: &mcpv1alpha1.MCPTelemetryConfigReference{
				Name: "referenced-config",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(telemetryConfig, server).
		WithStatusSubresource(&mcpv1alpha1.MCPTelemetryConfig{}).
		Build()

	r := &MCPTelemetryConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	result, err := r.handleDeletion(ctx, telemetryConfig)
	assert.NoError(t, err)
	// Should requeue because the config is still referenced
	assert.Greater(t, result.RequeueAfter, time.Duration(0), "Should requeue when still referenced")
	// Finalizer should NOT be removed
	assert.Contains(t, telemetryConfig.Finalizers, TelemetryConfigFinalizerName,
		"Finalizer should remain when config is still referenced")
}

func TestMCPTelemetryConfigReconciler_handleDeletion_AllowsWhenNotReferenced(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	now := metav1.Now()
	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "unreferenced-config",
			Namespace:         "default",
			Finalizers:        []string{TelemetryConfigFinalizerName},
			DeletionTimestamp: &now,
			Generation:        1,
		},
		Spec: newTelemetrySpec("https://otel-collector:4317", true, false),
	}

	// MCPServer exists but does NOT reference this config
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "unrelated-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			// No TelemetryConfigRef
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(telemetryConfig, server).
		Build()

	r := &MCPTelemetryConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	result, err := r.handleDeletion(ctx, telemetryConfig)
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter, "Should not requeue when not referenced")
	// Finalizer should be removed
	assert.NotContains(t, telemetryConfig.Finalizers, TelemetryConfigFinalizerName,
		"Finalizer should be removed when config is not referenced")
}

func TestMCPTelemetryConfigReconciler_handleDeletion_NoFinalizerIsNoOp(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	// Object with DeletionTimestamp but no finalizers.
	// We don't add it to the fake client (which rejects such objects)
	// because handleDeletion only reads from the object itself for the
	// no-finalizer fast path.
	now := metav1.Now()
	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "no-finalizer-config",
			Namespace:         "default",
			DeletionTimestamp: &now,
			// No finalizers
		},
		Spec: newTelemetrySpec("https://otel-collector:4317", true, false),
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &MCPTelemetryConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	result, err := r.handleDeletion(ctx, telemetryConfig)
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter, "Should not requeue")
}

// newTelemetrySpec creates a basic MCPTelemetryConfigSpec for testing.
func newTelemetrySpec(endpoint string, tracing, metrics bool) mcpv1alpha1.MCPTelemetryConfigSpec {
	return mcpv1alpha1.MCPTelemetryConfigSpec{
		OpenTelemetry: &mcpv1alpha1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: endpoint,
			Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: tracing},
			Metrics:  &mcpv1alpha1.OpenTelemetryMetricsConfig{Enabled: metrics},
		},
	}
}
