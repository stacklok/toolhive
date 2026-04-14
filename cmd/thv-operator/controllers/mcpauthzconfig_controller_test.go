// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
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
	// Import authorizer backends so they register with the factory registry.
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/http"
)

// validCedarConfig returns a RawExtension containing a valid Cedar backend config.
func validCedarConfig() runtime.RawExtension {
	return runtime.RawExtension{
		Raw: []byte(`{"policies":["permit(principal, action, resource);"],"entities_json":"[]"}`),
	}
}

// validHTTPPDPConfig returns a RawExtension containing a valid HTTP PDP backend config.
func validHTTPPDPConfig() runtime.RawExtension {
	return runtime.RawExtension{
		Raw: []byte(`{"http":{"url":"http://localhost:9000"},"claim_mapping":"standard"}`),
	}
}

func TestBuildFullAuthzConfigJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		spec          mcpv1alpha1.MCPAuthzConfigSpec
		expectError   bool
		expectType    string
		expectKey     string
		expectVersion string
	}{
		{
			name: "valid Cedar config produces correct JSON",
			spec: mcpv1alpha1.MCPAuthzConfigSpec{
				Type:   "cedarv1",
				Config: validCedarConfig(),
			},
			expectError:   false,
			expectType:    "cedarv1",
			expectKey:     "cedar",
			expectVersion: "1.0",
		},
		{
			name: "valid HTTP PDP config produces correct JSON",
			spec: mcpv1alpha1.MCPAuthzConfigSpec{
				Type:   "httpv1",
				Config: validHTTPPDPConfig(),
			},
			expectError:   false,
			expectType:    "httpv1",
			expectKey:     "pdp",
			expectVersion: "1.0",
		},
		{
			name: "unknown type returns error",
			spec: mcpv1alpha1.MCPAuthzConfigSpec{
				Type:   "unknown-type",
				Config: runtime.RawExtension{Raw: []byte(`{}`)},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := BuildFullAuthzConfigJSON(tt.spec)

			if tt.expectError {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Verify the result is valid JSON
			var parsed map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(result, &parsed), "Output must be valid JSON")

			// Verify "version" field
			var version string
			require.NoError(t, json.Unmarshal(parsed["version"], &version))
			assert.Equal(t, tt.expectVersion, version)

			// Verify "type" field
			var typ string
			require.NoError(t, json.Unmarshal(parsed["type"], &typ))
			assert.Equal(t, tt.expectType, typ)

			// Verify the backend-specific config key exists
			_, hasKey := parsed[tt.expectKey]
			assert.True(t, hasKey, "Output JSON should contain key %q", tt.expectKey)
		})
	}
}

func TestValidateAuthzConfigSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		spec        mcpv1alpha1.MCPAuthzConfigSpec
		expectError bool
	}{
		{
			name: "valid Cedar config with policies",
			spec: mcpv1alpha1.MCPAuthzConfigSpec{
				Type:   "cedarv1",
				Config: validCedarConfig(),
			},
			expectError: false,
		},
		{
			name: "valid HTTP PDP config",
			spec: mcpv1alpha1.MCPAuthzConfigSpec{
				Type:   "httpv1",
				Config: validHTTPPDPConfig(),
			},
			expectError: false,
		},
		{
			name: "Cedar config with empty policies fails validation",
			spec: mcpv1alpha1.MCPAuthzConfigSpec{
				Type:   "cedarv1",
				Config: runtime.RawExtension{Raw: []byte(`{"policies":[],"entities_json":"[]"}`)},
			},
			expectError: true,
		},
		{
			name: "unknown authorizer type fails",
			spec: mcpv1alpha1.MCPAuthzConfigSpec{
				Type:   "nonexistent",
				Config: runtime.RawExtension{Raw: []byte(`{}`)},
			},
			expectError: true,
		},
		{
			name: "empty config object fails Cedar validation",
			spec: mcpv1alpha1.MCPAuthzConfigSpec{
				Type:   "cedarv1",
				Config: runtime.RawExtension{Raw: []byte(`{}`)},
			},
			expectError: true,
		},
		{
			name: "HTTP PDP config missing url fails validation",
			spec: mcpv1alpha1.MCPAuthzConfigSpec{
				Type:   "httpv1",
				Config: runtime.RawExtension{Raw: []byte(`{"http":{},"claim_mapping":"standard"}`)},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateAuthzConfigSpec(tt.spec)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMCPAuthzConfigReconciler_ReconcileNotFound(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	// Empty client - no objects exist
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	r := &MCPAuthzConfigReconciler{
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

func TestMCPAuthzConfigReconciler_SteadyStateNoOp(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	authzConfig := &mcpv1alpha1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPAuthzConfigSpec{
			Type:   "cedarv1",
			Config: validCedarConfig(),
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(authzConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPAuthzConfig{}).
		Build()

	r := &MCPAuthzConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      authzConfig.Name,
			Namespace: authzConfig.Namespace,
		},
	}

	// First reconcile: add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	// Second reconcile: set hash and condition
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var afterInitial mcpv1alpha1.MCPAuthzConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &afterInitial)
	require.NoError(t, err)
	initialHash := afterInitial.Status.ConfigHash
	initialRV := afterInitial.ResourceVersion

	// Third reconcile with no changes: should be a no-op
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	var afterSteady mcpv1alpha1.MCPAuthzConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &afterSteady)
	require.NoError(t, err)
	assert.Equal(t, initialHash, afterSteady.Status.ConfigHash, "Hash should not change")
	assert.Equal(t, initialRV, afterSteady.ResourceVersion, "ResourceVersion should not change (no writes)")
}

func TestMCPAuthzConfigReconciler_ValidationFailureSetsCondition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Invalid config: Cedar type but empty policies
	authzConfig := &mcpv1alpha1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "invalid-config",
			Namespace:  "default",
			Finalizers: []string{AuthzConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPAuthzConfigSpec{
			Type:   "cedarv1",
			Config: runtime.RawExtension{Raw: []byte(`{"policies":[],"entities_json":"[]"}`)},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(authzConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPAuthzConfig{}).
		Build()

	r := &MCPAuthzConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      authzConfig.Name,
			Namespace: authzConfig.Namespace,
		},
	}

	// Reconcile should not return error (validation failures are not requeued)
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Check that the Valid condition is set to False
	var updatedConfig mcpv1alpha1.MCPAuthzConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)

	var foundCondition bool
	for _, cond := range updatedConfig.Status.Conditions {
		if cond.Type == mcpv1alpha1.ConditionTypeAuthzConfigValid {
			foundCondition = true
			assert.Equal(t, metav1.ConditionFalse, cond.Status, "Valid condition should be False")
			assert.Equal(t, mcpv1alpha1.ConditionReasonAuthzConfigInvalid, cond.Reason)
			break
		}
	}
	assert.True(t, foundCondition, "Should have a Valid condition")
}

func TestMCPAuthzConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		authzConfig            *mcpv1alpha1.MCPAuthzConfig
		existingWorkloads      []client.Object
		expectFinalizerRemoved bool
		expectRequeue          bool
	}{
		{
			name: "no referencing workloads removes finalizer",
			authzConfig: &mcpv1alpha1.MCPAuthzConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{AuthzConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1alpha1.MCPAuthzConfigSpec{
					Type:   "cedarv1",
					Config: validCedarConfig(),
				},
			},
			existingWorkloads:      nil,
			expectFinalizerRemoved: true,
			expectRequeue:          false,
		},
		{
			name: "referencing MCPServer blocks deletion",
			authzConfig: &mcpv1alpha1.MCPAuthzConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{AuthzConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1alpha1.MCPAuthzConfigSpec{
					Type:   "cedarv1",
					Config: validCedarConfig(),
				},
			},
			existingWorkloads: []client.Object{
				&mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referencing-server",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image: "example/mcp:latest",
						AuthzConfigRef: &mcpv1alpha1.MCPAuthzConfigReference{
							Name: "test-config",
						},
					},
				},
			},
			expectFinalizerRemoved: false,
			expectRequeue:          true,
		},
		{
			name: "referencing VirtualMCPServer blocks deletion",
			authzConfig: &mcpv1alpha1.MCPAuthzConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{AuthzConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1alpha1.MCPAuthzConfigSpec{
					Type:   "cedarv1",
					Config: validCedarConfig(),
				},
			},
			existingWorkloads: []client.Object{
				&mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referencing-vmcp",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type: "anonymous",
							AuthzConfigRef: &mcpv1alpha1.MCPAuthzConfigReference{
								Name: "test-config",
							},
						},
					},
				},
			},
			expectFinalizerRemoved: false,
			expectRequeue:          true,
		},
		{
			name: "referencing MCPRemoteProxy blocks deletion",
			authzConfig: &mcpv1alpha1.MCPAuthzConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{AuthzConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1alpha1.MCPAuthzConfigSpec{
					Type:   "cedarv1",
					Config: validCedarConfig(),
				},
			},
			existingWorkloads: []client.Object{
				&mcpv1alpha1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "referencing-proxy",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPRemoteProxySpec{
						RemoteURL: "https://example.com",
						AuthzConfigRef: &mcpv1alpha1.MCPAuthzConfigReference{
							Name: "test-config",
						},
					},
				},
			},
			expectFinalizerRemoved: false,
			expectRequeue:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

			objs := []client.Object{tt.authzConfig}
			objs = append(objs, tt.existingWorkloads...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPAuthzConfig{}).
				Build()

			r := &MCPAuthzConfigReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			result, err := r.handleDeletion(ctx, tt.authzConfig)

			assert.NoError(t, err)

			if tt.expectFinalizerRemoved {
				assert.NotContains(t, tt.authzConfig.Finalizers, AuthzConfigFinalizerName,
					"Finalizer should be removed")
				assert.Equal(t, time.Duration(0), result.RequeueAfter)
			}

			if tt.expectRequeue {
				assert.Greater(t, result.RequeueAfter, time.Duration(0),
					"Should requeue when workloads still reference the config")
				assert.Contains(t, tt.authzConfig.Finalizers, AuthzConfigFinalizerName,
					"Finalizer should remain when workloads reference the config")
			}
		})
	}
}

func TestMCPAuthzConfigReconciler_ConfigChangeTriggersHashUpdate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	authzConfig := &mcpv1alpha1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPAuthzConfigSpec{
			Type:   "cedarv1",
			Config: validCedarConfig(),
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(authzConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPAuthzConfig{}).
		Build()

	r := &MCPAuthzConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      authzConfig.Name,
			Namespace: authzConfig.Namespace,
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
	var updatedConfig mcpv1alpha1.MCPAuthzConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedConfig.Status.ConfigHash, "Config hash should be set")
	firstHash := updatedConfig.Status.ConfigHash

	// Update the config spec (simulate a change: add a second policy)
	updatedConfig.Spec.Config = runtime.RawExtension{
		Raw: []byte(`{"policies":["permit(principal, action, resource);","forbid(principal, action, resource) when { resource.sensitive == true };"],"entities_json":"[]"}`),
	}
	updatedConfig.Generation = 2
	err = fakeClient.Update(ctx, &updatedConfig)
	require.NoError(t, err)

	// Third reconciliation - should detect change and update hash
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Get final config and verify hash changed
	var finalConfig mcpv1alpha1.MCPAuthzConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &finalConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, finalConfig.Status.ConfigHash, "Config hash should still be set")
	assert.NotEqual(t, firstHash, finalConfig.Status.ConfigHash, "Hash should change when spec changes")
	assert.Equal(t, int64(2), finalConfig.Status.ObservedGeneration, "ObservedGeneration should be updated")
}

