// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

// TestMCPRemoteProxyFullReconciliation tests the complete reconciliation flow
func TestMCPRemoteProxyFullReconciliation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		proxy          *mcpv1alpha1.MCPRemoteProxy
		toolConfig     *mcpv1alpha1.MCPToolConfig
		externalAuth   *mcpv1alpha1.MCPExternalAuthConfig
		secret         *corev1.Secret
		validateResult func(*testing.T, *mcpv1alpha1.MCPRemoteProxy, client.Client)
	}{
		{
			name: "basic proxy with inline OIDC",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "basic-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.salesforce.com",
					Port:      8080,
					Transport: "streamable-http",
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://login.salesforce.com",
							Audience: "mcp.salesforce.com",
						},
					},
				},
			},
			validateResult: func(t *testing.T, proxy *mcpv1alpha1.MCPRemoteProxy, c client.Client) {
				t.Helper()

				// Verify ServiceAccount created
				sa := &corev1.ServiceAccount{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
					Namespace: proxy.Namespace,
				}, sa)
				assert.NoError(t, err, "ServiceAccount should be created")

				// Verify Role created
				role := &rbacv1.Role{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
					Namespace: proxy.Namespace,
				}, role)
				assert.NoError(t, err, "Role should be created")

				// Verify RoleBinding created
				rb := &rbacv1.RoleBinding{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
					Namespace: proxy.Namespace,
				}, rb)
				assert.NoError(t, err, "RoleBinding should be created")

				// Verify RunConfig ConfigMap created
				cm := &corev1.ConfigMap{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      fmt.Sprintf("%s-runconfig", proxy.Name),
					Namespace: proxy.Namespace,
				}, cm)
				assert.NoError(t, err, "RunConfig ConfigMap should be created")
				assert.Contains(t, cm.Data, "runconfig.json")

				// Verify Deployment created
				dep := &appsv1.Deployment{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      proxy.Name,
					Namespace: proxy.Namespace,
				}, dep)
				assert.NoError(t, err, "Deployment should be created")

				// Verify Service created
				svc := &corev1.Service{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      createProxyServiceName(proxy.Name),
					Namespace: proxy.Namespace,
				}, svc)
				assert.NoError(t, err, "Service should be created")
			},
		},
		{
			name: "proxy with all features",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "full-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      9090,
					Transport: "sse",
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.company.com",
							Audience: "mcp-proxy",
						},
					},
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "token-exchange",
					},
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "tool-filter",
					},
					AuthzConfig: &mcpv1alpha1.AuthzConfigRef{
						Type: mcpv1alpha1.AuthzConfigTypeInline,
						Inline: &mcpv1alpha1.InlineAuthzConfig{
							Policies: []string{
								`permit(principal, action == Action::"tools/list", resource);`,
							},
						},
					},
					Audit: &mcpv1alpha1.AuditConfig{
						Enabled: true,
					},
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
							Enabled:     true,
							ServiceName: "full-proxy",
						},
					},
				},
			},
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tool-filter",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
				Status: mcpv1alpha1.MCPToolConfigStatus{
					ConfigHash: "hash123",
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "token-exchange",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "client-id",
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "oauth-secret",
							Key:  "client-secret",
						},
						Audience: "api",
					},
				},
				Status: mcpv1alpha1.MCPExternalAuthConfigStatus{
					ConfigHash: "hash456",
				},
			},
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oauth-secret",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"client-secret": []byte("secret-value"),
				},
			},
			validateResult: func(t *testing.T, proxy *mcpv1alpha1.MCPRemoteProxy, c client.Client) {
				t.Helper()

				// Verify all resources created
				cm := &corev1.ConfigMap{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      fmt.Sprintf("%s-runconfig", proxy.Name),
					Namespace: proxy.Namespace,
				}, cm)
				assert.NoError(t, err)

				// Verify authz ConfigMap created
				authzCM := &corev1.ConfigMap{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      fmt.Sprintf("%s-authz-inline", proxy.Name),
					Namespace: proxy.Namespace,
				}, authzCM)
				assert.NoError(t, err)

				// Fetch updated proxy and verify status hashes
				updatedProxy := &mcpv1alpha1.MCPRemoteProxy{}
				err = c.Get(context.TODO(), types.NamespacedName{
					Name:      proxy.Name,
					Namespace: proxy.Namespace,
				}, updatedProxy)
				assert.NoError(t, err)
				assert.Equal(t, "hash123", updatedProxy.Status.ToolConfigHash)
				assert.Equal(t, "hash456", updatedProxy.Status.ExternalAuthConfigHash)
			},
		},
		{
			name: "proxy with validation failure",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "non-existent",
					},
				},
			},
			validateResult: func(t *testing.T, proxy *mcpv1alpha1.MCPRemoteProxy, c client.Client) {
				t.Helper()

				// Fetch updated proxy and verify status shows failure
				updatedProxy := &mcpv1alpha1.MCPRemoteProxy{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      proxy.Name,
					Namespace: proxy.Namespace,
				}, updatedProxy)
				require.NoError(t, err)
				assert.Equal(t, mcpv1alpha1.MCPRemoteProxyPhaseFailed, updatedProxy.Status.Phase)
				assert.Contains(t, updatedProxy.Status.Message, "Validation failed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			_ = rbacv1.AddToScheme(scheme)
			_ = appsv1.AddToScheme(scheme)

			objects := []runtime.Object{tt.proxy}
			if tt.toolConfig != nil {
				objects = append(objects, tt.toolConfig)
			}
			if tt.externalAuth != nil {
				objects = append(objects, tt.externalAuth)
			}
			if tt.secret != nil {
				objects = append(objects, tt.secret)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(&mcpv1alpha1.MCPRemoteProxy{}).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client:           fakeClient,
				Scheme:           scheme,
				PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
			}

			ctx := context.TODO()
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.proxy.Name,
					Namespace: tt.proxy.Namespace,
				},
			}

			// Run multiple reconciliation cycles to ensure all resources are created
			var reconcileErr error
			for i := 0; i < 3; i++ {
				_, err := reconciler.Reconcile(ctx, req)
				if err != nil {
					reconcileErr = err
					break
				}
			}

			// For validation failure test, we expect an error
			if tt.name == "proxy with validation failure" {
				assert.Error(t, reconcileErr)
			}

			if tt.validateResult != nil {
				tt.validateResult(t, tt.proxy, fakeClient)
			}
		})
	}
}

