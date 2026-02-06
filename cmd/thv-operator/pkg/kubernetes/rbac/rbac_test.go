// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// setupTestScheme creates and initializes a test scheme with core and RBAC types.
func setupTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))
	return scheme
}

// createTestOwner creates a ConfigMap to use as an owner for testing owner references.
// All test owners are created in the "default" namespace.
func createTestOwner(name string, uid types.UID) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       uid,
		},
	}
}

// assertOwnerReference verifies that an object has exactly one owner reference matching the expected owner.
// It checks the APIVersion, Kind, Name, UID, and that Controller and BlockOwnerDeletion are set correctly.
// All test owners are ConfigMaps.
func assertOwnerReference(t *testing.T, refs []metav1.OwnerReference, owner client.Object) {
	t.Helper()
	require.Len(t, refs, 1)
	ownerRef := refs[0]
	assert.Equal(t, "v1", ownerRef.APIVersion)
	assert.Equal(t, "ConfigMap", ownerRef.Kind)
	assert.Equal(t, owner.GetName(), ownerRef.Name)
	assert.Equal(t, owner.GetUID(), ownerRef.UID)
	assert.NotNil(t, ownerRef.Controller)
	assert.True(t, *ownerRef.Controller)
	assert.NotNil(t, ownerRef.BlockOwnerDeletion)
	assert.True(t, *ownerRef.BlockOwnerDeletion)
}

func TestGetServiceAccount(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully retrieves existing ServiceAccount", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(serviceAccount).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.GetServiceAccount(ctx, "test-sa", "default")

		require.NoError(t, err)
		assert.NotNil(t, retrieved)
		assert.Equal(t, "test-sa", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
	})

	t.Run("returns error when ServiceAccount does not exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.GetServiceAccount(ctx, "non-existent", "default")

		require.Error(t, err)
		assert.Nil(t, retrieved)
		assert.Contains(t, err.Error(), "failed to get service account non-existent in namespace default")
	})

	t.Run("retrieves ServiceAccount from specific namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		sa1 := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "namespace1",
			},
		}

		sa2 := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "namespace2",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(sa1, sa2).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.GetServiceAccount(ctx, "test-sa", "namespace2")

		require.NoError(t, err)
		assert.Equal(t, "namespace2", retrieved.Namespace)
	})
}

func TestUpsertServiceAccount(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully creates new ServiceAccount", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		automountToken := true
		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "new-sa",
				Namespace: "default",
				Labels: map[string]string{
					"app":         "test",
					"environment": "production",
					"team":        "platform",
				},
				Annotations: map[string]string{
					"annotation-key": "annotation-value",
					"description":    "test service account",
					"created-by":     "test-suite",
				},
			},
			AutomountServiceAccountToken: &automountToken,
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "registry-secret"},
			},
			Secrets: []corev1.ObjectReference{
				{Name: "token-secret"},
			},
		}

		result, err := client.UpsertServiceAccount(ctx, serviceAccount)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the service account was created correctly with all fields preserved
		retrieved, err := client.GetServiceAccount(ctx, "new-sa", "default")
		require.NoError(t, err)
		assert.Equal(t, "new-sa", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, "test", retrieved.Labels["app"])
		assert.Equal(t, "production", retrieved.Labels["environment"])
		assert.Equal(t, "platform", retrieved.Labels["team"])
		assert.Equal(t, "annotation-value", retrieved.Annotations["annotation-key"])
		assert.Equal(t, "test service account", retrieved.Annotations["description"])
		assert.Equal(t, "test-suite", retrieved.Annotations["created-by"])
		require.NotNil(t, retrieved.AutomountServiceAccountToken)
		assert.True(t, *retrieved.AutomountServiceAccountToken)
		assert.Len(t, retrieved.ImagePullSecrets, 1)
		assert.Equal(t, "registry-secret", retrieved.ImagePullSecrets[0].Name)
		assert.Len(t, retrieved.Secrets, 1)
		assert.Equal(t, "token-secret", retrieved.Secrets[0].Name)
	})

	t.Run("successfully updates existing ServiceAccount", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		automountTokenOld := true
		existingSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-sa",
				Namespace: "default",
			},
			AutomountServiceAccountToken: &automountTokenOld,
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingSA).
			Build()

		client := NewClient(fakeClient, scheme)

		automountTokenNew := false
		updatedSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-sa",
				Namespace: "default",
				Labels: map[string]string{
					"updated": "true",
				},
			},
			AutomountServiceAccountToken: &automountTokenNew,
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "new-secret"},
			},
		}

		result, err := client.UpsertServiceAccount(ctx, updatedSA)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the service account was updated correctly
		retrieved, err := client.GetServiceAccount(ctx, "existing-sa", "default")
		require.NoError(t, err)
		assert.Equal(t, "true", retrieved.Labels["updated"])
		require.NotNil(t, retrieved.AutomountServiceAccountToken)
		assert.False(t, *retrieved.AutomountServiceAccountToken)
		assert.Len(t, retrieved.ImagePullSecrets, 1)
		assert.Equal(t, "new-secret", retrieved.ImagePullSecrets[0].Name)
	})
}

