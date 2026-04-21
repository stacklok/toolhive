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

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
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
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return scheme
}

// newEntryFakeClient builds a fake client with all required indexes and status subresources.
func newEntryFakeClient(t *testing.T, scheme *runtime.Scheme, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&mcpv1beta1.MCPServerEntry{}).
		Build()
}

// newMCPGroup creates a minimal MCPGroup with the given phase.
func newMCPGroup(phase mcpv1beta1.MCPGroupPhase) *mcpv1beta1.MCPGroup {
	return &mcpv1beta1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testEntryGroupRef,
			Namespace: testEntryNS,
		},
		Status: mcpv1beta1.MCPGroupStatus{
			Phase: phase,
		},
	}
}

// newMCPServerEntry creates an MCPServerEntry with optional auth config and CA bundle refs.
func newMCPServerEntry(
	groupRef string,
	authConfigRef *mcpv1beta1.ExternalAuthConfigRef,
	caBundleRef *mcpv1beta1.CABundleSource,
) *mcpv1beta1.MCPServerEntry {
	return &mcpv1beta1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testEntryName,
			Namespace: testEntryNS,
		},
		Spec: mcpv1beta1.MCPServerEntrySpec{
			RemoteURL:             "https://example.com/mcp",
			Transport:             "sse",
			GroupRef:              &mcpv1beta1.MCPGroupRef{Name: groupRef},
			ExternalAuthConfigRef: authConfigRef,
			CABundleRef:           caBundleRef,
		},
	}
}

