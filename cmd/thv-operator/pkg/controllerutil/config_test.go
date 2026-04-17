// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestCalculateConfigHash(t *testing.T) {
	t.Parallel()

	t.Run("consistent hashing for same spec", func(t *testing.T) {
		t.Parallel()

		spec := mcpv1beta1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1", "tool2"},
		}

		hash1 := CalculateConfigHash(spec)
		hash2 := CalculateConfigHash(spec)

		assert.Equal(t, hash1, hash2, "Same spec should produce same hash")
		assert.NotEmpty(t, hash1, "Hash should not be empty")
	})

	t.Run("different hashes for different specs", func(t *testing.T) {
		t.Parallel()

		spec1 := mcpv1beta1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1"},
		}
		spec2 := mcpv1beta1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool2"},
		}

		hash1 := CalculateConfigHash(spec1)
		hash2 := CalculateConfigHash(spec2)

		assert.NotEqual(t, hash1, hash2, "Different specs should produce different hashes")
	})

	t.Run("works with different config types", func(t *testing.T) {
		t.Parallel()

		toolConfigSpec := mcpv1beta1.MCPToolConfigSpec{
			ToolsFilter: []string{"tool1"},
		}
		externalAuthSpec := mcpv1beta1.MCPExternalAuthConfigSpec{
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

		spec := mcpv1beta1.MCPToolConfigSpec{}

		hash1 := CalculateConfigHash(spec)
		hash2 := CalculateConfigHash(spec)

		assert.Equal(t, hash1, hash2)
		assert.NotEmpty(t, hash1)
	})
}

