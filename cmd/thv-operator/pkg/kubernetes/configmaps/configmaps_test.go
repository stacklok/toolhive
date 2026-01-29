// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package configmaps

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

	t.Run("successfully retrieves existing configmap", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.Get(ctx, "test-configmap", "default")

		require.NoError(t, err)
		assert.NotNil(t, retrieved)
		assert.Equal(t, "test-configmap", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, "value1", retrieved.Data["key1"])
		assert.Equal(t, "value2", retrieved.Data["key2"])
	})

	t.Run("returns error when configmap does not exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.Get(ctx, "non-existent", "default")

		require.Error(t, err)
		assert.Nil(t, retrieved)
		assert.Contains(t, err.Error(), "failed to get configmap non-existent in namespace default")
	})

	t.Run("retrieves configmap from specific namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		configMap1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "namespace1",
			},
			Data: map[string]string{
				"data": "namespace1-data",
			},
		}

		configMap2 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "namespace2",
			},
			Data: map[string]string{
				"data": "namespace2-data",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap1, configMap2).
			Build()

		client := NewClient(fakeClient, scheme)
		retrieved, err := client.Get(ctx, "test-configmap", "namespace2")

		require.NoError(t, err)
		assert.Equal(t, "namespace2", retrieved.Namespace)
		assert.Equal(t, "namespace2-data", retrieved.Data["data"])
	})
}

func TestGetValue(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	t.Run("successfully retrieves configmap value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"foo1": "bar1",
				"foo2": "bar2",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		client := NewClient(fakeClient, scheme)
		configMapRef := corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-configmap",
			},
			Key: "foo1",
		}

		value, err := client.GetValue(ctx, "default", configMapRef)

		require.NoError(t, err)
		assert.Equal(t, "bar1", value)
	})

	t.Run("returns error when configmap does not exist", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)
		configMapRef := corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "non-existent-configmap",
			},
			Key: "foo1",
		}

		value, err := client.GetValue(ctx, "default", configMapRef)

		require.Error(t, err)
		assert.Empty(t, value)
		assert.Contains(t, err.Error(), "failed to get configmap non-existent-configmap")
	})

	t.Run("returns error when key does not exist in configmap", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"foo1": "bar1",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		client := NewClient(fakeClient, scheme)
		configMapRef := corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-configmap",
			},
			Key: "non-existent-key",
		}

		value, err := client.GetValue(ctx, "default", configMapRef)

		require.Error(t, err)
		assert.Empty(t, value)
		assert.Contains(t, err.Error(), "key non-existent-key not found in configmap test-configmap")
	})

	t.Run("retrieves value from correct namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		configMap1 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "namespace1",
			},
			Data: map[string]string{
				"foo1": "bar1",
			},
		}

		configMap2 := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "namespace2",
			},
			Data: map[string]string{
				"foo2": "bar2",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap1, configMap2).
			Build()

		client := NewClient(fakeClient, scheme)
		configMapRef := corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-configmap",
			},
			Key: "foo2",
		}

		value, err := client.GetValue(ctx, "namespace2", configMapRef)

		require.NoError(t, err)
		assert.Equal(t, "bar2", value)
	})

	t.Run("handles empty configmap value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"empty-key": "",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(configMap).
			Build()

		client := NewClient(fakeClient, scheme)
		configMapRef := corev1.ConfigMapKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: "test-configmap",
			},
			Key: "empty-key",
		}

		value, err := client.GetValue(ctx, "default", configMapRef)

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

	t.Run("successfully creates a new configmap", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "new-configmap",
				Namespace: "default",
				Labels: map[string]string{
					"app": "test",
				},
				Annotations: map[string]string{
					"annotation-key": "annotation-value",
				},
			},
			Data: map[string]string{
				"foo1": "bar1",
				"foo2": "bar2",
			},
		}

		result, err := client.Upsert(ctx, configMap)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the configmap was created correctly
		retrieved, err := client.Get(ctx, "new-configmap", "default")
		require.NoError(t, err)
		assert.Equal(t, "new-configmap", retrieved.Name)
		assert.Equal(t, "default", retrieved.Namespace)
		assert.Equal(t, "bar1", retrieved.Data["foo1"])
		assert.Equal(t, "bar2", retrieved.Data["foo2"])
	})

	t.Run("successfully updates an existing configmap", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		existingConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key1": "old-value",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingConfigMap).
			Build()

		client := NewClient(fakeClient, scheme)

		updatedConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key1": "new-value",
				"key2": "additional-value",
			},
		}

		result, err := client.Upsert(ctx, updatedConfigMap)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the configmap was updated correctly
		retrieved, err := client.Get(ctx, "existing-configmap", "default")
		require.NoError(t, err)
		assert.Equal(t, "new-value", retrieved.Data["key1"])
		assert.Equal(t, "additional-value", retrieved.Data["key2"])
	})

	t.Run("preserves labels and annotations", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			Build()

		client := NewClient(fakeClient, scheme)

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "labeled-configmap",
				Namespace: "default",
				Labels: map[string]string{
					"environment": "production",
					"team":        "platform",
				},
				Annotations: map[string]string{
					"description": "test configmap",
					"created-by":  "test-suite",
					"version":     "1.0",
				},
			},
			Data: map[string]string{
				"data": "value",
			},
		}

		result, err := client.Upsert(ctx, configMap)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify labels and annotations are preserved
		retrieved, err := client.Get(ctx, "labeled-configmap", "default")
		require.NoError(t, err)
		assert.Equal(t, "production", retrieved.Labels["environment"])
		assert.Equal(t, "platform", retrieved.Labels["team"])
		assert.Equal(t, "test configmap", retrieved.Annotations["description"])
		assert.Equal(t, "test-suite", retrieved.Annotations["created-by"])
		assert.Equal(t, "1.0", retrieved.Annotations["version"])
	})
}

