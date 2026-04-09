// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

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

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

const (
	testEntryName     = "test-entry"
	testEntryNS       = "default"
	testAuthConfig    = "test-auth-config"
	testCAConfigMap   = "test-ca-bundle"
	testEntryGroupRef = "test-group"
)

// newEntryScheme creates a runtime scheme with the CRD and core types registered.
func newEntryScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return scheme
}

// newEntryFakeClient builds a fake client with all required indexes and status subresources.
func newEntryFakeClient(t *testing.T, scheme *runtime.Scheme, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1alpha1.MCPServerEntry{}).
		Build()
}

// newMCPGroup creates a minimal MCPGroup with the given phase.
func newMCPGroup(phase mcpv1alpha1.MCPGroupPhase) *mcpv1alpha1.MCPGroup {
	return &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testEntryGroupRef,
			Namespace: testEntryNS,
		},
		Status: mcpv1alpha1.MCPGroupStatus{
			Phase: phase,
		},
	}
}

// newMCPServerEntry creates an MCPServerEntry with optional auth config and CA bundle refs.
func newMCPServerEntry(
	groupRef string,
	authConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
	caBundleRef *mcpv1alpha1.CABundleSource,
) *mcpv1alpha1.MCPServerEntry {
	return &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testEntryName,
			Namespace: testEntryNS,
		},
		Spec: mcpv1alpha1.MCPServerEntrySpec{
			RemoteURL:             "https://example.com/mcp",
			Transport:             "sse",
			GroupRef:              groupRef,
			ExternalAuthConfigRef: authConfigRef,
			CABundleRef:           caBundleRef,
		},
	}
}

// newMCPExternalAuthConfig creates a minimal MCPExternalAuthConfig object.
func newMCPExternalAuthConfig(name, namespace string) *mcpv1alpha1.MCPExternalAuthConfig {
	return &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
		},
	}
}

// newConfigMap creates a minimal ConfigMap object.
func newConfigMap(name, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string]string{
			"ca.crt": "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
		},
	}
}

// assertCondition checks that a condition with the given type, status, and reason exists.
func assertCondition(
	t *testing.T,
	conditions []metav1.Condition,
	condType string,
	expectedStatus metav1.ConditionStatus,
	expectedReason string,
) {
	t.Helper()
	cond := meta.FindStatusCondition(conditions, condType)
	require.NotNilf(t, cond, "condition %q should be present", condType)
	assert.Equal(t, expectedStatus, cond.Status, "condition %q status", condType)
	assert.Equal(t, expectedReason, cond.Reason, "condition %q reason", condType)
}