func TestUpsertServiceAccountWithOwnerReference(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully creates ServiceAccount with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "test-uid-12345")

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-sa",
				Namespace: "default",
				Labels: map[string]string{
					"managed-by": "test",
				},
			},
		}

		result, err := client.UpsertServiceAccountWithOwnerReference(ctx, serviceAccount, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the service account was created with owner reference
		retrieved, err := client.GetServiceAccount(ctx, "owned-sa", "default")
		require.NoError(t, err)
		assertOwnerReference(t, retrieved.OwnerReferences, owner)
		assert.Equal(t, "test", retrieved.Labels["managed-by"])
	})

	t.Run("successfully updates ServiceAccount with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "test-uid-67890")

		existingSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-sa",
				Namespace: "default",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner, existingSA).
			Build()

		client := NewClient(fakeClient, scheme)

		automountToken := true
		updatedSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-sa",
				Namespace: "default",
			},
			AutomountServiceAccountToken: &automountToken,
		}

		result, err := client.UpsertServiceAccountWithOwnerReference(ctx, updatedSA, owner)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the service account was updated with owner reference
		retrieved, err := client.GetServiceAccount(ctx, "existing-sa", "default")
		require.NoError(t, err)
		require.NotNil(t, retrieved.AutomountServiceAccountToken)
		assert.True(t, *retrieved.AutomountServiceAccountToken)
		assertOwnerReference(t, retrieved.OwnerReferences, owner)
	})

	t.Run("returns error when create fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "owner-uid")

		// Use interceptor to simulate create failure
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					return errors.New("permission denied")
				},
			}).
			Build()

		client := NewClient(fakeClient, scheme)

		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
		}

		result, err := client.UpsertServiceAccountWithOwnerReference(ctx, serviceAccount, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert service account test-sa in namespace default")
		assert.Contains(t, err.Error(), "permission denied")
		assert.Equal(t, "unchanged", string(result))
	})

	t.Run("returns error when update fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "owner-uid")

		existingSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-sa",
				Namespace: "default",
			},
		}

		// Use interceptor to simulate update failure
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingSA).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
					return errors.New("conflict error")
				},
			}).
			Build()

		client := NewClient(fakeClient, scheme)

		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-sa",
				Namespace: "default",
			},
		}

		result, err := client.UpsertServiceAccountWithOwnerReference(ctx, serviceAccount, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert service account existing-sa in namespace default")
		assert.Contains(t, err.Error(), "conflict error")
		assert.Equal(t, "unchanged", string(result))
	})

	t.Run("preserves existing Secrets and ImagePullSecrets when not specified (OpenShift compatibility)", func(t *testing.T) {
		// This test verifies the fix for https://github.com/stacklok/toolhive/issues/3622
		// On OpenShift, the openshift-controller-manager automatically manages Secrets and
		// ImagePullSecrets fields on ServiceAccounts. If we overwrite these with nil during
		// reconciliation, OpenShift creates new secrets while old ones become orphaned.
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "test-uid-openshift")

		// Simulate an existing ServiceAccount with OpenShift-managed secrets
		existingSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-sa",
				Namespace: "default",
				Labels: map[string]string{
					"original": "label",
				},
			},
			// These would be managed by OpenShift's controller-manager
			Secrets: []corev1.ObjectReference{
				{Name: "openshift-sa-token-abc123"},
			},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "openshift-sa-dockercfg-xyz789"},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner, existingSA).
			Build()

		client := NewClient(fakeClient, scheme)

		// Update the SA without specifying Secrets or ImagePullSecrets
		// This simulates what EnsureRBACResources does
		updatedSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "openshift-sa",
				Namespace: "default",
				Labels: map[string]string{
					"updated": "label",
				},
			},
			// Secrets and ImagePullSecrets are nil - they should be preserved
		}

		result, err := client.UpsertServiceAccountWithOwnerReference(ctx, updatedSA, owner)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the service account was updated but preserved existing secrets
		retrieved, err := client.GetServiceAccount(ctx, "openshift-sa", "default")
		require.NoError(t, err)

		// Labels should be updated
		assert.Equal(t, "label", retrieved.Labels["updated"])

		// Secrets should be preserved (not overwritten with nil)
		require.Len(t, retrieved.Secrets, 1, "Secrets should be preserved")
		assert.Equal(t, "openshift-sa-token-abc123", retrieved.Secrets[0].Name)

		// ImagePullSecrets should be preserved (not overwritten with nil)
		require.Len(t, retrieved.ImagePullSecrets, 1, "ImagePullSecrets should be preserved")
		assert.Equal(t, "openshift-sa-dockercfg-xyz789", retrieved.ImagePullSecrets[0].Name)

		// Owner reference should be set
		assertOwnerReference(t, retrieved.OwnerReferences, owner)
	})

	t.Run("overwrites Secrets and ImagePullSecrets when explicitly specified", func(t *testing.T) {
		// Verify that when Secrets/ImagePullSecrets ARE specified, they get applied
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "test-uid-explicit")

		existingSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "explicit-sa",
				Namespace: "default",
			},
			Secrets: []corev1.ObjectReference{
				{Name: "old-token"},
			},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "old-dockercfg"},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner, existingSA).
			Build()

		client := NewClient(fakeClient, scheme)

		// Update with explicit new secrets
		updatedSA := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "explicit-sa",
				Namespace: "default",
			},
			Secrets: []corev1.ObjectReference{
				{Name: "new-token"},
			},
			ImagePullSecrets: []corev1.LocalObjectReference{
				{Name: "new-dockercfg"},
			},
		}

		result, err := client.UpsertServiceAccountWithOwnerReference(ctx, updatedSA, owner)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		retrieved, err := client.GetServiceAccount(ctx, "explicit-sa", "default")
		require.NoError(t, err)

		// Secrets should be overwritten with the new values
		require.Len(t, retrieved.Secrets, 1)
		assert.Equal(t, "new-token", retrieved.Secrets[0].Name)

		// ImagePullSecrets should be overwritten with the new values
		require.Len(t, retrieved.ImagePullSecrets, 1)
		assert.Equal(t, "new-dockercfg", retrieved.ImagePullSecrets[0].Name)
	})
}

