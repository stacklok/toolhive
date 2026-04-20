// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestMCPExternalAuthConfigReconciler_calculateConfigHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec mcpv1alpha1.MCPExternalAuthConfigSpec
	}{
		{
			name: "empty spec",
			spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			},
		},
		{
			name: "with token exchange config",
			spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
					TokenURL: "https://oauth.example.com/token",
					ClientID: "test-client-id",
					ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: "test-secret",
						Key:  "client-secret",
					},
					Audience: "backend-service",
					Scopes:   []string{"read", "write"},
				},
			},
		},
		{
			name: "with custom header",
			spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
				Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
					TokenURL: "https://oauth.example.com/token",
					ClientID: "test-client-id",
					ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
						Name: "test-secret",
						Key:  "client-secret",
					},
					Audience:                "backend-service",
					ExternalTokenHeaderName: "X-Upstream-Token",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &MCPExternalAuthConfigReconciler{}

			hash1 := r.calculateConfigHash(tt.spec)
			hash2 := r.calculateConfigHash(tt.spec)

			// Same spec should produce same hash
			assert.Equal(t, hash1, hash2, "Hash should be consistent for same spec")
			assert.NotEmpty(t, hash1, "Hash should not be empty")
		})
	}

	// Different specs should produce different hashes
	t.Run("different specs produce different hashes", func(t *testing.T) {
		t.Parallel()
		r := &MCPExternalAuthConfigReconciler{}
		spec1 := mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "client1",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "secret1",
					Key:  "key1",
				},
				Audience: "audience1",
			},
		}
		spec2 := mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "client2",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "secret2",
					Key:  "key2",
				},
				Audience: "audience2",
			},
		}

		hash1 := r.calculateConfigHash(spec1)
		hash2 := r.calculateConfigHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different specs should produce different hashes")
	})
}

func TestMCPExternalAuthConfigReconciler_Reconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig
		existingMCPServer  *mcpv1alpha1.MCPServer
		expectFinalizer    bool
		expectHash         bool
	}{
		{
			name: "new external auth config without references",
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
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
					},
				},
			},
			expectFinalizer: true,
			expectHash:      true,
		},
		{
			name: "external auth config with referencing mcpserver",
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
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
						Scopes:   []string{"read", "write"},
					},
				},
			},
			existingMCPServer: &mcpv1alpha1.MCPServer{
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
			},
			expectFinalizer: true,
			expectHash:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			// Create fake client with objects
			objs := []client.Object{tt.externalAuthConfig}
			if tt.existingMCPServer != nil {
				objs = append(objs, tt.existingMCPServer)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPExternalAuthConfig{}).
				Build()

			r := &MCPExternalAuthConfigReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Reconcile
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.externalAuthConfig.Name,
					Namespace: tt.externalAuthConfig.Namespace,
				},
			}

			// First reconciliation adds the finalizer and returns Requeue: true
			result, err := r.Reconcile(ctx, req)
			require.NoError(t, err)

			// If it's a new object, it will requeue to add finalizer
			if result.RequeueAfter > 0 {
				// Second reconciliation processes the actual logic
				result, err = r.Reconcile(ctx, req)
				require.NoError(t, err)
				assert.Equal(t, time.Duration(0), result.RequeueAfter)
			}

			// Check the updated MCPExternalAuthConfig
			var updatedConfig mcpv1alpha1.MCPExternalAuthConfig
			err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
			require.NoError(t, err)

			// Check finalizer
			if tt.expectFinalizer {
				assert.Contains(t, updatedConfig.Finalizers, ExternalAuthConfigFinalizerName,
					"MCPExternalAuthConfig should have finalizer")
			}

			// Check hash in status
			if tt.expectHash {
				assert.NotEmpty(t, updatedConfig.Status.ConfigHash,
					"MCPExternalAuthConfig status should have config hash")
			}

			// Check referencing workloads in status
			if tt.existingMCPServer != nil {
				assert.Contains(t, updatedConfig.Status.ReferencingWorkloads,
					mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: tt.existingMCPServer.Name},
					"Status should contain referencing MCPServer as WorkloadReference")
			}
		})
	}
}

