// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
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

func newAuthzTestReconciler(t *testing.T, objs ...client.Object) (*MCPAuthzConfigReconciler, client.Client) {
	t.Helper()

	scheme := testutil.NewScheme(t)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1beta1.MCPAuthzConfig{}).
		Build()

	return &MCPAuthzConfigReconciler{Client: fakeClient, Scheme: scheme}, fakeClient
}

func TestCanonicalizeSpecForHash(t *testing.T) {
	t.Parallel()

	mkSpec := func(raw string) mcpv1beta1.MCPAuthzConfigSpec {
		return mcpv1beta1.MCPAuthzConfigSpec{
			Type:   "cedarv1",
			Config: runtime.RawExtension{Raw: []byte(raw)},
		}
	}

	// All three variants are the same logical JSON object — they differ
	// only in whitespace and key order. The canonical hash must collapse
	// them to a single value.
	variants := []struct {
		name string
		raw  string
	}{
		{name: "compact", raw: `{"a":1,"b":2,"c":3}`},
		{name: "indented", raw: "{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3\n}"},
		{name: "reordered keys", raw: `{"c":3,"a":1,"b":2}`},
	}

	hashes := make(map[string]string, len(variants))
	for _, v := range variants {
		canonical := canonicalizeSpecForHash(mkSpec(v.raw))
		hashes[v.name] = ctrlutil.CalculateConfigHash(canonical)
	}

	for i := 1; i < len(variants); i++ {
		assert.Equal(t, hashes[variants[0].name], hashes[variants[i].name],
			"canonical hash for %q must match %q (whitespace/key-order only)",
			variants[i].name, variants[0].name)
	}

	// Empty Raw must be a stable no-op (no panic, original spec back).
	emptySpec := mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1"}
	assert.Equal(t, emptySpec, canonicalizeSpecForHash(emptySpec))

	// Malformed JSON must not crash; returns spec unchanged so the upstream
	// validator can surface the real error.
	bad := mkSpec(`{not-json`)
	got := canonicalizeSpecForHash(bad)
	assert.Equal(t, bad.Config.Raw, got.Config.Raw,
		"malformed JSON must round-trip unchanged for the validator to report it")

	// Caller's spec must not be mutated in place.
	original := mkSpec(`{"b":2,"a":1}`)
	originalRaw := append([]byte(nil), original.Config.Raw...)
	_ = canonicalizeSpecForHash(original)
	assert.Equal(t, originalRaw, original.Config.Raw,
		"canonicalizeSpecForHash must not mutate the caller's Config.Raw")
}

func TestMCPAuthzConfigReconciler_validateAuthzConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		spec        mcpv1beta1.MCPAuthzConfigSpec
		expectError bool
	}{
		{
			name:        "valid Cedar config with policies",
			spec:        mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
			expectError: false,
		},
		{
			name:        "valid HTTP PDP config",
			spec:        mcpv1beta1.MCPAuthzConfigSpec{Type: "httpv1", Config: validHTTPPDPConfig()},
			expectError: false,
		},
		{
			name:        "empty type fails structural validation",
			spec:        mcpv1beta1.MCPAuthzConfigSpec{Type: "", Config: validCedarConfig()},
			expectError: true,
		},
		{
			name:        "empty config raw fails structural validation",
			spec:        mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: runtime.RawExtension{Raw: []byte{}}},
			expectError: true,
		},
		{
			name:        "Cedar config with empty policies fails backend validation",
			spec:        mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: runtime.RawExtension{Raw: []byte(`{"policies":[],"entities_json":"[]"}`)}},
			expectError: true,
		},
		{
			name:        "unknown authorizer type fails",
			spec:        mcpv1beta1.MCPAuthzConfigSpec{Type: "nonexistent", Config: runtime.RawExtension{Raw: []byte(`{}`)}},
			expectError: true,
		},
		{
			name:        "empty config object fails Cedar validation",
			spec:        mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: runtime.RawExtension{Raw: []byte(`{}`)}},
			expectError: true,
		},
		{
			name:        "HTTP PDP config missing url fails validation",
			spec:        mcpv1beta1.MCPAuthzConfigSpec{Type: "httpv1", Config: runtime.RawExtension{Raw: []byte(`{"http":{},"claim_mapping":"standard"}`)}},
			expectError: true,
		},
	}

	r := &MCPAuthzConfigReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := r.validateAuthzConfig(&mcpv1beta1.MCPAuthzConfig{Spec: tt.spec})
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
	r, _ := newAuthzTestReconciler(t)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: "non-existent", Namespace: "default"}}

	result, err := r.Reconcile(ctx, req)
	assert.NoError(t, err, "Reconciling a missing resource should not return error")
	assert.Equal(t, time.Duration(0), result.RequeueAfter, "Should not requeue")
}

