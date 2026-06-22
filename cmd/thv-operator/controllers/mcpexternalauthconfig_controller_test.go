// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	stderrors "errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	"github.com/stacklok/toolhive/pkg/runner"
)

func TestMCPExternalAuthConfigReconciler_calculateConfigHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec mcpv1beta1.MCPExternalAuthConfigSpec
	}{
		{
			name: "empty spec",
			spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			},
		},
		{
			name: "with token exchange config",
			spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1beta1.TokenExchangeConfig{
					TokenURL: "https://oauth.example.com/token",
					ClientID: "test-client-id",
					ClientSecretRef: &mcpv1beta1.SecretKeyRef{
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
			spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
				TokenExchange: &mcpv1beta1.TokenExchangeConfig{
					TokenURL: "https://oauth.example.com/token",
					ClientID: "test-client-id",
					ClientSecretRef: &mcpv1beta1.SecretKeyRef{
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
		spec1 := mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "client1",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "secret1",
					Key:  "key1",
				},
				Audience: "audience1",
			},
		}
		spec2 := mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "client2",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
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
		externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig
		existingMCPServer  *mcpv1beta1.MCPServer
		expectFinalizer    bool
		expectHash         bool
	}{
		{
			name: "new external auth config without references",
			externalAuthConfig: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1beta1.SecretKeyRef{
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
			externalAuthConfig: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
						Scopes:   []string{"read", "write"},
					},
				},
			},
			existingMCPServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
				v1beta1test.WithExternalAuthConfigRef("test-config"),
			),
			expectFinalizer: true,
			expectHash:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := testutil.NewScheme(t)

			// Create fake client with objects
			objs := []client.Object{tt.externalAuthConfig}
			if tt.existingMCPServer != nil {
				objs = append(objs, tt.existingMCPServer)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
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
			var updatedConfig mcpv1beta1.MCPExternalAuthConfig
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
					mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: tt.existingMCPServer.Name},
					"Status should contain referencing MCPServer as WorkloadReference")
			}
		})
	}
}

func TestMCPExternalAuthConfigReconciler_findReferencingWorkloads(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	mcpServer1 := v1beta1test.NewMCPServer("server1", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("test-config"),
	)

	mcpServer2 := v1beta1test.NewMCPServer("server2", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("test-config"),
	)

	mcpServer3 := v1beta1test.NewMCPServer("server3", "default",
		v1beta1test.WithImage("test-image"),
		// No ExternalAuthConfigRef
	)

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
	assert.Contains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server1"})
	assert.Contains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server2"})
	assert.NotContains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server3"})
}

func TestGetExternalAuthConfigForMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mcpServer      *mcpv1beta1.MCPServer
		existingConfig *mcpv1beta1.MCPExternalAuthConfig
		expectConfig   bool
		expectError    bool
	}{
		{
			name: "mcpserver without external auth config ref",
			mcpServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
			),
			expectConfig: false,
			expectError:  true, // Expect an error when no ExternalAuthConfigRef is present
		},
		{
			name: "mcpserver with existing external auth config",
			mcpServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
				v1beta1test.WithExternalAuthConfigRef("test-config"),
			),
			existingConfig: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1beta1.SecretKeyRef{
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
			mcpServer: v1beta1test.NewMCPServer("test-server", "default",
				v1beta1test.WithImage("test-image"),
				v1beta1test.WithExternalAuthConfigRef("non-existent"),
			),
			expectConfig: false,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := testutil.NewScheme(t)

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
		externalAuthConfig     *mcpv1beta1.MCPExternalAuthConfig
		referencingServers     []*mcpv1beta1.MCPServer
		expectRequeue          bool
		expectFinalizerRemoved bool
	}{
		{
			name: "delete config without references",
			externalAuthConfig: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{ExternalAuthConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1beta1.SecretKeyRef{
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
			externalAuthConfig: &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{ExternalAuthConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
					TokenExchange: &mcpv1beta1.TokenExchangeConfig{
						TokenURL: "https://oauth.example.com/token",
						ClientID: "test-client",
						ClientSecretRef: &mcpv1beta1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
					},
				},
			},
			referencingServers: []*mcpv1beta1.MCPServer{
				v1beta1test.NewMCPServer("server1", "default",
					v1beta1test.WithImage("test-image"),
					v1beta1test.WithExternalAuthConfigRef("test-config"),
				),
			},
			expectRequeue:          true,
			expectFinalizerRemoved: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := testutil.NewScheme(t)

			// Build objects list
			objs := []client.Object{tt.externalAuthConfig}
			for _, server := range tt.referencingServers {
				objs = append(objs, server)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
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

	scheme := testutil.NewScheme(t)

	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	mcpServer := v1beta1test.NewMCPServer("test-server", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("test-config"),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
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
	var updatedConfig mcpv1beta1.MCPExternalAuthConfig
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
	var finalConfig mcpv1beta1.MCPExternalAuthConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &finalConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, finalConfig.Status.ConfigHash, "Config hash should still be set")
	assert.NotEqual(t, firstHash, finalConfig.Status.ConfigHash, "Hash should change when spec changes")
	assert.Equal(t, int64(2), finalConfig.Status.ObservedGeneration, "ObservedGeneration should be updated")

	// Verify MCPServer has annotation with new hash
	var updatedServer mcpv1beta1.MCPServer
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

	scheme := testutil.NewScheme(t)

	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
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
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
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

	var updatedConfig mcpv1beta1.MCPExternalAuthConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedConfig.Status.ConfigHash)
	assert.Empty(t, updatedConfig.Status.ReferencingWorkloads, "No workloads should be referencing yet")
	assert.EqualValues(t, 0, updatedConfig.Status.ReferenceCount)

	// Now add an MCPServer that references this config (without changing the config spec)
	mcpServer := v1beta1test.NewMCPServer("new-server", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("test-config"),
	)
	require.NoError(t, fakeClient.Create(ctx, mcpServer))

	// Reconcile again - hash hasn't changed, but referencing servers should be updated
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.Contains(t, updatedConfig.Status.ReferencingWorkloads,
		mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "new-server"},
		"ReferencingWorkloads should be updated even without hash change")
	assert.EqualValues(t, 1, updatedConfig.Status.ReferenceCount)
}

func TestMCPExternalAuthConfigReconciler_ReferencingWorkloadsRemovedOnServerDeletion(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := testutil.NewScheme(t)

	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	mcpServer := v1beta1test.NewMCPServer("server-to-delete", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("test-config"),
	)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(externalAuthConfig, mcpServer).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
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

	var updatedConfig mcpv1beta1.MCPExternalAuthConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.Contains(t, updatedConfig.Status.ReferencingWorkloads,
		mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server-to-delete"})
	assert.EqualValues(t, 1, updatedConfig.Status.ReferenceCount)

	// Delete the MCPServer
	require.NoError(t, fakeClient.Delete(ctx, mcpServer))

	// Reconcile again - referencing servers should be empty now
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.Empty(t, updatedConfig.Status.ReferencingWorkloads,
		"ReferencingWorkloads should be empty after server deletion")
	assert.EqualValues(t, 0, updatedConfig.Status.ReferenceCount)
}

func TestMCPExternalAuthConfigReconciler_findReferencingWorkloads_authServerRef(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auth-server-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:                       "https://auth.example.com",
				AuthorizationEndpointBaseURL: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
		},
	}

	// Server referencing via authServerRef
	serverViaAuthServerRef := v1beta1test.NewMCPServer("server-via-authserverref", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "auth-server-config"),
	)

	// Server referencing via externalAuthConfigRef
	serverViaExtAuth := v1beta1test.NewMCPServer("server-via-extauth", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("auth-server-config"),
	)

	// Server not referencing this config at all
	serverNoRef := v1beta1test.NewMCPServer("server-no-ref", "default",
		v1beta1test.WithImage("test-image"),
	)

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
	assert.Contains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server-via-authserverref"})
	assert.Contains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server-via-extauth"})
	assert.NotContains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server-no-ref"})
}

