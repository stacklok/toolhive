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
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestMCPOIDCConfigReconciler_calculateConfigHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec mcpv1alpha1.MCPOIDCConfigSpec
	}{
		{
			name: "kubernetesServiceAccount spec",
			spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
				KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
					ServiceAccount: "test-sa",
					Namespace:      "default",
					Issuer:         "https://kubernetes.default.svc",
				},
			},
		},
		{
			name: "configMapRef spec",
			spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeConfigMapRef,
				ConfigMapRef: &mcpv1alpha1.OIDCConfigMapRef{
					Name: "oidc-config",
					Key:  "oidc.json",
				},
			},
		},
		{
			name: "inline spec",
			spec: mcpv1alpha1.MCPOIDCConfigSpec{
				Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
				Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
					Issuer:   "https://accounts.google.com",
					ClientID: "my-client-id",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &MCPOIDCConfigReconciler{}

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
		r := &MCPOIDCConfigReconciler{}
		spec1 := mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "client1",
			},
		}
		spec2 := mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "client2",
			},
		}

		hash1 := r.calculateConfigHash(spec1)
		hash2 := r.calculateConfigHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different specs should produce different hashes")
	})
}

func TestMCPOIDCConfigReconciler_Reconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		oidcConfig      *mcpv1alpha1.MCPOIDCConfig
		existingServer  *mcpv1alpha1.MCPServer
		expectFinalizer bool
		expectHash      bool
	}{
		{
			name: "new oidc config without references",
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
					},
				},
			},
			expectFinalizer: true,
			expectHash:      true,
		},
		{
			name: "oidc config with referencing mcpserver",
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
					},
				},
			},
			existingServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
						Name:     "test-config",
						Audience: "test-audience",
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
			objs := []client.Object{tt.oidcConfig}
			if tt.existingServer != nil {
				objs = append(objs, tt.existingServer)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
				Build()

			r := &MCPOIDCConfigReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Recorder: events.NewFakeRecorder(10),
			}

			// Reconcile
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.oidcConfig.Name,
					Namespace: tt.oidcConfig.Namespace,
				},
			}

			// First reconciliation adds the finalizer and returns RequeueAfter > 0
			result, err := r.Reconcile(ctx, req)
			require.NoError(t, err)

			// If it's a new object, it will requeue to add finalizer
			if result.RequeueAfter > 0 {
				// Second reconciliation processes the actual logic
				result, err = r.Reconcile(ctx, req)
				require.NoError(t, err)
				assert.Equal(t, time.Duration(0), result.RequeueAfter)
			}

			// Check the updated MCPOIDCConfig
			var updatedConfig mcpv1alpha1.MCPOIDCConfig
			err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
			require.NoError(t, err)

			// Check finalizer
			if tt.expectFinalizer {
				assert.Contains(t, updatedConfig.Finalizers, OIDCConfigFinalizerName,
					"MCPOIDCConfig should have finalizer")
			}

			// Check hash in status
			if tt.expectHash {
				assert.NotEmpty(t, updatedConfig.Status.ConfigHash,
					"MCPOIDCConfig status should have config hash")
			}

			// Check referencing servers in status
			if tt.existingServer != nil {
				assert.Contains(t, updatedConfig.Status.ReferencingServers,
					tt.existingServer.Name,
					"Status should contain referencing MCPServer")
			}
		})
	}
}

func TestMCPOIDCConfigReconciler_findReferencingMCPServers(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer: "https://accounts.google.com",
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
			OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
				Name:     "test-config",
				Audience: "audience1",
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
			OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
				Name:     "test-config",
				Audience: "audience2",
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
			// No OIDCConfigRef
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig, mcpServer1, mcpServer2, mcpServer3).
		Build()

	r := &MCPOIDCConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()
	servers, err := r.findReferencingMCPServers(ctx, oidcConfig)
	require.NoError(t, err)

	assert.Len(t, servers, 2, "Should find 2 referencing MCPServers")

	serverNames := make([]string, len(servers))
	for i, s := range servers {
		serverNames[i] = s.Name
	}
	assert.Contains(t, serverNames, "server1")
	assert.Contains(t, serverNames, "server2")
	assert.NotContains(t, serverNames, "server3")
}

func TestGetOIDCConfigForMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mcpServer      *mcpv1alpha1.MCPServer
		existingConfig *mcpv1alpha1.MCPOIDCConfig
		expectConfig   bool
		expectError    bool
	}{
		{
			name: "mcpserver without oidc config ref",
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
			expectError:  true,
		},
		{
			name: "mcpserver with existing oidc config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
						Name:     "test-config",
						Audience: "test-audience",
					},
				},
			},
			existingConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
					},
				},
			},
			expectConfig: true,
			expectError:  false,
		},
		{
			name: "mcpserver with non-existent oidc config",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
						Name:     "non-existent",
						Audience: "test-audience",
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

			config, err := GetOIDCConfigForMCPServer(ctx, fakeClient, tt.mcpServer)

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

func TestMCPOIDCConfigReconciler_handleDeletion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		oidcConfig             *mcpv1alpha1.MCPOIDCConfig
		referencingServers     []*mcpv1alpha1.MCPServer
		expectError            bool
		expectRequeueAfter     time.Duration
		expectFinalizerRemoved bool
	}{
		{
			name: "delete config without references",
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{OIDCConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
					},
				},
			},
			expectError:            false,
			expectRequeueAfter:     0,
			expectFinalizerRemoved: true,
		},
		{
			name: "delete config with references",
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-config",
					Namespace:  "default",
					Finalizers: []string{OIDCConfigFinalizerName},
					DeletionTimestamp: &metav1.Time{
						Time: time.Now(),
					},
				},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
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
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
							Name:     "test-config",
							Audience: "test-audience",
						},
					},
				},
			},
			expectError:            false,
			expectRequeueAfter:     30 * time.Second,
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
			objs := []client.Object{tt.oidcConfig}
			for _, server := range tt.referencingServers {
				objs = append(objs, server)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
				Build()

			r := &MCPOIDCConfigReconciler{
				Client:   fakeClient,
				Scheme:   scheme,
				Recorder: events.NewFakeRecorder(10),
			}

			// Call handleDeletion directly
			result, err := r.handleDeletion(ctx, tt.oidcConfig)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, finalizer should still be present
				assert.Contains(t, tt.oidcConfig.Finalizers, OIDCConfigFinalizerName,
					"Finalizer should still be present after error")
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectRequeueAfter, result.RequeueAfter)

				if tt.expectFinalizerRemoved {
					assert.NotContains(t, tt.oidcConfig.Finalizers, OIDCConfigFinalizerName,
						"Finalizer should be removed")
				} else {
					assert.Contains(t, tt.oidcConfig.Finalizers, OIDCConfigFinalizerName,
						"Finalizer should still be present when deletion is blocked")
				}
			}
		})
	}
}

func TestMCPOIDCConfigReconciler_ConfigChangeTriggersReconciliation(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "original-client",
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
			OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
				Name:     "test-config",
				Audience: "test-audience",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig, mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
		Build()

	r := &MCPOIDCConfigReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(10),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      oidcConfig.Name,
			Namespace: oidcConfig.Namespace,
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
	var updatedConfig mcpv1alpha1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedConfig.Status.ConfigHash, "Config hash should be set")
	firstHash := updatedConfig.Status.ConfigHash

	// Update the config spec (simulate a change)
	updatedConfig.Spec.Inline.ClientID = "new-client"
	updatedConfig.Generation = 2
	err = fakeClient.Update(ctx, &updatedConfig)
	require.NoError(t, err)

	// Third reconciliation - should detect change and update hash
	result, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Get final config and verify hash changed
	var finalConfig mcpv1alpha1.MCPOIDCConfig
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
		updatedServer.Annotations["toolhive.stacklok.dev/oidcconfig-hash"],
		"MCPServer should have annotation with new config hash")
}

func TestMCPOIDCConfigReconciler_ReferencingServersUpdatedWithoutHashChange(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer: "https://accounts.google.com",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
		Build()

	r := &MCPOIDCConfigReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(10),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      oidcConfig.Name,
			Namespace: oidcConfig.Namespace,
		},
	}

	// First reconciliation - add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	// Second reconciliation - sets hash, no servers yet
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var updatedConfig mcpv1alpha1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.NotEmpty(t, updatedConfig.Status.ConfigHash)
	assert.Empty(t, updatedConfig.Status.ReferencingServers, "No servers should be referencing yet")

	// Now add an MCPServer that references this config (without changing the config spec)
	mcpServer := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "new-server",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
				Name:     "test-config",
				Audience: "test-audience",
			},
		},
	}
	require.NoError(t, fakeClient.Create(ctx, mcpServer))

	// Reconcile again - hash hasn't changed, but referencing servers should be updated
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.Contains(t, updatedConfig.Status.ReferencingServers, "new-server",
		"ReferencingServers should be updated even without hash change")
}

