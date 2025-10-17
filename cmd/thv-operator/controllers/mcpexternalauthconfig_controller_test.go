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
					ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
					ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
				ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
				ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
		})
	}
}

func TestMCPExternalAuthConfigReconciler_findReferencingMCPServers(t *testing.T) {
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
				ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
	servers, err := r.findReferencingMCPServers(ctx, externalAuthConfig)
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
		expectError            bool
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
							Name: "test-secret",
							Key:  "client-secret",
						},
						Audience: "backend-service",
					},
				},
			},
			expectError:            false,
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
						ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
			expectError:            true,
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
				Build()

			r := &MCPExternalAuthConfigReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Call handleDeletion directly
			result, err := r.handleDeletion(ctx, tt.externalAuthConfig)

			if tt.expectError {
				assert.Error(t, err)
				// When there's an error, finalizer should still be present
				assert.Contains(t, tt.externalAuthConfig.Finalizers, ExternalAuthConfigFinalizerName,
					"Finalizer should still be present after error")
			} else {
				assert.NoError(t, err)
				assert.Equal(t, time.Duration(0), result.RequeueAfter)

				// Check if finalizer was removed from the object in memory
				// Note: We check the original object because after finalizer removal,
				// Kubernetes will delete the object and it won't be in the fake client store
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
				ClientSecretRef: mcpv1alpha1.SecretKeyRef{
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
