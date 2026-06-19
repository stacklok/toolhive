// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/runner"
)

func TestGenerateAuthzVolumeConfig(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name               string
		authzConfig        *mcpv1beta1.AuthzConfigRef
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
			authzConfig: &mcpv1beta1.AuthzConfigRef{
				Type:      mcpv1beta1.AuthzConfigTypeConfigMap,
				ConfigMap: nil,
			},
			resourceName:      "test-resource",
			expectVolumeMount: false,
			expectVolume:      false,
		},
		{
			name: "ConfigMap type with default key",
			authzConfig: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeConfigMap,
				ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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
			authzConfig: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeConfigMap,
				ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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
			authzConfig: &mcpv1beta1.AuthzConfigRef{
				Type:   mcpv1beta1.AuthzConfigTypeInline,
				Inline: nil,
			},
			resourceName:      "test-resource",
			expectVolumeMount: false,
			expectVolume:      false,
		},
		{
			name: "Inline type with valid config",
			authzConfig: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeInline,
				Inline: &mcpv1beta1.InlineAuthzConfig{
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
			authzConfig: &mcpv1beta1.AuthzConfigRef{
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
	authzConfig := &mcpv1beta1.AuthzConfigRef{
		Type: mcpv1beta1.AuthzConfigTypeInline,
		Inline: &mcpv1beta1.InlineAuthzConfig{
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

	scheme := testutil.NewScheme(t)

	t.Run("Nil authz config returns nil", func(t *testing.T) {
		t.Parallel()

		client := fake.NewClientBuilder().WithScheme(scheme).Build()
		err := EnsureAuthzConfigMap(
			context.Background(),
			client,
			scheme,
			&mcpv1beta1.MCPServer{},
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
		authzConfig := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
				Name: "my-config",
			},
		}

		err := EnsureAuthzConfigMap(
			context.Background(),
			client,
			scheme,
			&mcpv1beta1.MCPServer{},
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
		authzConfig := &mcpv1beta1.AuthzConfigRef{
			Type:   mcpv1beta1.AuthzConfigTypeInline,
			Inline: nil,
		}

		err := EnsureAuthzConfigMap(
			context.Background(),
			client,
			scheme,
			&mcpv1beta1.MCPServer{},
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
		owner := v1beta1test.NewMCPServer("test-server", "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
				m.UID = "test-uid"
			}),
		)
		authzConfig := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeInline,
			Inline: &mcpv1beta1.InlineAuthzConfig{
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
		owner := v1beta1test.NewMCPServer("test-server", "default",
			v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) {
				m.UID = "test-uid-2"
			}),
		)
		authzConfig := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeInline,
			Inline: &mcpv1beta1.InlineAuthzConfig{
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

	scheme := testutil.NewScheme(t)

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

		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeInline,
			Inline: &mcpv1beta1.InlineAuthzConfig{
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

		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type:   mcpv1beta1.AuthzConfigTypeInline,
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
		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type:      mcpv1beta1.AuthzConfigTypeConfigMap,
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
		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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

		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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
		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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

		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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

		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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

		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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

		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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

		authzRef := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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

		authzRef := &mcpv1beta1.AuthzConfigRef{
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

// TestBuildInlineCedarAuthzConfig verifies that the JWT-claim mapping fields
// declared on the parent AuthzConfigRef (GroupClaimName, RoleClaimName,
// GroupEntityType) are threaded into the cedar.ConfigOptions of the inline
// runner path. The CRD docstring on AuthzConfigRef promises spec-over-ConfigMap
// override semantics for these fields; this test locks the inline half of that
// promise on the MCPServer / MCPRemoteProxy runner code path, where they were
// previously dropped on the floor.
func TestBuildInlineCedarAuthzConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		authzRef  *mcpv1beta1.AuthzConfigRef
		wantOpts  *cedar.ConfigOptions
		expectErr string
	}{
		{
			name:      "nil ref returns error",
			authzRef:  nil,
			expectErr: "inline authz config type specified but inline config is nil",
		},
		{
			name: "nil Inline subtree returns error",
			authzRef: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeInline,
			},
			expectErr: "inline authz config type specified but inline config is nil",
		},
		{
			name: "claim-mapping fields flow into cedar options",
			authzRef: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeInline,
				Inline: &mcpv1beta1.InlineAuthzConfig{
					Policies:     []string{`permit(principal, action, resource);`},
					EntitiesJSON: `[{"uid":{"type":"ClaimGroup","id":"eng"}}]`,
				},
				GroupClaimName:  "groups",
				RoleClaimName:   "roles",
				GroupEntityType: "ClaimGroup",
			},
			wantOpts: &cedar.ConfigOptions{
				Policies:        []string{`permit(principal, action, resource);`},
				EntitiesJSON:    `[{"uid":{"type":"ClaimGroup","id":"eng"}}]`,
				GroupClaimName:  "groups",
				RoleClaimName:   "roles",
				GroupEntityType: "ClaimGroup",
			},
		},
		{
			name: "unset claim-mapping fields stay empty",
			authzRef: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeInline,
				Inline: &mcpv1beta1.InlineAuthzConfig{
					Policies: []string{`permit(principal, action, resource);`},
				},
			},
			wantOpts: &cedar.ConfigOptions{
				Policies: []string{`permit(principal, action, resource);`},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := BuildInlineCedarAuthzConfig(tt.authzRef)
			if tt.expectErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectErr)
				return
			}
			require.NoError(t, err)
			got, err := ExtractCedarAuthzOptions(cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOpts.Policies, got.Policies)
			assert.Equal(t, tt.wantOpts.EntitiesJSON, got.EntitiesJSON)
			assert.Equal(t, tt.wantOpts.GroupClaimName, got.GroupClaimName)
			assert.Equal(t, tt.wantOpts.RoleClaimName, got.RoleClaimName)
			assert.Equal(t, tt.wantOpts.GroupEntityType, got.GroupEntityType)
		})
	}
}

// TestApplyClaimMappingOverrides verifies the spec-over-ConfigMap precedence
// the CRD docstring on AuthzConfigRef promises for JWT-claim mapping fields.
// The ConfigMap payload contributes a baseline; non-empty spec-level fields on
// AuthzConfigRef override field-by-field. Empty spec-level fields preserve the
// ConfigMap value (no accidental clobbering).
func TestApplyClaimMappingOverrides(t *testing.T) {
	t.Parallel()

	baseCfg := func(t *testing.T, opts cedar.ConfigOptions) *authz.Config {
		t.Helper()
		cfg, err := authz.NewConfig(cedar.Config{
			Version: AuthzConfigVersion,
			Type:    cedar.ConfigType,
			Options: &opts,
		})
		require.NoError(t, err)
		return cfg
	}

	tests := []struct {
		name     string
		cfg      func(t *testing.T) *authz.Config
		authzRef *mcpv1beta1.AuthzConfigRef
		wantOpts cedar.ConfigOptions
	}{
		{
			name: "nil ref leaves cedar options untouched",
			cfg: func(t *testing.T) *authz.Config {
				t.Helper()
				return baseCfg(t, cedar.ConfigOptions{
					Policies:       []string{`permit(principal, action, resource);`},
					GroupClaimName: "cm-groups",
				})
			},
			authzRef: nil,
			wantOpts: cedar.ConfigOptions{
				Policies:       []string{`permit(principal, action, resource);`},
				GroupClaimName: "cm-groups",
			},
		},
		{
			name: "no overrides on ref leaves cedar options untouched",
			cfg: func(t *testing.T) *authz.Config {
				t.Helper()
				return baseCfg(t, cedar.ConfigOptions{
					Policies:        []string{`permit(principal, action, resource);`},
					GroupClaimName:  "cm-groups",
					GroupEntityType: "CMGroup",
				})
			},
			authzRef: &mcpv1beta1.AuthzConfigRef{Type: mcpv1beta1.AuthzConfigTypeConfigMap},
			wantOpts: cedar.ConfigOptions{
				Policies:        []string{`permit(principal, action, resource);`},
				GroupClaimName:  "cm-groups",
				GroupEntityType: "CMGroup",
			},
		},
		{
			name: "spec GroupClaimName overrides ConfigMap value",
			cfg: func(t *testing.T) *authz.Config {
				t.Helper()
				return baseCfg(t, cedar.ConfigOptions{
					Policies:       []string{`permit(principal, action, resource);`},
					GroupClaimName: "cm-groups",
				})
			},
			authzRef: &mcpv1beta1.AuthzConfigRef{
				Type:           mcpv1beta1.AuthzConfigTypeConfigMap,
				GroupClaimName: "spec-groups",
			},
			wantOpts: cedar.ConfigOptions{
				Policies:       []string{`permit(principal, action, resource);`},
				GroupClaimName: "spec-groups",
			},
		},
		{
			name: "all three spec fields override; unset fields keep ConfigMap value",
			cfg: func(t *testing.T) *authz.Config {
				t.Helper()
				return baseCfg(t, cedar.ConfigOptions{
					Policies:        []string{`permit(principal, action, resource);`},
					GroupClaimName:  "cm-groups",
					RoleClaimName:   "cm-roles",
					GroupEntityType: "CMGroup",
				})
			},
			authzRef: &mcpv1beta1.AuthzConfigRef{
				Type:            mcpv1beta1.AuthzConfigTypeConfigMap,
				GroupClaimName:  "spec-groups",
				GroupEntityType: "SpecGroup",
				// RoleClaimName intentionally left unset to verify CM fallback.
			},
			wantOpts: cedar.ConfigOptions{
				Policies:        []string{`permit(principal, action, resource);`},
				GroupClaimName:  "spec-groups",
				RoleClaimName:   "cm-roles",
				GroupEntityType: "SpecGroup",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.cfg(t)
			got, err := ApplyClaimMappingOverrides(cfg, tt.authzRef)
			require.NoError(t, err)
			require.NotNil(t, got)
			opts, err := ExtractCedarAuthzOptions(got)
			require.NoError(t, err)
			assert.Equal(t, tt.wantOpts.Policies, opts.Policies)
			assert.Equal(t, tt.wantOpts.GroupClaimName, opts.GroupClaimName)
			assert.Equal(t, tt.wantOpts.RoleClaimName, opts.RoleClaimName)
			assert.Equal(t, tt.wantOpts.GroupEntityType, opts.GroupEntityType)
		})
	}
}

// TestApplyClaimMappingOverrides_NilConfig is a defense-in-depth check that the
// helper does not panic on a nil cfg input. Callers on the runner path should
// not pass nil, but the override is invoked unconditionally after the loader
// so the contract is "safe to call".
func TestApplyClaimMappingOverrides_NilConfig(t *testing.T) {
	t.Parallel()
	got, err := ApplyClaimMappingOverrides(nil, &mcpv1beta1.AuthzConfigRef{
		Type:           mcpv1beta1.AuthzConfigTypeConfigMap,
		GroupClaimName: "spec-groups",
	})
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestExtractCedarAuthzOptions covers the unwrap helper used by both the
// runner path (via ApplyClaimMappingOverrides) and the vMCP converter. The
// helper localises the dependency on pkg/authz/authorizers/cedar so callers
// outside pkg/authz do not have to import it directly.
func TestExtractCedarAuthzOptions(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns the embedded cedar options", func(t *testing.T) {
		t.Parallel()
		cfg, err := authz.NewConfig(cedar.Config{
			Version: AuthzConfigVersion,
			Type:    cedar.ConfigType,
			Options: &cedar.ConfigOptions{
				Policies:     []string{`permit(principal, action, resource);`},
				EntitiesJSON: `[]`,
			},
		})
		require.NoError(t, err)
		opts, err := ExtractCedarAuthzOptions(cfg)
		require.NoError(t, err)
		require.NotNil(t, opts)
		assert.Equal(t, []string{`permit(principal, action, resource);`}, opts.Policies)
	})

	t.Run("nil config returns error", func(t *testing.T) {
		t.Parallel()
		opts, err := ExtractCedarAuthzOptions(nil)
		require.Error(t, err)
		assert.Nil(t, opts)
	})
}
