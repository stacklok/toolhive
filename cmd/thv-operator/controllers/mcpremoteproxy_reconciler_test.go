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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/rbac"
)

// TestMCPRemoteProxyFullReconciliation tests the complete reconciliation flow
func TestMCPRemoteProxyFullReconciliation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		proxy          *mcpv1beta1.MCPRemoteProxy
		toolConfig     *mcpv1beta1.MCPToolConfig
		externalAuth   *mcpv1beta1.MCPExternalAuthConfig
		secret         *corev1.Secret
		validateResult func(*testing.T, *mcpv1beta1.MCPRemoteProxy, client.Client)
	}{
		{
			name: "basic proxy with inline OIDC",
			proxy: v1beta1test.NewMCPRemoteProxy("basic-proxy", "default",
				v1beta1test.WithRemoteProxyURL("https://mcp.salesforce.com"),
				v1beta1test.WithRemoteProxyTransport("streamable-http")),
			validateResult: func(t *testing.T, proxy *mcpv1beta1.MCPRemoteProxy, c client.Client) {
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
			proxy: v1beta1test.NewMCPRemoteProxy("full-proxy", "default",
				v1beta1test.WithRemoteProxyPort(9090),
				v1beta1test.WithRemoteProxyTransport("sse"),
				v1beta1test.WithRemoteProxyExternalAuthConfigRef("token-exchange"),
				v1beta1test.WithRemoteProxyToolConfigRef("tool-filter"),
				v1beta1test.WithRemoteProxyAuthzConfig(&mcpv1beta1.AuthzConfigRef{
					Type: mcpv1beta1.AuthzConfigTypeInline,
					Inline: &mcpv1beta1.InlineAuthzConfig{
						Policies: []string{
							`permit(principal, action == Action::"tools/list", resource);`,
						},
					},
				}),
				v1beta1test.WithRemoteProxyAudit(&mcpv1beta1.AuditConfig{
					Enabled: true,
				})),
			toolConfig: &mcpv1beta1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tool-filter",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
				Status: mcpv1beta1.MCPToolConfigStatus{
					ConfigHash: "hash123",
				},
			},
			externalAuth: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "token-exchange",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "client-id",
						ClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "oauth-secret",
							Key:  "client-secret",
						},
						Audience: "api",
					},
				},
				Status: mcpv1beta1.MCPExternalAuthConfigStatus{
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
			validateResult: func(t *testing.T, proxy *mcpv1beta1.MCPRemoteProxy, c client.Client) {
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
				updatedProxy := &mcpv1beta1.MCPRemoteProxy{}
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
			proxy: v1beta1test.NewMCPRemoteProxy("invalid-proxy", "default",
				v1beta1test.WithRemoteProxyExternalAuthConfigRef("non-existent")),
			validateResult: func(t *testing.T, proxy *mcpv1beta1.MCPRemoteProxy, c client.Client) {
				t.Helper()

				// Fetch updated proxy and verify status shows failure
				updatedProxy := &mcpv1beta1.MCPRemoteProxy{}
				err := c.Get(context.TODO(), types.NamespacedName{
					Name:      proxy.Name,
					Namespace: proxy.Namespace,
				}, updatedProxy)
				require.NoError(t, err)
				assert.Equal(t, mcpv1beta1.MCPRemoteProxyPhaseFailed, updatedProxy.Status.Phase)
				assert.Contains(t, updatedProxy.Status.Message, "Validation failed")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []client.Object{tt.proxy}
			if tt.toolConfig != nil {
				objects = append(objects, tt.toolConfig)
			}
			if tt.externalAuth != nil {
				objects = append(objects, tt.externalAuth)
			}
			if tt.secret != nil {
				objects = append(objects, tt.secret)
			}

			reconciler, fakeClient := newTestMCPRemoteProxyReconciler(t, objects...)
			reconciler.Recorder = events.NewFakeRecorder(10)

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

	toolConfig := &mcpv1beta1.MCPToolConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dynamic-tools",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1"},
		},
		Status: mcpv1beta1.MCPToolConfigStatus{
			ConfigHash: "initial-hash",
		},
	}

	proxy := v1beta1test.NewMCPRemoteProxy("config-watch-proxy", "default",
		v1beta1test.WithRemoteProxyToolConfigRef("dynamic-tools"))

	scheme := testutil.NewScheme(t)
	_ = rbacv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy, toolConfig).
		WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}, &mcpv1beta1.MCPToolConfig{}).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		Recorder:         events.NewFakeRecorder(10),
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
	updatedProxy := &mcpv1beta1.MCPRemoteProxy{}
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

	proxy := v1beta1test.NewMCPRemoteProxy("status-proxy", "default")

	scheme := testutil.NewScheme(t)
	_ = rbacv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy).
		WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client:           fakeClient,
		Scheme:           scheme,
		Recorder:         events.NewFakeRecorder(10),
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

	updatedProxy := &mcpv1beta1.MCPRemoteProxy{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      proxy.Name,
		Namespace: proxy.Namespace,
	}, updatedProxy)
	require.NoError(t, err)
	assert.Equal(t, mcpv1beta1.MCPRemoteProxyPhasePending, updatedProxy.Status.Phase)
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
	assert.Equal(t, mcpv1beta1.MCPRemoteProxyPhaseReady, updatedProxy.Status.Phase)
	assert.Contains(t, updatedProxy.Status.Message, "running")

	// Verify status URL was set
	assert.NotEmpty(t, updatedProxy.Status.URL)
	expectedURL := createProxyServiceURL(proxy.Name, proxy.Namespace, int32(proxy.GetProxyPort()))
	assert.Equal(t, expectedURL, updatedProxy.Status.URL)
}