// newMCPExternalAuthConfig creates a minimal MCPExternalAuthConfig object.
func newMCPExternalAuthConfig(name, namespace string) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
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
		entry      *mcpv1beta1.MCPServerEntry
		objects    []client.Object
		wantErr    bool
		wantPhase  mcpv1beta1.MCPServerEntryPhase
		conditions []struct {
			condType string
			status   metav1.ConditionStatus
			reason   string
		}
	}{
		{
			name: "happy path - all refs valid",
			entry: newMCPServerEntry(testEntryGroupRef,
				&mcpv1beta1.ExternalAuthConfigRef{Name: testAuthConfig},
				&mcpv1beta1.CABundleSource{
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: testCAConfigMap},
						Key:                  "ca.crt",
					},
				},
			),
			objects: []client.Object{
				newMCPGroup(mcpv1beta1.MCPGroupPhaseReady),
				newMCPExternalAuthConfig(testAuthConfig, testEntryNS),
				newConfigMap(testCAConfigMap, testEntryNS),
			},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseValid,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1beta1.ConditionTypeMCPServerEntryAuthConfigValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryAuthConfigValid},
				{mcpv1beta1.ConditionTypeMCPServerEntryCABundleRefValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryCABundleRefValid},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryValid},
			},
		},
		{
			name:      "happy path - optional refs nil",
			entry:     newMCPServerEntry(testEntryGroupRef, nil, nil),
			objects:   []client.Object{newMCPGroup(mcpv1beta1.MCPGroupPhaseReady)},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseValid,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1beta1.ConditionTypeMCPServerEntryAuthConfigValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryAuthConfigNotConfigured},
				{mcpv1beta1.ConditionTypeMCPServerEntryCABundleRefValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryCABundleRefNotConfigured},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryValid},
			},
		},
		{
			name:      "group ref not found",
			entry:     newMCPServerEntry("nonexistent-group", nil, nil),
			objects:   []client.Object{},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryGroupRefNotFound},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name:  "group ref not ready",
			entry: newMCPServerEntry(testEntryGroupRef, nil, nil),
			// MCPGroup exists but has empty phase (not Ready)
			objects:   []client.Object{newMCPGroup("")},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryGroupRefNotReady},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "auth config ref not found",
			entry: newMCPServerEntry(testEntryGroupRef,
				&mcpv1beta1.ExternalAuthConfigRef{Name: "nonexistent-auth"},
				nil,
			),
			objects:   []client.Object{newMCPGroup(mcpv1beta1.MCPGroupPhaseReady)},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1beta1.ConditionTypeMCPServerEntryAuthConfigValidated, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryAuthConfigNotFound},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "CA bundle ref not found",
			entry: newMCPServerEntry(testEntryGroupRef,
				nil,
				&mcpv1beta1.CABundleSource{
					ConfigMapRef: &corev1.ConfigMapKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "nonexistent-cm"},
						Key:                  "ca.crt",
					},
				},
			),
			objects:   []client.Object{newMCPGroup(mcpv1beta1.MCPGroupPhaseReady)},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1beta1.ConditionTypeMCPServerEntryCABundleRefValidated, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryCABundleRefNotFound},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "SSRF - loopback IP rejected",
			entry: func() *mcpv1beta1.MCPServerEntry {
				e := newMCPServerEntry(testEntryGroupRef, nil, nil)
				e.Spec.RemoteURL = "http://127.0.0.1:8080/"
				return e
			}(),
			objects:   []client.Object{newMCPGroup(mcpv1beta1.MCPGroupPhaseReady)},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryRemoteURLValidated, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryRemoteURLInvalid},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "SSRF - metadata endpoint rejected",
			entry: func() *mcpv1beta1.MCPServerEntry {
				e := newMCPServerEntry(testEntryGroupRef, nil, nil)
				e.Spec.RemoteURL = "http://169.254.169.254/latest/meta-data/"
				return e
			}(),
			objects:   []client.Object{newMCPGroup(mcpv1beta1.MCPGroupPhaseReady)},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryRemoteURLValidated, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryRemoteURLInvalid},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryInvalid},
			},
		},
		{
			name: "SSRF - kubernetes.default.svc rejected",
			entry: func() *mcpv1beta1.MCPServerEntry {
				e := newMCPServerEntry(testEntryGroupRef, nil, nil)
				e.Spec.RemoteURL = "http://kubernetes.default.svc/"
				return e
			}(),
			objects:   []client.Object{newMCPGroup(mcpv1beta1.MCPGroupPhaseReady)},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseFailed,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryRemoteURLValidated, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryRemoteURLInvalid},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionFalse, mcpv1beta1.ConditionReasonMCPServerEntryInvalid},
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
				&mcpv1beta1.CABundleSource{ConfigMapRef: nil},
			),
			objects:   []client.Object{newMCPGroup(mcpv1beta1.MCPGroupPhaseReady)},
			wantPhase: mcpv1beta1.MCPServerEntryPhaseValid,
			conditions: []struct {
				condType string
				status   metav1.ConditionStatus
				reason   string
			}{
				{mcpv1beta1.ConditionTypeMCPServerEntryGroupRefValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryGroupRefValidated},
				{mcpv1beta1.ConditionTypeMCPServerEntryAuthConfigValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryAuthConfigNotConfigured},
				{mcpv1beta1.ConditionTypeMCPServerEntryCABundleRefValidated, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryCABundleRefNotConfigured},
				{mcpv1beta1.ConditionTypeMCPServerEntryValid, metav1.ConditionTrue, mcpv1beta1.ConditionReasonMCPServerEntryValid},
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
			var updatedEntry mcpv1beta1.MCPServerEntry
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

	group := &mcpv1beta1.MCPGroup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testEntryGroupRef,
			Namespace: testEntryNS,
		},
	}
	entry1 := &mcpv1beta1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "entry1", Namespace: testEntryNS},
		Spec:       mcpv1beta1.MCPServerEntrySpec{RemoteURL: "https://a.example.com", Transport: "sse", GroupRef: &mcpv1beta1.MCPGroupRef{Name: testEntryGroupRef}},
	}
	entry2 := &mcpv1beta1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "entry2", Namespace: testEntryNS},
		Spec:       mcpv1beta1.MCPServerEntrySpec{RemoteURL: "https://b.example.com", Transport: "sse", GroupRef: &mcpv1beta1.MCPGroupRef{Name: testEntryGroupRef}},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(group, entry1, entry2).
		WithStatusSubresource(&mcpv1beta1.MCPGroup{}, &mcpv1beta1.MCPServerEntry{}).
		WithIndex(&mcpv1beta1.MCPServer{}, "spec.groupRef", func(obj client.Object) []string {
			s := obj.(*mcpv1beta1.MCPServer)
			if s.Spec.GroupRef.GetName() == "" {
				return nil
			}
			return []string{s.Spec.GroupRef.GetName()}
		}).
		WithIndex(&mcpv1beta1.MCPRemoteProxy{}, "spec.groupRef", func(obj client.Object) []string {
			p := obj.(*mcpv1beta1.MCPRemoteProxy)
			if p.Spec.GroupRef.GetName() == "" {
				return nil
			}
			return []string{p.Spec.GroupRef.GetName()}
		}).
		WithIndex(&mcpv1beta1.MCPServerEntry{}, "spec.groupRef", func(obj client.Object) []string {
			e := obj.(*mcpv1beta1.MCPServerEntry)
			if e.Spec.GroupRef.GetName() == "" {
				return nil
			}
			return []string{e.Spec.GroupRef.GetName()}
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

	var updatedGroup mcpv1beta1.MCPGroup
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedGroup)
	require.NoError(t, err)

	assert.Equal(t, mcpv1beta1.MCPGroupPhaseReady, updatedGroup.Status.Phase)
	assert.Equal(t, int32(2), updatedGroup.Status.EntryCount)
	assert.ElementsMatch(t, []string{"entry1", "entry2"}, updatedGroup.Status.Entries)
}