func TestGetRole(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully retrieves existing Role", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-role",
				Namespace: "default",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(role).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.GetRole(ctx, "test-role", "default")

		require.NoError(t, err)
		assert.NotNil(t, retrieved)
		assert.Equal(t, "test-role", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Len(t, retrieved.Rules, 1)
	})

	t.Run("returns error when Role does not exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.GetRole(ctx, "non-existent", "default")

		require.Error(t, err)
		assert.Nil(t, retrieved)
		assert.Contains(t, err.Error(), "failed to get role non-existent in namespace default")
	})

	t.Run("retrieves Role from specific namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		role1 := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-role",
				Namespace: "namespace1",
			},
		}

		role2 := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-role",
				Namespace: "namespace2",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(role1, role2).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.GetRole(ctx, "test-role", "namespace2")

		require.NoError(t, err)
		assert.Equal(t, "namespace2", retrieved.Namespace)
	})
}

func TestUpsertRole(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully creates new Role", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "new-role",
				Namespace: "default",
				Labels: map[string]string{
					"app":         "test",
					"environment": "production",
					"team":        "platform",
				},
				Annotations: map[string]string{
					"description": "test role",
					"created-by":  "test-suite",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
				},
				{
					APIGroups: []string{"apps"},
					Resources: []string{"deployments"},
					Verbs:     []string{"get", "update"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "create", "update"},
				},
			},
		}

		result, err := client.UpsertRole(ctx, role)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the role was created correctly with all fields preserved
		retrieved, err := client.GetRole(ctx, "new-role", "default")
		require.NoError(t, err)
		assert.Equal(t, "new-role", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, "test", retrieved.Labels["app"])
		assert.Equal(t, "production", retrieved.Labels["environment"])
		assert.Equal(t, "platform", retrieved.Labels["team"])
		assert.Equal(t, "test role", retrieved.Annotations["description"])
		assert.Equal(t, "test-suite", retrieved.Annotations["created-by"])
		assert.Len(t, retrieved.Rules, 3)
		assert.Equal(t, []string{"pods"}, retrieved.Rules[0].Resources)
		assert.Equal(t, []string{"get", "list"}, retrieved.Rules[0].Verbs)
		assert.Equal(t, []string{"deployments"}, retrieved.Rules[1].Resources)
		assert.Equal(t, []string{"get", "update"}, retrieved.Rules[1].Verbs)
		assert.Equal(t, []string{"configmaps"}, retrieved.Rules[2].Resources)
		assert.Equal(t, []string{"get", "create", "update"}, retrieved.Rules[2].Verbs)
	})

	t.Run("successfully updates existing Role", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		existingRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-role",
				Namespace: "default",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get"},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingRole).
			Build()

		client := NewClient(fakeClient, scheme)

		updatedRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-role",
				Namespace: "default",
				Labels: map[string]string{
					"updated": "true",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"services"},
					Verbs:     []string{"get"},
				},
			},
		}

		result, err := client.UpsertRole(ctx, updatedRole)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the role was updated correctly
		retrieved, err := client.GetRole(ctx, "existing-role", "default")
		require.NoError(t, err)
		assert.Equal(t, "true", retrieved.Labels["updated"])
		assert.Len(t, retrieved.Rules, 2)
		assert.Equal(t, []string{"get", "list", "watch"}, retrieved.Rules[0].Verbs)
		assert.Equal(t, []string{"services"}, retrieved.Rules[1].Resources)
	})
}

