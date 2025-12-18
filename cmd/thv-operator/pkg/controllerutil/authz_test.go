package controllerutil

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

func TestGenerateAuthzVolumeConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name               string
		authzConfig        *mcpv1alpha1.AuthzConfigRef
		resourceName       string
		expectVolumeMount  bool
		expectVolume       bool
		expectedVolumeName string
		expectedMountPath  string
	}{
		{
			name:              "Nil authz config",
			authzConfig:       nil,
			resourceName:      "test-resource",
			expectVolumeMount: false,
			expectVolume:      false,
		},
		{
			name: "ConfigMap type with nil ConfigMap ref",
			authzConfig: &mcpv1alpha1.AuthzConfigRef{
				Type:      mcpv1alpha1.AuthzConfigTypeConfigMap,
				ConfigMap: nil,
			},
			resourceName:      "test-resource",
			expectVolumeMount: false,
			expectVolume:      false,
		},
		{
			name: "ConfigMap type with default key",
			authzConfig: &mcpv1alpha1.AuthzConfigRef{
				Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
				ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
					Name: "my-authz-config",
				},
			},
			resourceName:       "test-resource",
			expectVolumeMount:  true,
			expectVolume:       true,
			expectedVolumeName: "authz-config",
			expectedMountPath:  "/etc/toolhive/authz",
		},
		{
			name: "ConfigMap type with custom key",
			authzConfig: &mcpv1alpha1.AuthzConfigRef{
				Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
				ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
					Name: "my-authz-config",
					Key:  "custom-authz.json",
				},
			},
			resourceName:       "test-resource",
			expectVolumeMount:  true,
			expectVolume:       true,
			expectedVolumeName: "authz-config",
			expectedMountPath:  "/etc/toolhive/authz",
		},
		{
			name: "Inline type with nil inline config",
			authzConfig: &mcpv1alpha1.AuthzConfigRef{
				Type:   mcpv1alpha1.AuthzConfigTypeInline,
				Inline: nil,
			},
			resourceName:      "test-resource",
			expectVolumeMount: false,
			expectVolume:      false,
		},
		{
			name: "Inline type with valid config",
			authzConfig: &mcpv1alpha1.AuthzConfigRef{
				Type: mcpv1alpha1.AuthzConfigTypeInline,
				Inline: &mcpv1alpha1.InlineAuthzConfig{
					Policies: []string{`permit(principal, action, resource);`},
				},
			},
			resourceName:       "test-resource",
			expectVolumeMount:  true,
			expectVolume:       true,
			expectedVolumeName: "authz-config",
			expectedMountPath:  "/etc/toolhive/authz",
		},
		{
			name: "Unknown type returns nil",
			authzConfig: &mcpv1alpha1.AuthzConfigRef{
				Type: "unknown",
			},
			resourceName:      "test-resource",
			expectVolumeMount: false,
			expectVolume:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			volumeMount, volume := GenerateAuthzVolumeConfig(tc.authzConfig, tc.resourceName)

			if tc.expectVolumeMount {
				require.NotNil(t, volumeMount)
				assert.Equal(t, tc.expectedVolumeName, volumeMount.Name)
				assert.Equal(t, tc.expectedMountPath, volumeMount.MountPath)
				assert.True(t, volumeMount.ReadOnly)
			} else {
				assert.Nil(t, volumeMount)
			}

			if tc.expectVolume {
				require.NotNil(t, volume)
				assert.Equal(t, tc.expectedVolumeName, volume.Name)
			} else {
				assert.Nil(t, volume)
			}
		})
	}
}

func TestGenerateAuthzVolumeConfigInlineConfigMapName(t *testing.T) {
	t.Parallel()

	// Test that inline config generates the correct ConfigMap name
	authzConfig := &mcpv1alpha1.AuthzConfigRef{
		Type: mcpv1alpha1.AuthzConfigTypeInline,
		Inline: &mcpv1alpha1.InlineAuthzConfig{
			Policies: []string{`permit(principal, action, resource);`},
		},
	}

	_, volume := GenerateAuthzVolumeConfig(authzConfig, "my-server")
	require.NotNil(t, volume)
	require.NotNil(t, volume.ConfigMap)
	assert.Equal(t, "my-server-authz-inline", volume.ConfigMap.Name)
}