func TestMCPAuthzConfigReconciler_SteadyStateNoOp(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default", Generation: 1},
		Spec:       mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
	}
	r, fakeClient := newAuthzTestReconciler(t, authzConfig)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: authzConfig.Name, Namespace: authzConfig.Namespace}}

	// First reconcile: add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	// Second reconcile: set hash and condition
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var afterInitial mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &afterInitial))
	initialHash := afterInitial.Status.ConfigHash
	initialRV := afterInitial.ResourceVersion

	// Third reconcile with no changes: should be a no-op
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	var afterSteady mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &afterSteady))
	assert.Equal(t, initialHash, afterSteady.Status.ConfigHash, "Hash should not change")
	assert.Equal(t, initialRV, afterSteady.ResourceVersion, "ResourceVersion should not change (no writes)")
}

func TestMCPAuthzConfigReconciler_ValidationFailureSetsCondition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	// Invalid config: Cedar type but empty policies
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "invalid-config",
			Namespace:  "default",
			Finalizers: []string{AuthzConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{
			Type:   "cedarv1",
			Config: runtime.RawExtension{Raw: []byte(`{"policies":[],"entities_json":"[]"}`)},
		},
	}
	r, fakeClient := newAuthzTestReconciler(t, authzConfig)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: authzConfig.Name, Namespace: authzConfig.Namespace}}

	// Reconcile should not return error (validation failures are not requeued)
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var updatedConfig mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &updatedConfig))

	var foundCondition bool
	for _, cond := range updatedConfig.Status.Conditions {
		if cond.Type == mcpv1beta1.ConditionTypeAuthzConfigValid {
			foundCondition = true
			assert.Equal(t, metav1.ConditionFalse, cond.Status, "Valid condition should be False")
			assert.Equal(t, mcpv1beta1.ConditionReasonAuthzConfigInvalid, cond.Reason)
			break
		}
	}
	assert.True(t, foundCondition, "Should have a Valid condition")
}

func TestMCPAuthzConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	deletingConfig := func() *mcpv1beta1.MCPAuthzConfig {
		return &mcpv1beta1.MCPAuthzConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "test-config",
				Namespace:         "default",
				Finalizers:        []string{AuthzConfigFinalizerName},
				DeletionTimestamp: &metav1.Time{Time: time.Now()},
			},
			Spec: mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
		}
	}

	tests := []struct {
		name                   string
		authzConfig            *mcpv1beta1.MCPAuthzConfig
		existingWorkloads      []client.Object
		expectFinalizerRemoved bool
		expectRequeue          bool
	}{
		{
			name:                   "no referencing workloads removes finalizer",
			authzConfig:            deletingConfig(),
			existingWorkloads:      nil,
			expectFinalizerRemoved: true,
		},
		{
			name:        "referencing MCPServer blocks deletion",
			authzConfig: deletingConfig(),
			existingWorkloads: []client.Object{
				v1beta1test.NewMCPServer("referencing-server", "default",
					v1beta1test.WithImage("example/mcp:latest"),
					v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
						m.Spec.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: "test-config"}
					}),
				),
			},
			expectRequeue: true,
		},
		{
			name:        "referencing VirtualMCPServer blocks deletion",
			authzConfig: deletingConfig(),
			existingWorkloads: []client.Object{
				&mcpv1beta1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: "referencing-vmcp", Namespace: "default"},
					Spec: mcpv1beta1.VirtualMCPServerSpec{
						IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
							Type:           "anonymous",
							AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "test-config"},
						},
					},
				},
			},
			expectRequeue: true,
		},
		{
			name:        "referencing MCPRemoteProxy blocks deletion",
			authzConfig: deletingConfig(),
			existingWorkloads: []client.Object{
				&mcpv1beta1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{Name: "referencing-proxy", Namespace: "default"},
					Spec: mcpv1beta1.MCPRemoteProxySpec{
						RemoteURL:      "https://example.com",
						AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "test-config"},
					},
				},
			},
			expectRequeue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			objs := append([]client.Object{tt.authzConfig}, tt.existingWorkloads...)
			r, _ := newAuthzTestReconciler(t, objs...)

			result, err := r.handleDeletion(ctx, tt.authzConfig)
			assert.NoError(t, err)

			if tt.expectFinalizerRemoved {
				assert.NotContains(t, tt.authzConfig.Finalizers, AuthzConfigFinalizerName, "Finalizer should be removed")
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

// TestMCPAuthzConfigReconciler_FinalizerRemovedAfterLastRefDropped exercises the
// transition that flips deletion-protection state: a config blocked by a
// workload, then the workload disappears, then handleDeletion runs again and
// the finalizer is removed. The static-state cases above only cover the
// "blocked" and "no refs" endpoints; the transition between them is the
// behaviour users actually rely on.
func TestMCPAuthzConfigReconciler_FinalizerRemovedAfterLastRefDropped(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-config",
			Namespace:         "default",
			Finalizers:        []string{AuthzConfigFinalizerName},
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
	}
	workload := v1beta1test.NewMCPServer("ref-server", "default",
		v1beta1test.WithImage("example/mcp:latest"),
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: "test-config"}
		}),
	)
	r, fakeClient := newAuthzTestReconciler(t, authzConfig, workload)

	// First call: workload references the config — finalizer stays.
	result, err := r.handleDeletion(ctx, authzConfig)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0), "first call should requeue")
	assert.Contains(t, authzConfig.Finalizers, AuthzConfigFinalizerName, "finalizer should remain on block")

	// Drop the only referencing workload, then re-run handleDeletion.
	require.NoError(t, fakeClient.Delete(ctx, workload))

	result, err = r.handleDeletion(ctx, authzConfig)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter, "no refs remain — should not requeue")
	assert.NotContains(t, authzConfig.Finalizers, AuthzConfigFinalizerName,
		"finalizer must be removed when the last referencing workload disappears")
}

