// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
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

func TestGetTelemetryConfigForMCPRemoteProxy(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)

	tests := []struct {
		name            string
		proxy           *mcpv1beta1.MCPRemoteProxy
		telemetryConfig *mcpv1beta1.MCPTelemetryConfig
		expectNil       bool
		expectError     bool
		expectedName    string
	}{
		{
			name:        "nil ref returns nil without error",
			proxy:       v1beta1test.NewMCPRemoteProxy("test-proxy", "default"),
			expectNil:   true,
			expectError: false,
		},
		{
			name: "fetches referenced config",
			proxy: v1beta1test.NewMCPRemoteProxy("test-proxy", "default",
				v1beta1test.WithRemoteProxyTelemetryConfigRef("my-telemetry"),
			),
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-telemetry", Namespace: "default"},
			},
			expectNil:    false,
			expectError:  false,
			expectedName: "my-telemetry",
		},
		{
			name: "not found returns nil without error",
			proxy: v1beta1test.NewMCPRemoteProxy("test-proxy", "default",
				v1beta1test.WithRemoteProxyTelemetryConfigRef("missing"),
			),
			expectNil:   true,
			expectError: false,
		},
		{
			name: "cross-namespace returns nil (not found)",
			proxy: v1beta1test.NewMCPRemoteProxy("test-proxy", "namespace-b",
				v1beta1test.WithRemoteProxyTelemetryConfigRef("shared-config"),
			),
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

	scheme := testutil.NewScheme(t)

	tests := []struct {
		name            string
		vmcp            *mcpv1beta1.VirtualMCPServer
		telemetryConfig *mcpv1beta1.MCPTelemetryConfig
		expectNil       bool
		expectError     bool
		expectedName    string
	}{
		{
			name:        "nil ref returns nil without error",
			vmcp:        v1beta1test.NewVirtualMCPServer("test-vmcp", "default"),
			expectNil:   true,
			expectError: false,
		},
		{
			name: "fetches referenced config",
			vmcp: v1beta1test.NewVirtualMCPServer("test-vmcp", "default",
				v1beta1test.WithVMCPTelemetryConfigRef("my-telemetry"),
			),
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-telemetry", Namespace: "default"},
			},
			expectNil:    false,
			expectError:  false,
			expectedName: "my-telemetry",
		},
		{
			name: "not found returns nil without error",
			vmcp: v1beta1test.NewVirtualMCPServer("test-vmcp", "default",
				v1beta1test.WithVMCPTelemetryConfigRef("missing"),
			),
			expectNil:   true,
			expectError: false,
		},
		{
			name: "cross-namespace returns nil (not found)",
			vmcp: v1beta1test.NewVirtualMCPServer("test-vmcp", "namespace-b",
				v1beta1test.WithVMCPTelemetryConfigRef("shared-config"),
			),
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