func TestEnsureAuthzConfigMap(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	t.Run("Nil authz config returns nil", func(t *testing.T) {
		t.Parallel()

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		err := EnsureAuthzConfigMap(
			context.Background(),
			client,
			scheme,
			&mcpv1alpha1.MCPServer{},
			"default",
			"test-resource",
			nil,
			nil,
		)
		assert.NoError(t, err)
	})

	t.Run("ConfigMap type returns nil", func(t *testing.T) {
		t.Parallel()

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		authzConfig := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "my-config",
			},
		}

		err := EnsureAuthzConfigMap(
			context.Background(),
			client,
			scheme,
			&mcpv1alpha1.MCPServer{},
			"default",
			"test-resource",
			authzConfig,
			nil,
		)
		assert.NoError(t, err)
	})

	t.Run("Inline type without inline config returns nil", func(t *testing.T) {
		t.Parallel()

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		authzConfig := &mcpv1alpha1.AuthzConfigRef{
			Type:   mcpv1alpha1.AuthzConfigTypeInline,
			Inline: nil,
		}

		err := EnsureAuthzConfigMap(
			context.Background(),
			client,
			scheme,
			&mcpv1alpha1.MCPServer{},
			"default",
			"test-resource",
			authzConfig,
			nil,
		)
		assert.NoError(t, err)
	})

	t.Run("Inline type creates ConfigMap", func(t *testing.T) {
		t.Parallel()

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		owner := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-server",
				Namespace: "default",
				UID:       "test-uid",
			},
		}
		authzConfig := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeInline,
			Inline: &mcpv1alpha1.InlineAuthzConfig{
				Policies:     []string{`permit(principal, action, resource);`},
				EntitiesJSON: `[]`,
			},
		}
		labels := map[string]string{
			"app": "test",
		}

		err := EnsureAuthzConfigMap(
			context.Background(),
			client,
			scheme,
			owner,
			"default",
			"test-resource",
			authzConfig,
			labels,
		)
		require.NoError(t, err)

		// Verify the ConfigMap was created
		var cm corev1.ConfigMap
		err = client.Get(context.Background(), getKey("default", "test-resource-authz-inline"), &cm)
		require.NoError(t, err)
		assert.Equal(t, "test", cm.Labels["app"])
		assert.Contains(t, cm.Data, DefaultAuthzKey)

		// Verify the ConfigMap data contains the correct structure
		var data map[string]interface{}
		err = json.Unmarshal([]byte(cm.Data[DefaultAuthzKey]), &data)
		require.NoError(t, err)
		assert.Equal(t, "1.0", data["version"])
		assert.Equal(t, "cedarv1", data["type"])
	})

	t.Run("Inline type with empty EntitiesJSON defaults to empty array", func(t *testing.T) {
		t.Parallel()

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		owner := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-server",
				Namespace: "default",
				UID:       "test-uid-2",
			},
		}
		authzConfig := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeInline,
			Inline: &mcpv1alpha1.InlineAuthzConfig{
				Policies: []string{`permit(principal, action, resource);`},
				// EntitiesJSON is empty
			},
		}

		err := EnsureAuthzConfigMap(
			context.Background(),
			client,
			scheme,
			owner,
			"default",
			"test-resource-2",
			authzConfig,
			nil,
		)
		require.NoError(t, err)

		// Verify the ConfigMap was created
		var cm corev1.ConfigMap
		err = client.Get(context.Background(), getKey("default", "test-resource-2-authz-inline"), &cm)
		require.NoError(t, err)

		// Verify EntitiesJSON defaults to "[]"
		var data map[string]interface{}
		err = json.Unmarshal([]byte(cm.Data[DefaultAuthzKey]), &data)
		require.NoError(t, err)
		cedar, ok := data["cedar"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "[]", cedar["entities_json"])
	})
}