// TestCommonHelpers tests the shared helper functions
func TestCommonHelpers(t *testing.T) {
	t.Parallel()

	t.Run("GetExternalAuthConfigByName", func(t *testing.T) {
		t.Parallel()

		externalAuth := &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-auth",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			},
		}

		scheme := testutil.NewScheme(t)
		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(externalAuth).
			Build()

		result, err := ctrlutil.GetExternalAuthConfigByName(context.TODO(), fakeClient, "default", "test-auth")
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "test-auth", result.Name)
	})

	t.Run("GenerateAuthzVolumeConfig - ConfigMap", func(t *testing.T) {
		t.Parallel()

		authzConfig := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeConfigMap,
			ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
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

		authzConfig := &mcpv1beta1.AuthzConfigRef{
			Type: mcpv1beta1.AuthzConfigTypeInline,
			Inline: &mcpv1beta1.InlineAuthzConfig{
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

	proxy := v1beta1test.NewMCPRemoteProxy("authz-test-proxy", "default")

	authzConfig := &mcpv1beta1.AuthzConfigRef{
		Type: mcpv1beta1.AuthzConfigTypeInline,
		Inline: &mcpv1beta1.InlineAuthzConfig{
			Policies: []string{
				`permit(principal, action == Action::"tools/list", resource);`,
			},
			EntitiesJSON: `[]`,
		},
	}

	scheme := testutil.NewScheme(t)
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

// TestRBACClientIntegration tests the rbac.Client integration
func TestRBACClientIntegration(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("rbac-test-proxy", "default",
		v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
			p.UID = "test-uid"
		}))

	scheme := testutil.NewScheme(t)
	_ = rbacv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy).
		Build()

	rbacClient := rbac.NewClient(fakeClient, scheme)

	// Test ServiceAccount creation
	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sa",
			Namespace: proxy.Namespace,
		},
	}
	_, err := rbacClient.UpsertServiceAccountWithOwnerReference(context.TODO(), serviceAccount, proxy)
	assert.NoError(t, err)

	// Verify ServiceAccount was created
	sa := &corev1.ServiceAccount{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "test-sa",
		Namespace: proxy.Namespace,
	}, sa)
	assert.NoError(t, err)

	// Test Role creation
	role := &rbacv1.Role{
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
	_, err = rbacClient.UpsertRoleWithOwnerReference(context.TODO(), role, proxy)
	assert.NoError(t, err)

	// Verify Role was created
	createdRole := &rbacv1.Role{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      "test-role",
		Namespace: proxy.Namespace,
	}, createdRole)
	assert.NoError(t, err)
}

