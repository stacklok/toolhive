// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestEnsureAuthzConfigMap(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	tests := []struct {
		name               string
		mcpServer          *mcpv1beta1.MCPServer
		expectConfigMap    bool
		expectedConfigData string
	}{
		{
			name:            "no authz config",
			mcpServer:       v1beta1test.NewMCPServer("test-server", "test-namespace", v1beta1test.WithImage("test-image")),
			expectConfigMap: false,
		},
		{
			name: "configmap authz config (no inline ConfigMap needed)",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1beta1.AuthzConfigRef{
						Type: mcpv1beta1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
							Name: "external-authz-config",
						},
					},
				},
			},
			expectConfigMap: false,
		},
		{
			name: "inline authz config",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1beta1.AuthzConfigRef{
						Type: mcpv1beta1.AuthzConfigTypeInline,
						Inline: &mcpv1beta1.InlineAuthzConfig{
							Policies: []string{
								`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
							},
							EntitiesJSON: `[{"uid": {"type": "User", "id": "alice"}, "attrs": {}, "parents": []}]`,
						},
					},
				},
			},
			expectConfigMap:    true,
			expectedConfigData: `{"cedar":{"entities_json":"[{\"uid\": {\"type\": \"User\", \"id\": \"alice\"}, \"attrs\": {}, \"parents\": []}]","policies":["permit(principal, action == Action::\"call_tool\", resource == Tool::\"weather\");"]},"type":"cedarv1","version":"1.0"}`,
		},
		{
			name: "inline authz config with default entities",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1beta1.AuthzConfigRef{
						Type: mcpv1beta1.AuthzConfigTypeInline,
						Inline: &mcpv1beta1.InlineAuthzConfig{
							Policies: []string{
								`permit(principal, action, resource);`,
							},
							// EntitiesJSON not specified, should default to "[]"
						},
					},
				},
			},
			expectConfigMap:    true,
			expectedConfigData: `{"cedar":{"entities_json":"[]","policies":["permit(principal, action, resource);"]},"type":"cedarv1","version":"1.0"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.mcpServer).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			err := reconciler.ensureAuthzConfigMap(ctx, tt.mcpServer)
			require.NoError(t, err)

			if tt.expectConfigMap {
				// Check that ConfigMap was created
				configMapName := tt.mcpServer.Name + "-authz-inline"
				configMap := &corev1.ConfigMap{}
				err := fakeClient.Get(ctx, client.ObjectKey{
					Name:      configMapName,
					Namespace: tt.mcpServer.Namespace,
				}, configMap)
				require.NoError(t, err)

				// Verify ConfigMap content
				require.Contains(t, configMap.Data, "authz.json")
				assert.Equal(t, tt.expectedConfigData, configMap.Data["authz.json"])

				// Verify owner reference
				require.Len(t, configMap.OwnerReferences, 1)
				assert.Equal(t, tt.mcpServer.Name, configMap.OwnerReferences[0].Name)
				assert.Equal(t, "MCPServer", configMap.OwnerReferences[0].Kind)

				// Verify specific labels
				assert.Equal(t, "inline", configMap.Labels["toolhive.stacklok.io/authz"])
				assert.Equal(t, "true", configMap.Labels["toolhive"])
				assert.Equal(t, tt.mcpServer.Name, configMap.Labels["toolhive-name"])
			}
		})
	}
}