func TestAddAuthzConfigOptions(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	t.Run("Nil authz ref returns nil", func(t *testing.T) {
		t.Parallel()

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			nil,
			"default",
			nil,
			&options,
		)
		assert.NoError(t, err)
		assert.Empty(t, options)
	})

	t.Run("Inline type adds config", func(t *testing.T) {
		t.Parallel()

		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeInline,
			Inline: &mcpv1alpha1.InlineAuthzConfig{
				Policies:     []string{`permit(principal, action, resource);`},
				EntitiesJSON: `[]`,
			},
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			nil,
			"default",
			authzRef,
			&options,
		)
		require.NoError(t, err)
		assert.Len(t, options, 1)
	})

	t.Run("Inline type with nil inline config returns error", func(t *testing.T) {
		t.Parallel()

		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type:   mcpv1alpha1.AuthzConfigTypeInline,
			Inline: nil,
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			nil,
			"default",
			authzRef,
			&options,
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "inline authz config type specified but inline config is nil")
	})

	t.Run("ConfigMap type with nil ConfigMap ref returns error", func(t *testing.T) {
		t.Parallel()

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type:      mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: nil,
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			client,
			"default",
			authzRef,
			&options,
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "reference is missing name")
	})

	t.Run("ConfigMap type with empty name returns error", func(t *testing.T) {
		t.Parallel()

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "",
			},
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			client,
			"default",
			authzRef,
			&options,
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "reference is missing name")
	})

	t.Run("ConfigMap type with nil client returns error", func(t *testing.T) {
		t.Parallel()

		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "my-config",
			},
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			nil,
			"default",
			authzRef,
			&options,
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "kubernetes client is not configured")
	})

	t.Run("ConfigMap type with non-existent ConfigMap returns error", func(t *testing.T) {
		t.Parallel()

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "non-existent",
			},
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			client,
			"default",
			authzRef,
			&options,
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get Authz ConfigMap")
	})

	t.Run("ConfigMap type with missing key returns error", func(t *testing.T) {
		t.Parallel()

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "authz-config",
				Namespace: "default",
			},
			Data: map[string]string{
				"other-key": "some data",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "authz-config",
			},
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			client,
			"default",
			authzRef,
			&options,
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "is missing key")
	})

	t.Run("ConfigMap type with empty value returns error", func(t *testing.T) {
		t.Parallel()

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "authz-config-empty",
				Namespace: "default",
			},
			Data: map[string]string{
				DefaultAuthzKey: "   ",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "authz-config-empty",
			},
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			client,
			"default",
			authzRef,
			&options,
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "is empty")
	})

	t.Run("ConfigMap type with invalid config returns error", func(t *testing.T) {
		t.Parallel()

		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "authz-config-invalid",
				Namespace: "default",
			},
			Data: map[string]string{
				DefaultAuthzKey: "not valid json or yaml",
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "authz-config-invalid",
			},
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			client,
			"default",
			authzRef,
			&options,
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse authz config")
	})

	t.Run("ConfigMap type with valid config adds option", func(t *testing.T) {
		t.Parallel()

		validConfig := `{
			"version": "1.0",
			"type": "cedarv1",
			"cedar": {
				"policies": ["permit(principal, action, resource);"],
				"entities_json": "[]"
			}
		}`
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "authz-config-valid",
				Namespace: "default",
			},
			Data: map[string]string{
				DefaultAuthzKey: validConfig,
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "authz-config-valid",
			},
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			client,
			"default",
			authzRef,
			&options,
		)
		require.NoError(t, err)
		assert.Len(t, options, 1)
	})

	t.Run("ConfigMap type with custom key", func(t *testing.T) {
		t.Parallel()

		validConfig := `{
			"version": "1.0",
			"type": "cedarv1",
			"cedar": {
				"policies": ["permit(principal, action, resource);"],
				"entities_json": "[]"
			}
		}`
		cm := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "authz-config-custom-key",
				Namespace: "default",
			},
			Data: map[string]string{
				"custom.json": validConfig,
			},
		}
		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cm).Build()

		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "authz-config-custom-key",
				Key:  "custom.json",
			},
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			client,
			"default",
			authzRef,
			&options,
		)
		require.NoError(t, err)
		assert.Len(t, options, 1)
	})

	t.Run("Unknown type returns error", func(t *testing.T) {
		t.Parallel()

		authzRef := &mcpv1alpha1.AuthzConfigRef{
			Type: "unknown",
		}

		var options []runner.RunConfigBuilderOption
		err := AddAuthzConfigOptions(
			context.Background(),
			nil,
			"default",
			authzRef,
			&options,
		)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown authz config type")
	})
}

// Helper function to create a NamespacedName key
func getKey(namespace, name string) struct {
	Namespace string
	Name      string
} {
	return struct {
		Namespace string
		Name      string
	}{Namespace: namespace, Name: name}
}