// TestMCPAuthzConfigReconciler_ReconcileKeepsExistingForeignCondition verifies
// that when the controller observes a foreign-owned condition already on the
// object and then writes its own Valid=True, it folds its condition into the
// existing set rather than dropping the foreign one. It catches the
// mutate-outside-the-closure bug: a condition set before MutateAndPatchStatus
// snapshots the object would produce an empty diff, and the controller-owned
// Valid condition would never land (the bottom assertion catches that).
//
// The concurrent-writer guarantee — that a condition written by a disjoint
// owner between the reconciler's Get and its patch survives because
// MutateAndPatchStatus sends a partial merge-patch rather than a full PUT — is
// proven against the shared ctrlutil.MutateAndPatchStatus helper (used by all
// three config controllers) in
// TestMCPOIDCConfigReconciler_ConcurrentForeignConditionSurvivesMergePatch.
func TestMCPAuthzConfigReconciler_ReconcileKeepsExistingForeignCondition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default", Generation: 1},
		Spec:       mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
		Status: mcpv1beta1.MCPAuthzConfigStatus{
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
	r, fakeClient := newAuthzTestReconciler(t, authzConfig)
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: authzConfig.Name, Namespace: authzConfig.Namespace}}

	// First reconcile adds the finalizer; second runs the success path and
	// writes Valid=True without touching any foreign condition.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var after mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &after))

	foreign := meta.FindStatusCondition(after.Status.Conditions, "ForeignControllerSays")
	require.NotNil(t, foreign,
		"foreign condition must survive an MCPAuthzConfig reconcile — the controller must fold its own condition into the existing set, not replace it")
	assert.Equal(t, metav1.ConditionTrue, foreign.Status, "foreign condition value must not be modified")
	assert.Equal(t, "ExternallySet", foreign.Reason)

	// And our own Valid=True landed.
	own := meta.FindStatusCondition(after.Status.Conditions, mcpv1beta1.ConditionTypeAuthzConfigValid)
	require.NotNil(t, own, "controller-owned Valid condition must land")
	assert.Equal(t, metav1.ConditionTrue, own.Status)
}