func TestMCPExternalAuthConfigReconciler_findReferencingWorkloads(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

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
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	mcpServer1 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server1",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-config",
			},
		},
	}

	mcpServer2 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server2",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-config",
			},
		},
	}

	mcpServer3 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server3",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			// No ExternalAuthConfigRef
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(externalAuthConfig, mcpServer1, mcpServer2, mcpServer3).
		Build()

	r := &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()
	refs, err := r.findReferencingWorkloads(ctx, externalAuthConfig)
	require.NoError(t, err)

	assert.Len(t, refs, 2, "Should find 2 referencing workloads")
	assert.Contains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server1"})
	assert.Contains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server2"})
	assert.NotContains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server3"})
}

func TestGetExternalAuthConfigForMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mcpServer      *mcpv1alpha1.MCPServer
		existingConfig *mcpv1alpha1.MCPExternalAuthConfig
		expectConfig   bool
		expectError    bool
	}{
		{
			name: "mcpserver without external auth config ref",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
				},
			},
			expectConfig: false,
			expectError:  true, // Expect an error when no ExternalAuthConfigRef is present
		},
		{
			name: "mcpserver with existing external auth config",
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
			},
			existingConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
					},
				},
			},
			expectConfig: true,
			expectError:  false,
		},
		{
			name: "mcpserver with non-existent external auth config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
						Name: "non-existent",
					},
				},
			},
			expectConfig: false,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

			objs := []client.Object{}
			if tt.existingConfig != nil {
				objs = append(objs, tt.existingConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			config, err := GetExternalAuthConfigForMCPServer(ctx, fakeClient, tt.mcpServer)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, config)
			} else {
				assert.NoError(t, err)
				if tt.expectConfig {
					assert.NotNil(t, config)
					assert.Equal(t, tt.existingConfig.Name, config.Name)
				} else {
					assert.Nil(t, config)
				}
			}
		})
	}
}

func TestMCPExternalAuthConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		externalAuthConfig     *mcpv1alpha1.MCPExternalAuthConfig
		referencingServers     []*mcpv1alpha1.MCPServer
		expectRequeue          bool
		expectFinalizerRemoved bool
	}{
		{
			name: "delete config without references",
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{ExternalAuthConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
					},
				},
			},
			expectRequeue:          false,
			expectFinalizerRemoved: true,
		},
		{
			name: "delete config with references",
			externalAuthConfig: &mcpv1alpha1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{ExternalAuthConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
					Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
					},
				},
			},
			referencingServers: []*mcpv1alpha1.MCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "server1",
						Namespace: "default",
					},
					Spec: mcpv1alpha1.MCPServerSpec{
						Image: "test-image",
						ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
							Name: "test-config",
						},
					},
				},
			},
			expectRequeue:          true,
			expectFinalizerRemoved: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

			// Build objects list
			objs := []client.Object{tt.externalAuthConfig}
			for _, server := range tt.referencingServers {
				objs = append(objs, server)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPExternalAuthConfig{}).
				Build()

			r := &MCPExternalAuthConfigReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Call handleDeletion directly
			result, err := r.handleDeletion(ctx, tt.externalAuthConfig)
			require.NoError(t, err)

			if tt.expectRequeue {
				// When still referenced, deletion is blocked with requeue
				assert.Greater(t, result.RequeueAfter, time.Duration(0),
					"Should requeue when references exist")
				assert.Contains(t, tt.externalAuthConfig.Finalizers, ExternalAuthConfigFinalizerName,
					"Finalizer should still be present when blocked")
			} else {
				assert.Equal(t, time.Duration(0), result.RequeueAfter)

				// Check if finalizer was removed from the object in memory
				if tt.expectFinalizerRemoved {
					assert.NotContains(t, tt.externalAuthConfig.Finalizers, ExternalAuthConfigFinalizerName,
						"Finalizer should be removed")
				}
			}
		})
	}
}

