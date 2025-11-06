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

// TestMCPRemoteProxyValidateSpec tests the spec validation logic
func TestMCPRemoteProxyValidateSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		proxy       *mcpv1alpha1.MCPRemoteProxy
		expectError bool
		errContains string
	}{
		{
			name: "valid spec",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "valid-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.salesforce.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://login.salesforce.com",
							Audience: "mcp.salesforce.com",
						},
					},
				},
			},
			expectError: false,
		},
		{
			name: "missing remote URL",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-url-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					Port: 8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.example.com",
							Audience: "mcp-proxy",
						},
					},
				},
			},
			expectError: true,
			errContains: "remoteURL is required",
		},
		// Note: "missing OIDC config" test removed - OIDCConfig is now a required value type
		// with kubebuilder:validation:Required, so the API server prevents resources without it
		{
			name: "with valid external auth config",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "external-auth-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					Port:      8080,
					OIDCConfig: mcpv1alpha1.OIDCConfigRef{
						Type: mcpv1alpha1.OIDCConfigTypeInline,
						Inline: &mcpv1alpha1.InlineOIDCConfig{
							Issuer:   "https://auth.company.com",
							Audience: "mcp-proxy",
						},
					},
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "exchange-config",
					},
				},
			},
			expectError: true,
			errContains: "failed to validate external auth config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.proxy).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := reconciler.validateSpec(context.TODO(), tt.proxy)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestMCPRemoteProxyReconcile_CreateResources tests the reconciliation creates all necessary resources
func TestMCPRemoteProxyReconcile_CreateResources(t *testing.T) {
	t.Parallel()

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: "test-ns",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.salesforce.com",
			Port:      8080,
			OIDCConfig: mcpv1alpha1.OIDCConfigRef{
				Type: mcpv1alpha1.OIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCConfig{
					Issuer:   "https://login.salesforce.com",
					Audience: "mcp.salesforce.com",
				},
			},
		},
	}

	scheme := createRunConfigTestScheme()
	// Add RBAC types to scheme
	_ = rbacv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy).
		WithStatusSubresource(proxy).
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

	// First reconcile should create resources
	result, err := reconciler.Reconcile(ctx, req)
	require.NoError(t, err)
	// Result should not request immediate requeue
	assert.Equal(t, int64(0), result.RequeueAfter.Nanoseconds())

	// Verify ServiceAccount was created
	sa := &corev1.ServiceAccount{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
		Namespace: proxy.Namespace,
	}, sa)
	assert.NoError(t, err, "ServiceAccount should be created")

	// Verify Role was created
	role := &rbacv1.Role{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
		Namespace: proxy.Namespace,
	}, role)
	assert.NoError(t, err, "Role should be created")

	// Verify RoleBinding was created
	rb := &rbacv1.RoleBinding{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
		Namespace: proxy.Namespace,
	}, rb)
	assert.NoError(t, err, "RoleBinding should be created")

	// Verify RunConfig ConfigMap was created
	cm := &corev1.ConfigMap{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      fmt.Sprintf("%s-runconfig", proxy.Name),
		Namespace: proxy.Namespace,
	}, cm)
	assert.NoError(t, err, "RunConfig ConfigMap should be created")

	// Verify Deployment was created
	deployment := &appsv1.Deployment{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      proxy.Name,
		Namespace: proxy.Namespace,
	}, deployment)
	assert.NoError(t, err, "Deployment should be created")

	// Verify Service was created
	svc := &corev1.Service{}
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      createProxyServiceName(proxy.Name),
		Namespace: proxy.Namespace,
	}, svc)
	assert.NoError(t, err, "Service should be created")
}

// TestMCPRemoteProxyReconcile_NotFound tests reconciliation when resource is not found
func TestMCPRemoteProxyReconcile_NotFound(t *testing.T) {
	t.Parallel()

	scheme := createRunConfigTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "non-existent",
			Namespace: "default",
		},
	}

	result, err := reconciler.Reconcile(context.TODO(), req)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), result.RequeueAfter.Nanoseconds())
}