func TestFindReferencingMCPServers(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	t.Run("finds servers referencing toolconfig", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		server1 := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server1",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1beta1.ToolConfigRef{
					Name: "test-config",
				},
			},
		}

		server2 := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server2",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1beta1.ToolConfigRef{
					Name: "test-config",
				},
			},
		}

		server3 := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server3",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1beta1.ToolConfigRef{
					Name: "other-config",
				},
			},
		}

		server4 := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server4",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "test-image",
				// No ToolConfigRef
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server1, server2, server3, server4).
			Build()

		servers, err := FindReferencingMCPServers(ctx, fakeClient, "default", "test-config",
			func(server *mcpv1beta1.MCPServer) *string {
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

		server1 := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server1",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "test-image",
				ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{
					Name: "auth-config",
				},
			},
		}

		server2 := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server2",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "test-image",
				// No ExternalAuthConfigRef
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server1, server2).
			Build()

		servers, err := FindReferencingMCPServers(ctx, fakeClient, "default", "auth-config",
			func(server *mcpv1beta1.MCPServer) *string {
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

		server := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server1",
				Namespace: "default",
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "test-image",
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server).
			Build()

		servers, err := FindReferencingMCPServers(ctx, fakeClient, "default", "non-existent-config",
			func(server *mcpv1beta1.MCPServer) *string {
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

		server1 := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server1",
				Namespace: "namespace1",
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1beta1.ToolConfigRef{
					Name: "test-config",
				},
			},
		}

		server2 := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "server2",
				Namespace: "namespace2",
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image: "test-image",
				ToolConfigRef: &mcpv1beta1.ToolConfigRef{
					Name: "test-config",
				},
			},
		}

		fakeClient := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(server1, server2).
			Build()

		servers, err := FindReferencingMCPServers(ctx, fakeClient, "namespace1", "test-config",
			func(server *mcpv1beta1.MCPServer) *string {
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

func TestSortWorkloadRefs(t *testing.T) {
	t.Parallel()

	t.Run("sorts by kind then name", func(t *testing.T) {
		t.Parallel()

		refs := []mcpv1beta1.WorkloadReference{
			{Kind: "VirtualMCPServer", Name: "beta"},
			{Kind: "MCPServer", Name: "gamma"},
			{Kind: "MCPServer", Name: "alpha"},
			{Kind: "VirtualMCPServer", Name: "alpha"},
		}

		SortWorkloadRefs(refs)

		assert.Equal(t, []mcpv1beta1.WorkloadReference{
			{Kind: "MCPServer", Name: "alpha"},
			{Kind: "MCPServer", Name: "gamma"},
			{Kind: "VirtualMCPServer", Name: "alpha"},
			{Kind: "VirtualMCPServer", Name: "beta"},
		}, refs)
	})

	t.Run("empty slice is a no-op", func(t *testing.T) {
		t.Parallel()
		var refs []mcpv1beta1.WorkloadReference
		SortWorkloadRefs(refs)
		assert.Empty(t, refs)
	})

	t.Run("single element is unchanged", func(t *testing.T) {
		t.Parallel()
		refs := []mcpv1beta1.WorkloadReference{{Kind: "MCPServer", Name: "only"}}
		SortWorkloadRefs(refs)
		assert.Equal(t, []mcpv1beta1.WorkloadReference{{Kind: "MCPServer", Name: "only"}}, refs)
	})
}

func TestWorkloadRefsEqual(t *testing.T) {
	t.Parallel()

	t.Run("equal slices", func(t *testing.T) {
		t.Parallel()
		a := []mcpv1beta1.WorkloadReference{
			{Kind: "MCPServer", Name: "alpha"},
			{Kind: "MCPServer", Name: "beta"},
		}
		b := []mcpv1beta1.WorkloadReference{
			{Kind: "MCPServer", Name: "alpha"},
			{Kind: "MCPServer", Name: "beta"},
		}
		assert.True(t, WorkloadRefsEqual(a, b))
	})

	t.Run("different order is not equal", func(t *testing.T) {
		t.Parallel()
		a := []mcpv1beta1.WorkloadReference{
			{Kind: "MCPServer", Name: "alpha"},
			{Kind: "MCPServer", Name: "beta"},
		}
		b := []mcpv1beta1.WorkloadReference{
			{Kind: "MCPServer", Name: "beta"},
			{Kind: "MCPServer", Name: "alpha"},
		}
		assert.False(t, WorkloadRefsEqual(a, b))
	})

	t.Run("different lengths", func(t *testing.T) {
		t.Parallel()
		a := []mcpv1beta1.WorkloadReference{{Kind: "MCPServer", Name: "alpha"}}
		b := []mcpv1beta1.WorkloadReference{
			{Kind: "MCPServer", Name: "alpha"},
			{Kind: "MCPServer", Name: "beta"},
		}
		assert.False(t, WorkloadRefsEqual(a, b))
	})

	t.Run("both nil", func(t *testing.T) {
		t.Parallel()
		assert.True(t, WorkloadRefsEqual(nil, nil))
	})

	t.Run("nil vs empty", func(t *testing.T) {
		t.Parallel()
		assert.True(t, WorkloadRefsEqual(nil, []mcpv1beta1.WorkloadReference{}))
	})
}

func TestFindWorkloadRefsFromMCPServers(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	t.Run("returns sorted refs", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		// Create servers in reverse alphabetical order to verify sorting
		servers := []mcpv1beta1.MCPServer{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "charlie", Namespace: "ns"},
				Spec:       mcpv1beta1.MCPServerSpec{Image: "img", ToolConfigRef: &mcpv1beta1.ToolConfigRef{Name: "cfg"}},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "ns"},
				Spec:       mcpv1beta1.MCPServerSpec{Image: "img", ToolConfigRef: &mcpv1beta1.ToolConfigRef{Name: "cfg"}},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "bravo", Namespace: "ns"},
				Spec:       mcpv1beta1.MCPServerSpec{Image: "img", ToolConfigRef: &mcpv1beta1.ToolConfigRef{Name: "cfg"}},
			},
		}

		builder := fake.NewClientBuilder().WithScheme(scheme)
		for i := range servers {
			builder = builder.WithObjects(&servers[i])
		}
		fakeClient := builder.Build()

		refs, err := FindWorkloadRefsFromMCPServers(ctx, fakeClient, "ns", "cfg",
			func(s *mcpv1beta1.MCPServer) *string {
				if s.Spec.ToolConfigRef != nil {
					return &s.Spec.ToolConfigRef.Name
				}
				return nil
			})

		require.NoError(t, err)
		require.Len(t, refs, 3)
		assert.Equal(t, "alpha", refs[0].Name)
		assert.Equal(t, "bravo", refs[1].Name)
		assert.Equal(t, "charlie", refs[2].Name)
		for _, ref := range refs {
			assert.Equal(t, "MCPServer", ref.Kind)
		}
	})

	t.Run("returns empty for no matches", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

		refs, err := FindWorkloadRefsFromMCPServers(ctx, fakeClient, "ns", "cfg",
			func(_ *mcpv1beta1.MCPServer) *string {
				return nil
			})

		require.NoError(t, err)
		assert.Empty(t, refs)
	})
}