func TestUpsertRoleWithOwnerReference(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully creates Role with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "test-uid-12345")

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-role",
				Namespace: "default",
				Labels: map[string]string{
					"managed-by": "test",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get"},
				},
			},
		}

		result, err := client.UpsertRoleWithOwnerReference(ctx, role, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the role was created with owner reference
		retrieved, err := client.GetRole(ctx, "owned-role", "default")
		require.NoError(t, err)
		assertOwnerReference(t, retrieved.OwnerReferences, owner)
		assert.Equal(t, "test", retrieved.Labels["managed-by"])
		assert.Len(t, retrieved.Rules, 1)
	})

	t.Run("successfully updates Role with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "test-uid-67890")

		existingRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-role",
				Namespace: "default",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get"},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner, existingRole).
			Build()

		client := NewClient(fakeClient, scheme)

		updatedRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-role",
				Namespace: "default",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
				},
			},
		}

		result, err := client.UpsertRoleWithOwnerReference(ctx, updatedRole, owner)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the role was updated with owner reference
		retrieved, err := client.GetRole(ctx, "existing-role", "default")
		require.NoError(t, err)
		assert.Len(t, retrieved.Rules, 1)
		assert.Equal(t, []string{"get", "list"}, retrieved.Rules[0].Verbs)
		assertOwnerReference(t, retrieved.OwnerReferences, owner)
	})

	t.Run("returns error when create fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "owner-uid")

		// Use interceptor to simulate create failure
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					return errors.New("permission denied")
				},
			}).
			Build()

		client := NewClient(fakeClient, scheme)

		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-role",
				Namespace: "default",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get"},
				},
			},
		}

		result, err := client.UpsertRoleWithOwnerReference(ctx, role, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert role test-role in namespace default")
		assert.Contains(t, err.Error(), "permission denied")
		assert.Equal(t, "unchanged", string(result))
	})

	t.Run("returns error when update fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "owner-uid")

		existingRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-role",
				Namespace: "default",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get"},
				},
			},
		}

		// Use interceptor to simulate update failure
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingRole).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
					return errors.New("conflict error")
				},
			}).
			Build()

		client := NewClient(fakeClient, scheme)

		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-role",
				Namespace: "default",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get", "list"},
				},
			},
		}

		result, err := client.UpsertRoleWithOwnerReference(ctx, role, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert role existing-role in namespace default")
		assert.Contains(t, err.Error(), "conflict error")
		assert.Equal(t, "unchanged", string(result))
	})
}