func TestMCPServerEntryReconciler_Reconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// objects to seed the fake client (entry is always first)
		entry      *mcpv1alpha1.MCPServerEntry
		objects    []client.Object
		wantErr    bool
		wantPhase  mcpv1alpha1.MCPServerEntryPhase
		conditions []struct {
			condType string
			status   metav1.ConditionStatus
			reason   string
		}
	}{
		{
			name: "happy path - all refs valid",
			entry: newMCPServerEntry(testEntryGroupRef,
				&mcpv1alpha1.ExternalAuthConfigRef{Name: testAuthConfig},
				&mcpv1alpha1.CABundleSource{
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: testCAConfigMap},
						Key:                  "ca.crt",
					},
				},
			),
			objects: []client.Object{
				newMCPGroup(mcpv1alpha1.MCPGroupPhaseReady),
				newMCPExternalAuthConfig(testAuthConfig, testEntryNS),
				newConfigMap(testCAConfigMap, testEntryNS),
			},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseValid,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigValid},
				{mcpv1alpha1.ConditionTypeMCPServerEntryCABundleRefValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryCABundleRefValid},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryValid},
			},
		},
		{
			name:      "happy path - optional refs nil",
			entry:     newMCPServerEntry(testEntryGroupRef, nil, nil),
			objects:   []client.Object{newMCPGroup(mcpv1alpha1.MCPGroupPhaseReady)},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseValid,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigNotConfigured},
				{mcpv1alpha1.ConditionTypeMCPServerEntryCABundleRefValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryCABundleRefNotConfigured},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryValid},
			},
		},
		{
			name:      "group ref not found",
			entry:     newMCPServerEntry("nonexistent-group", nil, nil),
			objects:   []client.Object{},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefNotFound},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name:  "group ref not ready",
			entry: newMCPServerEntry(testEntryGroupRef, nil, nil),
			// MCPGroup exists but has empty phase (not Ready)
			objects:   []client.Object{newMCPGroup("")},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefNotReady},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "auth config ref not found",
			entry: newMCPServerEntry(testEntryGroupRef,
				&mcpv1alpha1.ExternalAuthConfigRef{Name: "nonexistent-auth"},
				nil,
			),
			objects:   []client.Object{newMCPGroup(mcpv1alpha1.MCPGroupPhaseReady)},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValidated, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigNotFound},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "CA bundle ref not found",
			entry: newMCPServerEntry(testEntryGroupRef,
				nil,
				&mcpv1alpha1.CABundleSource{
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "nonexistent-cm"},
						Key:                  "ca.crt",
					},
				},
			),
			objects:   []client.Object{newMCPGroup(mcpv1alpha1.MCPGroupPhaseReady)},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1alpha1.ConditionTypeMCPServerEntryCABundleRefValidated, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryCABundleRefNotFound},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "SSRF - loopback IP rejected",
			entry: func() *mcpv1alpha1.MCPServerEntry {
				e := newMCPServerEntry(testEntryGroupRef, nil, nil)
				e.Spec.RemoteURL = "http://127.0.0.1:8080/"
				return e
			}(),
			objects:   []client.Object{newMCPGroup(mcpv1alpha1.MCPGroupPhaseReady)},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryRemoteURLValidated, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryRemoteURLInvalid},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "SSRF - metadata endpoint rejected",
			entry: func() *mcpv1alpha1.MCPServerEntry {
				e := newMCPServerEntry(testEntryGroupRef, nil, nil)
				e.Spec.RemoteURL = "http://169.254.169.254/latest/meta-data/"
				return e
			}(),
			objects:   []client.Object{newMCPGroup(mcpv1alpha1.MCPGroupPhaseReady)},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryRemoteURLValidated, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryRemoteURLInvalid},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "SSRF - kubernetes.default.svc rejected",
			entry: func() *mcpv1alpha1.MCPServerEntry {
				e := newMCPServerEntry(testEntryGroupRef, nil, nil)
				e.Spec.RemoteURL = "http://kubernetes.default.svc/"
				return e
			}(),
			objects:   []client.Object{newMCPGroup(mcpv1alpha1.MCPGroupPhaseReady)},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryRemoteURLValidated, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryRemoteURLInvalid},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1alpha1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name:      "entry not found returns no error and no requeue",
			entry:     nil, // no entry seeded
			wantPhase: "",  // not checked
		},
		{
			name: "CA bundle ref with nil configMapRef treated as not configured",
			entry: newMCPServerEntry(testEntryGroupRef,
				nil,
				&mcpv1alpha1.CABundleSource{ConfigMapRef: nil},
			),
			objects:   []client.Object{newMCPGroup(mcpv1alpha1.MCPGroupPhaseReady)},
			wantPhase: mcpv1alpha1.MCPServerEntryPhaseValid,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1alpha1.ConditionTypeMCPServerEntryAuthConfigValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryAuthConfigNotConfigured},
				{mcpv1alpha1.ConditionTypeMCPServerEntryCABundleRefValidated, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryCABundleRefNotConfigured},
				{mcpv1alpha1.ConditionTypeMCPServerEntryValid, metav1.ConditionTrue, mcpv1alpha1.ConditionReasonMCPServerEntryValid},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			scheme := newEntryScheme(t)

			objs := append([]client.Object{}, tt.objects...)
			if tt.entry != nil {
				objs = append(objs, tt.entry)
			}

			fakeClient := newEntryFakeClient(t, scheme, objs...)

			r := &MCPServerEntryReconciler{Client: fakeClient}

			entryName := testEntryName
			entryNS := testEntryNS
			if tt.entry != nil {
				entryName = tt.entry.Name
				entryNS = tt.entry.Namespace
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      entryName,
					Namespace: entryNS,
				},
			}

			result, err := r.Reconcile(ctx, req)

			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// For the "entry not found" case, just verify no requeue
			if tt.entry == nil {
				assert.Zero(t, result.RequeueAfter, "Should not requeue for non-existent entry")
				return
			}

			assert.Zero(t, result.RequeueAfter, "Should not requeue on success")

			// Fetch the updated entry from the fake client
			var updatedEntry mcpv1alpha1.MCPServerEntry
			err = fakeClient.Get(ctx, req.NamespacedName, &updatedEntry)
			require.NoError(t, err)

			assert.Equal(t, tt.wantPhase, updatedEntry.Status.Phase)

			for _, c := range tt.conditions {
				assertCondition(t, updatedEntry.Status.Conditions, c.condType, c.status, c.reason)
			}
		})
	}
}