func TestEnsureAuthzConfigMap_Updates(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	// Create MCPServer with initial inline authz config
	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "test-namespace",
			UID:       "test-uid",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			Image: "test-image",
			AuthzConfig: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeInline,
				Inline: &mcpv1beta1.InlineAuthzConfig{
					Policies: []string{
						`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
					},
					EntitiesJSON: `[]`,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(mcpServer).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Step 1: Create the ConfigMap
	err := reconciler.ensureAuthzConfigMap(ctx, mcpServer)
	require.NoError(t, err)

	// Verify ConfigMap was created with initial data
	configMapName := mcpServer.Name + "-authz-inline"
	configMap := &corev1.ConfigMap{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Name:      configMapName,
		Namespace: mcpServer.Namespace,
	}, configMap)
	require.NoError(t, err)

	initialData := configMap.Data["authz.json"]
	require.Contains(t, initialData, `call_tool`)
	require.Contains(t, initialData, `weather`)

	// Step 2: Update the MCPServer with different policies
	mcpServer.Spec.AuthzConfig.Inline.Policies = []string{
		`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
		`forbid(principal, action == Action::"call_tool", resource);`,
	}
	mcpServer.Spec.AuthzConfig.Inline.EntitiesJSON = `[{"uid": {"type": "User", "id": "alice"}}]`

	// Step 3: Call ensureAuthzConfigMap again to trigger update
	err = reconciler.ensureAuthzConfigMap(ctx, mcpServer)
	require.NoError(t, err)

	// Step 4: Verify ConfigMap was updated with new data
	updatedConfigMap := &corev1.ConfigMap{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Name:      configMapName,
		Namespace: mcpServer.Namespace,
	}, updatedConfigMap)
	require.NoError(t, err)

	updatedData := updatedConfigMap.Data["authz.json"]
	// Verify old data is gone
	require.NotContains(t, updatedData, `weather`, "Old policy should be removed")
	// Verify new data is present
	require.Contains(t, updatedData, `get_prompt`, "New policy should be present")
	require.Contains(t, updatedData, `greeting`, "New policy should be present")
	require.Contains(t, updatedData, `forbid`, "New forbid policy should be present")
	require.Contains(t, updatedData, `alice`, "New entities should be present")

	// Verify the data actually changed
	require.NotEqual(t, initialData, updatedData, "ConfigMap data should have been updated")
}

func TestGenerateAuthzVolumeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		mcpServer          *mcpv1beta1.MCPServer
		expectVolumeMount  bool
		expectedConfigName string
	}{
		{
			name:              "no authz config",
			mcpServer:         v1beta1test.NewMCPServer("test-server", "test-namespace", v1beta1test.WithImage("test-image")),
			expectVolumeMount: false,
		},
		{
			name: "configmap authz config",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1beta1.AuthzConfigRef{
						Type: mcpv1beta1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
							Name: "external-authz-config",
							Key:  "custom-authz.json",
						},
					},
				},
			},
			expectVolumeMount:  true,
			expectedConfigName: "external-authz-config",
		},
		{
			name: "inline authz config",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1beta1.AuthzConfigRef{
						Type: mcpv1beta1.AuthzConfigTypeInline,
						Inline: &mcpv1beta1.InlineAuthzConfig{
							Policies: []string{
								`permit(principal, action, resource);`,
							},
						},
					},
				},
			},
			expectVolumeMount:  true,
			expectedConfigName: "test-server-authz-inline",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			volumeMount, volume := ctrlutil.GenerateAuthzVolumeConfig(tt.mcpServer.Spec.AuthzConfig, tt.mcpServer.Name)

			if tt.expectVolumeMount {
				require.NotNil(t, volumeMount, "Expected volume mount to be created")
				require.NotNil(t, volume, "Expected volume to be created")

				// Verify volume mount
				assert.Equal(t, "authz-config", volumeMount.Name)
				assert.Equal(t, "/etc/toolhive/authz", volumeMount.MountPath)
				assert.True(t, volumeMount.ReadOnly)

				// Verify volume
				assert.Equal(t, "authz-config", volume.Name)
				require.NotNil(t, volume.ConfigMap)
				assert.Equal(t, tt.expectedConfigName, volume.ConfigMap.Name)

				// Verify Items mapping
				require.Len(t, volume.ConfigMap.Items, 1)
				assert.Equal(t, "authz.json", volume.ConfigMap.Items[0].Path)
			} else {
				assert.Nil(t, volumeMount, "Expected no volume mount")
				assert.Nil(t, volume, "Expected no volume")
			}
		})
	}
}