func TestGetTelemetryConfigForMCPRemoteProxy(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	tests := []struct {
		name            string
		proxy           *mcpv1beta1.MCPRemoteProxy
		telemetryConfig *mcpv1beta1.MCPTelemetryConfig
		expectNil       bool
		expectError     bool
		expectedName    string
	}{
		{
			name: "nil ref returns nil without error",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec:       mcpv1beta1.MCPRemoteProxySpec{TelemetryConfigRef: nil},
			},
			expectNil:   true,
			expectError: false,
		},
		{
			name: "fetches referenced config",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "my-telemetry"},
				},
			},
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-telemetry", Namespace: "default"},
			},
			expectNil:    false,
			expectError:  false,
			expectedName: "my-telemetry",
		},
		{
			name: "not found returns nil without error",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "missing"},
				},
			},
			expectNil:   true,
			expectError: false,
		},
		{
			name: "cross-namespace returns nil (not found)",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "namespace-b"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "shared-config"},
				},
			},
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "namespace-a"},
			},
			expectNil:   true,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.telemetryConfig != nil {
				builder = builder.WithObjects(tt.telemetryConfig)
			}
			fakeClient := builder.Build()

			result, err := GetTelemetryConfigForMCPRemoteProxy(ctx, fakeClient, tt.proxy)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
				return
			}

			assert.NoError(t, err)
			if tt.expectNil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, tt.expectedName, result.Name)
			}
		})
	}
}

func TestGetTelemetryConfigForVirtualMCPServer(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	tests := []struct {
		name            string
		vmcp            *mcpv1beta1.VirtualMCPServer
		telemetryConfig *mcpv1beta1.MCPTelemetryConfig
		expectNil       bool
		expectError     bool
		expectedName    string
	}{
		{
			name: "nil ref returns nil without error",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec:       mcpv1beta1.VirtualMCPServerSpec{TelemetryConfigRef: nil},
			},
			expectNil:   true,
			expectError: false,
		},
		{
			name: "fetches referenced config",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "my-telemetry"},
				},
			},
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-telemetry", Namespace: "default"},
			},
			expectNil:    false,
			expectError:  false,
			expectedName: "my-telemetry",
		},
		{
			name: "not found returns nil without error",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "missing"},
				},
			},
			expectNil:   true,
			expectError: false,
		},
		{
			name: "cross-namespace returns nil (not found)",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "namespace-b"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "shared-config"},
				},
			},
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "shared-config", Namespace: "namespace-a"},
			},
			expectNil:   true,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.telemetryConfig != nil {
				builder = builder.WithObjects(tt.telemetryConfig)
			}
			fakeClient := builder.Build()

			result, err := GetTelemetryConfigForVirtualMCPServer(ctx, fakeClient, tt.vmcp)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
				return
			}

			assert.NoError(t, err)
			if tt.expectNil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, tt.expectedName, result.Name)
			}
		})
	}
}
