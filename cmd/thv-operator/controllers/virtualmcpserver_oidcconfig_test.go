// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus/mocks"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestVirtualMCPServerReconciler_handleOIDCConfig(t *testing.T) {
	t.Parallel()

	// validOIDCCondition is a helper to build a Valid=True condition slice.
	validOIDCCondition := []metav1.Condition{{
		Type: "Valid", Status: metav1.ConditionTrue, Reason: "ValidationSucceeded",
	}}

	tests := []struct {
		name                       string
		vmcp                       *mcpv1alpha1.VirtualMCPServer
		oidcConfig                 *mcpv1alpha1.MCPOIDCConfig
		expectError                bool
		expectErrorContains        string
		expectHash                 string
		expectHashCleared          bool
		expectConditionStatus      *metav1.ConditionStatus
		expectConditionReason      string
		expectReferencingServer    bool
		existingReferencingServers []string
	}{
		{
			name: "no ref clears previously stored hash",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "default"},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{Type: "anonymous"},
					Config:       vmcpconfig.Config{Group: "test-group"},
				},
				Status: mcpv1alpha1.VirtualMCPServerStatus{OIDCConfigHash: "old"},
			},
			expectHashCleared: true,
		},
		{
			name: "referenced config not found sets NotFound condition",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "default"},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type:          "oidc",
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "missing", Audience: "aud"},
					},
					Config: vmcpconfig.Config{Group: "test-group"},
				},
			},
			expectError:           true,
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1alpha1.ConditionReasonOIDCConfigRefNotFound,
		},
		{
			name: "config with Valid=False sets NotValid condition",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "default"},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type:          "oidc",
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "bad", Audience: "aud"},
					},
					Config: vmcpconfig.Config{Group: "test-group"},
				},
			},
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "default"},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type:   mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{Issuer: "https://x"},
				},
				Status: mcpv1alpha1.MCPOIDCConfigStatus{
					Conditions: []metav1.Condition{{
						Type: "Valid", Status: metav1.ConditionFalse, Reason: "ValidationFailed",
						Message: "missing fields",
					}},
				},
			},
			expectError:           true,
			expectErrorContains:   "not valid",
			expectConditionStatus: conditionStatusPtr(metav1.ConditionFalse),
			expectConditionReason: mcpv1alpha1.ConditionReasonOIDCConfigRefNotValid,
		},
		{
			name: "valid config tracks hash and adds to ReferencingServers",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "default"},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type:          "oidc",
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "ok", Audience: "aud"},
					},
					Config: vmcpconfig.Config{Group: "test-group"},
				},
			},
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "default"},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type:   mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{Issuer: "https://x", ClientID: "c"},
				},
				Status: mcpv1alpha1.MCPOIDCConfigStatus{
					ConfigHash: "hash-123",
					Conditions: validOIDCCondition,
				},
			},
			expectHash:              "hash-123",
			expectConditionStatus:   conditionStatusPtr(metav1.ConditionTrue),
			expectConditionReason:   mcpv1alpha1.ConditionReasonOIDCConfigRefValid,
			expectReferencingServer: true,
		},
		{
			name: "already referenced is idempotent",
			vmcp: &mcpv1alpha1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "v", Namespace: "default"},
				Spec: mcpv1alpha1.VirtualMCPServerSpec{
					IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
						Type:          "oidc",
						OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "ok", Audience: "aud"},
					},
					Config: vmcpconfig.Config{Group: "test-group"},
				},
			},
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "default"},
				Spec: mcpv1alpha1.MCPOIDCConfigSpec{
					Type:   mcpv1alpha1.MCPOIDCConfigTypeInline,
					Inline: &mcpv1alpha1.InlineOIDCSharedConfig{Issuer: "https://x", ClientID: "c"},
				},
				Status: mcpv1alpha1.MCPOIDCConfigStatus{
					ConfigHash:         "hash-123",
					Conditions:         validOIDCCondition,
					ReferencingServers: []string{"vmcp/v"},
				},
			},
			existingReferencingServers: []string{"vmcp/v"},
			expectHash:                 "hash-123",
			expectConditionStatus:      conditionStatusPtr(metav1.ConditionTrue),
			expectConditionReason:      mcpv1alpha1.ConditionReasonOIDCConfigRefValid,
			expectReferencingServer:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			objs := []runtime.Object{tt.vmcp}
			if tt.oidcConfig != nil {
				objs = append(objs, tt.oidcConfig)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(
					&mcpv1alpha1.VirtualMCPServer{},
					&mcpv1alpha1.MCPOIDCConfig{},
				).
				Build()

			reconciler := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			ctrl := gomock.NewController(t)
			statusManager := mocks.NewMockStatusManager(ctrl)

			// Set up mock expectations for condition-setting calls
			if tt.expectConditionStatus != nil {
				statusManager.EXPECT().SetCondition(
					mcpv1alpha1.ConditionOIDCConfigRefValidated,
					tt.expectConditionReason,
					gomock.Any(),
					*tt.expectConditionStatus,
				).Times(1)
			}

			err := reconciler.handleOIDCConfig(ctx, tt.vmcp, statusManager)

			if tt.expectError {
				assert.Error(t, err)
				if tt.expectErrorContains != "" {
					assert.Contains(t, err.Error(), tt.expectErrorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			if tt.expectHash != "" {
				assert.Equal(t, tt.expectHash, tt.vmcp.Status.OIDCConfigHash)
			}
			if tt.expectHashCleared {
				assert.Empty(t, tt.vmcp.Status.OIDCConfigHash)
			}

			if tt.expectReferencingServer && tt.oidcConfig != nil {
				var updated mcpv1alpha1.MCPOIDCConfig
				require.NoError(t, fakeClient.Get(ctx, client.ObjectKeyFromObject(tt.oidcConfig), &updated))
				assert.Contains(t, updated.Status.ReferencingServers, "vmcp/"+tt.vmcp.Name)

				if tt.existingReferencingServers != nil {
					// Verify no duplicates were added
					count := 0
					for _, ref := range updated.Status.ReferencingServers {
						if ref == "vmcp/"+tt.vmcp.Name {
							count++
						}
					}
					assert.Equal(t, 1, count, "should not have duplicate entries in ReferencingServers")
				}
			}
		})
	}
}