func TestMCPExternalAuthConfigReconciler_findReferencingWorkloads_bothRefsOnSameServer(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	// A server has externalAuthConfigRef pointing to "token-exchange-config"
	// AND authServerRef pointing to "embedded-auth-config".
	// Both configs should discover this server during reconciliation.

	tokenExchangeConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "token-exchange-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	embeddedAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "embedded-auth-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:                       "https://auth.example.com",
				AuthorizationEndpointBaseURL: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
		},
	}

	// Server with both refs pointing to different configs
	serverWithBothRefs := v1beta1test.NewMCPServer("server-with-both-refs", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("token-exchange-config"),
		v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "embedded-auth-config"),
	)

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
	assert.Contains(t, refsForTokenExchange, mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server-with-both-refs"})

	// Reconciling the embedded-auth-config should find the server via authServerRef
	refsForEmbedded, err := r.findReferencingWorkloads(ctx, embeddedAuthConfig)
	require.NoError(t, err)
	assert.Len(t, refsForEmbedded, 1, "embedded-auth-config should find server via authServerRef")
	assert.Contains(t, refsForEmbedded, mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server-with-both-refs"})

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

	scheme := testutil.NewScheme(t)

	// A server has both externalAuthConfigRef and authServerRef pointing to the SAME config.
	// The server should appear only once in the results.
	config := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
	}

	server := v1beta1test.NewMCPServer("server-both-same", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("shared-config"),
		v1beta1test.WithAuthServerRef("MCPExternalAuthConfig", "shared-config"),
	)

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

	scheme := testutil.NewScheme(t)

	config := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "auth-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:                       "https://auth.example.com",
				AuthorizationEndpointBaseURL: "https://auth.example.com",
				SigningKeySecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "signing-key", Key: "private.pem"},
				},
				HMACSecretRefs: []mcpv1beta1.SecretKeyRef{
					{Name: "hmac-secret", Key: "hmac"},
				},
			},
		},
	}

	// MCPRemoteProxy referencing via externalAuthConfigRef
	proxyViaExtAuth := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy-via-extauth",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			RemoteURL: "https://remote.example.com",
			ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{
				Name: "auth-config",
			},
		},
	}

	// MCPRemoteProxy referencing via authServerRef
	proxyViaAuthServerRef := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy-via-authserverref",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			RemoteURL: "https://remote.example.com",
			AuthServerRef: &mcpv1beta1.AuthServerRef{
				Kind: "MCPExternalAuthConfig",
				Name: "auth-config",
			},
		},
	}

	// MCPRemoteProxy not referencing this config
	proxyNoRef := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "proxy-no-ref",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			RemoteURL: "https://remote.example.com",
		},
	}

	// MCPServer also referencing the same config
	server := v1beta1test.NewMCPServer("server-ref", "default",
		v1beta1test.WithImage("test-image"),
		v1beta1test.WithExternalAuthConfigRef("auth-config"),
	)

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
	assert.Contains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPServer", Name: "server-ref"})
	assert.Contains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPRemoteProxy", Name: "proxy-via-extauth"})
	assert.Contains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPRemoteProxy", Name: "proxy-via-authserverref"})
	assert.NotContains(t, refs, mcpv1beta1.WorkloadReference{Kind: "MCPRemoteProxy", Name: "proxy-no-ref"})
}