// TestValidateAuthzPrimaryUpstreamProviderIgnored locks the advisory condition
// behaviour: when the deprecated
// spec.authzConfig.inline.primaryUpstreamProvider field is set on an MCPServer,
// the operator surfaces AuthzPrimaryUpstreamProviderIgnored=True because
// MCPServer has no embedded auth server to act on the value. Clearing the field
// removes the condition. This is the only structural backstop against the
// field's silent no-op on MCPServer; the relocation of primaryUpstreamProvider
// onto EmbeddedAuthServerConfig in PR #5290 does not affect this advisory.
func TestValidateAuthzPrimaryUpstreamProviderIgnored(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		authzConfig       *mcpv1beta1.AuthzConfigRef
		preexisting       *metav1.Condition
		wantPresent       bool
		wantReason        string
		wantMessageSubstr string
	}{
		{
			name:        "nil AuthzConfig leaves no advisory",
			authzConfig: nil,
			wantPresent: false,
		},
		{
			name: "deprecated inline primary set fires the advisory",
			authzConfig: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeInline,
				Inline: &mcpv1beta1.InlineAuthzConfig{
					Policies:                []string{`permit(principal, action, resource);`},
					PrimaryUpstreamProvider: "okta",
				},
			},
			wantPresent:       true,
			wantReason:        mcpv1beta1.ConditionReasonAuthzPrimaryUpstreamProviderIgnored,
			wantMessageSubstr: `primaryUpstreamProvider="okta"`,
		},
		{
			name: "inline without primary leaves no advisory",
			authzConfig: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeInline,
				Inline: &mcpv1beta1.InlineAuthzConfig{
					Policies: []string{`permit(principal, action, resource);`},
				},
			},
			wantPresent: false,
		},
		{
			name: "configMap authz with no inline leaves no advisory",
			authzConfig: &mcpv1beta1.AuthzConfigRef{
				Type:      mcpv1beta1.AuthzConfigTypeConfigMap,
				ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{Name: "authz-cm"},
			},
			wantPresent: false,
		},
		{
			name: "pre-existing advisory is cleared when field is unset",
			authzConfig: &mcpv1beta1.AuthzConfigRef{
				Type: mcpv1beta1.AuthzConfigTypeInline,
				Inline: &mcpv1beta1.InlineAuthzConfig{
					Policies: []string{`permit(principal, action, resource);`},
				},
			},
			preexisting: &metav1.Condition{
				Type:   mcpv1beta1.ConditionTypeAuthzPrimaryUpstreamProviderIgnored,
				Status: metav1.ConditionTrue,
				Reason: mcpv1beta1.ConditionReasonAuthzPrimaryUpstreamProviderIgnored,
			},
			wantPresent: false,
		},
	}

	r := &MCPServerReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mcpServer := &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "default", Generation: 7},
				Spec:       mcpv1beta1.MCPServerSpec{AuthzConfig: tt.authzConfig},
			}
			if tt.preexisting != nil {
				mcpServer.Status.Conditions = []metav1.Condition{*tt.preexisting}
			}
			r.validateAuthzPrimaryUpstreamProviderIgnored(mcpServer)

			cond := meta.FindStatusCondition(mcpServer.Status.Conditions, mcpv1beta1.ConditionTypeAuthzPrimaryUpstreamProviderIgnored)
			if !tt.wantPresent {
				assert.Nil(t, cond, "advisory should be absent")
				return
			}
			require.NotNil(t, cond, "advisory should be set")
			assert.Equal(t, metav1.ConditionTrue, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
			assert.Equal(t, int64(7), cond.ObservedGeneration)
			if tt.wantMessageSubstr != "" {
				assert.Contains(t, cond.Message, tt.wantMessageSubstr)
			}
		})
	}
}