// TestMCPGroupReconciler_MCPServerEntryIntegration verifies the MCPGroup controller
// correctly tracks MCPServerEntries in its Entries and EntryCount status fields.
func TestMCPGroupReconciler_MCPServerEntryIntegration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := newEntryScheme(t)

	group := &mcpv1alpha1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testEntryGroupRef,
			Namespace: testEntryNS,
		},
	}
	entry1 := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "entry1", Namespace: testEntryNS},
		Spec:       mcpv1alpha1.MCPServerEntrySpec{RemoteURL: "https://a.example.com", Transport: "sse", GroupRef: testEntryGroupRef},
	}
	entry2 := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "entry2", Namespace: testEntryNS},
		Spec:       mcpv1alpha1.MCPServerEntrySpec{RemoteURL: "https://b.example.com", Transport: "sse", GroupRef: testEntryGroupRef},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(group, entry1, entry2).
		WithStatusSubresource(&mcpv1alpha1.MCPGroup{}, &mcpv1alpha1.MCPServerEntry{}).
		WithIndex(&mcpv1alpha1.MCPServer{}, "spec.groupRef", func(obj client.Object) []string {
			s := obj.(*mcpv1alpha1.MCPServer)
			if s.Spec.GroupRef == "" {
				return nil
			}
			return []string{s.Spec.GroupRef}
		}).
		WithIndex(&mcpv1alpha1.MCPRemoteProxy{}, "spec.groupRef", func(obj client.Object) []string {
			p := obj.(*mcpv1alpha1.MCPRemoteProxy)
			if p.Spec.GroupRef == "" {
				return nil
			}
			return []string{p.Spec.GroupRef}
		}).
		WithIndex(&mcpv1alpha1.MCPServerEntry{}, "spec.groupRef", func(obj client.Object) []string {
			e := obj.(*mcpv1alpha1.MCPServerEntry)
			if e.Spec.GroupRef == "" {
				return nil
			}
			return []string{e.Spec.GroupRef}
		}).
		Build()

	r := &MCPGroupReconciler{Client: fakeClient}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      testEntryGroupRef,
			Namespace: testEntryNS,
		},
	}

	// First reconcile adds the finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.True(t, result.RequeueAfter > 0, "Should requeue after adding finalizer")

	// Second reconcile processes normally
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Zero(t, result.RequeueAfter, "Should not requeue")

	var updatedGroup mcpv1alpha1.MCPGroup
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedGroup)
	require.NoError(t, err)

	assert.Equal(t, mcpv1alpha1.MCPGroupPhaseReady, updatedGroup.Status.Phase)
	assert.Equal(t, int32(2), updatedGroup.Status.EntryCount)
	assert.ElementsMatch(t, []string{"entry1", "entry2"}, updatedGroup.Status.Entries)
}

// TestMCPGroupReconciler_EntryDeletionHandler verifies that updateReferencingEntriesOnDeletion
// sets the GroupRefValidated condition to False on all referencing MCPServerEntries.
func TestMCPGroupReconciler_EntryDeletionHandler(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := newEntryScheme(t)

	entry1 := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "entry1", Namespace: testEntryNS},
		Spec:       mcpv1alpha1.MCPServerEntrySpec{RemoteURL: "https://a.example.com", Transport: "sse", GroupRef: testEntryGroupRef},
	}
	entry2 := &mcpv1alpha1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "entry2", Namespace: testEntryNS},
		Spec:       mcpv1alpha1.MCPServerEntrySpec{RemoteURL: "https://b.example.com", Transport: "sse", GroupRef: testEntryGroupRef},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(entry1, entry2).
		WithStatusSubresource(&mcpv1alpha1.MCPServerEntry{}).
		Build()

	r := &MCPGroupReconciler{Client: fakeClient}

	// Build the slice of entries as the controller would receive them
	entries := []mcpv1alpha1.MCPServerEntry{*entry1, *entry2}

	r.updateReferencingEntriesOnDeletion(ctx, entries, testEntryGroupRef)

	// Verify both entries have the GroupRefValidated condition set to False
	for _, entryName := range []string{"entry1", "entry2"} {
		var updated mcpv1alpha1.MCPServerEntry
		err := fakeClient.Get(ctx, types.NamespacedName{Name: entryName, Namespace: testEntryNS}, &updated)
		require.NoError(t, err, "should be able to fetch entry %s", entryName)

		assertCondition(t, updated.Status.Conditions,
			mcpv1alpha1.ConditionTypeMCPServerEntryGroupRefValidated,
			metav1.ConditionFalse,
			mcpv1alpha1.ConditionReasonMCPServerEntryGroupRefNotFound,
		)
	}
}