func TestGetRoleBinding(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully retrieves existing RoleBinding", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-rb",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "test-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "test-sa",
					Namespace: "default",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(roleBinding).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.GetRoleBinding(ctx, "test-rb", "default")

		require.NoError(t, err)
		assert.NotNil(t, retrieved)
		assert.Equal(t, "test-rb", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, "test-role", retrieved.RoleRef.Name)
		assert.Len(t, retrieved.Subjects, 1)
	})

	t.Run("returns error when RoleBinding does not exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.GetRoleBinding(ctx, "non-existent", "default")

		require.Error(t, err)
		assert.Nil(t, retrieved)
		assert.Contains(t, err.Error(), "failed to get role binding non-existent in namespace default")
	})

	t.Run("retrieves RoleBinding from specific namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		rb1 := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-rb",
				Namespace: "namespace1",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "role1",
			},
		}

		rb2 := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-rb",
				Namespace: "namespace2",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "role2",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(rb1, rb2).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.GetRoleBinding(ctx, "test-rb", "namespace2")

		require.NoError(t, err)
		assert.Equal(t, "namespace2", retrieved.Namespace)
		assert.Equal(t, "role2", retrieved.RoleRef.Name)
	})
}

func TestUpsertRoleBinding(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully creates new RoleBinding", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "new-rb",
				Namespace: "default",
				Labels: map[string]string{
					"app": "test",
				},
				Annotations: map[string]string{
					"description": "test role binding",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "test-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "test-sa",
					Namespace: "default",
				},
				{
					Kind: "User",
					Name: "test-user",
				},
			},
		}

		result, err := client.UpsertRoleBinding(ctx, roleBinding)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the role binding was created correctly
		retrieved, err := client.GetRoleBinding(ctx, "new-rb", "default")
		require.NoError(t, err)
		assert.Equal(t, "new-rb", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, "test", retrieved.Labels["app"])
		assert.Equal(t, "test role binding", retrieved.Annotations["description"])
		assert.Equal(t, "test-role", retrieved.RoleRef.Name)
		assert.Equal(t, "Role", retrieved.RoleRef.Kind)
		assert.Equal(t, "rbac.authorization.k8s.io", retrieved.RoleRef.APIGroup)
		assert.Len(t, retrieved.Subjects, 2)
		assert.Equal(t, "test-sa", retrieved.Subjects[0].Name)
		assert.Equal(t, "test-user", retrieved.Subjects[1].Name)
	})

	t.Run("successfully updates existing RoleBinding Subjects only", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Set CreationTimestamp to simulate an existing object
		creationTime := metav1.Now()
		existingRB := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "existing-rb",
				Namespace:         "default",
				CreationTimestamp: creationTime,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "original-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "old-sa",
					Namespace: "default",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingRB).
			Build()

		client := NewClient(fakeClient, scheme)

		// Update with different subjects and different role ref
		updatedRB := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-rb",
				Namespace: "default",
				Labels: map[string]string{
					"updated": "true",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "new-role", // This should NOT be updated
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "new-sa",
					Namespace: "default",
				},
				{
					Kind: "User",
					Name: "new-user",
				},
			},
		}

		result, err := client.UpsertRoleBinding(ctx, updatedRB)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the role binding was updated correctly
		retrieved, err := client.GetRoleBinding(ctx, "existing-rb", "default")
		require.NoError(t, err)
		assert.Equal(t, "true", retrieved.Labels["updated"])
		// RoleRef should NOT have changed (immutability)
		assert.Equal(t, "original-role", retrieved.RoleRef.Name)
		// Subjects should be updated
		assert.Len(t, retrieved.Subjects, 2)
		assert.Equal(t, "new-sa", retrieved.Subjects[0].Name)
		assert.Equal(t, "new-user", retrieved.Subjects[1].Name)
	})

	t.Run("RoleRef is set on creation", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-rb",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "cluster-admin",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: "admin",
				},
			},
		}

		result, err := client.UpsertRoleBinding(ctx, roleBinding)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify RoleRef was set correctly
		retrieved, err := client.GetRoleBinding(ctx, "test-rb", "default")
		require.NoError(t, err)
		assert.Equal(t, "rbac.authorization.k8s.io", retrieved.RoleRef.APIGroup)
		assert.Equal(t, "ClusterRole", retrieved.RoleRef.Kind)
		assert.Equal(t, "cluster-admin", retrieved.RoleRef.Name)
	})

	t.Run("RoleRef is NOT changed on update", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Set CreationTimestamp to simulate an existing object
		creationTime := metav1.Now()
		existingRB := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "immutable-rb",
				Namespace:         "default",
				CreationTimestamp: creationTime,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "immutable-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: "user1",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingRB).
			Build()

		client := NewClient(fakeClient, scheme)

		// Attempt to update with different RoleRef
		updatedRB := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "immutable-rb",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "different-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: "user2",
				},
			},
		}

		result, err := client.UpsertRoleBinding(ctx, updatedRB)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify RoleRef was NOT changed (immutability preserved)
		retrieved, err := client.GetRoleBinding(ctx, "immutable-rb", "default")
		require.NoError(t, err)
		assert.Equal(t, "Role", retrieved.RoleRef.Kind)
		assert.Equal(t, "immutable-role", retrieved.RoleRef.Name)
		// But subjects should be updated
		assert.Equal(t, "user2", retrieved.Subjects[0].Name)
	})
}