// TestGenerateTokenExchangeEnvVarsShared tests the shared token exchange env var helper
func TestGenerateTokenExchangeEnvVarsShared(t *testing.T) {
	t.Parallel()

	externalAuth := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-exchange",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.com/token",
				ClientID: "client-id",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "secret",
					Key:  "key",
				},
				Audience: "api",
			},
		},
	}

	scheme := testutil.NewScheme(t)
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(externalAuth).
		Build()

	ref := &mcpv1beta1.ExternalAuthConfigRef{
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

// TestValidateSpecConfigurationConditions tests that validateSpec sets the ConfigurationValid condition correctly
func TestValidateSpecConfigurationConditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		proxy           *mcpv1beta1.MCPRemoteProxy
		existingObjects []runtime.Object
		expectError     bool
		errContains     string
		expectCondition string // expected reason for ConfigurationValid condition
		conditionStatus metav1.ConditionStatus
	}{
		{
			name:            "valid proxy with no OIDC config",
			proxy:           v1beta1test.NewMCPRemoteProxy("no-oidc-proxy", "default"),
			expectError:     false,
			expectCondition: mcpv1beta1.ConditionReasonConfigurationValid,
			conditionStatus: metav1.ConditionTrue,
		},
		{
			name: "invalid Cedar policy syntax is rejected",
			proxy: v1beta1test.NewMCPRemoteProxy("invalid-cedar-proxy", "default",
				v1beta1test.WithRemoteProxyAuthzConfig(&mcpv1beta1.AuthzConfigRef{
					Type: mcpv1beta1.AuthzConfigTypeInline,
					Inline: &mcpv1beta1.InlineAuthzConfig{
						Policies: []string{"not valid cedar"},
					},
				})),
			expectError:     true,
			errContains:     "invalid syntax",
			expectCondition: mcpv1beta1.ConditionReasonAuthzPolicySyntaxInvalid,
			conditionStatus: metav1.ConditionFalse,
		},
		{
			name: "referenced authz ConfigMap not found is rejected",
			proxy: v1beta1test.NewMCPRemoteProxy("missing-configmap-proxy", "default",
				v1beta1test.WithRemoteProxyAuthzConfig(&mcpv1beta1.AuthzConfigRef{
					Type: mcpv1beta1.AuthzConfigTypeConfigMap,
					ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
						Name: "does-not-exist",
					},
				})),
			expectError:     true,
			errContains:     "not found",
			expectCondition: mcpv1beta1.ConditionReasonAuthzConfigMapNotFound,
			conditionStatus: metav1.ConditionFalse,
		},
		{
			name: "referenced header secret not found is rejected",
			proxy: v1beta1test.NewMCPRemoteProxy("missing-header-secret-proxy", "default",
				v1beta1test.WithRemoteProxyHeaderForward(&mcpv1beta1.HeaderForwardConfig{
					AddHeadersFromSecret: []mcpv1beta1.HeaderFromSecret{
						{
							HeaderName: "X-API-Key",
							ValueSecretRef: &mcpv1beta1.SecretKeyRef{
								Name: "missing-secret",
								Key:  "api-key",
							},
						},
					},
				})),
			expectError:     true,
			errContains:     "not found",
			expectCondition: mcpv1beta1.ConditionReasonHeaderSecretNotFound,
			conditionStatus: metav1.ConditionFalse,
		},
		{
			name: "referenced authz ConfigMap with malformed payload is rejected",
			proxy: v1beta1test.NewMCPRemoteProxy("malformed-configmap-proxy", "default",
				v1beta1test.WithRemoteProxyAuthzConfig(&mcpv1beta1.AuthzConfigRef{
					Type: mcpv1beta1.AuthzConfigTypeConfigMap,
					ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
						Name: "malformed-authz",
					},
				})),
			existingObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "malformed-authz", Namespace: "default"},
					Data:       map[string]string{ctrlutil.DefaultAuthzKey: "this is not valid yaml or json: {"},
				},
			},
			expectError:     true,
			errContains:     "malformed-authz",
			expectCondition: mcpv1beta1.ConditionReasonAuthzConfigMapInvalid,
			conditionStatus: metav1.ConditionFalse,
		},
		{
			name: "referenced authz ConfigMap with non-Cedar payload is rejected",
			proxy: v1beta1test.NewMCPRemoteProxy("non-cedar-configmap-proxy", "default",
				v1beta1test.WithRemoteProxyAuthzConfig(&mcpv1beta1.AuthzConfigRef{
					Type: mcpv1beta1.AuthzConfigTypeConfigMap,
					ConfigMap: &mcpv1beta1.ConfigMapAuthzRef{
						Name: "non-cedar-authz",
					},
				})),
			existingObjects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "non-cedar-authz", Namespace: "default"},
					Data: map[string]string{
						ctrlutil.DefaultAuthzKey: `{"version":"1.0","type":"httpv1","pdp":{"http":{"url":"http://pdp.example.com"},"claim_mapping":"standard"}}`,
					},
				},
			},
			expectError:     true,
			errContains:     "not a Cedar config",
			expectCondition: mcpv1beta1.ConditionReasonAuthzConfigMapInvalid,
			conditionStatus: metav1.ConditionFalse,
		},
		{
			name: "malformed remote URL is rejected",
			proxy: v1beta1test.NewMCPRemoteProxy("bad-scheme-proxy", "default",
				v1beta1test.WithRemoteProxyURL("ftp://bad-scheme.example.com")),
			expectError:     true,
			errContains:     "scheme",
			expectCondition: mcpv1beta1.ConditionReasonRemoteURLInvalid,
			conditionStatus: metav1.ConditionFalse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			objects := append([]runtime.Object{tt.proxy}, tt.existingObjects...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{}).
				Build()

			fakeRecorder := events.NewFakeRecorder(10)
			reconciler := &MCPRemoteProxyReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Recorder: fakeRecorder,
			}

			err := reconciler.validateSpec(context.TODO(), tt.proxy)

			if tt.expectError {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}

			// Verify the ConfigurationValid condition was set
			cond := meta.FindStatusCondition(tt.proxy.Status.Conditions, mcpv1beta1.ConditionTypeConfigurationValid)
			require.NotNil(t, cond, "ConfigurationValid condition should be set")
			assert.Equal(t, tt.conditionStatus, cond.Status)
			assert.Equal(t, tt.expectCondition, cond.Reason)

			// Verify an event was recorded for failures
			if tt.expectError {
				select {
				case event := <-fakeRecorder.Events:
					assert.Contains(t, event, tt.expectCondition)
				default:
					t.Error("expected a warning event to be recorded")
				}
			}
		})
	}
}