// TestMCPExternalAuthConfigReconciler_IdentitySynthesizedCondition asserts
// the advisory IdentitySynthesized condition tracks the upstreamProviders
// shape: True+name(s) when any OAuth2 upstream lacks userInfo, False when
// all have userInfo, absent for non-embeddedAuthServer types.
func TestMCPExternalAuthConfigReconciler_IdentitySynthesizedCondition(t *testing.T) {
	t.Parallel()

	signing := []mcpv1beta1.SecretKeyRef{{Name: "signing-key", Key: "private.pem"}}

	embeddedAuthServer := func(upstreams ...mcpv1beta1.UpstreamProviderConfig) *mcpv1beta1.EmbeddedAuthServerConfig {
		return &mcpv1beta1.EmbeddedAuthServerConfig{
			Issuer:               "https://auth.example.com",
			SigningKeySecretRefs: signing,
			UpstreamProviders:    upstreams,
		}
	}
	oauth2Upstream := func(name string, withUserInfo bool) mcpv1beta1.UpstreamProviderConfig {
		cfg := &mcpv1beta1.OAuth2UpstreamConfig{
			AuthorizationEndpoint: "https://idp.example.com/authorize",
			TokenEndpoint:         "https://idp.example.com/token",
			ClientID:              "client",
		}
		if withUserInfo {
			cfg.UserInfo = &mcpv1beta1.UserInfoConfig{EndpointURL: "https://idp.example.com/userinfo"}
		}
		return mcpv1beta1.UpstreamProviderConfig{
			Name:         name,
			Type:         mcpv1beta1.UpstreamProviderTypeOAuth2,
			OAuth2Config: cfg,
		}
	}

	tests := []struct {
		name              string
		spec              mcpv1beta1.MCPExternalAuthConfigSpec
		wantConditionType bool                   // whether the condition should be present at all
		wantStatus        metav1.ConditionStatus // ignored when wantConditionType is false
		wantReason        string
		wantNamesInMsg    []string // every value must appear in the message
	}{
		{
			name: "non-embeddedAuthServer type does not emit the condition",
			spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeUnauthenticated,
			},
			wantConditionType: false,
		},
		{
			name: "embeddedAuthServer with all OAuth2 upstreams having userInfo emits False",
			spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: embeddedAuthServer(
					oauth2Upstream("primary", true),
					oauth2Upstream("secondary", true),
				),
			},
			wantConditionType: true,
			wantStatus:        metav1.ConditionFalse,
			wantReason:        mcpv1beta1.ConditionReasonIdentitySynthesizedInactive,
		},
		{
			name: "embeddedAuthServer with one OAuth2 upstream missing userInfo emits True with name in message",
			spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: embeddedAuthServer(
					oauth2Upstream("primary", true),
					oauth2Upstream("atlassian", false),
				),
			},
			wantConditionType: true,
			wantStatus:        metav1.ConditionTrue,
			wantReason:        mcpv1beta1.ConditionReasonIdentitySynthesizedActive,
			wantNamesInMsg:    []string{"atlassian"},
		},
		{
			name: "multiple synthesizing upstreams are listed in the message",
			spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
				EmbeddedAuthServer: embeddedAuthServer(
					oauth2Upstream("zeta", false),
					oauth2Upstream("alpha", false),
				),
			},
			wantConditionType: true,
			wantStatus:        metav1.ConditionTrue,
			wantReason:        mcpv1beta1.ConditionReasonIdentitySynthesizedActive,
			wantNamesInMsg:    []string{"alpha", "zeta"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: tt.spec,
			}

			scheme := testutil.NewScheme(t)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cfg).
				WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
				Build()

			r := &MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme}
			req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}}

			// First reconcile adds the finalizer; second runs the body.
			result, err := r.Reconcile(t.Context(), req)
			require.NoError(t, err)
			if result.RequeueAfter > 0 {
				_, err = r.Reconcile(t.Context(), req)
				require.NoError(t, err)
			}

			var got mcpv1beta1.MCPExternalAuthConfig
			require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &got))

			cond := findCondition(got.Status.Conditions, mcpv1beta1.ConditionTypeIdentitySynthesized)
			if !tt.wantConditionType {
				assert.Nil(t, cond, "IdentitySynthesized condition should not be set for non-embeddedAuthServer types")
				return
			}

			require.NotNil(t, cond, "IdentitySynthesized condition should be set")
			assert.Equal(t, tt.wantStatus, cond.Status)
			assert.Equal(t, tt.wantReason, cond.Reason)
			for _, name := range tt.wantNamesInMsg {
				assert.Contains(t, cond.Message, name,
					"upstream %q should be named in the condition message", name)
			}
		})
	}
}

