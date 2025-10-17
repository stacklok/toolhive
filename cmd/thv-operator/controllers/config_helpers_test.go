package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

func TestCalculateConfigHash(t *testing.T) {
	t.Parallel()

	t.Run("consistent hashing for same spec", func(t *testing.T) {
		t.Parallel()

		spec := mcpv1alpha1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1", "tool2"},
		}

		hash1 := CalculateConfigHash(spec)
		hash2 := CalculateConfigHash(spec)

		assert.Equal(t, hash1, hash2, "Same spec should produce same hash")
		assert.NotEmpty(t, hash1, "Hash should not be empty")
	})

	t.Run("different hashes for different specs", func(t *testing.T) {
		t.Parallel()

		spec1 := mcpv1alpha1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1"},
		}
		spec2 := mcpv1alpha1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool2"},
		}

		hash1 := CalculateConfigHash(spec1)
		hash2 := CalculateConfigHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different specs should produce different hashes")
	})

	t.Run("works with different config types", func(t *testing.T) {
		t.Parallel()

		toolConfigSpec := mcpv1alpha1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1"},
		}
		externalAuthSpec := mcpv1alpha1.MCPExternalAuthConfigSpec{
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
		}

		hash1 := CalculateConfigHash(toolConfigSpec)
		hash2 := CalculateConfigHash(externalAuthSpec)

		assert.NotEmpty(t, hash1)
		assert.NotEmpty(t, hash2)
		// Hashes should be different for different types
		assert.NotEqual(t, hash1, hash2)
	})

	t.Run("empty spec produces consistent hash", func(t *testing.T) {
		t.Parallel()

		spec := mcpv1alpha1.MCPToolConfigSpec{}

		hash1 := CalculateConfigHash(spec)
		hash2 := CalculateConfigHash(spec)

		assert.Equal(t, hash1, hash2)
		assert.NotEmpty(t, hash1)
	})
}

func TestFindReferencingMCPServers(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	t.Run("finds servers referencing toolconfig", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		server1 := &mcpv1alpha1.MCPServer{
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

		server2 := &mcpv1alpha1.MCPServer{
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

		server3 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server3",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
					Name: "other-config",
				},
			},
		}

		server4 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server4",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
				// No ToolConfigRef
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server1, server2, server3, server4).
			Build()

		servers, err := FindReferencingMCPServers(ctx, fakeClient, "default", "test-config",
			func(server *mcpv1alpha1.MCPServer) *string {
				if server.Spec.ToolConfigRef != nil {
					return &server.Spec.ToolConfigRef.Name
				}
				return nil
			})

		require.NoError(t, err)
		assert.Len(t, servers, 2, "Should find 2 referencing servers")

		serverNames := make([]string, len(servers))
		for i, s := range servers {
			serverNames[i] = s.Name
		}
		assert.Contains(t, serverNames, "server1")
		assert.Contains(t, serverNames, "server2")
		assert.NotContains(t, serverNames, "server3")
		assert.NotContains(t, serverNames, "server4")
	})

	t.Run("finds servers referencing external auth config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		server1 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server1",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
				ExternalAuthConfigRef: &mcpv1alpha1.ExternalAuthConfigRef{
					Name: "auth-config",
				},
			},
		}

		server2 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server2",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
				// No ExternalAuthConfigRef
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server1, server2).
			Build()

		servers, err := FindReferencingMCPServers(ctx, fakeClient, "default", "auth-config",
			func(server *mcpv1alpha1.MCPServer) *string {
				if server.Spec.ExternalAuthConfigRef != nil {
					return &server.Spec.ExternalAuthConfigRef.Name
				}
				return nil
			})

		require.NoError(t, err)
		assert.Len(t, servers, 1, "Should find 1 referencing server")
		assert.Equal(t, "server1", servers[0].Name)
	})

	t.Run("returns empty list when no servers reference config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		server := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server1",
				Namespace: "default",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server).
			Build()

		servers, err := FindReferencingMCPServers(ctx, fakeClient, "default", "non-existent-config",
			func(server *mcpv1alpha1.MCPServer) *string {
				if server.Spec.ToolConfigRef != nil {
					return &server.Spec.ToolConfigRef.Name
				}
				return nil
			})

		require.NoError(t, err)
		assert.Empty(t, servers, "Should return empty list")
	})

	t.Run("only finds servers in same namespace", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		server1 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server1",
				Namespace: "namespace1",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
					Name: "test-config",
				},
			},
		}

		server2 := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server2",
				Namespace: "namespace2",
			},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1alpha1.ToolConfigRef{
					Name: "test-config",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server1, server2).
			Build()

		servers, err := FindReferencingMCPServers(ctx, fakeClient, "namespace1", "test-config",
			func(server *mcpv1alpha1.MCPServer) *string {
				if server.Spec.ToolConfigRef != nil {
					return &server.Spec.ToolConfigRef.Name
				}
				return nil
			})

		require.NoError(t, err)
		assert.Len(t, servers, 1, "Should only find servers in namespace1")
		assert.Equal(t, "server1", servers[0].Name)
		assert.Equal(t, "namespace1", servers[0].Namespace)
	})
}