// TestHandleToolConfig tests tool config reference handling
func TestHandleToolConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		proxy       *mcpv1alpha1.MCPRemoteProxy
		toolConfig  *mcpv1alpha1.MCPToolConfig
		expectError bool
		errContains string
	}{
		{
			name: "no tool config reference",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-tools-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
			},
			expectError: false,
		},
		{
			name: "valid tool config reference",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tools-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "tool-config",
					},
				},
			},
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tool-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
				Status: mcpv1alpha1.MCPToolConfigStatus{
					ConfigHash: "abc123",
				},
			},
			expectError: false,
		},
		{
			name: "tool config hash update",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tools-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "tool-config",
					},
				},
				Status: mcpv1alpha1.MCPRemoteProxyStatus{
					ToolConfigHash: "old-hash",
				},
			},
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tool-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
				Status: mcpv1alpha1.MCPToolConfigStatus{
					ConfigHash: "new-hash",
				},
			},
			expectError: false,
		},
		{
			name: "tool config reference removed",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "tools-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
				Status: mcpv1alpha1.MCPRemoteProxyStatus{
					ToolConfigHash: "old-hash",
				},
			},
			expectError: false,
		},
		{
			name: "tool config not found",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "broken-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "non-existent",
					},
				},
			},
			expectError: true,
			errContains: "failed to get MCPToolConfig",
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

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(&mcpv1alpha1.MCPRemoteProxy{}).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := reconciler.handleToolConfig(context.TODO(), tt.proxy)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)

				// Verify status updates
				updatedProxy := &mcpv1alpha1.MCPRemoteProxy{}
				err := fakeClient.Get(context.TODO(), client.ObjectKey{
					Name:      tt.proxy.Name,
					Namespace: tt.proxy.Namespace,
				}, updatedProxy)
				assert.NoError(t, err)

				if tt.toolConfig != nil && tt.proxy.Spec.ToolConfigRef != nil {
					// Hash should be set to the tool config's hash
					assert.Equal(t, tt.toolConfig.Status.ConfigHash, updatedProxy.Status.ToolConfigHash,
						"Status hash should be updated to match tool config")
				} else if tt.proxy.Spec.ToolConfigRef == nil && tt.proxy.Status.ToolConfigHash != "" {
					// Hash should be cleared when reference is removed
					assert.Empty(t, updatedProxy.Status.ToolConfigHash,
						"Status hash should be cleared when reference is removed")
				}
			}
		})
	}
}

// TestHandleExternalAuthConfig tests external auth config reference handling
func TestHandleExternalAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		proxy        *mcpv1alpha1.MCPRemoteProxy
		externalAuth *mcpv1alpha1.MCPExternalAuthConfig
		expectError  bool
		errContains  string
	}{
		{
			name: "no external auth reference",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-auth-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
			},
			expectError: false,
		},
		{
			name: "valid external auth reference",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "auth-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "auth-config",
					},
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "auth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://keycloak.com/token",
						ClientID: "client-id",
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
						Audience: "api",
					},
				},
				Status: mcpv1alpha1.MCPExternalAuthConfigStatus{
					ConfigHash: "xyz789",
				},
			},
			expectError: false,
		},
		{
			name: "external auth config hash update",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "auth-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "auth-config",
					},
				},
				Status: mcpv1alpha1.MCPRemoteProxyStatus{
					ExternalAuthConfigHash: "old-hash",
				},
			},
			externalAuth: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "auth-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://keycloak.com/token",
						ClientID: "client-id",
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "secret",
							Key:  "key",
						},
						Audience: "api",
					},
				},
				Status: mcpv1alpha1.MCPExternalAuthConfigStatus{
					ConfigHash: "new-hash",
				},
			},
			expectError: false,
		},
		{
			name: "external auth config reference removed",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "auth-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
				Status: mcpv1alpha1.MCPRemoteProxyStatus{
					ExternalAuthConfigHash: "old-hash",
				},
			},
			expectError: false,
		},
		{
			name: "external auth config not found",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "broken-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "non-existent",
					},
				},
			},
			expectError: true,
			errContains: "failed to get MCPExternalAuthConfig",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.proxy}
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

			err := reconciler.handleExternalAuthConfig(context.TODO(), tt.proxy)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)

				// Verify status updates
				updatedProxy := &mcpv1alpha1.MCPRemoteProxy{}
				err := fakeClient.Get(context.TODO(), client.ObjectKey{
					Name:      tt.proxy.Name,
					Namespace: tt.proxy.Namespace,
				}, updatedProxy)
				assert.NoError(t, err)

				if tt.externalAuth != nil && tt.proxy.Spec.ExternalAuthConfigRef != nil {
					// Hash should be set to the external auth config's hash
					assert.Equal(t, tt.externalAuth.Status.ConfigHash, updatedProxy.Status.ExternalAuthConfigHash,
						"Status hash should be updated to match external auth config")
				} else if tt.proxy.Spec.ExternalAuthConfigRef == nil && tt.proxy.Status.ExternalAuthConfigHash != "" {
					// Hash should be cleared when reference is removed
					assert.Empty(t, updatedProxy.Status.ExternalAuthConfigHash,
						"Status hash should be cleared when reference is removed")
				}
			}
		})
	}
}

// TestLabelsForMCPRemoteProxy tests label generation
func TestLabelsForMCPRemoteProxy(t *testing.T) {
	t.Parallel()

	expected := map[string]string{
		"app":                        "mcpremoteproxy",
		"app.kubernetes.io/name":     "mcpremoteproxy",
		"app.kubernetes.io/instance": "test-proxy",
		"toolhive":                   "true",
		"toolhive-name":              "test-proxy",
	}

	result := labelsForMCPRemoteProxy("test-proxy")
	assert.Equal(t, expected, result)
}