// TestMCPExternalAuthConfigReconciler_IdentitySynthesizedTransitionsOnValidationFailure
// pins the contract that the IdentitySynthesized advisory is recomputed from
// the current spec on every reconcile, including the validation-failure path.
// Without this, breaking a previously-valid spec would leave a stale
// IdentitySynthesized=True dangling alongside Valid=False — naming an
// upstream that the broken spec no longer mentions.
func TestMCPExternalAuthConfigReconciler_IdentitySynthesizedTransitionsOnValidationFailure(t *testing.T) {
	t.Parallel()

	signing := []mcpv1beta1.SecretKeyRef{{Name: "signing-key", Key: "private.pem"}}
	syntheticUpstream := mcpv1beta1.UpstreamProviderConfig{
		Name: "atlassian",
		Type: mcpv1beta1.UpstreamProviderTypeOAuth2,
		OAuth2Config: &mcpv1beta1.OAuth2UpstreamConfig{
			AuthorizationEndpoint: "https://idp.example.com/authorize",
			TokenEndpoint:         "https://idp.example.com/token",
			ClientID:              "client",
			// UserInfo intentionally nil — synthesizes identity.
		},
	}

	cfg := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "transition-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				Issuer:               "https://auth.example.com",
				SigningKeySecretRefs: signing,
				UpstreamProviders:    []mcpv1beta1.UpstreamProviderConfig{syntheticUpstream},
			},
		},
	}

	scheme := testutil.NewScheme(t)

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
		Build()

	r := &MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: cfg.Name, Namespace: cfg.Namespace}}

	// First reconcile adds the finalizer; the requeued reconcile runs the body.
	result, err := r.Reconcile(t.Context(), req)
	require.NoError(t, err)
	if result.RequeueAfter > 0 {
		_, err = r.Reconcile(t.Context(), req)
		require.NoError(t, err)
	}

	var initial mcpv1beta1.MCPExternalAuthConfig
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &initial))

	cond := findCondition(initial.Status.Conditions, mcpv1beta1.ConditionTypeIdentitySynthesized)
	require.NotNil(t, cond, "synthesizing upstream should produce IdentitySynthesized condition")
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonIdentitySynthesizedActive, cond.Reason)
	assert.Contains(t, cond.Message, "atlassian", "initial message must name the synthesizing upstream")

	validCond := findCondition(initial.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	require.NotNil(t, validCond)
	assert.Equal(t, metav1.ConditionTrue, validCond.Status)

	// Mutate the spec to break validation: empty UpstreamProviders fails
	// validateEmbeddedAuthServer ("at least one upstream provider is
	// required") AND removes the synthesizing upstream that the prior
	// IdentitySynthesized=True message names.
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &initial))
	initial.Spec.EmbeddedAuthServer.UpstreamProviders = nil
	require.NoError(t, fakeClient.Update(t.Context(), &initial))

	_, err = r.Reconcile(t.Context(), req)
	require.NoError(t, err)

	var after mcpv1beta1.MCPExternalAuthConfig
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &after))

	validCond = findCondition(after.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	require.NotNil(t, validCond)
	assert.Equal(t, metav1.ConditionFalse, validCond.Status, "validation must fail on empty upstream list")
	assert.Equal(t, "ValidationFailed", validCond.Reason)

	cond = findCondition(after.Status.Conditions, mcpv1beta1.ConditionTypeIdentitySynthesized)
	require.NotNil(t, cond, "advisory must be recomputed on the validation-failure path, not left stale")
	assert.Equal(t, metav1.ConditionFalse, cond.Status,
		"empty upstream list has no synthesizing providers; advisory must flip to False")
	assert.Equal(t, mcpv1beta1.ConditionReasonIdentitySynthesizedInactive, cond.Reason)
	assert.NotContains(t, cond.Message, "atlassian",
		"stale message naming the now-removed upstream must not survive the broken edit")
}

// findCondition returns a pointer to the named condition, or nil when absent.
func findCondition(conditions []metav1.Condition, t string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == t {
			return &conditions[i]
		}
	}
	return nil
}