// TestMCPGroupReconciler_EntryDeletionHandler verifies that updateReferencingEntriesOnDeletion
// sets the GroupRefValidated condition to False on all referencing MCPServerEntries.
func TestMCPGroupReconciler_EntryDeletionHandler(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := newEntryScheme(t)

	entry1 := &mcpv1beta1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "entry1", Namespace: testEntryNS},
		Spec:       mcpv1beta1.MCPServerEntrySpec{RemoteURL: "https://a.example.com", Transport: "sse", GroupRef: &mcpv1beta1.MCPGroupRef{Name: testEntryGroupRef}},
	}
	entry2 := &mcpv1beta1.MCPServerEntry{
		ObjectMeta: metav1.ObjectMeta{Name: "entry2", Namespace: testEntryNS},
		Spec:       mcpv1beta1.MCPServerEntrySpec{RemoteURL: "https://b.example.com", Transport: "sse", GroupRef: &mcpv1beta1.MCPGroupRef{Name: testEntryGroupRef}},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(entry1, entry2).
		WithStatusSubresource(&mcpv1beta1.MCPServerEntry{}).
		Build()

	r := &MCPGroupReconciler{Client: fakeClient}

	// Build the slice of entries as the controller would receive them
	entries := []mcpv1beta1.MCPServerEntry{*entry1, *entry2}

	r.updateReferencingEntriesOnDeletion(ctx, entries, testEntryGroupRef)

	// Verify both entries have the GroupRefValidated condition set to False
	for _, entryName := range []string{"entry1", "entry2"} {
		var updated mcpv1beta1.MCPServerEntry
		err := fakeClient.Get(ctx, types.NamespacedName{Name: entryName, Namespace: testEntryNS}, &updated)
		require.NoError(t, err, "should be able to fetch entry %s", entryName)

		assertCondition(t, updated.Status.Conditions,
			mcpv1beta1.ConditionTypeMCPServerEntryGroupRefValidated,
			metav1.ConditionFalse,
			mcpv1beta1.ConditionReasonMCPServerEntryGroupRefNotFound,
		)
	}
}