// TestServiceNameGeneration tests service name generation
func TestServiceNameGeneration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		proxyName   string
		expected    string
		expectedURL string
	}{
		{
			proxyName:   "salesforce-proxy",
			expected:    "mcp-salesforce-proxy-remote-proxy",
			expectedURL: "http://mcp-salesforce-proxy-remote-proxy.default.svc.cluster.local:8080",
		},
		{
			proxyName:   "simple",
			expected:    "mcp-simple-remote-proxy",
			expectedURL: "http://mcp-simple-remote-proxy.default.svc.cluster.local:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.proxyName, func(t *testing.T) {
			t.Parallel()

			serviceName := createProxyServiceName(tt.proxyName)
			assert.Equal(t, tt.expected, serviceName)

			serviceURL := createProxyServiceURL(tt.proxyName, "default", 8080)
			assert.Equal(t, tt.expectedURL, serviceURL)
		})
	}
}

// TestEnsureRBACResources tests RBAC resource creation
func TestEnsureRBACResources(t *testing.T) {
	t.Parallel()

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rbac-proxy",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://mcp.example.com",
			Port:      8080,
		},
	}

	scheme := createRunConfigTestScheme()
	// Add RBAC types to scheme
	_ = rbacv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(proxy).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := reconciler.ensureRBACResources(context.TODO(), proxy)
	require.NoError(t, err)

	// Verify ServiceAccount
	sa := &corev1.ServiceAccount{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
		Namespace: proxy.Namespace,
	}, sa)
	assert.NoError(t, err)
	assert.Equal(t, proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name), sa.Name)

	// Verify Role
	role := &rbacv1.Role{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
		Namespace: proxy.Namespace,
	}, role)
	assert.NoError(t, err)
	assert.Equal(t, remoteProxyRBACRules, role.Rules)

	// Verify RoleBinding
	rb := &rbacv1.RoleBinding{}
	err = fakeClient.Get(context.TODO(), types.NamespacedName{
		Name:      proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name),
		Namespace: proxy.Namespace,
	}, rb)
	assert.NoError(t, err)
	assert.Equal(t, proxyRunnerServiceAccountNameForRemoteProxy(proxy.Name), rb.RoleRef.Name)
}

// TestUpdateMCPRemoteProxyStatus tests status update logic
func TestUpdateMCPRemoteProxyStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		proxy         *mcpv1alpha1.MCPRemoteProxy
		pods          []corev1.Pod
		expectedPhase mcpv1alpha1.MCPRemoteProxyPhase
	}{
		{
			name: "running pod",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "running-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "running-proxy-pod",
						Namespace: "default",
						Labels:    labelsForMCPRemoteProxy("running-proxy"),
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				},
			},
			expectedPhase: mcpv1alpha1.MCPRemoteProxyPhaseReady,
		},
		{
			name: "pending pod",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pending-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pending-proxy-pod",
						Namespace: "default",
						Labels:    labelsForMCPRemoteProxy("pending-proxy"),
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodPending,
					},
				},
			},
			expectedPhase: mcpv1alpha1.MCPRemoteProxyPhasePending,
		},
		{
			name: "failed pod",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "failed-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
			},
			pods: []corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "failed-proxy-pod",
						Namespace: "default",
						Labels:    labelsForMCPRemoteProxy("failed-proxy"),
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodFailed,
					},
				},
			},
			expectedPhase: mcpv1alpha1.MCPRemoteProxyPhaseFailed,
		},
		{
			name: "no pods",
			proxy: &mcpv1alpha1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-pods-proxy",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPRemoteProxySpec{
					RemoteURL: "https://mcp.example.com",
				},
			},
			pods:          []corev1.Pod{},
			expectedPhase: mcpv1alpha1.MCPRemoteProxyPhasePending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := createRunConfigTestScheme()
			objects := []runtime.Object{tt.proxy}
			for i := range tt.pods {
				objects = append(objects, &tt.pods[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(tt.proxy).
				Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := reconciler.updateMCPRemoteProxyStatus(context.TODO(), tt.proxy)
			assert.NoError(t, err)

			// Fetch updated proxy
			updatedProxy := &mcpv1alpha1.MCPRemoteProxy{}
			err = fakeClient.Get(context.TODO(), types.NamespacedName{
				Name:      tt.proxy.Name,
				Namespace: tt.proxy.Namespace,
			}, updatedProxy)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedPhase, updatedProxy.Status.Phase)
		})
	}
}

// TestGetToolConfigForMCPRemoteProxy tests tool config fetching
func TestGetToolConfigForMCPRemoteProxy(t *testing.T) {
	t.Parallel()

	toolConfig := &mcpv1alpha1.MCPToolConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-tools",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1"},
		},
	}

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
				Name: "test-tools",
			},
		},
	}

	scheme := createRunConfigTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(toolConfig, proxy).
		Build()

	result, err := ctrlutil.GetToolConfigForMCPRemoteProxy(context.TODO(), fakeClient, proxy)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "test-tools", result.Name)
}

// TestGetExternalAuthConfigForMCPRemoteProxy tests external auth config fetching
func TestGetExternalAuthConfigForMCPRemoteProxy(t *testing.T) {
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

	proxy := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-proxy",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-auth",
			},
		},
	}

	scheme := createRunConfigTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(externalAuth, proxy).
		Build()

	result, err := ctrlutil.GetExternalAuthConfigForMCPRemoteProxy(context.TODO(), fakeClient, proxy)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "test-auth", result.Name)
}
