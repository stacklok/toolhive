package secrets

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestGet(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	t.Run("successfully retrieves existing secret", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key1": []byte("value1"),
				"key2": []byte("value2"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.Get(ctx, "test-secret", "default")

		require.NoError(t, err)
		assert.NotNil(t, retrieved)
		assert.Equal(t, "test-secret", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, []byte("value1"), retrieved.Data["key1"])
		assert.Equal(t, []byte("value2"), retrieved.Data["key2"])
	})

	t.Run("returns error when secret does not exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.Get(ctx, "non-existent", "default")

		require.Error(t, err)
		assert.Nil(t, retrieved)
		assert.Contains(t, err.Error(), "failed to get secret non-existent in namespace default")
	})

	t.Run("retrieves secret from specific namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		secret1 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "namespace1",
			},
			Data: map[string][]byte{
				"data": []byte("namespace1-data"),
			},
		}

		secret2 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "namespace2",
			},
			Data: map[string][]byte{
				"data": []byte("namespace2-data"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret1, secret2).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.Get(ctx, "test-secret", "namespace2")

		require.NoError(t, err)
		assert.Equal(t, "namespace2", retrieved.Namespace)
		assert.Equal(t, []byte("namespace2-data"), retrieved.Data["data"])
	})
}

func TestGetValue(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	t.Run("successfully retrieves secret value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"password": []byte("super-secret-password"),
				"username": []byte("admin"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret).
			Build()

		client := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-secret",
			},
			Key: "password",
		}

		value, err := client.GetValue(ctx, "default", secretRef)

		require.NoError(t, err)
		assert.Equal(t, "super-secret-password", value)
	})

	t.Run("returns error when secret does not exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "non-existent-secret",
			},
			Key: "password",
		}

		value, err := client.GetValue(ctx, "default", secretRef)

		require.Error(t, err)
		assert.Empty(t, value)
		assert.Contains(t, err.Error(), "failed to get secret non-existent-secret")
	})

	t.Run("returns error when key does not exist in secret", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"password": []byte("super-secret-password"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret).
			Build()

		client := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-secret",
			},
			Key: "non-existent-key",
		}

		value, err := client.GetValue(ctx, "default", secretRef)

		require.Error(t, err)
		assert.Empty(t, value)
		assert.Contains(t, err.Error(), "key non-existent-key not found in secret test-secret")
	})

	t.Run("retrieves value from correct namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		secret1 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "namespace1",
			},
			Data: map[string][]byte{
				"password": []byte("password1"),
			},
		}

		secret2 := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "namespace2",
			},
			Data: map[string][]byte{
				"password": []byte("password2"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret1, secret2).
			Build()

		client := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-secret",
			},
			Key: "password",
		}

		value, err := client.GetValue(ctx, "namespace2", secretRef)

		require.NoError(t, err)
		assert.Equal(t, "password2", value)
	})

	t.Run("handles empty secret value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"empty-key": []byte(""),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret).
			Build()

		client := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-secret",
			},
			Key: "empty-key",
		}

		value, err := client.GetValue(ctx, "default", secretRef)

		require.NoError(t, err)
		assert.Empty(t, value)
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