func TestUpsertRoleBindingWithOwnerReference(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully creates RoleBinding with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "test-uid-12345")

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-rb",
				Namespace: "default",
				Labels: map[string]string{
					"managed-by": "test",
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "test-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "test-sa",
					Namespace: "default",
				},
			},
		}

		result, err := client.UpsertRoleBindingWithOwnerReference(ctx, roleBinding, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the role binding was created with owner reference
		retrieved, err := client.GetRoleBinding(ctx, "owned-rb", "default")
		require.NoError(t, err)
		assertOwnerReference(t, retrieved.OwnerReferences, owner)
		assert.Equal(t, "test", retrieved.Labels["managed-by"])
		assert.Len(t, retrieved.Subjects, 1)
		assert.Equal(t, "test-sa", retrieved.Subjects[0].Name)
	})

	t.Run("successfully updates RoleBinding with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "test-uid-67890")

		existingRB := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-rb",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "test-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: "old-user",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner, existingRB).
			Build()

		client := NewClient(fakeClient, scheme)

		updatedRB := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-rb",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "test-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: "new-user",
				},
			},
		}

		result, err := client.UpsertRoleBindingWithOwnerReference(ctx, updatedRB, owner)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the role binding was updated with owner reference
		retrieved, err := client.GetRoleBinding(ctx, "existing-rb", "default")
		require.NoError(t, err)
		assert.Len(t, retrieved.Subjects, 1)
		assert.Equal(t, "new-user", retrieved.Subjects[0].Name)
		assertOwnerReference(t, retrieved.OwnerReferences, owner)
	})

	t.Run("returns error when create fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "owner-uid")

		// Use interceptor to simulate create failure
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
					return errors.New("permission denied")
				},
			}).
			Build()

		client := NewClient(fakeClient, scheme)

		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-rb",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "test-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: "test-user",
				},
			},
		}

		result, err := client.UpsertRoleBindingWithOwnerReference(ctx, roleBinding, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert role binding test-rb in namespace default")
		assert.Contains(t, err.Error(), "permission denied")
		assert.Equal(t, "unchanged", string(result))
	})

	t.Run("returns error when update fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := createTestOwner("owner-cm", "owner-uid")

		existingRB := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-rb",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "test-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: "old-user",
				},
			},
		}

		// Use interceptor to simulate update failure
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingRB).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
					return errors.New("conflict error")
				},
			}).
			Build()

		client := NewClient(fakeClient, scheme)

		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-rb",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Role",
				Name:     "test-role",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: "new-user",
				},
			},
		}

		result, err := client.UpsertRoleBindingWithOwnerReference(ctx, roleBinding, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert role binding existing-rb in namespace default")
		assert.Contains(t, err.Error(), "conflict error")
		assert.Equal(t, "unchanged", string(result))
	})
}