// TestMCPExternalAuthConfigReconciler_OBO_DefaultHandler_SetsEnterpriseRequired
// proves the dispatch wiring: with the default OBO handler installed (the
// upstream-only state), reconciling an obo-typed MCPExternalAuthConfig surfaces
// Valid=False / Reason=EnterpriseRequired rather than the generic "unsupported
// external auth type" path. Drives the reconciler directly with a fake client
// to bypass the CRD enum (which does not admit "obo" until #5329 lands).
func TestMCPExternalAuthConfigReconciler_OBO_DefaultHandler_SetsEnterpriseRequired(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	cfg := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "obo-config",
			Namespace: "default",
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO:  &mcpv1beta1.OBOConfig{},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
		Build()

	r := &MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      cfg.Name,
		Namespace: cfg.Namespace,
	}}

	// First reconcile adds the finalizer; the requeued reconcile runs the body.
	result, err := r.Reconcile(t.Context(), req)
	require.NoError(t, err)
	if result.RequeueAfter > 0 {
		_, err = r.Reconcile(t.Context(), req)
		require.NoError(t, err)
	}

	var updated mcpv1beta1.MCPExternalAuthConfig
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &updated))

	validCond := findCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	require.NotNil(t, validCond, "Valid condition must be set for OBO-typed config")
	assert.Equal(t, metav1.ConditionFalse, validCond.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonEnterpriseRequired, validCond.Reason,
		"the exact reason string is part of the user-facing contract — external consumers pattern-match on it")

	// Generic-error guard: the dispatch path must NOT leak a generic
	// "unsupported external auth type" or "unknown middleware type" message.
	assert.NotContains(t, validCond.Message, "unsupported external auth type")
	assert.NotContains(t, validCond.Message, "unknown middleware type")

	// Positive assertion: condition.Message must surface the sentinel's
	// user-facing text. Without this check a refactor that emptied or
	// rewrote the message would still pass the reason-string assertion.
	assert.Equal(t, obo.ErrEnterpriseRequired.Error(), validCond.Message,
		"condition.Message must surface the sentinel's user-facing text")
}

// TestMCPExternalAuthConfigReconciler_OBO_ClearsStaleIdentitySynthesized
// proves that when a user switches an existing config from embeddedAuthServer
// (which set IdentitySynthesized=True) to obo, the stale IdentitySynthesized
// condition is removed alongside the new Valid=False/EnterpriseRequired
// condition. Regression for the bug where setInvalid's MutateAndPatchStatus
// diffed against an already-mutated snapshot and silently dropped the
// IdentitySynthesized removal.
func TestMCPExternalAuthConfigReconciler_OBO_ClearsStaleIdentitySynthesized(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	// Construct a config that is already in the obo type but has a stale
	// IdentitySynthesized condition left over from a prior embeddedAuthServer
	// configuration. The reconciler must remove that condition on its next
	// pass even though the failure path now routes through MutateAndPatchStatus.
	cfg := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "obo-config",
			Namespace:  "default",
			Finalizers: []string{ExternalAuthConfigFinalizerName},
			Generation: 2,
		},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeOBO,
			OBO:  &mcpv1beta1.OBOConfig{},
		},
		Status: mcpv1beta1.MCPExternalAuthConfigStatus{
			Conditions: []metav1.Condition{
				{
					Type:               mcpv1beta1.ConditionTypeIdentitySynthesized,
					Status:             metav1.ConditionTrue,
					Reason:             mcpv1beta1.ConditionReasonIdentitySynthesizedActive,
					Message:            "stale message from the embeddedAuthServer days",
					ObservedGeneration: 1,
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cfg).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
		Build()

	r := &MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{
		Name:      cfg.Name,
		Namespace: cfg.Namespace,
	}}

	_, err := r.Reconcile(t.Context(), req)
	require.NoError(t, err)

	var updated mcpv1beta1.MCPExternalAuthConfig
	require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &updated))

	// IdentitySynthesized must be removed (the spec is no longer embeddedAuthServer).
	assert.Nil(t, findCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeIdentitySynthesized),
		"stale IdentitySynthesized condition must be removed once the spec leaves embeddedAuthServer")

	// Valid=False/EnterpriseRequired must be set in the same reconcile pass.
	validCond := findCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	require.NotNil(t, validCond)
	assert.Equal(t, metav1.ConditionFalse, validCond.Status)
	assert.Equal(t, mcpv1beta1.ConditionReasonEnterpriseRequired, validCond.Reason)
}

// defaultOBOHandlerStub returns a handler whose every method returns
// obo.ErrEnterpriseRequired — the same shape as the package default. Used to
// restore the registered handler after a test that overrode it.
func defaultOBOHandlerStub() ctrlutil.OBOHandler {
	return ctrlutil.OBOHandler{
		Validate: func(*mcpv1beta1.MCPExternalAuthConfig) error { return obo.ErrEnterpriseRequired },
		ApplyRunConfig: func(
			context.Context, client.Client, string,
			*mcpv1beta1.MCPExternalAuthConfig, *[]runner.RunConfigBuilderOption,
		) error {
			return obo.ErrEnterpriseRequired
		},
		SecretEnvVars: func(*mcpv1beta1.MCPExternalAuthConfig) ([]corev1.EnvVar, error) {
			return nil, obo.ErrEnterpriseRequired
		},
	}
}