func TestUpsert(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	t.Run("successfully creates a new secret", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "new-secret",
				Namespace: "default",
				Labels: map[string]string{
					"app": "test",
				},
				Annotations: map[string]string{
					"annotation-key": "annotation-value",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"username": []byte("admin"),
				"password": []byte("secret123"),
			},
		}

		result, err := client.Upsert(ctx, secret)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the secret was created correctly
		retrieved, err := client.Get(ctx, "new-secret", "default")
		require.NoError(t, err)
		assert.Equal(t, "new-secret", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, []byte("admin"), retrieved.Data["username"])
		assert.Equal(t, []byte("secret123"), retrieved.Data["password"])
		assert.Equal(t, corev1.SecretTypeOpaque, retrieved.Type)
	})

	t.Run("successfully updates an existing secret", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		existingSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key1": []byte("old-value"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingSecret).
			Build()

		client := NewClient(fakeClient, scheme)

		updatedSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key1": []byte("new-value"),
				"key2": []byte("additional-value"),
			},
		}

		result, err := client.Upsert(ctx, updatedSecret)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the secret was updated correctly
		retrieved, err := client.Get(ctx, "existing-secret", "default")
		require.NoError(t, err)
		assert.Equal(t, []byte("new-value"), retrieved.Data["key1"])
		assert.Equal(t, []byte("additional-value"), retrieved.Data["key2"])
	})

	t.Run("preserves labels and annotations", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "labeled-secret",
				Namespace: "default",
				Labels: map[string]string{
					"environment": "production",
					"team":        "platform",
				},
				Annotations: map[string]string{
					"description": "test secret",
					"created-by":  "test-suite",
					"version":     "1.0",
				},
			},
			Data: map[string][]byte{
				"data": []byte("value"),
			},
		}

		result, err := client.Upsert(ctx, secret)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify labels and annotations are preserved
		retrieved, err := client.Get(ctx, "labeled-secret", "default")
		require.NoError(t, err)
		assert.Equal(t, "production", retrieved.Labels["environment"])
		assert.Equal(t, "platform", retrieved.Labels["team"])
		assert.Equal(t, "test secret", retrieved.Annotations["description"])
		assert.Equal(t, "test-suite", retrieved.Annotations["created-by"])
		assert.Equal(t, "1.0", retrieved.Annotations["version"])
	})

	t.Run("handles secret type correctly", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		testCases := []struct {
			name       string
			secretType corev1.SecretType
		}{
			{
				name:       "opaque-secret",
				secretType: corev1.SecretTypeOpaque,
			},
			{
				name:       "dockercfg-secret",
				secretType: corev1.SecretTypeDockercfg,
			},
			{
				name:       "tls-secret",
				secretType: corev1.SecretTypeTLS,
			},
			{
				name:       "basic-auth-secret",
				secretType: corev1.SecretTypeBasicAuth,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tc.name,
						Namespace: "default",
					},
					Type: tc.secretType,
					Data: map[string][]byte{
						"key": []byte("value"),
					},
				}

				result, err := client.Upsert(ctx, secret)

				require.NoError(t, err)
				assert.Equal(t, "created", string(result))

				// Verify the secret type is set correctly
				retrieved, err := client.Get(ctx, tc.name, "default")
				require.NoError(t, err)
				assert.Equal(t, tc.secretType, retrieved.Type)
			})
		}
	})
}

