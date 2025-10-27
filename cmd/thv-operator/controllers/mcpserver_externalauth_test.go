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
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestMCPServerReconciler_handleExternalAuthConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		mcpServer          *mcpv1alpha1.MCPServer
		externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig
		expectError        bool
		expectHash         string
		expectHashCleared  bool
	}{
		{
			name: "no external auth config reference",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					// No ExternalAuthConfigRef
				},
				Status: mcpv1alpha1.MCPServerStatus{},
			},
			expectError:       false,
			expectHash:        "",
			expectHashCleared: false,
		},
		{
			name: "external auth config reference exists",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "test-config",
					},
				},
				Status: mcpv1alpha1.MCPServerStatus{},
			},
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
					},
				},
				Status: mcpv1alpha1.MCPExternalAuthConfigStatus{
					ConfigHash: "test-hash-123",
				},
			},
			expectError: false,
			expectHash:  "test-hash-123",
		},
		{
			name: "external auth config not found",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "non-existent-config",
					},
				},
				Status: mcpv1alpha1.MCPServerStatus{},
			},
			expectError: true,
		},
		{
			name: "external auth config hash changed",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "test-config",
					},
				},
				Status: mcpv1alpha1.MCPServerStatus{
					ExternalAuthConfigHash: "old-hash",
				},
			},
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "new-audience", // Changed config
					},
				},
				Status: mcpv1alpha1.MCPExternalAuthConfigStatus{
					ConfigHash: "new-hash-456",
				},
			},
			expectError: false,
			expectHash:  "new-hash-456",
		},
		{
			name: "clear hash when reference is removed",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					// No ExternalAuthConfigRef (was removed)
				},
				Status: mcpv1alpha1.MCPServerStatus{
					ExternalAuthConfigHash: "old-hash-to-clear",
				},
			},
			expectError:       false,
			expectHash:        "",
			expectHashCleared: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			// Build objects for fake client
			objs := []runtime.Object{tt.mcpServer}
			if tt.externalAuthConfig != nil {
				objs = append(objs, tt.externalAuthConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
				Build()

			reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

			// Execute
			err := reconciler.handleExternalAuthConfig(ctx, tt.mcpServer)

			// Assert
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				if tt.expectHash != "" {
					assert.Equal(t, tt.expectHash, tt.mcpServer.Status.ExternalAuthConfigHash,
						"Hash should be updated in status")
				}

				if tt.expectHashCleared {
					assert.Empty(t, tt.mcpServer.Status.ExternalAuthConfigHash,
						"Hash should be cleared from status")
				}
			}
		})
	}
}

func TestMCPServerReconciler_handleExternalAuthConfig_SameNamespace(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// External auth config in a different namespace
	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "other-namespace",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: mcpv1alpha1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
		Status: mcpv1alpha1.MCPExternalAuthConfigStatus{
			ConfigHash: "test-hash-123",
		},
	}

	// MCPServer in different namespace
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-config", // References config in same namespace (default)
			},
		},
		Status: mcpv1alpha1.MCPServerStatus{},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	// Execute - should fail because config is in different namespace
	err := reconciler.handleExternalAuthConfig(ctx, mcpServer)

	// Assert - should get an error because config is not in same namespace
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMCPServerReconciler_handleExternalAuthConfig_HashUpdateTrigger(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: mcpv1alpha1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
		Status: mcpv1alpha1.MCPExternalAuthConfigStatus{
			ConfigHash: "initial-hash",
		},
	}

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-config",
			},
		},
		Status: mcpv1alpha1.MCPServerStatus{
			ExternalAuthConfigHash: "initial-hash",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}, &mcpv1alpha1.MCPExternalAuthConfig{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	// First call - hash is the same, no update needed
	err := reconciler.handleExternalAuthConfig(ctx, mcpServer)
	assert.NoError(t, err)
	assert.Equal(t, "initial-hash", mcpServer.Status.ExternalAuthConfigHash)

	// Simulate external auth config change - need to get the object first
	var updatedConfig mcpv1alpha1.MCPExternalAuthConfig
	err = fakeClient.Get(ctx, client.ObjectKey{Name: "test-config", Namespace: "default"}, &updatedConfig)
	require.NoError(t, err)

	updatedConfig.Status.ConfigHash = "updated-hash"
	err = fakeClient.Status().Update(ctx, &updatedConfig)
	require.NoError(t, err)

	// Second call - hash changed, should update
	err = reconciler.handleExternalAuthConfig(ctx, mcpServer)
	assert.NoError(t, err)
	assert.Equal(t, "updated-hash", mcpServer.Status.ExternalAuthConfigHash,
		"Hash should be updated to new value")
}

func TestMCPServerReconciler_handleExternalAuthConfig_NoHashInConfig(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	// External auth config without hash in status
	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: mcpv1alpha1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
		Status: mcpv1alpha1.MCPExternalAuthConfigStatus{
			// ConfigHash is empty
		},
	}

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-config",
			},
		},
		Status: mcpv1alpha1.MCPServerStatus{},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPServer{}).
		Build()

	reconciler := newTestMCPServerReconciler(fakeClient, scheme, kubernetes.PlatformKubernetes)

	// Execute
	err := reconciler.handleExternalAuthConfig(ctx, mcpServer)

	// Assert - should succeed, but hash will be empty
	assert.NoError(t, err)
	assert.Empty(t, mcpServer.Status.ExternalAuthConfigHash,
		"Hash should be empty when external auth config has no hash")
}