func TestNewClient(t *testing.T) {
	t.Parallel()

	t.Run("creates client successfully", func(t *testing.T) {
		t.Parallel()

		scheme := runtime.NewScheme()
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		assert.NotNil(t, client)
	})
}

func TestEnsureRBACResources(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("creates all RBAC resources when none exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		owner := createTestOwner("test-owner", "test-uid")

		rules := []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list"},
			},
		}

		labels := map[string]string{
			"app": "test",
		}

		_, err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
			Name:      "test-rbac",
			Namespace: "default",
			Rules:     rules,
			Owner:     owner,
			Labels:    labels,
		})

		require.NoError(t, err)

		// Verify ServiceAccount was created
		sa := &corev1.ServiceAccount{}
		err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-rbac", Namespace: "default"}, sa)
		require.NoError(t, err)
		assert.Equal(t, "test-rbac", sa.Name)
		assert.Equal(t, "default", sa.Namespace)
		assert.Equal(t, labels, sa.Labels)

		// Verify Role was created
		role := &rbacv1.Role{}
		err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-rbac", Namespace: "default"}, role)
		require.NoError(t, err)
		assert.Equal(t, "test-rbac", role.Name)
		assert.Equal(t, "default", role.Namespace)
		assert.Equal(t, rules, role.Rules)
		assert.Equal(t, labels, role.Labels)

		// Verify RoleBinding was created
		rb := &rbacv1.RoleBinding{}
		err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-rbac", Namespace: "default"}, rb)
		require.NoError(t, err)
		assert.Equal(t, "test-rbac", rb.Name)
		assert.Equal(t, "default", rb.Namespace)
		assert.Equal(t, labels, rb.Labels)
		assert.Equal(t, "test-rbac", rb.RoleRef.Name)
		assert.Len(t, rb.Subjects, 1)
		assert.Equal(t, "ServiceAccount", rb.Subjects[0].Kind)
		assert.Equal(t, "test-rbac", rb.Subjects[0].Name)
	})

	t.Run("updates existing RBAC resources", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		existingRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-rbac",
				Namespace: "default",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"configmaps"},
					Verbs:     []string{"get"},
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingRole).
			Build()

		client := NewClient(fakeClient, scheme)

		owner := createTestOwner("test-owner", "test-uid")

		newRules := []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list"},
			},
		}

		_, err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
			Name:      "test-rbac",
			Namespace: "default",
			Rules:     newRules,
			Owner:     owner,
		})

		require.NoError(t, err)

		// Verify Role was updated
		role := &rbacv1.Role{}
		err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-rbac", Namespace: "default"}, role)
		require.NoError(t, err)
		assert.Equal(t, newRules, role.Rules)
	})

	t.Run("is idempotent", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		owner := createTestOwner("test-owner", "test-uid")

		rules := []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list"},
			},
		}

		params := EnsureRBACResourcesParams{
			Name:      "test-rbac",
			Namespace: "default",
			Rules:     rules,
			Owner:     owner,
		}

		// Create resources first time
		_, err := client.EnsureRBACResources(ctx, params)
		require.NoError(t, err)

		// Create resources second time - should not error
		_, err = client.EnsureRBACResources(ctx, params)
		require.NoError(t, err)
	})

	t.Run("returns error when ServiceAccount creation fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(
					ctx context.Context,
					client client.WithWatch,
					obj client.Object,
					opts ...client.CreateOption,
				) error {
					if _, ok := obj.(*corev1.ServiceAccount); ok {
						return errors.New("service account creation failed")
					}
					return client.Create(ctx, obj, opts...)
				},
			}).
			Build()

		client := NewClient(fakeClient, scheme)

		owner := createTestOwner("test-owner", "test-uid")

		_, err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
			Name:      "test-rbac",
			Namespace: "default",
			Rules:     []rbacv1.PolicyRule{},
			Owner:     owner,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to ensure service account")
	})

	t.Run("returns error when Role creation fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(
					ctx context.Context,
					client client.WithWatch,
					obj client.Object,
					opts ...client.CreateOption,
				) error {
					if _, ok := obj.(*rbacv1.Role); ok {
						return errors.New("role creation failed")
					}
					return client.Create(ctx, obj, opts...)
				},
			}).
			Build()

		client := NewClient(fakeClient, scheme)

		owner := createTestOwner("test-owner", "test-uid")

		_, err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
			Name:      "test-rbac",
			Namespace: "default",
			Rules:     []rbacv1.PolicyRule{},
			Owner:     owner,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to ensure role")
	})

	t.Run("returns error when RoleBinding creation fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithInterceptorFuncs(interceptor.Funcs{
				Create: func(
					ctx context.Context,
					client client.WithWatch,
					obj client.Object,
					opts ...client.CreateOption,
				) error {
					if _, ok := obj.(*rbacv1.RoleBinding); ok {
						return errors.New("rolebinding creation failed")
					}
					return client.Create(ctx, obj, opts...)
				},
			}).
			Build()

		client := NewClient(fakeClient, scheme)

		owner := createTestOwner("test-owner", "test-uid")

		_, err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
			Name:      "test-rbac",
			Namespace: "default",
			Rules:     []rbacv1.PolicyRule{},
			Owner:     owner,
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to ensure role binding")
	})

	t.Run("works without labels", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		owner := createTestOwner("test-owner", "test-uid")

		_, err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
			Name:      "test-rbac",
			Namespace: "default",
			Rules:     []rbacv1.PolicyRule{},
			Owner:     owner,
			// Labels intentionally omitted
		})

		require.NoError(t, err)

		// Verify resources were created without labels
		sa := &corev1.ServiceAccount{}
		err = fakeClient.Get(ctx, types.NamespacedName{Name: "test-rbac", Namespace: "default"}, sa)
		require.NoError(t, err)
		assert.Nil(t, sa.Labels)
	})
}