func TestUpsertWithOwnerReference(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	t.Run("successfully creates secret with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Create an owner object (using ConfigMap as a simple owner)
		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-configmap",
				Namespace: "default",
				UID:       "test-uid-12345",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner).
			Build()

		client := NewClient(fakeClient, scheme)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key": []byte("value"),
			},
		}

		result, err := client.UpsertWithOwnerReference(ctx, secret, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the secret was created with owner reference
		retrieved, err := client.Get(ctx, "owned-secret", "default")
		require.NoError(t, err)
		assert.Len(t, retrieved.OwnerReferences, 1)

		ownerRef := retrieved.OwnerReferences[0]
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "owner-configmap", ownerRef.Name)
		assert.Equal(t, owner.UID, ownerRef.UID)
		assert.NotNil(t, ownerRef.Controller)
		assert.True(t, *ownerRef.Controller)
		assert.NotNil(t, ownerRef.BlockOwnerDeletion)
		assert.True(t, *ownerRef.BlockOwnerDeletion)
	})

	t.Run("successfully updates secret with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Create an owner object
		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-configmap",
				Namespace: "default",
				UID:       "test-uid-67890",
			},
		}

		// Create existing secret without owner reference
		existingSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key": []byte("old-value"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner, existingSecret).
			Build()

		client := NewClient(fakeClient, scheme)

		updatedSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key": []byte("new-value"),
			},
		}

		result, err := client.UpsertWithOwnerReference(ctx, updatedSecret, owner)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the secret was updated with owner reference
		retrieved, err := client.Get(ctx, "existing-secret", "default")
		require.NoError(t, err)
		assert.Equal(t, []byte("new-value"), retrieved.Data["key"])
		assert.Len(t, retrieved.OwnerReferences, 1)

		ownerRef := retrieved.OwnerReferences[0]
		assert.Equal(t, "ConfigMap", ownerRef.Kind)
		assert.Equal(t, "owner-configmap", ownerRef.Name)
		assert.Equal(t, owner.UID, ownerRef.UID)
	})

	t.Run("owner reference is set correctly", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Create an owner object with specific metadata
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

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"managed-by": "test",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"test-key": []byte("test-value"),
			},
		}

		result, err := client.UpsertWithOwnerReference(ctx, secret, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify owner reference fields are set correctly
		retrieved, err := client.Get(ctx, "test-secret", "test-namespace")
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

		// Verify the secret data and labels were also set correctly
		assert.Equal(t, []byte("test-value"), retrieved.Data["test-key"])
		assert.Equal(t, "test", retrieved.Labels["managed-by"])
		assert.Equal(t, corev1.SecretTypeOpaque, retrieved.Type)
	})

	t.Run("preserves existing data when updating with owner reference", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "default",
				UID:       "owner-uid",
			},
		}

		// Create secret with initial data
		existingSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "update-test-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"initial-key": []byte("initial-value"),
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner, existingSecret).
			Build()

		secretsClient := NewClient(fakeClient, scheme)

		// Update with new data and owner reference
		updatedSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "update-test-secret",
				Namespace: "default",
				Labels: map[string]string{
					"updated": "true",
				},
			},
			Data: map[string][]byte{
				"updated-key": []byte("updated-value"),
			},
		}

		result, err := secretsClient.UpsertWithOwnerReference(ctx, updatedSecret, owner)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the secret was updated correctly
		retrieved, err := secretsClient.Get(ctx, "update-test-secret", "default")
		require.NoError(t, err)

		// Data should be replaced with new data
		assert.Equal(t, []byte("updated-value"), retrieved.Data["updated-key"])
		assert.NotContains(t, retrieved.Data, "initial-key")

		// Labels should be set
		assert.Equal(t, "true", retrieved.Labels["updated"])

		// Owner reference should be set
		require.Len(t, retrieved.OwnerReferences, 1)
		assert.Equal(t, "owner-cm", retrieved.OwnerReferences[0].Name)
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

		secretsClient := NewClient(fakeClient, scheme)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key": []byte("value"),
			},
		}

		result, err := secretsClient.UpsertWithOwnerReference(ctx, secret, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert secret test-secret in namespace default")
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

		existingSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key": []byte("old-value"),
			},
		}

		// Use interceptor to simulate update failure
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingSecret).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
					return errors.New("conflict error")
				},
			}).
			Build()

		secretsClient := NewClient(fakeClient, scheme)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key": []byte("new-value"),
			},
		}

		result, err := secretsClient.UpsertWithOwnerReference(ctx, secret, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert secret existing-secret in namespace default")
		assert.Contains(t, err.Error(), "conflict error")
		assert.Equal(t, "unchanged", string(result))
	})

	t.Run("returns error when owner is in different namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Owner in different namespace than secret
		owner := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owner-cm",
				Namespace: "other-namespace",
				UID:       "owner-uid",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		secretsClient := NewClient(fakeClient, scheme)

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "default",
			},
			Data: map[string][]byte{
				"key": []byte("value"),
			},
		}

		result, err := secretsClient.UpsertWithOwnerReference(ctx, secret, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to set controller reference")
		assert.Equal(t, "unchanged", string(result))
	})
}
