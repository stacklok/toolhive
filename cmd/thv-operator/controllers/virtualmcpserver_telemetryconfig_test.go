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

func TestHandleTelemetryConfig_VirtualMCPServer(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	tests := []struct {
		name               string
		vmcp               *mcpv1beta1.VirtualMCPServer
		telemetryConfig    *mcpv1beta1.MCPTelemetryConfig
		expectError        bool
		expectTelCfgNil    bool
		expectedHash       string
		expectedCondType   string
		expectedCondStatus metav1.ConditionStatus
		expectedCondReason string
		expectHashCleared  bool
		expectCondRemoved  bool
	}{
		{
			name: "nil ref clears hash and removes condition",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec:       mcpv1beta1.VirtualMCPServerSpec{TelemetryConfigRef: nil},
				Status: mcpv1beta1.VirtualMCPServerStatus{
					TelemetryConfigHash: "old-hash",
					Conditions: []metav1.Condition{
						{
							Type:   mcpv1beta1.ConditionTypeVirtualMCPServerTelemetryConfigRefValidated,
							Status: metav1.ConditionTrue,
							Reason: mcpv1beta1.ConditionReasonVirtualMCPServerTelemetryConfigRefValid,
						},
					},
				},
			},
			expectError:       false,
			expectTelCfgNil:   true,
			expectHashCleared: true,
			expectCondRemoved: true,
		},
		{
			name: "valid ref sets condition true and updates hash",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "my-telemetry"},
				},
			},
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-telemetry", Namespace: "default"},
				Spec:       newTelemetrySpec("https://otel-collector:4317", true, false),
				Status: mcpv1beta1.MCPTelemetryConfigStatus{
					ConfigHash: "abc123",
				},
			},
			expectError:        false,
			expectedHash:       "abc123",
			expectedCondType:   mcpv1beta1.ConditionTypeVirtualMCPServerTelemetryConfigRefValidated,
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonVirtualMCPServerTelemetryConfigRefValid,
		},
		{
			name: "not found sets condition false",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "missing"},
				},
			},
			expectError:        true,
			expectedCondType:   mcpv1beta1.ConditionTypeVirtualMCPServerTelemetryConfigRefValidated,
			expectedCondStatus: metav1.ConditionFalse,
			expectedCondReason: mcpv1beta1.ConditionReasonVirtualMCPServerTelemetryConfigRefNotFound,
		},
		{
			name: "invalid config sets condition false",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "invalid-telemetry"},
				},
			},
			// Spec with endpoint but no tracing/metrics enabled -> Validate() fails
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "invalid-telemetry", Namespace: "default"},
				Spec: mcpv1beta1.MCPTelemetryConfigSpec{
					OpenTelemetry: &mcpv1beta1.MCPTelemetryOTelConfig{
						Enabled:  true,
						Endpoint: "https://otel-collector:4317",
						Tracing:  &mcpv1beta1.OpenTelemetryTracingConfig{Enabled: false},
						Metrics:  &mcpv1beta1.OpenTelemetryMetricsConfig{Enabled: false},
					},
				},
			},
			expectError:        true,
			expectedCondType:   mcpv1beta1.ConditionTypeVirtualMCPServerTelemetryConfigRefValidated,
			expectedCondStatus: metav1.ConditionFalse,
			expectedCondReason: mcpv1beta1.ConditionReasonVirtualMCPServerTelemetryConfigRefInvalid,
		},
		{
			name: "hash change triggers update",
			vmcp: &mcpv1beta1.VirtualMCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: "test-vmcp", Namespace: "default"},
				Spec: mcpv1beta1.VirtualMCPServerSpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "my-telemetry"},
				},
				Status: mcpv1beta1.VirtualMCPServerStatus{
					TelemetryConfigHash: "old-hash",
				},
			},
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{Name: "my-telemetry", Namespace: "default"},
				Spec:       newTelemetrySpec("https://otel-collector:4317", true, false),
				Status: mcpv1beta1.MCPTelemetryConfigStatus{
					ConfigHash: "new-hash",
				},
			},
			expectError:        false,
			expectedHash:       "new-hash",
			expectedCondType:   mcpv1beta1.ConditionTypeVirtualMCPServerTelemetryConfigRefValidated,
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonVirtualMCPServerTelemetryConfigRefValid,
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

			reconciler := &VirtualMCPServerReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			statusManager := virtualmcpserverstatus.NewStatusManager(tt.vmcp)
			telCfg, err := reconciler.handleTelemetryConfig(ctx, tt.vmcp, statusManager)

			if tt.expectError {
				require.Error(t, err)
				assert.Nil(t, telCfg, "telemetry config should be nil on error")
			} else {
				require.NoError(t, err)
			}

			if tt.expectTelCfgNil {
				assert.Nil(t, telCfg, "telemetry config should be nil")
			}

			// Apply collected status changes to check assertions
			status := &tt.vmcp.Status
			statusManager.UpdateStatus(ctx, status)

			if tt.expectHashCleared {
				assert.Empty(t, status.TelemetryConfigHash, "hash should be cleared")
			}

			if tt.expectCondRemoved {
				for _, c := range status.Conditions {
					assert.NotEqual(t,
						mcpv1beta1.ConditionTypeVirtualMCPServerTelemetryConfigRefValidated,
						c.Type, "stale TelemetryConfigRefValidated condition should be removed")
				}
			}

			if tt.expectedCondType != "" {
				var found bool
				for _, c := range status.Conditions {
					if c.Type == tt.expectedCondType {
						found = true
						assert.Equal(t, tt.expectedCondStatus, c.Status)
						assert.Equal(t, tt.expectedCondReason, c.Reason)
						break
					}
				}
				assert.True(t, found, "expected condition %s not found", tt.expectedCondType)
			}

			if tt.expectedHash != "" {
				assert.Equal(t, tt.expectedHash, status.TelemetryConfigHash)
			}
		})
	}
}

func TestMapTelemetryConfigToVirtualMCPServer(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	vmcp1 := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "vmcp1", Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "shared-telemetry"},
		},
	}
	vmcp2 := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "vmcp2", Namespace: "default"},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "other-telemetry"},
		},
	}
	vmcp3 := &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "vmcp3", Namespace: "default"},
		Spec:       mcpv1beta1.VirtualMCPServerSpec{}, // no ref
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vmcp1, vmcp2, vmcp3).
		Build()

	reconciler := &VirtualMCPServerReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()

	telemetryConfig := &mcpv1beta1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-telemetry", Namespace: "default"},
	}

	requests := reconciler.mapTelemetryConfigToVirtualMCPServer(ctx, telemetryConfig)

	require.Len(t, requests, 1)
	assert.Equal(t, types.NamespacedName{Name: "vmcp1", Namespace: "default"}, requests[0].NamespacedName)
}
