package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestGenerateAuthzArgs(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name         string
		mcpServer    *mcpv1alpha1.MCPServer
		configMaps   []corev1.ConfigMap
		expectedArgs []string
	}{
		{
			name: "no authz config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
				},
			},
			expectedArgs: nil,
		},
		{
			name: "configmap authz config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
							Name: "test-authz-config",
							Key:  "authz.json",
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-authz-config",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"authz.json": `{
							"version": "1.0",
							"type": "cedarv1",
							"cedar": {
								"policies": ["permit(principal, action == Action::\"call_tool\", resource == Tool::\"weather\");"],
								"entities_json": "[]"
							}
						}`,
					},
				},
			},
			expectedArgs: []string{"--authz-config=/etc/toolhive/authz/authz.json"},
		},
		{
			name: "inline authz config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeInline,
						Inline: &mcpv1alpha1.InlineAuthzConfig{
							Policies: []string{
								`permit(principal, action == Action::"call_tool", resource == Tool::"weather");`,
								`permit(principal, action == Action::"get_prompt", resource == Prompt::"greeting");`,
							},
							EntitiesJSON: "[]",
						},
					},
				},
			},
			expectedArgs: []string{"--authz-config=/etc/toolhive/authz/authz.json"},
		},
		{
			name: "configmap authz config with default key",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
							Name: "test-authz-config",
							// Key not specified, should default to "authz.json"
						},
					},
				},
			},
			configMaps: []corev1.ConfigMap{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-authz-config",
						Namespace: "test-namespace",
					},
					Data: map[string]string{
						"authz.json": `{
							"version": "1.0",
							"type": "cedarv1",
							"cedar": {
								"policies": ["permit(principal, action, resource);"],
								"entities_json": "[]"
							}
						}`,
					},
				},
			},
			expectedArgs: []string{"--authz-config=/etc/toolhive/authz/authz.json"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create fake client with ConfigMaps
			objects := []runtime.Object{tt.mcpServer}
			for i := range tt.configMaps {
				objects = append(objects, &tt.configMaps[i])
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				Build()

			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			args := reconciler.generateAuthzArgs(tt.mcpServer)
			assert.Equal(t, tt.expectedArgs, args)
		})
	}
}

func TestEnsureAuthzConfigMap(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name               string
		mcpServer          *mcpv1alpha1.MCPServer
		expectConfigMap    bool
		expectedConfigData string
	}{
		{
			name: "no authz config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
				},
			},
			expectConfigMap: false,
		},
		{
			name: "configmap authz config (no inline ConfigMap needed)",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
							Name: "external-authz-config",
						},
					},
				},
			},
			expectConfigMap: false,
		},
		{
			name: "inline authz config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeInline,
						Inline: &mcpv1alpha1.InlineAuthzConfig{
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
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeInline,
						Inline: &mcpv1alpha1.InlineAuthzConfig{
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

			reconciler := &MCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

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

func TestGenerateAuthzVolumeConfig(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name               string
		mcpServer          *mcpv1alpha1.MCPServer
		expectVolumeMount  bool
		expectedConfigName string
	}{
		{
			name: "no authz config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
				},
			},
			expectVolumeMount: false,
		},
		{
			name: "configmap authz config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
						ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
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
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "test-namespace",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeInline,
						Inline: &mcpv1alpha1.InlineAuthzConfig{
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

			reconciler := &MCPServerReconciler{}

			volumeMount, volume := reconciler.generateAuthzVolumeConfig(tt.mcpServer)

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