// TestMCPRemoteProxyConfigChangePropagation tests that config changes trigger reconciliation
func TestMCPRemoteProxyConfigChangePropagation(t *testing.T) {
	t.Parallel()

	toolConfig := &mcpv1alpha1.MCPToolConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dynamic-tools",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1"},
		},
		Status: mcpv1alpha1.MCPToolConfigStatus{
			ConfigHash: "initial-hash",
		},
	}

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "config-watch-proxy",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.example.com",
			Port:      8080,
			OIDCConfig: mcpv1alpha1.OIDCConfigRef{
				Type: mcpv1alpha1.OIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer:   "https://auth.example.com",
					Audience: "mcp-proxy",
				},
			},
			ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
				Name: "dynamic-tools",
			},
		},
	}

	scheme := createRunConfigTestScheme()
	_ = rbacv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy, toolConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPRemoteProxy{}, &mcpv1alpha1.MCPToolConfig{}).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	ctx := context.TODO()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      proxy.Name,
			Namespace: proxy.Namespace,
		},
	}

	// Initial reconciliation
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	// Verify initial hash stored
	updatedProxy := &mcpv1alpha1.MCPRemoteProxy{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      proxy.Name,
		Namespace: proxy.Namespace,
	}, updatedProxy)
	require.NoError(t, err)
	assert.Equal(t, "initial-hash", updatedProxy.Status.ToolConfigHash)

	// Update ToolConfig hash
	toolConfig.Status.ConfigHash = "updated-hash"
	err = fakeClient.Status().Update(ctx, toolConfig)
	require.NoError(t, err)

	// Reconcile again
	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	// Verify hash updated
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      proxy.Name,
		Namespace: proxy.Namespace,
	}, updatedProxy)
	require.NoError(t, err)
	assert.Equal(t, "updated-hash", updatedProxy.Status.ToolConfigHash)
}

// TestMCPRemoteProxyStatusProgression tests status updates through lifecycle
func TestMCPRemoteProxyStatusProgression(t *testing.T) {
	t.Parallel()

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "status-proxy",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.example.com",
			Port:      8080,
			OIDCConfig: mcpv1alpha1.OIDCConfigRef{
				Type: mcpv1alpha1.OIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer:   "https://auth.example.com",
					Audience: "mcp-proxy",
				},
			},
		},
	}

	scheme := createRunConfigTestScheme()
	_ = rbacv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy).
		WithStatusSubresource(&mcpv1alpha1.MCPRemoteProxy{}).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	ctx := context.TODO()
	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      proxy.Name,
			Namespace: proxy.Namespace,
		},
	}

	// Initial reconciliation - no pods yet
	_, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	updatedProxy := &mcpv1alpha1.MCPRemoteProxy{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      proxy.Name,
		Namespace: proxy.Namespace,
	}, updatedProxy)
	require.NoError(t, err)
	assert.Equal(t, mcpv1alpha1.MCPRemoteProxyPhasePending, updatedProxy.Status.Phase)
	assert.Contains(t, updatedProxy.Status.Message, "No pods")

	// Add a running pod
	runningPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "status-proxy-pod",
			Namespace: "default",
			Labels:    labelsForMCPRemoteProxy("status-proxy"),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	err = fakeClient.Create(ctx, runningPod)
	require.NoError(t, err)

	// Reconcile again with running pod
	_, err = reconciler.Reconcile(ctx, req)
	require.NoError(t, err)

	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      proxy.Name,
		Namespace: proxy.Namespace,
	}, updatedProxy)
	require.NoError(t, err)
	assert.Equal(t, mcpv1alpha1.MCPRemoteProxyPhaseReady, updatedProxy.Status.Phase)
	assert.Contains(t, updatedProxy.Status.Message, "running")

	// Verify status URL was set
	assert.NotEmpty(t, updatedProxy.Status.URL)
	expectedURL := createProxyServiceURL(proxy.Name, proxy.Namespace, proxy.Spec.Port)
	assert.Equal(t, expectedURL, updatedProxy.Status.URL)
}