// TestValidateAndHandleConfigs tests the validation and config handling
func TestValidateAndHandleConfigs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		proxy        *mcpv1beta1.MCPRemoteProxy
		toolConfig   *mcpv1beta1.MCPToolConfig
		externalAuth *mcpv1beta1.MCPExternalAuthConfig
		expectError  bool
		expectPhase  mcpv1beta1.MCPRemoteProxyPhase
	}{
		{
			name: "valid configs",
			proxy: v1beta1test.NewMCPRemoteProxy("valid-proxy", "default",
				v1beta1test.WithRemoteProxyToolConfigRef("valid-tools")),
			toolConfig: &mcpv1beta1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-tools",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1"},
				},
				Status: mcpv1beta1.MCPToolConfigStatus{
					ConfigHash: "hash",
				},
			},
			expectError: false,
		},
		{
			name: "missing tool config",
			proxy: v1beta1test.NewMCPRemoteProxy("missing-tool-proxy", "default",
				v1beta1test.WithRemoteProxyToolConfigRef("non-existent")),
			expectError: true,
			expectPhase: mcpv1beta1.MCPRemoteProxyPhaseFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			objects := []client.Object{tt.proxy}
			if tt.toolConfig != nil {
				objects = append(objects, tt.toolConfig)
			}
			if tt.externalAuth != nil {
				objects = append(objects, tt.externalAuth)
			}

			reconciler, fakeClient := newTestMCPRemoteProxyReconciler(t, objects...)
			reconciler.Recorder = events.NewFakeRecorder(10)

			err := reconciler.validateAndHandleConfigs(context.TODO(), tt.proxy)

			if tt.expectError {
				assert.Error(t, err)

				// Verify status was updated
				updatedProxy := &mcpv1beta1.MCPRemoteProxy{}
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

// TestMCPRemoteProxy_ValidateAuthzPrimaryUpstreamProviderIgnored locks the
// advisory condition behaviour on MCPRemoteProxy: the deprecated
// spec.authzConfig.inline.primaryUpstreamProvider field continues to fire
// AuthzPrimaryUpstreamProviderIgnored=True after the relocation of the field
// onto EmbeddedAuthServerConfig, because MCPRemoteProxy has no embedded auth
// server to act on the value regardless of where it lives. Clearing the field
// removes the condition.
func TestMCPRemoteProxy_ValidateAuthzPrimaryUpstreamProviderIgnored(t *testing.T) {
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

	r := &MCPRemoteProxyReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			proxy := v1beta1test.NewMCPRemoteProxy("p", "default",
				v1beta1test.WithRemoteProxyURL(""),
				v1beta1test.WithRemoteProxyPort(0),
				v1beta1test.WithRemoteProxyAuthzConfig(tt.authzConfig),
				v1beta1test.MutateRemoteProxy(func(p *mcpv1beta1.MCPRemoteProxy) {
					p.Generation = 7
				}))
			if tt.preexisting != nil {
				proxy.Status.Conditions = []metav1.Condition{*tt.preexisting}
			}
			r.validateAuthzPrimaryUpstreamProviderIgnored(proxy)

			cond := meta.FindStatusCondition(proxy.Status.Conditions, mcpv1beta1.ConditionTypeAuthzPrimaryUpstreamProviderIgnored)
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
