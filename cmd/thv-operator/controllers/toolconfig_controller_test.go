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

func TestToolConfigReconciler_calculateConfigHash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec mcpv1alpha1.MCPToolConfigSpec
	}{
		{
			name: "empty spec",
			spec: mcpv1alpha1.MCPToolConfigSpec{},
		},
		{
			name: "with tools filter",
			spec: mcpv1alpha1.MCPToolConfigSpec{
				ToolsFilter: []string{"tool1", "tool2", "tool3"},
			},
		},
		{
			name: "with tools override",
			spec: mcpv1alpha1.MCPToolConfigSpec{
				ToolsOverride: map[string]mcpv1alpha1.ToolOverride{
					"tool1": {
						Name:        "renamed-tool1",
						Description: "Custom description",
					},
				},
			},
		},
		{
			name: "with both filter and override",
			spec: mcpv1alpha1.MCPToolConfigSpec{
				ToolsFilter: []string{"tool1", "tool2"},
				ToolsOverride: map[string]mcpv1alpha1.ToolOverride{
					"tool1": {
						Name:        "renamed-tool1",
						Description: "Custom description",
					},
					"tool2": {
						Name:        "renamed-tool2",
						Description: "Another custom description",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &ToolConfigReconciler{}

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
		r := &ToolConfigReconciler{}
		spec1 := mcpv1alpha1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1"},
		}
		spec2 := mcpv1alpha1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool2"},
		}

		hash1 := r.calculateConfigHash(spec1)
		hash2 := r.calculateConfigHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different specs should produce different hashes")
	})
}

func TestToolConfigReconciler_Reconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		toolConfig        *mcpv1alpha1.MCPToolConfig
		existingMCPServer *mcpv1alpha1.MCPServer
		expectFinalizer   bool
		expectHash        bool
	}{
		{
			name: "new toolconfig without references",
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1", "tool2"},
				},
			},
			expectFinalizer: true,
			expectHash:      true,
		},
		{
			name: "toolconfig with referencing mcpserver",
			toolConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1"},
					ToolsOverride: map[string]mcpv1alpha1.ToolOverride{
						"tool1": {
							Name:        "renamed-tool",
							Description: "Custom description",
						},
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
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
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
			objs := []client.Object{tt.toolConfig}
			if tt.existingMCPServer != nil {
				objs = append(objs, tt.existingMCPServer)
			}
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&mcpv1alpha1.MCPToolConfig{}).
				Build()

			r := &ToolConfigReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Reconcile
			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.toolConfig.Name,
					Namespace: tt.toolConfig.Namespace,
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

			// Check the updated MCPToolConfig
			var updatedConfig mcpv1alpha1.MCPToolConfig
			err = fakeClient.Get(ctx, req.NamespacedName, &updatedConfig)
			require.NoError(t, err)

			// Check finalizer
			if tt.expectFinalizer {
				assert.Contains(t, updatedConfig.Finalizers, ToolConfigFinalizerName,
					"MCPToolConfig should have finalizer")
			}

			// Check hash in status
			if tt.expectHash {
				assert.NotEmpty(t, updatedConfig.Status.ConfigHash,
					"MCPToolConfig status should have config hash")
			}

			// Check referencing servers in status
			if tt.existingMCPServer != nil {
				assert.Contains(t, updatedConfig.Status.ReferencingServers,
					tt.existingMCPServer.Name,
					"Status should contain referencing MCPServer")
			}
		})
	}
}

func TestToolConfigReconciler_findReferencingMCPServers(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	toolConfig := &mcpv1alpha1.MCPToolConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1"},
		},
	}

	mcpServer1 := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "server1",
			Namespace: "default",
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			Image: "test-image",
			ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
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
			ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
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
			// No ToolConfigRef
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(toolConfig, mcpServer1, mcpServer2, mcpServer3).
		Build()

	r := &ToolConfigReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()
	servers, err := r.findReferencingMCPServers(ctx, toolConfig)
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

func TestGetToolConfigForMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		mcpServer      *mcpv1alpha1.MCPServer
		existingConfig *mcpv1alpha1.MCPToolConfig
		expectConfig   bool
		expectError    bool
	}{
		{
			name: "mcpserver without toolconfig ref",
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
			expectError:  true, // Changed to expect an error when no ToolConfigRef is present
		},
		{
			name: "mcpserver with existing toolconfig",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
						Name: "test-config",
					},
				},
			},
			existingConfig: &mcpv1alpha1.MCPToolConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPToolConfigSpec{
					ToolsFilter: []string{"tool1"},
				},
			},
			expectConfig: true,
			expectError:  false,
		},
		{
			name: "mcpserver with non-existent toolconfig",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image",
					ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
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

			config, err := GetToolConfigForMCPServer(ctx, fakeClient, tt.mcpServer)

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