// TestCommonHelpers tests the shared helper functions
func TestCommonHelpers(t *testing.T) {
	t.Parallel()

	t.Run("GetExternalAuthConfigByName", func(t *testing.T) {
		t.Parallel()

		externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-auth",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			},
		}

		scheme := createRunConfigTestScheme()
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(externalAuth).
			Build()

		result, err := ctrlutil.GetExternalAuthConfigByName(context.TODO(), fakeClient, "default", "test-auth")
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "test-auth", result.Name)
	})

	t.Run("GenerateOpenTelemetryEnvVars", func(t *testing.T) {
		t.Parallel()

		telemetryConfig := &mcpv1alpha1.TelemetryConfig{
			OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
				Enabled:     true,
				ServiceName: "test-service",
			},
		}

		envVars := ctrlutil.GenerateOpenTelemetryEnvVars(telemetryConfig, "test-resource", "test-ns")
		require.Len(t, envVars, 1)
		assert.Equal(t, "OTEL_RESOURCE_ATTRIBUTES", envVars[0].Name)
		assert.Contains(t, envVars[0].Value, "service.name=test-service")
		assert.Contains(t, envVars[0].Value, "service.namespace=test-ns")
	})

	t.Run("GenerateAuthzVolumeConfig - ConfigMap", func(t *testing.T) {
		t.Parallel()

		authzConfig := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1alpha1.ConfigMapAuthzRef{
				Name: "authz-cm",
				Key:  "policies.json",
			},
		}

		volumeMount, volume := ctrlutil.GenerateAuthzVolumeConfig(authzConfig, "test-resource")
		require.NotNil(t, volumeMount)
		require.NotNil(t, volume)
		assert.Equal(t, "authz-config", volumeMount.Name)
		assert.Equal(t, "/etc/toolhive/authz", volumeMount.MountPath)
		assert.True(t, volumeMount.ReadOnly)
	})

	t.Run("GenerateAuthzVolumeConfig - Inline", func(t *testing.T) {
		t.Parallel()

		authzConfig := &mcpv1alpha1.AuthzConfigRef{
			Type: mcpv1alpha1.AuthzConfigTypeInline,
			Inline: &mcpv1alpha1.InlineAuthzConfig{
				Policies: []string{"permit(principal, action, resource);"},
			},
		}

		volumeMount, volume := ctrlutil.GenerateAuthzVolumeConfig(authzConfig, "test-resource")
		require.NotNil(t, volumeMount)
		require.NotNil(t, volume)
		assert.Equal(t, "test-resource-authz-inline", volume.ConfigMap.Name)
	})
}

// TestEnsureAuthzConfigMapShared tests the shared authz ConfigMap helper
func TestEnsureAuthzConfigMapShared(t *testing.T) {
	t.Parallel()

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "authz-test-proxy",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.example.com",
			OIDCConfig: mcpv1alpha1.OIDCConfigRef{
				Type: mcpv1alpha1.OIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer:   "https://auth.example.com",
					Audience: "mcp-proxy",
				},
			},
		},
	}

	authzConfig := &mcpv1alpha1.AuthzConfigRef{
		Type: mcpv1alpha1.AuthzConfigTypeInline,
		Inline: &mcpv1alpha1.InlineAuthzConfig{
			Policies: []string{
				`permit(principal, action == Action::"tools/list", resource);`,
			},
			EntitiesJSON: `[]`,
		},
	}

	scheme := createRunConfigTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy).
		Build()

	labels := labelsForMCPRemoteProxy(proxy.Name)
	labels[authzLabelKey] = authzLabelValueInline

	err := ctrlutil.EnsureAuthzConfigMap(
		context.TODO(),
		fakeClient,
		scheme,
		proxy,
		proxy.Namespace,
		proxy.Name,
		authzConfig,
		labels,
	)
	assert.NoError(t, err)

	// Verify ConfigMap was created
	cm := &corev1.ConfigMap{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      fmt.Sprintf("%s-authz-inline", proxy.Name),
		Namespace: proxy.Namespace,
	}, cm)
	assert.NoError(t, err)
	assert.Contains(t, cm.Data, ctrlutil.DefaultAuthzKey)
	assert.Contains(t, cm.Data[ctrlutil.DefaultAuthzKey], "tools/list")
}