func TestMCPAuthzConfigReconciler_ConfigChangeTriggersHashUpdate(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default", Generation: 1},
		Spec:       mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
	}
	r, fakeClient := newAuthzTestReconciler(t, authzConfig)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: authzConfig.Name, Namespace: authzConfig.Namespace}}

	// First reconciliation - add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0), "Should requeue after adding finalizer")

	// Second reconciliation - calculate hash
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	var updatedConfig mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &updatedConfig))
	assert.NotEmpty(t, updatedConfig.Status.ConfigHash, "Config hash should be set")
	firstHash := updatedConfig.Status.ConfigHash

	// Update the config spec (simulate a change: add a second policy)
	updatedConfig.Spec.Config = runtime.RawExtension{
		//nolint:lll // policy fixture is intentionally on one line
		Raw: []byte(`{"policies":["permit(principal, action, resource);","forbid(principal, action, resource) when { resource.sensitive == true };"],"entities_json":"[]"}`),
	}
	updatedConfig.Generation = 2
	require.NoError(t, fakeClient.Update(ctx, &updatedConfig))

	// Third reconciliation - should detect change and update hash
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var finalConfig mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &finalConfig))
	assert.NotEmpty(t, finalConfig.Status.ConfigHash, "Config hash should still be set")
	assert.NotEqual(t, firstHash, finalConfig.Status.ConfigHash, "Hash should change when spec changes")
	assert.Equal(t, int64(2), finalConfig.Status.ObservedGeneration, "ObservedGeneration should be updated")
}

func TestMCPAuthzConfigReconciler_HashAndRefsLandInOneReconcile(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default", Generation: 1},
		Spec:       mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
	}
	server := v1beta1test.NewMCPServer("ref-server", "default",
		v1beta1test.WithImage("example/mcp:latest"),
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
			m.Spec.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: "test-config"}
		}),
	)
	r, fakeClient := newAuthzTestReconciler(t, authzConfig, server)
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: authzConfig.Name, Namespace: authzConfig.Namespace}}

	// First reconcile adds the finalizer; second runs the success path.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var after mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &after))

	// F4 regression: hash, references, AND ObservedGeneration must all land
	// in the same success-path reconcile. The previous shape returned early
	// on hashChanged and left ReferenceCount at zero until the next event.
	assert.NotEmpty(t, after.Status.ConfigHash, "ConfigHash should be set")
	assert.Equal(t, int32(1), after.Status.ReferenceCount,
		"ReferenceCount must match the referencing workload list in the same reconcile that wrote the hash")
	require.Len(t, after.Status.ReferencingWorkloads, 1)
	assert.Equal(t, "ref-server", after.Status.ReferencingWorkloads[0].Name)
	assert.Equal(t, int64(1), after.Status.ObservedGeneration,
		"ObservedGeneration must land in the same patch as the hash")
}

func TestMCPAuthzConfigReconciler_ClearsDeletionBlockedOnSuccessPath(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	// Seed the resource with a stale DeletionBlocked=True condition from a
	// previous (cancelled) deletion attempt. The next non-deleting reconcile
	// must clear it so operators don't see a stale block warning.
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
			Finalizers: []string{AuthzConfigFinalizerName},
		},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
		Status: mcpv1beta1.MCPAuthzConfigStatus{
			Conditions: []metav1.Condition{
				{
					Type:               mcpv1beta1.ConditionTypeDeletionBlocked,
					Status:             metav1.ConditionTrue,
					Reason:             "ReferencedByWorkloads",
					Message:            "Cannot delete: referenced by workloads",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}
	r, fakeClient := newAuthzTestReconciler(t, authzConfig)
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: authzConfig.Name, Namespace: authzConfig.Namespace}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var after mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &after))

	assert.Nil(t, meta.FindStatusCondition(after.Status.Conditions, mcpv1beta1.ConditionTypeDeletionBlocked),
		"DeletionBlocked condition must be removed on the non-deletion success path")
	assert.Equal(t, metav1.ConditionTrue,
		meta.FindStatusCondition(after.Status.Conditions, mcpv1beta1.ConditionTypeAuthzConfigValid).Status,
		"Valid=True condition must remain")
}