func TestVirtualMCPServerReconciler_updateOIDCConfigReferencingServers(t *testing.T) {
	t.Parallel()

	t.Run("adds new vmcp server name with prefix", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		cfg := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
			Status:     mcpv1alpha1.MCPOIDCConfigStatus{ReferencingServers: []string{"existing"}},
		}
		fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg).
			WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).Build()
		r := &VirtualMCPServerReconciler{Client: fc, Scheme: scheme}

		require.NoError(t, r.updateOIDCConfigReferencingServers(ctx, cfg, "my-vmcp"))
		assert.ElementsMatch(t, []string{"existing", "vmcp/my-vmcp"}, cfg.Status.ReferencingServers)
	})

	t.Run("does not duplicate existing vmcp entry", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

		cfg := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "default"},
			Status:     mcpv1alpha1.MCPOIDCConfigStatus{ReferencingServers: []string{"vmcp/my-vmcp"}},
		}
		fc := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg).
			WithStatusSubresource(&mcpv1alpha1.MCPOIDCConfig{}).Build()
		r := &VirtualMCPServerReconciler{Client: fc, Scheme: scheme}

		require.NoError(t, r.updateOIDCConfigReferencingServers(ctx, cfg, "my-vmcp"))
		assert.Len(t, cfg.Status.ReferencingServers, 1)
	})
}