// TestEnsureRBACResourceShared tests the shared RBAC resource helper
func TestEnsureRBACResourceShared(t *testing.T) {
	t.Parallel()

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbac-test-proxy",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.example.com",
			OIDCConfig: mcpv1alpha1.OIDCConfigRef{
				Type: mcpv1alpha1.OIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer:   "https://auth.example.com",
					Audience: "mcp-proxy",
				},
			},
		},
	}

	scheme := createRunConfigTestScheme()
	_ = rbacv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy).
		Build()

	// Test ServiceAccount creation
	err := ctrlutil.EnsureRBACResource(context.TODO(), fakeClient, scheme, proxy, "ServiceAccount", func() client.Object {
		return &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-sa",
				Namespace: proxy.Namespace,
			},
		}
	})
	assert.NoError(t, err)

	// Verify ServiceAccount was created
	sa := &corev1.ServiceAccount{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "test-sa",
		Namespace: proxy.Namespace,
	}, sa)
	assert.NoError(t, err)

	// Test Role creation
	err = ctrlutil.EnsureRBACResource(context.TODO(), fakeClient, scheme, proxy, "Role", func() client.Object {
		return &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-role",
				Namespace: proxy.Namespace,
			},
			Rules: []rbacv1.PolicyRule{
				{
					APIGroups: []string{""},
					Resources: []string{"pods"},
					Verbs:     []string{"get"},
				},
			},
		}
	})
	assert.NoError(t, err)

	// Verify Role was created
	role := &rbacv1.Role{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "test-role",
		Namespace: proxy.Namespace,
	}, role)
	assert.NoError(t, err)
}

// TestGenerateTokenExchangeEnvVarsShared tests the shared token exchange env var helper
func TestGenerateTokenExchangeEnvVarsShared(t *testing.T) {
	t.Parallel()

	externalAuth := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-exchange",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.com/token",
				ClientID: "client-id",
				ClientSecretRef: mcpv1alpha1.SecretKeyRef{
					Name: "secret",
					Key:  "key",
				},
				Audience: "api",
			},
		},
	}

	scheme := createRunConfigTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(externalAuth).
		Build()

	ref := &mcpv1alpha1.ExternalAuthConfigRef{
		Name: "test-exchange",
	}

	envVars, err := ctrlutil.GenerateTokenExchangeEnvVars(
		context.TODO(),
		fakeClient,
		"default",
		ref,
		ctrlutil.GetExternalAuthConfigByName,
	)
	assert.NoError(t, err)
	require.Len(t, envVars, 1)
	assert.Equal(t, "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET", envVars[0].Name)
	require.NotNil(t, envVars[0].ValueFrom)
	require.NotNil(t, envVars[0].ValueFrom.SecretKeyRef)
	assert.Equal(t, "secret", envVars[0].ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "key", envVars[0].ValueFrom.SecretKeyRef.Key)
}

// TestValidateAndHandleConfigs tests the validation and config handling
func TestValidateAndHandleConfigs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		proxy        *mcpv1alpha1.MCPRemoteProxy
		toolConfig   *mcpv1alpha1.MCPToolConfig
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		expectError  bool
		expectPhase  mcpv1alpha1.MCPRemoteProxyPhase
	}{
		{
			name: "valid configs",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "valid-tools",
					},
				},
			},
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-tools",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1"},
				},
				Status: mcpv1alpha1.MCPToolConfigStatus{
					ConfigHash: "hash",
				},
			},
			expectError: false,
		},
		{
			name: "missing tool config",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "missing-tool-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "non-existent",
					},
				},
			},
			expectError: true,
			expectPhase: mcpv1alpha1.MCPRemoteProxyPhaseFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.proxy}
			if tt.toolConfig != nil {
				objects = append(objects, tt.toolConfig)
			}
			if tt.externalAuth != nil {
				objects = append(objects, tt.externalAuth)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(&mcpv1alpha1.MCPRemoteProxy{}).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := reconciler.validateAndHandleConfigs(context.TODO(), tt.proxy)

			if tt.expectError {
				assert.Error(t, err)

				// Verify status was updated
				updatedProxy := &mcpv1alpha1.MCPRemoteProxy{}
				getErr := fakeClient.Get(context.TODO(), types.NamespacedName{
					Name:      tt.proxy.Name,
					Namespace: tt.proxy.Namespace,
				}, updatedProxy)
				require.NoError(t, getErr)
				if tt.expectPhase != "" {
					assert.Equal(t, tt.expectPhase, updatedProxy.Status.Phase)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