func TestMCPExternalAuthConfigReconciler_ConfigChangeTriggersReconciliation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
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
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPExternalAuthConfig{}).
		Build()

	r := &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      externalAuthConfig.Name,
			Namespace: externalAuthConfig.Namespace,
		},
	}

	// First reconciliation - add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0), "Should requeue after adding finalizer")

	// Second reconciliation - calculate hash
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), result.RequeueAfter)

	// Get updated config and check hash was set
	var updatedConfig mcpv1alpha1.MCPExternalAuthConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedConfig.Status.ConfigHash, "Config hash should be set")
	firstHash := updatedConfig.Status.ConfigHash

	// Update the config spec (simulate a change)
	updatedConfig.Spec.TokenExchange.Audience = "new-audience"
	updatedConfig.Generation = 2
	err = fakeClient.Update(ctx, &updatedConfig)
	require.NoError(t, err)

	// Third reconciliation - should detect change and update hash
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Get final config and verify hash changed
	var finalConfig mcpv1alpha1.MCPExternalAuthConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &finalConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, finalConfig.Status.ConfigHash, "Config hash should still be set")
	assert.NotEqual(t, firstHash, finalConfig.Status.ConfigHash, "Hash should change when spec changes")
	assert.Equal(t, int64(2), finalConfig.Status.ObservedGeneration, "ObservedGeneration should be updated")

	// Verify MCPServer has annotation with new hash
	var updatedServer mcpv1alpha1.MCPServer
	err = fakeClient.Get(ctx, types.NamespacedName{
		Name:      mcpServer.Name,
		Namespace: mcpServer.Namespace,
	}, &updatedServer)
	require.NoError(t, err)
	assert.Equal(t, finalConfig.Status.ConfigHash,
		updatedServer.Annotations["toolhive.stacklok.dev/externalauthconfig-hash"],
		"MCPServer should have annotation with new config hash")
}

func TestMCPExternalAuthConfigReconciler_ReferencingWorkloadsUpdatedWithoutHashChange(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(externalAuthConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPExternalAuthConfig{}).
		Build()

	r := &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      externalAuthConfig.Name,
			Namespace: externalAuthConfig.Namespace,
		},
	}

	// First reconciliation - add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	// Second reconciliation - sets hash, no servers yet
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var updatedConfig mcpv1alpha1.MCPExternalAuthConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedConfig.Status.ConfigHash)
	assert.Empty(t, updatedConfig.Status.ReferencingWorkloads, "No workloads should be referencing yet")

	// Now add an MCPServer that references this config (without changing the config spec)
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "new-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-config",
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, mcpServer))

	// Reconcile again - hash hasn't changed, but referencing servers should be updated
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.Contains(t, updatedConfig.Status.ReferencingWorkloads,
		mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "new-server"},
		"ReferencingWorkloads should be updated even without hash change")
}

func TestMCPExternalAuthConfigReconciler_ReferencingWorkloadsRemovedOnServerDeletion(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-to-delete",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "test-config",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPExternalAuthConfig{}).
		Build()

	r := &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      externalAuthConfig.Name,
			Namespace: externalAuthConfig.Namespace,
		},
	}

	// Add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	// Set hash and referencing servers
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var updatedConfig mcpv1alpha1.MCPExternalAuthConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.Contains(t, updatedConfig.Status.ReferencingWorkloads,
		mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server-to-delete"})

	// Delete the MCPServer
	require.NoError(t, fakeClient.Delete(ctx, mcpServer))

	// Reconcile again - referencing servers should be empty now
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.Empty(t, updatedConfig.Status.ReferencingWorkloads,
		"ReferencingWorkloads should be empty after server deletion")
}

func TestMCPExternalAuthConfigReconciler_findReferencingWorkloads_authServerRef(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auth-server-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer:                       "https://auth.example.com",
				AuthorizationEndpointBaseURL: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
		},
	}

	// Server referencing via authServerRef
	serverViaAuthServerRef := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-via-authserverref",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			AuthServerRef: &mcpv1alpha1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "auth-server-config",
			},
		},
	}

	// Server referencing via externalAuthConfigRef
	serverViaExtAuth := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-via-extauth",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "auth-server-config",
			},
		},
	}

	// Server not referencing this config at all
	serverNoRef := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-no-ref",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(externalAuthConfig, serverViaAuthServerRef, serverViaExtAuth, serverNoRef).
		Build()

	r := &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()
	refs, err := r.findReferencingWorkloads(ctx, externalAuthConfig)
	require.NoError(t, err)

	assert.Len(t, refs, 2, "Should find 2 referencing workloads (one via authServerRef, one via externalAuthConfigRef)")
	assert.Contains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server-via-authserverref"})
	assert.Contains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server-via-extauth"})
	assert.NotContains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server-no-ref"})
}