// TestMCPExternalAuthConfigReconciler_OBO_ErrorTriageInReconcile drives the
// production three-way error triage in the OBO branch of Reconcile by
// registering a stub OBO handler whose Validate returns each test's error,
// then calling Reconcile and asserting on the resulting condition (or the
// returned error, for the transient case). The three buckets are:
//
//   - errors.Is(err, ErrEnterpriseRequired) → permanent, EnterpriseRequired
//   - errors.As(err, &*obo.ValidationError) → permanent, InvalidConfig
//   - anything else                         → transient; Reconcile returns
//     the error, no condition write
//
// This exercises the production errors.Is decision (so a regression that
// swaps stderrors.Is for == would be caught — that would break the vMCP
// "failed to convert to strategy: %w" wrap path) AND the new errors.As
// decision for *obo.ValidationError.
//
//nolint:paralleltest // Mutates package-level oboHandler via RegisterOBOHandler.
func TestMCPExternalAuthConfigReconciler_OBO_ErrorTriageInReconcile(t *testing.T) {
	tests := []struct {
		name        string
		validateErr error
		// Exactly one of these two paths is exercised per test case:
		// either the reconciler writes a permanent Valid=False condition with
		// the expected reason/message, or it returns a transient error and
		// writes no Valid condition.
		wantTransient bool
		wantReason    string
		wantMessage   string
	}{
		{
			name:        "bare sentinel produces EnterpriseRequired (permanent)",
			validateErr: obo.ErrEnterpriseRequired,
			wantReason:  mcpv1beta1.ConditionReasonEnterpriseRequired,
			wantMessage: obo.ErrEnterpriseRequired.Error(),
		},
		{
			name:        "wrapped sentinel still produces EnterpriseRequired via errors.Is",
			validateErr: fmt.Errorf("outer: %w", obo.ErrEnterpriseRequired),
			wantReason:  mcpv1beta1.ConditionReasonEnterpriseRequired,
			wantMessage: "outer: " + obo.ErrEnterpriseRequired.Error(),
		},
		{
			name:        "ValidationError produces InvalidConfig (permanent)",
			validateErr: &obo.ValidationError{Message: "audience must be a non-empty URL"},
			wantReason:  mcpv1beta1.ConditionReasonInvalidConfig,
			wantMessage: "audience must be a non-empty URL",
		},
		{
			name:        "wrapped ValidationError still produces InvalidConfig via errors.As",
			validateErr: fmt.Errorf("validating obo spec: %w", &obo.ValidationError{Message: "missing tokenURL"}),
			wantReason:  mcpv1beta1.ConditionReasonInvalidConfig,
			// setInvalid is called with the unwrapped ValidationError, so its
			// Message lands verbatim — the wrap prefix is discarded on purpose
			// so handler-author-visible text controls what kubectl describe shows.
			wantMessage: "missing tokenURL",
		},
		{
			name:          "unclassified error is transient — Reconcile returns it, no condition write",
			validateErr:   stderrors.New("transient JWKS fetch failed"),
			wantTransient: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Always restore the default after the subtest so we don't leak
			// the stub into other tests that share this package.
			t.Cleanup(func() { ctrlutil.RegisterOBOHandler(defaultOBOHandlerStub()) })

			// Install the per-case stub: Validate returns tt.validateErr;
			// the other two methods keep the default behavior.
			stub := defaultOBOHandlerStub()
			stub.Validate = func(*mcpv1beta1.MCPExternalAuthConfig) error { return tt.validateErr }
			ctrlutil.RegisterOBOHandler(stub)

			scheme := testutil.NewScheme(t)

			cfg := &mcpv1beta1.MCPExternalAuthConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "obo-config",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
					Type: mcpv1beta1.ExternalAuthTypeOBO,
					OBO:  &mcpv1beta1.OBOConfig{},
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(cfg).
				WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
				Build()

			r := &MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme}
			req := reconcile.Request{NamespacedName: types.NamespacedName{
				Name:      cfg.Name,
				Namespace: cfg.Namespace,
			}}

			// First reconcile may just add the finalizer; the requeued
			// reconcile runs the body that exercises the triage.
			result, err := r.Reconcile(t.Context(), req)
			if result.RequeueAfter > 0 {
				require.NoError(t, err)
				result, err = r.Reconcile(t.Context(), req)
			}

			var updated mcpv1beta1.MCPExternalAuthConfig
			require.NoError(t, fakeClient.Get(t.Context(), req.NamespacedName, &updated))
			validCond := findCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)

			if tt.wantTransient {
				// Transient path: Reconcile must return an error that
				// preserves the underlying cause (errors.Is must still match
				// it through the wrap), and no Valid condition is written.
				require.Error(t, err, "transient errors must propagate so controller-runtime requeues")
				assert.ErrorIs(t, err, tt.validateErr,
					"the transient error must remain inspectable via errors.Is")
				assert.Nil(t, validCond,
					"transient errors must not write a Valid condition; "+
						"locking the resource into a permanent state would block self-healing")
				return
			}

			require.NoError(t, err)
			require.NotNil(t, validCond)
			assert.Equal(t, metav1.ConditionFalse, validCond.Status)
			assert.Equal(t, tt.wantReason, validCond.Reason)
			assert.Equal(t, tt.wantMessage, validCond.Message)
		})
	}
}