func TestGetAllRBACResources(t *testing.T) {
	t.Parallel()

	scheme := setupTestScheme(t)

	t.Run("successfully retrieves all RBAC resources", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Create test resources
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
		}
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get"},
				},
			},
		}
		rb := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: RBACAPIGroup,
				Kind:     "Role",
				Name:     "test-sa",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      "test-sa",
					Namespace: "default",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(sa, role, rb).
			Build()

		rbacClient := NewClient(fakeClient, scheme)

		// Get all resources
		gotSA, gotRole, gotRB, err := rbacClient.GetAllRBACResources(ctx, "test-sa", "default")
		require.NoError(t, err)

		// Verify all resources were retrieved
		assert.Equal(t, "test-sa", gotSA.Name)
		assert.Equal(t, "default", gotSA.Namespace)
		assert.Equal(t, "test-sa", gotRole.Name)
		assert.Equal(t, "default", gotRole.Namespace)
		assert.Equal(t, role.Rules, gotRole.Rules)
		assert.Equal(t, "test-sa", gotRB.Name)
		assert.Equal(t, "default", gotRB.Namespace)
		assert.Equal(t, rb.RoleRef, gotRB.RoleRef)
	})

	t.Run("returns error when ServiceAccount not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		rbacClient := NewClient(fakeClient, scheme)

		_, _, _, err := rbacClient.GetAllRBACResources(ctx, "nonexistent", "default")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "service account")
	})

	t.Run("returns error when Role not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Only create ServiceAccount
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(sa).
			Build()

		rbacClient := NewClient(fakeClient, scheme)

		_, _, _, err := rbacClient.GetAllRBACResources(ctx, "test-sa", "default")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "role")
	})

	t.Run("returns error when RoleBinding not found", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Create ServiceAccount and Role but not RoleBinding
		sa := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
		}
		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "default",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(sa, role).
			Build()

		rbacClient := NewClient(fakeClient, scheme)

		_, _, _, err := rbacClient.GetAllRBACResources(ctx, "test-sa", "default")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "role binding")
	})
}
