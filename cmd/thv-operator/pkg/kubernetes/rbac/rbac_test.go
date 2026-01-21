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

func TestGetServiceAccount(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

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

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

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
					"app": "test",
				},
				Annotations: map[string]string{
					"annotation-key": "annotation-value",
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

		// Verify the service account was created correctly
		retrieved, err := client.GetServiceAccount(ctx, "new-sa", "default")
		require.NoError(t, err)
		assert.Equal(t, "new-sa", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, "test", retrieved.Labels["app"])
		assert.Equal(t, "annotation-value", retrieved.Annotations["annotation-key"])
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

	t.Run("preserves labels and annotations", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "labeled-sa",
				Namespace: "default",
				Labels: map[string]string{
					"environment": "production",
					"team":        "platform",
				},
				Annotations: map[string]string{
					"description": "test service account",
					"created-by":  "test-suite",
				},
			},
		}

		result, err := client.UpsertServiceAccount(ctx, serviceAccount)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify labels and annotations are preserved
		retrieved, err := client.GetServiceAccount(ctx, "labeled-sa", "default")
		require.NoError(t, err)
		assert.Equal(t, "production", retrieved.Labels["environment"])
		assert.Equal(t, "platform", retrieved.Labels["team"])
		assert.Equal(t, "test service account", retrieved.Annotations["description"])
		assert.Equal(t, "test-suite", retrieved.Annotations["created-by"])
	})
}

func TestUpsertServiceAccountWithOwnerReference(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	t.Run("successfully creates ServiceAccount with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "test-uid-12345",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-sa",
				Namespace: "default",
			},
		}

		result, err := client.UpsertServiceAccountWithOwnerReference(ctx, serviceAccount, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the service account was created with owner reference
		retrieved, err := client.GetServiceAccount(ctx, "owned-sa", "default")
		require.NoError(t, err)
		assert.Len(t, retrieved.OwnerReferences, 1)

		ownerRef := retrieved.OwnerReferences[0]
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "owner-cm", ownerRef.Name)
		assert.Equal(t, owner.UID, ownerRef.UID)
		assert.NotNil(t, ownerRef.Controller)
		assert.True(t, *ownerRef.Controller)
		assert.NotNil(t, ownerRef.BlockOwnerDeletion)
		assert.True(t, *ownerRef.BlockOwnerDeletion)
	})

	t.Run("successfully updates ServiceAccount with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "test-uid-67890",
			},
		}

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
		assert.Len(t, retrieved.OwnerReferences, 1)

		ownerRef := retrieved.OwnerReferences[0]
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "owner-cm", ownerRef.Name)
		assert.Equal(t, owner.UID, ownerRef.UID)
	})

	t.Run("owner reference fields set correctly", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "test-namespace",
				UID:       "unique-test-uid",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		serviceAccount := &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"managed-by": "test",
				},
			},
		}

		result, err := client.UpsertServiceAccountWithOwnerReference(ctx, serviceAccount, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify owner reference fields are set correctly
		retrieved, err := client.GetServiceAccount(ctx, "test-sa", "test-namespace")
		require.NoError(t, err)

		require.Len(t, retrieved.OwnerReferences, 1)
		ownerRef := retrieved.OwnerReferences[0]

		// Verify all owner reference fields
		assert.Equal(t, "v1", ownerRef.APIVersion)
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "test-owner", ownerRef.Name)
		assert.Equal(t, "unique-test-uid", string(ownerRef.UID))

		// Verify controller and block owner deletion flags
		require.NotNil(t, ownerRef.Controller)
		assert.True(t, *ownerRef.Controller)
		require.NotNil(t, ownerRef.BlockOwnerDeletion)
		assert.True(t, *ownerRef.BlockOwnerDeletion)

		// Verify labels were also set correctly
		assert.Equal(t, "test", retrieved.Labels["managed-by"])
	})

	t.Run("returns error when create fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "owner-uid",
			},
		}

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

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "owner-uid",
			},
		}

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
}

func TestGetRole(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

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

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

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
					"app": "test",
				},
				Annotations: map[string]string{
					"description": "test role",
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
			},
		}

		result, err := client.UpsertRole(ctx, role)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the role was created correctly
		retrieved, err := client.GetRole(ctx, "new-role", "default")
		require.NoError(t, err)
		assert.Equal(t, "new-role", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, "test", retrieved.Labels["app"])
		assert.Equal(t, "test role", retrieved.Annotations["description"])
		assert.Len(t, retrieved.Rules, 2)
		assert.Equal(t, []string{"pods"}, retrieved.Rules[0].Resources)
		assert.Equal(t, []string{"deployments"}, retrieved.Rules[1].Resources)
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

	t.Run("preserves labels, annotations, and Rules", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "labeled-role",
				Namespace: "default",
				Labels: map[string]string{
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
					Resources: []string{"configmaps"},
					Verbs:     []string{"get", "create", "update"},
				},
			},
		}

		result, err := client.UpsertRole(ctx, role)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify labels, annotations, and rules are preserved
		retrieved, err := client.GetRole(ctx, "labeled-role", "default")
		require.NoError(t, err)
		assert.Equal(t, "production", retrieved.Labels["environment"])
		assert.Equal(t, "platform", retrieved.Labels["team"])
		assert.Equal(t, "test role", retrieved.Annotations["description"])
		assert.Equal(t, "test-suite", retrieved.Annotations["created-by"])
		assert.Len(t, retrieved.Rules, 1)
		assert.Equal(t, []string{"get", "create", "update"}, retrieved.Rules[0].Verbs)
	})
}

