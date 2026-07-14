// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
)

func TestMapAuthzConfigMapToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	const ns = "default"
	const cmName = "shared-authz"

	// vmcpWithConfigMapRef references cmName via configMap.
	vmcpWithConfigMapRef := v1beta1test.NewVirtualMCPServer("vmcp-cm", ns,
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
			Type: "oidc",
			AuthzConfig: &mcpv1beta1.AuthzConfigRef{
				Type:      mcpv1beta1.AuthzConfigTypeConfigMap,
				ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{Name: cmName, Key: "authz.json"},
			},
		}),
	)
	// vmcpDifferentCM references a different ConfigMap by the same type.
	vmcpDifferentCM := v1beta1test.NewVirtualMCPServer("vmcp-other-cm", ns,
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
			Type: "oidc",
			AuthzConfig: &mcpv1beta1.AuthzConfigRef{
				Type:      mcpv1beta1.AuthzConfigTypeConfigMap,
				ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{Name: "other-authz"},
			},
		}),
	)
	// vmcpInline uses inline authz — same name on Inline.something should not match.
	vmcpInline := v1beta1test.NewVirtualMCPServer("vmcp-inline", ns,
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
			Type: "oidc",
			AuthzConfig: &mcpv1beta1.AuthzConfigRef{
				Type:   mcpv1beta1.AuthzConfigTypeInline,
				Inline: &mcpv1beta1.InlineAuthzConfig{Policies: []string{"permit(principal, action, resource);"}},
			},
		}),
	)
	// vmcpNoIncomingAuth has no incoming auth configured.
	vmcpNoIncomingAuth := v1beta1test.NewVirtualMCPServer("vmcp-no-auth", ns)
	// vmcpInOtherNamespace references the same name in a different namespace.
	vmcpInOtherNamespace := v1beta1test.NewVirtualMCPServer("vmcp-other-ns", "other",
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
			Type: "oidc",
			AuthzConfig: &mcpv1beta1.AuthzConfigRef{
				Type:      mcpv1beta1.AuthzConfigTypeConfigMap,
				ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{Name: cmName},
			},
		}),
	)

	scheme := testutil.NewScheme(t)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			vmcpWithConfigMapRef,
			vmcpDifferentCM,
			vmcpInline,
			vmcpNoIncomingAuth,
			vmcpInOtherNamespace,
		).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	tests := []struct {
		name     string
		obj      client.Object
		expected []types.NamespacedName
	}{
		{
			name: "configMap matched by name in same namespace",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: ns},
			},
			expected: []types.NamespacedName{{Name: "vmcp-cm", Namespace: ns}},
		},
		{
			name: "configMap in other namespace only matches vmcp in that namespace",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: "other"},
			},
			expected: []types.NamespacedName{{Name: "vmcp-other-ns", Namespace: "other"}},
		},
		{
			name: "configMap not referenced by any vmcp",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "unreferenced", Namespace: ns},
			},
			expected: nil,
		},
		{
			name:     "non-configMap object returns nil",
			obj:      &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: ns}},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requests := reconciler.mapAuthzConfigMapToVirtualMCPServer(t.Context(), tt.obj)
			require.Len(t, requests, len(tt.expected))
			got := make([]types.NamespacedName, 0, len(requests))
			for _, r := range requests {
				got = append(got, r.NamespacedName)
			}
			assert.ElementsMatch(t, tt.expected, got)
		})
	}
}

func TestConfigMapDataChangedPredicate(t *testing.T) {
	t.Parallel()

	p := configMapDataChangedPredicate()

	baseCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "default"},
		Data:       map[string]string{"authz.json": `{"policies":[]}`},
	}

	t.Run("create event is admitted", func(t *testing.T) {
		t.Parallel()
		assert.True(t, p.Create(event.CreateEvent{Object: baseCM}))
	})

	t.Run("delete event is admitted", func(t *testing.T) {
		t.Parallel()
		assert.True(t, p.Delete(event.DeleteEvent{Object: baseCM}))
	})

	t.Run("generic event is rejected", func(t *testing.T) {
		t.Parallel()
		assert.False(t, p.Generic(event.GenericEvent{Object: baseCM}))
	})

	t.Run("update event with data change is admitted", func(t *testing.T) {
		t.Parallel()
		oldCM := baseCM.DeepCopy()
		newCM := baseCM.DeepCopy()
		newCM.Data = map[string]string{"authz.json": `{"policies":["permit(principal, action, resource);"]}`}
		assert.True(t, p.Update(event.UpdateEvent{ObjectOld: oldCM, ObjectNew: newCM}))
	})

	t.Run("update event with binary data change is admitted", func(t *testing.T) {
		t.Parallel()
		oldCM := baseCM.DeepCopy()
		newCM := baseCM.DeepCopy()
		newCM.BinaryData = map[string][]byte{"blob": []byte("x")}
		assert.True(t, p.Update(event.UpdateEvent{ObjectOld: oldCM, ObjectNew: newCM}))
	})

	t.Run("update event with label-only change is rejected", func(t *testing.T) {
		t.Parallel()
		oldCM := baseCM.DeepCopy()
		newCM := baseCM.DeepCopy()
		newCM.Labels = map[string]string{"updated": "true"}
		assert.False(t, p.Update(event.UpdateEvent{ObjectOld: oldCM, ObjectNew: newCM}))
	})

	t.Run("update event with annotation-only change is rejected", func(t *testing.T) {
		t.Parallel()
		oldCM := baseCM.DeepCopy()
		newCM := baseCM.DeepCopy()
		newCM.Annotations = map[string]string{"updated": "true"}
		assert.False(t, p.Update(event.UpdateEvent{ObjectOld: oldCM, ObjectNew: newCM}))
	})

	t.Run("update event with no changes is rejected", func(t *testing.T) {
		t.Parallel()
		oldCM := baseCM.DeepCopy()
		newCM := baseCM.DeepCopy()
		assert.False(t, p.Update(event.UpdateEvent{ObjectOld: oldCM, ObjectNew: newCM}))
	})

	t.Run("update event on non-configMap object is rejected", func(t *testing.T) {
		t.Parallel()
		oldObj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"}}
		newObj := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "default"}}
		assert.False(t, p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}))
	})
}
