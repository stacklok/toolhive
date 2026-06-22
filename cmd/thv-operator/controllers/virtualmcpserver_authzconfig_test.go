// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
)

// vmcpWithAuthzRef builds a VirtualMCPServer that references the named
// MCPAuthzConfig (nil refName leaves AuthzConfigRef unset).
func vmcpWithAuthzRef(refName string, status mcpv1beta1.VirtualMCPServerStatus) *mcpv1beta1.VirtualMCPServer {
	incoming := &mcpv1beta1.IncomingAuthConfig{Type: "anonymous"}
	if refName != "" {
		incoming.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: refName}
	}
	return &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
		Spec:       mcpv1beta1.VirtualMCPServerSpec{IncomingAuth: incoming},
		Status:     status,
	}
}

func TestVirtualMCPServerReconciler_handleAuthzConfig(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	tests := []struct {
		name               string
		vmcp               *mcpv1beta1.VirtualMCPServer
		authzConfig        *mcpv1beta1.MCPAuthzConfig
		expectError        bool
		expectedHash       string
		expectedCondStatus metav1.ConditionStatus
		expectedCondReason string
		expectHashCleared  bool
		expectCondRemoved  bool
	}{
		{
			name: "nil ref clears hash and removes condition",
			vmcp: vmcpWithAuthzRef("", mcpv1beta1.VirtualMCPServerStatus{
				AuthzConfigHash: "old-hash",
				Conditions: []metav1.Condition{{
					Type:   mcpv1beta1.ConditionAuthzConfigRefValidated,
					Status: metav1.ConditionTrue,
					Reason: mcpv1beta1.ConditionReasonAuthzConfigRefValid,
				}},
			}),
			expectHashCleared: true,
			expectCondRemoved: true,
		},
		{
			name:               "valid ref sets condition true and updates hash",
			vmcp:               vmcpWithAuthzRef("my-authz", mcpv1beta1.VirtualMCPServerStatus{}),
			authzConfig:        authzConfigForTest("my-authz", true, "abc123"),
			expectedHash:       "abc123",
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonAuthzConfigRefValid,
		},
		{
			name:               "not found sets condition false",
			vmcp:               vmcpWithAuthzRef("missing", mcpv1beta1.VirtualMCPServerStatus{}),
			expectError:        true,
			expectedCondStatus: metav1.ConditionFalse,
			expectedCondReason: mcpv1beta1.ConditionReasonAuthzConfigRefNotFound,
		},
		{
			name:               "invalid config sets condition false",
			vmcp:               vmcpWithAuthzRef("invalid-authz", mcpv1beta1.VirtualMCPServerStatus{}),
			authzConfig:        authzConfigForTest("invalid-authz", false, "xyz"),
			expectError:        true,
			expectedCondStatus: metav1.ConditionFalse,
			expectedCondReason: mcpv1beta1.ConditionReasonAuthzConfigRefNotValid,
		},
		{
			name: "hash change triggers update",
			vmcp: vmcpWithAuthzRef("my-authz", mcpv1beta1.VirtualMCPServerStatus{
				AuthzConfigHash: "old-hash",
			}),
			authzConfig:        authzConfigForTest("my-authz", true, "new-hash"),
			expectedHash:       "new-hash",
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonAuthzConfigRefValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.authzConfig != nil {
				builder = builder.WithObjects(tt.authzConfig)
			}
			reconciler := &VirtualMCPServerReconciler{
				Client: builder.Build(),
				Scheme: scheme,
			}

			statusManager := virtualmcpserverstatus.NewStatusManager(tt.vmcp)
			err := reconciler.handleAuthzConfig(ctx, tt.vmcp, statusManager)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			status := &tt.vmcp.Status
			statusManager.UpdateStatus(ctx, status)

			if tt.expectHashCleared {
				assert.Empty(t, status.AuthzConfigHash, "hash should be cleared")
			}

			if tt.expectCondRemoved {
				for _, c := range status.Conditions {
					assert.NotEqual(t, mcpv1beta1.ConditionAuthzConfigRefValidated, c.Type,
						"stale AuthzConfigRefValidated condition should be removed")
				}
			}

			if tt.expectedCondReason != "" {
				var found bool
				for _, c := range status.Conditions {
					if c.Type == mcpv1beta1.ConditionAuthzConfigRefValidated {
						found = true
						assert.Equal(t, tt.expectedCondStatus, c.Status)
						assert.Equal(t, tt.expectedCondReason, c.Reason)
						break
					}
				}
				assert.True(t, found, "expected AuthzConfigRefValidated condition not found")
			}

			if tt.expectedHash != "" {
				assert.Equal(t, tt.expectedHash, status.AuthzConfigHash)
			}
		})
	}
}

func TestMapAuthzConfigToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	vmcp1 := vmcpWithAuthzRef("shared-authz", mcpv1beta1.VirtualMCPServerStatus{})
	vmcp1.Name = "vmcp1"
	vmcp2 := vmcpWithAuthzRef("other-authz", mcpv1beta1.VirtualMCPServerStatus{})
	vmcp2.Name = "vmcp2"
	vmcp3 := vmcpWithAuthzRef("", mcpv1beta1.VirtualMCPServerStatus{}) // no ref
	vmcp3.Name = "vmcp3"

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp1, vmcp2, vmcp3).
		Build()

	reconciler := &VirtualMCPServerReconciler{Client: fakeClient, Scheme: scheme}

	authzConfig := &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-authz", Namespace: "default"},
	}

	requests := reconciler.mapAuthzConfigToVirtualMCPServer(t.Context(), authzConfig)

	require.Len(t, requests, 1)
	assert.Equal(t, types.NamespacedName{Name: "vmcp1", Namespace: "default"}, requests[0].NamespacedName)
}