func TestMCPAuthzConfigReconciler_ValidationRecovery(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	// Start with invalid config: Cedar with empty policies
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "recovery-config",
			Namespace:  "default",
			Finalizers: []string{AuthzConfigFinalizerName},
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{
			Type:   "cedarv1",
			Config: runtime.RawExtension{Raw: []byte(`{"policies":[],"entities_json":"[]"}`)},
		},
	}
	r, fakeClient := newAuthzTestReconciler(t, authzConfig)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: authzConfig.Name, Namespace: authzConfig.Namespace}}

	// Reconcile invalid config - should set Valid=False
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	var invalidConfig mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &invalidConfig))

	var foundFalse bool
	for _, cond := range invalidConfig.Status.Conditions {
		if cond.Type == mcpv1beta1.ConditionTypeAuthzConfigValid {
			assert.Equal(t, metav1.ConditionFalse, cond.Status)
			foundFalse = true
		}
	}
	require.True(t, foundFalse, "Should have Valid=False condition")
	assert.Empty(t, invalidConfig.Status.ConfigHash, "Hash should not be set for invalid config")

	// Fix the config by adding a valid policy
	invalidConfig.Spec.Config = validCedarConfig()
	invalidConfig.Generation = 2
	require.NoError(t, fakeClient.Update(ctx, &invalidConfig))

	// Reconcile again - should set Valid=True and compute hash
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var recoveredConfig mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &recoveredConfig))

	var foundTrue bool
	for _, cond := range recoveredConfig.Status.Conditions {
		if cond.Type == mcpv1beta1.ConditionTypeAuthzConfigValid {
			assert.Equal(t, metav1.ConditionTrue, cond.Status, "Valid condition should recover to True")
			assert.Equal(t, mcpv1beta1.ConditionReasonAuthzConfigValid, cond.Reason)
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
		expectedRefs      []mcpv1beta1.WorkloadReference
		expectEmpty       bool
	}{
		{
			name:            "all three workload types referencing the same config",
			authzConfigName: "shared-config",
			existingWorkloads: []client.Object{
				v1beta1test.NewMCPServer("my-server", "default",
					v1beta1test.WithImage("example/mcp:latest"),
					v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
						m.Spec.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: "shared-config"}
					}),
				),
				&mcpv1beta1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: "my-vmcp", Namespace: "default"},
					Spec: mcpv1beta1.VirtualMCPServerSpec{
						IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
							Type:           "anonymous",
							AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "shared-config"},
						},
					},
				},
				&mcpv1beta1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{Name: "my-proxy", Namespace: "default"},
					Spec: mcpv1beta1.MCPRemoteProxySpec{
						RemoteURL:      "https://example.com",
						AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "shared-config"},
					},
				},
			},
			expectedRefs: []mcpv1beta1.WorkloadReference{
				{Kind: mcpv1beta1.WorkloadKindMCPRemoteProxy, Name: "my-proxy"},
				{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: "my-server"},
				{Kind: mcpv1beta1.WorkloadKindVirtualMCPServer, Name: "my-vmcp"},
			},
		},
		{
			name:            "no workloads reference the config",
			authzConfigName: "unused-config",
			existingWorkloads: []client.Object{
				v1beta1test.NewMCPServer("unrelated-server", "default",
					v1beta1test.WithImage("example/mcp:latest"),
					v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
						m.Spec.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: "other-config"}
					}),
				),
				&mcpv1beta1.VirtualMCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: "unrelated-vmcp", Namespace: "default"},
					Spec: mcpv1beta1.VirtualMCPServerSpec{
						IncomingAuth: &mcpv1beta1.IncomingAuthConfig{Type: "anonymous"},
					},
				},
				&mcpv1beta1.MCPRemoteProxy{
					ObjectMeta: metav1.ObjectMeta{Name: "unrelated-proxy", Namespace: "default"},
					Spec:       mcpv1beta1.MCPRemoteProxySpec{RemoteURL: "https://example.com"},
				},
			},
			expectEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			r, _ := newAuthzTestReconciler(t, tt.existingWorkloads...)

			authzConfig := &mcpv1beta1.MCPAuthzConfig{
				ObjectMeta: metav1.ObjectMeta{Name: tt.authzConfigName, Namespace: "default"},
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

// TestMCPAuthzConfigReconciler_watchHandlers verifies that the workload watch map
// functions enqueue both the currently-referenced config and any config that still
// lists the workload in its ReferencingWorkloads status (stale-ref cleanup).
func TestMCPAuthzConfigReconciler_watchHandlers(t *testing.T) {
	t.Parallel()

	// A config that still lists workloads it no longer should, to verify stale-ref enqueue.
	staleConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "stale-config", Namespace: "default"},
		Spec:       mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
		Status: mcpv1beta1.MCPAuthzConfigStatus{
			ReferencingWorkloads: []mcpv1beta1.WorkloadReference{
				{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: "srv"},
				{Kind: mcpv1beta1.WorkloadKindVirtualMCPServer, Name: "vmcp"},
				{Kind: mcpv1beta1.WorkloadKindMCPRemoteProxy, Name: "proxy"},
			},
		},
	}

	tests := []struct {
		name     string
		obj      client.Object
		mapFunc  func(*MCPAuthzConfigReconciler) func(t *testing.T) []reconcile.Request
		expected map[string]struct{}
	}{
		{
			name: "MCPServer with ref enqueues current and stale configs",
			obj: v1beta1test.NewMCPServer("srv", "default",
				v1beta1test.WithImage("example/mcp:latest"),
				v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
					m.Spec.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: "current-config"}
				}),
			),
			expected: map[string]struct{}{"current-config": {}, "stale-config": {}},
		},
		{
			name: "VirtualMCPServer with ref enqueues current and stale configs",
			obj: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					IncomingAuth: &mcpv1beta1.IncomingAuthConfig{
						Type:           "anonymous",
						AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "current-config"},
					},
				},
			},
			expected: map[string]struct{}{"current-config": {}, "stale-config": {}},
		},
		{
			name: "MCPRemoteProxy with ref enqueues current and stale configs",
			obj: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "proxy", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					RemoteURL:      "https://example.com",
					AuthzConfigRef: &mcpv1beta1.MCPAuthzConfigReference{Name: "current-config"},
				},
			},
			expected: map[string]struct{}{"current-config": {}, "stale-config": {}},
		},
		{
			name: "MCPServer without ref only enqueues stale config",
			obj: v1beta1test.NewMCPServer("srv", "default",
				v1beta1test.WithImage("example/mcp:latest"),
			),
			expected: map[string]struct{}{"stale-config": {}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			// DeepCopy the shared fixture: newAuthzTestReconciler builds a fake
			// client whose versionedTracker.Add mutates ObjectMeta.ResourceVersion
			// in place. Passing the same staleConfig pointer into parallel subtests
			// would race on that write (#5502); each subtest gets its own copy.
			r, _ := newAuthzTestReconciler(t, staleConfig.DeepCopy(), tt.obj)

			requests := func() []reconcile.Request {
				switch tt.obj.(type) {
				case *mcpv1beta1.MCPServer:
					return r.mapMCPServerToAuthzConfig(ctx, tt.obj)
				case *mcpv1beta1.VirtualMCPServer:
					return r.mapVirtualMCPServerToAuthzConfig(ctx, tt.obj)
				case *mcpv1beta1.MCPRemoteProxy:
					return r.mapMCPRemoteProxyToAuthzConfig(ctx, tt.obj)
				default:
					t.Fatalf("unexpected object type %T", tt.obj)
					return nil
				}
			}()

			got := make(map[string]struct{}, len(requests))
			for _, req := range requests {
				assert.Equal(t, "default", req.Namespace)
				got[req.Name] = struct{}{}
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestMCPAuthzConfigReconciler_watchHandlersWrongType verifies the map functions
// gracefully ignore objects of an unexpected type.
func TestMCPAuthzConfigReconciler_watchHandlersWrongType(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r, _ := newAuthzTestReconciler(t)

	wrong := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "default"}}
	assert.Nil(t, r.mapMCPServerToAuthzConfig(ctx, wrong))
	assert.Nil(t, r.mapVirtualMCPServerToAuthzConfig(ctx, wrong))
	assert.Nil(t, r.mapMCPRemoteProxyToAuthzConfig(ctx, wrong))
}

// TestMCPAuthzConfigReconciler_DeletionWithoutFinalizer verifies that handleDeletion
// is a no-op when the config never had the finalizer (the object is passed directly
// rather than created in the fake client, which rejects a deletionTimestamp without a
// finalizer).
func TestMCPAuthzConfigReconciler_DeletionWithoutFinalizer(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "no-finalizer",
			Namespace:         "default",
			DeletionTimestamp: &metav1.Time{Time: time.Now()},
		},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
	}
	r, _ := newAuthzTestReconciler(t)

	result, err := r.handleDeletion(ctx, authzConfig)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)
}

// TestMCPAuthzConfigReconciler_AddsFinalizer verifies the first reconcile adds the
// finalizer and requeues.
func TestMCPAuthzConfigReconciler_AddsFinalizer(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "needs-finalizer", Namespace: "default", Generation: 1},
		Spec:       mcpv1beta1.MCPAuthzConfigSpec{Type: "cedarv1", Config: validCedarConfig()},
	}
	r, fakeClient := newAuthzTestReconciler(t, authzConfig)

	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: authzConfig.Name, Namespace: authzConfig.Namespace}}

	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	var updated mcpv1beta1.MCPAuthzConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &updated))
	assert.Contains(t, updated.Finalizers, AuthzConfigFinalizerName)
}