func TestMCPOIDCConfigReconciler_ReferencingServersRemovedOnServerDeletion(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer: "https://accounts.google.com",
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
			OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
				Name:     "test-config",
				Audience: "test-audience",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig, mcpServer).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
		Build()

	r := &MCPOIDCConfigReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(10),
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      oidcConfig.Name,
			Namespace: oidcConfig.Namespace,
		},
	}

	// Add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	// Set hash and referencing servers
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	var updatedConfig mcpv1alpha1.MCPOIDCConfig
	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.Contains(t, updatedConfig.Status.ReferencingServers, "server-to-delete")

	// Delete the MCPServer
	require.NoError(t, fakeClient.Delete(ctx, mcpServer))

	// Reconcile again - referencing servers should be empty now
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
	require.NoError(t, err)
	assert.Empty(t, updatedConfig.Status.ReferencingServers,
		"ReferencingServers should be empty after server deletion")
}

func TestMCPOIDCConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *mcpv1alpha1.MCPOIDCConfig
		expectError bool
	}{
		{
			name: "valid kubernetesServiceAccount config",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: "test-sa",
						Issuer:         "https://kubernetes.default.svc",
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid configMapRef config",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeConfigMapRef,
					ConfigMapRef: &mcpv1alpha1.OIDCConfigMapRef{
						Name: "oidc-config",
						Key:  "oidc.json",
					},
				},
			},
			expectError: false,
		},
		{
			name: "valid inline config",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer:   "https://accounts.google.com",
						ClientID: "test-client",
					},
				},
			},
			expectError: false,
		},
		{
			name: "invalid kubernetesServiceAccount set but type is inline",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: "test-sa",
					},
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
						Issuer: "https://accounts.google.com",
					},
				},
			},
			expectError: true,
		},
		{
			name: "invalid no config variant set",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
					// No inline config set
				},
			},
			expectError: true,
		},
		{
			name: "invalid multiple config variants set",
			config: &mcpv1alpha1.MCPOIDCConfig{
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type: mcpv1alpha1.MCPOIDCConfigTypeKubernetesServiceAccount,
					KubernetesServiceAccount: &mcpv1alpha1.KubernetesServiceAccountOIDCConfig{
						ServiceAccount: "test-sa",
					},
					ConfigMapRef: &mcpv1alpha1.OIDCConfigMapRef{
						Name: "oidc-config",
					},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.config.Validate()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestMCPOIDCConfigReconciler_DuplicateAudienceWarning(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	oidcConfig := &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-config",
			Namespace:  "default",
			Generation: 1,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "test-client",
			},
		},
	}

	// Two MCPServers sharing the same audience — should trigger warning
	mcpServer1 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server1",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
				Name:     "test-config",
				Audience: "shared-audience",
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
			OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{
				Name:     "test-config",
				Audience: "shared-audience",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(oidcConfig, mcpServer1, mcpServer2).
		WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).
		Build()

	fakeRecorder := events.NewFakeRecorder(10)
	r := &MCPOIDCConfigReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: fakeRecorder,
	}

	req := reconcile.Request{
		NamespacedName: types.NamespacedName{
			Name:      oidcConfig.Name,
			Namespace: oidcConfig.Namespace,
		},
	}

	// First reconciliation - add finalizer
	result, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter, time.Duration(0))

	// Second reconciliation - detects hash change, cascades, and checks audiences
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Verify a DuplicateAudience warning event was emitted
	// FakeRecorder.Events is chan string with format: "eventtype reason action note"
	select {
	case event := <-fakeRecorder.Events:
		assert.Contains(t, event, "DuplicateAudience",
			"Expected DuplicateAudience event")
		assert.Contains(t, event, "shared-audience",
			"Event should mention the duplicate audience")
	default:
		t.Fatal("Expected a DuplicateAudience warning event but none was emitted")
	}
}