func TestMCPAuthzConfigReconciler_ValidationRecovery(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// Start with invalid config: Cedar with empty policies
	authzConfig := &mcpv1alpha1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "recovery-config",
			Namespace:  "default",
			Finalizers: []string{AuthzConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPAuthzConfigSpec{
			Type:   "cedarv1",
			Config: runtime.RawExtension{Raw: []byte(`{"policies":[],"entities_json":"[]"}`)},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(authzConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPAuthzConfig{}).
		Build()

	r := &MCPAuthzConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      authzConfig.Name,
			Namespace: authzConfig.Namespace,
		},
	}

	// Reconcile invalid config - should set Valid=False
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var invalidConfig mcpv1alpha1.MCPAuthzConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &invalidConfig)
	require.NoError(t, err)

	var foundFalse bool
	for _, cond := range invalidConfig.Status.Conditions {
		if cond.Type == mcpv1alpha1.ConditionTypeAuthzConfigValid {
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			foundFalse = true
		}
	}
	require.True(t, foundFalse, "Should have Valid=False condition")
	assert.Empty(t, invalidConfig.Status.ConfigHash, "Hash should not be set for invalid config")

	// Fix the config by adding a valid policy
	invalidConfig.Spec.Config = validCedarConfig()
	invalidConfig.Generation = 2
	err = fakeClient.Update(ctx, &invalidConfig)
	require.NoError(t, err)

	// Reconcile again - should set Valid=True and compute hash
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var recoveredConfig mcpv1alpha1.MCPAuthzConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &recoveredConfig)
	require.NoError(t, err)

	var foundTrue bool
	for _, cond := range recoveredConfig.Status.Conditions {
		if cond.Type == mcpv1alpha1.ConditionTypeAuthzConfigValid {
			assert.Equal(t, metav1.ConditionTrue, cond.Status, "Valid condition should recover to True")
			assert.Equal(t, mcpv1alpha1.ConditionReasonAuthzConfigValid, cond.Reason)
			foundTrue = true
		}
	}
	assert.True(t, foundTrue, "Should have Valid=True condition after fix")
	assert.NotEmpty(t, recoveredConfig.Status.ConfigHash, "Hash should be set after recovery")
}

func TestMCPAuthzConfigReconciler_findReferencingWorkloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		authzConfigName   string
		existingWorkloads []client.Object
		expectedRefs      []mcpv1alpha1.WorkloadReference
		expectEmpty       bool
	}{
		{
			name:            "all three workload types referencing the same config",
			authzConfigName: "shared-config",
			existingWorkloads: []client.Object{
				&mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-server",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image: "example/mcp:latest",
						AuthzConfigRef: &mcpv1alpha1.MCPAuthzConfigReference{
							Name: "shared-config",
						},
					},
				},
				&mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-vmcp",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type: "anonymous",
							AuthzConfigRef: &mcpv1alpha1.MCPAuthzConfigReference{
								Name: "shared-config",
							},
						},
					},
				},
				&mcpv1alpha1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-proxy",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPRemoteProxySpec{
						RemoteURL: "https://example.com",
						AuthzConfigRef: &mcpv1alpha1.MCPAuthzConfigReference{
							Name: "shared-config",
						},
					},
				},
			},
			expectedRefs: []mcpv1alpha1.WorkloadReference{
				{Kind: mcpv1alpha1.WorkloadKindMCPRemoteProxy, Name: "my-proxy"},
				{Kind: mcpv1alpha1.WorkloadKindMCPServer, Name: "my-server"},
				{Kind: mcpv1alpha1.WorkloadKindVirtualMCPServer, Name: "my-vmcp"},
			},
		},
		{
			name:            "no workloads reference the config",
			authzConfigName: "unused-config",
			existingWorkloads: []client.Object{
				&mcpv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unrelated-server",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image: "example/mcp:latest",
						AuthzConfigRef: &mcpv1alpha1.MCPAuthzConfigReference{
							Name: "other-config",
						},
					},
				},
				&mcpv1alpha1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unrelated-vmcp",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type: "anonymous",
						},
					},
				},
				&mcpv1alpha1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unrelated-proxy",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPRemoteProxySpec{
						RemoteURL: "https://example.com",
					},
				},
			},
			expectEmpty: true,
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
				WithObjects(tt.existingWorkloads...).
				Build()

			r := &MCPAuthzConfigReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			authzConfig := &mcpv1alpha1.MCPAuthzConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.authzConfigName,
					Namespace: "default",
				},
			}

			refs, err := r.findReferencingWorkloads(ctx, authzConfig)
			require.NoError(t, err)

			if tt.expectEmpty {
				assert.Empty(t, refs)
			} else {
				assert.Equal(t, tt.expectedRefs, refs)
			}
		})
	}
}

func TestBuildFullAuthzConfigJSON_EmptyConfigRaw(t *testing.T) {
	t.Parallel()

	spec := mcpv1alpha1.MCPAuthzConfigSpec{
		Type:   "cedarv1",
		Config: runtime.RawExtension{Raw: []byte{}},
	}

	result, err := BuildFullAuthzConfigJSON(spec)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "config field is empty")
}
