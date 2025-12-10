package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetSecret(t *testing.T) {
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

		kubeClient := NewClient(fakeClient, scheme)
		retrieved, err := kubeClient.GetSecret(ctx, "test-secret", "default")

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

		kubeClient := NewClient(fakeClient, scheme)
		retrieved, err := kubeClient.GetSecret(ctx, "non-existent", "default")

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

		kubeClient := NewClient(fakeClient, scheme)
		retrieved, err := kubeClient.GetSecret(ctx, "test-secret", "namespace2")

		require.NoError(t, err)
		assert.Equal(t, "namespace2", retrieved.Namespace)
		assert.Equal(t, []byte("namespace2-data"), retrieved.Data["data"])
	})
}

func TestGetSecretValue(t *testing.T) {
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

		kubeClient := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-secret",
			},
			Key: "password",
		}

		value, err := kubeClient.GetSecretValue(ctx, "default", secretRef)

		require.NoError(t, err)
		assert.Equal(t, "super-secret-password", value)
	})

	t.Run("returns error when secret does not exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		kubeClient := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "non-existent-secret",
			},
			Key: "password",
		}

		value, err := kubeClient.GetSecretValue(ctx, "default", secretRef)

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

		kubeClient := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-secret",
			},
			Key: "non-existent-key",
		}

		value, err := kubeClient.GetSecretValue(ctx, "default", secretRef)

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

		kubeClient := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-secret",
			},
			Key: "password",
		}

		value, err := kubeClient.GetSecretValue(ctx, "namespace2", secretRef)

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

		kubeClient := NewClient(fakeClient, scheme)
		secretRef := corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-secret",
			},
			Key: "empty-key",
		}

		value, err := kubeClient.GetSecretValue(ctx, "default", secretRef)

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

		kubeClient := NewClient(fakeClient, scheme)

		assert.NotNil(t, kubeClient)
		assert.Equal(t, fakeClient, kubeClient.GetClient())
	})
}