// TestMCPExternalAuthConfigReconciler_ReconcileKeepsExistingForeignCondition
// verifies that when the controller observes a foreign-owned condition already
// on the object and then writes its own Valid=True, it folds its condition into
// the existing set rather than dropping the foreign one. It catches the
// mutate-outside-the-closure bug: a condition set before MutateAndPatchStatus
// snapshots the object would produce an empty diff, and the controller-owned
// Valid condition would never land (the bottom assertion catches that).
//
// The concurrent-writer guarantee — that a condition written by a disjoint
// owner between the reconciler's Get and its patch survives because
// MutateAndPatchStatus sends a partial merge-patch rather than a full PUT — is
// proven against the shared ctrlutil.MutateAndPatchStatus helper (used by all
// three config controllers) in
// TestMCPOIDCConfigReconciler_ConcurrentForeignConditionSurvivesMergePatch.
func TestMCPExternalAuthConfigReconciler_ReconcileKeepsExistingForeignCondition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	scheme := testutil.NewScheme(t)

	externalAuthConfig := &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default", Generation: 1},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeTokenExchange,
			TokenExchange: &mcpv1beta1.TokenExchangeConfig{
				TokenURL: "https://oauth.example.com/token",
				ClientID: "test-client",
				ClientSecretRef: &mcpv1beta1.SecretKeyRef{
					Name: "test-secret",
					Key:  "client-secret",
				},
				Audience: "backend-service",
			},
		},
		Status: mcpv1beta1.MCPExternalAuthConfigStatus{
			Conditions: []metav1.Condition{
				{
					Type:               "ForeignControllerSays",
					Status:             metav1.ConditionTrue,
					Reason:             "ExternallySet",
					Message:            "set by a hypothetical sibling owner of this resource",
					LastTransitionTime: metav1.Now(),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(externalAuthConfig).
		WithStatusSubresource(&mcpv1beta1.MCPExternalAuthConfig{}).
		Build()
	r := &MCPExternalAuthConfigReconciler{Client: fakeClient, Scheme: scheme}
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: externalAuthConfig.Name, Namespace: externalAuthConfig.Namespace}}

	// First reconcile adds the finalizer; second runs the success path and
	// writes Valid=True without touching any foreign condition.
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var after mcpv1beta1.MCPExternalAuthConfig
	require.NoError(t, fakeClient.Get(ctx, req.NamespacedName, &after))

	foreign := meta.FindStatusCondition(after.Status.Conditions, "ForeignControllerSays")
	require.NotNil(t, foreign,
		"foreign condition must survive an MCPExternalAuthConfig reconcile — the controller must fold its own condition into the existing set, not replace it")
	assert.Equal(t, metav1.ConditionTrue, foreign.Status, "foreign condition value must not be modified")
	assert.Equal(t, "ExternallySet", foreign.Reason)

	// And our own Valid=True landed.
	own := meta.FindStatusCondition(after.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	require.NotNil(t, own, "controller-owned Valid condition must land")
	assert.Equal(t, metav1.ConditionTrue, own.Status)
}