func TestUpsertRoleWithOwnerReference(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	t.Run("successfully creates Role with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "test-uid-12345",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-role",
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

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the role was created with owner reference
		retrieved, err := client.GetRole(ctx, "owned-role", "default")
		require.NoError(t, err)
		assert.Len(t, retrieved.OwnerReferences, 1)

		ownerRef := retrieved.OwnerReferences[0]
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "owner-cm", ownerRef.Name)
		assert.Equal(t, owner.UID, ownerRef.UID)
		assert.NotNil(t, ownerRef.Controller)
		assert.True(t, *ownerRef.Controller)
		assert.NotNil(t, ownerRef.BlockOwnerDeletion)
		assert.True(t, *ownerRef.BlockOwnerDeletion)
	})

	t.Run("successfully updates Role with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "test-uid-67890",
			},
		}

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
		assert.Len(t, retrieved.OwnerReferences, 1)

		ownerRef := retrieved.OwnerReferences[0]
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "owner-cm", ownerRef.Name)
		assert.Equal(t, owner.UID, ownerRef.UID)
	})

	t.Run("owner reference fields set correctly", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "test-namespace",
				UID:       "unique-test-uid",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		role := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-role",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"managed-by": "test",
				},
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get"},
				},
			},
		}

		result, err := client.UpsertRoleWithOwnerReference(ctx, role, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify owner reference fields are set correctly
		retrieved, err := client.GetRole(ctx, "test-role", "test-namespace")
		require.NoError(t, err)

		require.Len(t, retrieved.OwnerReferences, 1)
		ownerRef := retrieved.OwnerReferences[0]

		// Verify all owner reference fields
		assert.Equal(t, "v1", ownerRef.APIVersion)
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "test-owner", ownerRef.Name)
		assert.Equal(t, "unique-test-uid", string(ownerRef.UID))

		// Verify controller and block owner deletion flags
		require.NotNil(t, ownerRef.Controller)
		assert.True(t, *ownerRef.Controller)
		require.NotNil(t, ownerRef.BlockOwnerDeletion)
		assert.True(t, *ownerRef.BlockOwnerDeletion)

		// Verify labels and rules were also set correctly
		assert.Equal(t, "test", retrieved.Labels["managed-by"])
		assert.Len(t, retrieved.Rules, 1)
	})

	t.Run("returns error when create fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "owner-uid",
			},
		}

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

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "owner-uid",
			},
		}

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

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

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

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

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

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	t.Run("successfully creates RoleBinding with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "test-uid-12345",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-rb",
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

		result, err := client.UpsertRoleBindingWithOwnerReference(ctx, roleBinding, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the role binding was created with owner reference
		retrieved, err := client.GetRoleBinding(ctx, "owned-rb", "default")
		require.NoError(t, err)
		assert.Len(t, retrieved.OwnerReferences, 1)

		ownerRef := retrieved.OwnerReferences[0]
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "owner-cm", ownerRef.Name)
		assert.Equal(t, owner.UID, ownerRef.UID)
		assert.NotNil(t, ownerRef.Controller)
		assert.True(t, *ownerRef.Controller)
		assert.NotNil(t, ownerRef.BlockOwnerDeletion)
		assert.True(t, *ownerRef.BlockOwnerDeletion)
	})

	t.Run("successfully updates RoleBinding with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "test-uid-67890",
			},
		}

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
		assert.Len(t, retrieved.OwnerReferences, 1)

		ownerRef := retrieved.OwnerReferences[0]
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "owner-cm", ownerRef.Name)
		assert.Equal(t, owner.UID, ownerRef.UID)
	})

	t.Run("owner reference fields set correctly", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "test-namespace",
				UID:       "unique-test-uid",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		roleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-rb",
				Namespace: "test-namespace",
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
					Namespace: "test-namespace",
				},
			},
		}

		result, err := client.UpsertRoleBindingWithOwnerReference(ctx, roleBinding, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify owner reference fields are set correctly
		retrieved, err := client.GetRoleBinding(ctx, "test-rb", "test-namespace")
		require.NoError(t, err)

		require.Len(t, retrieved.OwnerReferences, 1)
		ownerRef := retrieved.OwnerReferences[0]

		// Verify all owner reference fields
		assert.Equal(t, "v1", ownerRef.APIVersion)
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "test-owner", ownerRef.Name)
		assert.Equal(t, "unique-test-uid", string(ownerRef.UID))

		// Verify controller and block owner deletion flags
		require.NotNil(t, ownerRef.Controller)
		assert.True(t, *ownerRef.Controller)
		require.NotNil(t, ownerRef.BlockOwnerDeletion)
		assert.True(t, *ownerRef.BlockOwnerDeletion)

		// Verify labels and subjects were also set correctly
		assert.Equal(t, "test", retrieved.Labels["managed-by"])
		assert.Len(t, retrieved.Subjects, 1)
		assert.Equal(t, "test-sa", retrieved.Subjects[0].Name)
	})

	t.Run("returns error when create fails", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "owner-uid",
			},
		}

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

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "owner-uid",
			},
		}

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

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

	t.Run("creates all RBAC resources when none exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "default",
				UID:       "test-uid",
			},
		}

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

		err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
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

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "default",
				UID:       "test-uid",
			},
		}

		newRules := []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list"},
			},
		}

		err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
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

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "default",
				UID:       "test-uid",
			},
		}

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
		err := client.EnsureRBACResources(ctx, params)
		require.NoError(t, err)

		// Create resources second time - should not error
		err = client.EnsureRBACResources(ctx, params)
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

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "default",
				UID:       "test-uid",
			},
		}

		err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
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

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "default",
				UID:       "test-uid",
			},
		}

		err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
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

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "default",
				UID:       "test-uid",
			},
		}

		err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
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

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-owner",
				Namespace: "default",
				UID:       "test-uid",
			},
		}

		err := client.EnsureRBACResources(ctx, EnsureRBACResourcesParams{
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

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))

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