func TestMCPExternalAuthConfigReconciler_findReferencingWorkloads_bothRefsOnSameServer(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	// A server has externalAuthConfigRef pointing to "token-exchange-config"
	// AND authServerRef pointing to "embedded-auth-config".
	// Both configs should discover this server during reconciliation.

	tokenExchangeConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "token-exchange-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	embeddedAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "embedded-auth-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer:                       "https://auth.example.com",
				AuthorizationEndpointBaseURL: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
		},
	}

	// Server with both refs pointing to different configs
	serverWithBothRefs := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-with-both-refs",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "token-exchange-config",
			},
			AuthServerRef: &mcpv1alpha1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "embedded-auth-config",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(tokenExchangeConfig, embeddedAuthConfig, serverWithBothRefs).
		Build()

	r := &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()

	// Reconciling the token-exchange-config should find the server via externalAuthConfigRef
	refsForTokenExchange, err := r.findReferencingWorkloads(ctx, tokenExchangeConfig)
	require.NoError(t, err)
	assert.Len(t, refsForTokenExchange, 1, "token-exchange-config should find server via externalAuthConfigRef")
	assert.Contains(t, refsForTokenExchange, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server-with-both-refs"})

	// Reconciling the embedded-auth-config should find the server via authServerRef
	refsForEmbedded, err := r.findReferencingWorkloads(ctx, embeddedAuthConfig)
	require.NoError(t, err)
	assert.Len(t, refsForEmbedded, 1, "embedded-auth-config should find server via authServerRef")
	assert.Contains(t, refsForEmbedded, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server-with-both-refs"})

	// Also verify findReferencingMCPServers returns the server for both configs
	serversForTokenExchange, err := r.findReferencingMCPServers(ctx, tokenExchangeConfig)
	require.NoError(t, err)
	assert.Len(t, serversForTokenExchange, 1)
	assert.Equal(t, "server-with-both-refs", serversForTokenExchange[0].Name)

	serversForEmbedded, err := r.findReferencingMCPServers(ctx, embeddedAuthConfig)
	require.NoError(t, err)
	assert.Len(t, serversForEmbedded, 1)
	assert.Equal(t, "server-with-both-refs", serversForEmbedded[0].Name)
}

func TestMCPExternalAuthConfigReconciler_findReferencingMCPServers_deduplicates(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	// A server has both externalAuthConfigRef and authServerRef pointing to the SAME config.
	// The server should appear only once in the results.
	config := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1alpha1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1alpha1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-both-same",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "shared-config",
			},
			AuthServerRef: &mcpv1alpha1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "shared-config",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(config, server).
		Build()

	r := &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()
	servers, err := r.findReferencingMCPServers(ctx, config)
	require.NoError(t, err)
	assert.Len(t, servers, 1, "Server should appear only once even when both refs point to the same config")
	assert.Equal(t, "server-both-same", servers[0].Name)
}

func TestMCPExternalAuthConfigReconciler_findReferencingWorkloads_mcpRemoteProxy(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	config := &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auth-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer:                       "https://auth.example.com",
				AuthorizationEndpointBaseURL: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1alpha1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
		},
	}

	// MCPRemoteProxy referencing via externalAuthConfigRef
	proxyViaExtAuth := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy-via-extauth",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://remote.example.com",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "auth-config",
			},
		},
	}

	// MCPRemoteProxy referencing via authServerRef
	proxyViaAuthServerRef := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy-via-authserverref",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://remote.example.com",
			AuthServerRef: &mcpv1alpha1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "auth-config",
			},
		},
	}

	// MCPRemoteProxy not referencing this config
	proxyNoRef := &mcpv1alpha1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy-no-ref",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPRemoteProxySpec{
			RemoteURL: "https://remote.example.com",
		},
	}

	// MCPServer also referencing the same config
	server := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server-ref",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
				Name: "auth-config",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(config, proxyViaExtAuth, proxyViaAuthServerRef, proxyNoRef, server).
		Build()

	r := &MCPExternalAuthConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()
	refs, err := r.findReferencingWorkloads(ctx, config)
	require.NoError(t, err)

	assert.Len(t, refs, 3, "Should find 3 referencing workloads (1 MCPServer + 2 MCPRemoteProxies)")
	assert.Contains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPServer", Name: "server-ref"})
	assert.Contains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPRemoteProxy", Name: "proxy-via-extauth"})
	assert.Contains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPRemoteProxy", Name: "proxy-via-authserverref"})
	assert.NotContains(t, refs, mcpv1alpha1.WorkloadReference{Kind: "MCPRemoteProxy", Name: "proxy-no-ref"})
}