func TestUpsertWithOwnerReference(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	t.Run("successfully creates configmap with owner reference", func(t *testing.T) {
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

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "owned-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key": "value",
			},
		}

		result, err := client.UpsertWithOwnerReference(ctx, configMap, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify the configmap was created with owner reference
		retrieved, err := client.Get(ctx, "owned-configmap", "default")
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

	t.Run("successfully updates configmap with owner reference", func(t *testing.T) {
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

		// Create existing configmap without owner reference
		existingConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key": "old-value",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner, existingConfigMap).
			Build()

		client := NewClient(fakeClient, scheme)

		updatedConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key": "new-value",
			},
		}

		result, err := client.UpsertWithOwnerReference(ctx, updatedConfigMap, owner)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the configmap was updated with owner reference
		retrieved, err := client.Get(ctx, "existing-configmap", "default")
		require.NoError(t, err)
		assert.Equal(t, "new-value", retrieved.Data["key"])
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

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "test-namespace",
				Labels: map[string]string{
					"managed-by": "test",
				},
			},
			Data: map[string]string{
				"test-key": "test-value",
			},
		}

		result, err := client.UpsertWithOwnerReference(ctx, configMap, owner)

		require.NoError(t, err)
		assert.Equal(t, "created", string(result))

		// Verify owner reference fields are set correctly
		retrieved, err := client.Get(ctx, "test-configmap", "test-namespace")
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

		// Verify the configmap data and labels were also set correctly
		assert.Equal(t, "test-value", retrieved.Data["test-key"])
		assert.Equal(t, "test", retrieved.Labels["managed-by"])
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

		// Create configmap with initial data
		existingConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "update-test-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"initial-key": "initial-value",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(owner, existingConfigMap).
			Build()

		configMapsClient := NewClient(fakeClient, scheme)

		// Update with new data and owner reference
		updatedConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "update-test-configmap",
				Namespace: "default",
				Labels: map[string]string{
					"updated": "true",
				},
			},
			Data: map[string]string{
				"updated-key": "updated-value",
			},
		}

		result, err := configMapsClient.UpsertWithOwnerReference(ctx, updatedConfigMap, owner)

		require.NoError(t, err)
		assert.Equal(t, "updated", string(result))

		// Verify the configmap was updated correctly
		retrieved, err := configMapsClient.Get(ctx, "update-test-configmap", "default")
		require.NoError(t, err)

		// Data should be replaced with new data
		assert.Equal(t, "updated-value", retrieved.Data["updated-key"])
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

		configMapsClient := NewClient(fakeClient, scheme)

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key": "value",
			},
		}

		result, err := configMapsClient.UpsertWithOwnerReference(ctx, configMap, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert configmap test-configmap in namespace default")
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

		existingConfigMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key": "old-value",
			},
		}

		// Use interceptor to simulate update failure
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(existingConfigMap).
			WithInterceptorFuncs(interceptor.Funcs{
				Update: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.UpdateOption) error {
					return errors.New("conflict error")
				},
			}).
			Build()

		configMapsClient := NewClient(fakeClient, scheme)

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "existing-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key": "new-value",
			},
		}

		result, err := configMapsClient.UpsertWithOwnerReference(ctx, configMap, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to upsert configmap existing-configmap in namespace default")
		assert.Contains(t, err.Error(), "conflict error")
		assert.Equal(t, "unchanged", string(result))
	})

	t.Run("returns error when owner is in different namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// Owner in different namespace than configmap
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

		configMapsClient := NewClient(fakeClient, scheme)

		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-configmap",
				Namespace: "default",
			},
			Data: map[string]string{
				"key": "value",
			},
		}

		result, err := configMapsClient.UpsertWithOwnerReference(ctx, configMap, owner)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to set controller reference")
		assert.Equal(t, "unchanged", string(result))
	})
}