func TestVirtualMCPServerReconciler_mapOIDCConfigToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		oidcConfig        *mcpv1alpha1.MCPOIDCConfig
		virtualMCPServers []mcpv1alpha1.VirtualMCPServer
		expectedRequests  int
		expectedNames     []string
	}{
		{
			name: "maps matching VirtualMCPServers",
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-oidc", Namespace: "default"},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "vmcp-1", Namespace: "default"},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type:          "oidc",
							OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "my-oidc", Audience: "aud"},
						},
						Config: vmcpconfig.Config{Group: "g"},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "vmcp-2", Namespace: "default"},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type:          "oidc",
							OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "my-oidc", Audience: "aud2"},
						},
						Config: vmcpconfig.Config{Group: "g"},
					},
				},
			},
			expectedRequests: 2,
			expectedNames:    []string{"vmcp-1", "vmcp-2"},
		},
		{
			name: "does not map non-matching VirtualMCPServers",
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-oidc", Namespace: "default"},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "vmcp-1", Namespace: "default"},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
							Type:          "oidc",
							OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "other-oidc", Audience: "aud"},
						},
						Config: vmcpconfig.Config{Group: "g"},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
		{
			name: "handles VirtualMCPServer without IncomingAuth",
			oidcConfig: &mcpv1alpha1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-oidc", Namespace: "default"},
			},
			virtualMCPServers: []mcpv1alpha1.VirtualMCPServer{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "vmcp-no-auth", Namespace: "default"},
					Spec: mcpv1alpha1.VirtualMCPServerSpec{
						Config: vmcpconfig.Config{Group: "g"},
					},
				},
			},
			expectedRequests: 0,
			expectedNames:    []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

			objs := []client.Object{tt.oidcConfig}
			for i := range tt.virtualMCPServers {
				objs = append(objs, &tt.virtualMCPServers[i])
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			r := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			requests := r.mapOIDCConfigToVirtualMCPServer(context.Background(), tt.oidcConfig)

			assert.Len(t, requests, tt.expectedRequests)

			var names []string
			for _, req := range requests {
				names = append(names, req.Name)
			}
			for _, expectedName := range tt.expectedNames {
				assert.Contains(t, names, expectedName)
			}
		})
	}
}

func TestMCPOIDCConfigReconciler_findReferencingServers_withVirtualMCPServer(t *testing.T) {
	t.Parallel()

	t.Run("returns both MCPServer and VirtualMCPServer references", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
		require.NoError(t, corev1.AddToScheme(scheme))

		cfg := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "shared-oidc", Namespace: "default"},
		}
		mcpServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "server-1", Namespace: "default"},
			Spec: mcpv1alpha1.MCPServerSpec{
				Image:         "img",
				OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "shared-oidc", Audience: "aud"},
			},
		}
		vmcp := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "vmcp-1", Namespace: "default"},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type:          "oidc",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "shared-oidc", Audience: "aud"},
				},
				Config: vmcpconfig.Config{Group: "g"},
			},
		}

		fc := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(cfg, mcpServer, vmcp).Build()
		r := &MCPOIDCConfigReconciler{Client: fc, Scheme: scheme}

		refs, err := r.findReferencingServers(ctx, cfg)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"server-1", "vmcp/vmcp-1"}, refs)
	})

	t.Run("returns only VirtualMCPServer reference when no MCPServer references", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()

		scheme := runtime.NewScheme()
		require.NoError(t, mcpv1alpha1.AddToScheme(scheme))
		require.NoError(t, corev1.AddToScheme(scheme))

		cfg := &mcpv1alpha1.MCPOIDCConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "vmcp-only-oidc", Namespace: "default"},
		}
		// MCPServer that does NOT reference this config
		unrelatedServer := &mcpv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "default"},
			Spec:       mcpv1alpha1.MCPServerSpec{Image: "img"},
		}
		vmcp := &mcpv1alpha1.VirtualMCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "vmcp-2", Namespace: "default"},
			Spec: mcpv1alpha1.VirtualMCPServerSpec{
				IncomingAuth: &mcpv1alpha1.IncomingAuthConfig{
					Type:          "oidc",
					OIDCConfigRef: &mcpv1alpha1.MCPOIDCConfigReference{Name: "vmcp-only-oidc", Audience: "aud"},
				},
				Config: vmcpconfig.Config{Group: "g"},
			},
		}

		fc := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(cfg, unrelatedServer, vmcp).Build()
		r := &MCPOIDCConfigReconciler{Client: fc, Scheme: scheme}

		refs, err := r.findReferencingServers(ctx, cfg)
		require.NoError(t, err)
		assert.Equal(t, []string{"vmcp/vmcp-2"}, refs)
	})
}
