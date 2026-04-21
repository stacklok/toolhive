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
)

func TestHandleTelemetryConfig_MCPRemoteProxy(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	tests := []struct {
		name               string
		proxy              *mcpv1beta1.MCPRemoteProxy
		telemetryConfig    *mcpv1beta1.MCPTelemetryConfig
		expectError        bool
		expectedHash       string
		expectedCondType   string
		expectedCondStatus metav1.ConditionStatus
		expectedCondReason string
		expectNoCondition  bool
		expectHashCleared  bool
	}{
		{
			name: "nil ref clears hash and removes condition",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec:       mcpv1beta1.MCPRemoteProxySpec{TelemetryConfigRef: nil},
				Status: mcpv1beta1.MCPRemoteProxyStatus{
					TelemetryConfigHash: "old-hash",
				},
			},
			expectError:       false,
			expectNoCondition: true,
			expectHashCleared: true,
		},
		{
			name: "valid ref sets condition true and updates hash",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
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
			expectedCondType:   mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefValid,
		},
		{
			name: "not found sets condition false",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "missing"},
				},
			},
			expectError:        true,
			expectedCondType:   mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
			expectedCondStatus: metav1.ConditionFalse,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefNotFound,
		},
		{
			name: "invalid config sets condition false",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "invalid-telemetry"},
				},
			},
			// Spec with endpoint but no tracing/metrics enabled → Validate() fails
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
			expectedCondType:   mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
			expectedCondStatus: metav1.ConditionFalse,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefInvalid,
		},
		{
			name: "hash change triggers update",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "my-telemetry"},
				},
				Status: mcpv1beta1.MCPRemoteProxyStatus{
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
			expectedCondType:   mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefValid,
		},
		{
			name: "recovery from False condition persists True",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec: mcpv1beta1.MCPRemoteProxySpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "my-telemetry"},
				},
				Status: mcpv1beta1.MCPRemoteProxyStatus{
					TelemetryConfigHash: "abc123",
					Conditions: []metav1.Condition{
						{
							Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
							Status: metav1.ConditionFalse,
							Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefFetchError,
						},
					},
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
			expectedCondType:   mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
			expectedCondStatus: metav1.ConditionTrue,
			expectedCondReason: mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefValid,
		},
		{
			name: "nil ref with stale condition persists removal",
			proxy: &mcpv1beta1.MCPRemoteProxy{
				ObjectMeta: metav1.ObjectMeta{Name: "test-proxy", Namespace: "default"},
				Spec:       mcpv1beta1.MCPRemoteProxySpec{TelemetryConfigRef: nil},
				Status: mcpv1beta1.MCPRemoteProxyStatus{
					Conditions: []metav1.Condition{
						{
							Type:   mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated,
							Status: metav1.ConditionFalse,
							Reason: mcpv1beta1.ConditionReasonMCPRemoteProxyTelemetryConfigRefNotFound,
						},
					},
				},
			},
			expectError:       false,
			expectNoCondition: true,
			expectHashCleared: true,
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
			builder = builder.WithStatusSubresource(&mcpv1beta1.MCPRemoteProxy{})
			builder = builder.WithObjects(tt.proxy)
			fakeClient := builder.Build()

			reconciler := &MCPRemoteProxyReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			err := reconciler.handleTelemetryConfig(ctx, tt.proxy)

			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			// Re-fetch persisted state from the fake client.
			// For success paths, the handler persists via r.Status().Update().
			// For error paths, conditions are set in-memory but the caller
			// (validateAndHandleConfigs) is responsible for persisting — so
			// we use in-memory state for error-path condition assertions.
			persisted := &mcpv1beta1.MCPRemoteProxy{}
			require.NoError(t, fakeClient.Get(ctx, types.NamespacedName{
				Name: tt.proxy.Name, Namespace: tt.proxy.Namespace,
			}, persisted))

			// For success paths, assert on persisted state.
			// For error paths, assert conditions on in-memory state (caller persists).
			statusToCheck := persisted.Status
			if tt.expectError {
				statusToCheck = tt.proxy.Status
			}

			if tt.expectNoCondition {
				for _, c := range persisted.Status.Conditions {
					assert.NotEqual(t, mcpv1beta1.ConditionTypeMCPRemoteProxyTelemetryConfigRefValidated, c.Type,
						"condition should have been removed from persisted state")
				}
			}

			if tt.expectHashCleared {
				assert.Empty(t, persisted.Status.TelemetryConfigHash, "hash should be cleared")
			}

			if tt.expectedCondType != "" {
				var found bool
				for _, c := range statusToCheck.Conditions {
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
				assert.Equal(t, tt.expectedHash, persisted.Status.TelemetryConfigHash)
			}
		})
	}
}

func TestMapTelemetryConfigToMCPRemoteProxy(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	proxy1 := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy1", Namespace: "default"},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "shared-telemetry"},
		},
	}
	proxy2 := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy2", Namespace: "default"},
		Spec: mcpv1beta1.MCPRemoteProxySpec{
			TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{Name: "other-telemetry"},
		},
	}
	proxy3 := &mcpv1beta1.MCPRemoteProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "proxy3", Namespace: "default"},
		Spec:       mcpv1beta1.MCPRemoteProxySpec{}, // no ref
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(proxy1, proxy2, proxy3).
		Build()

	reconciler := &MCPRemoteProxyReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	ctx := t.Context()

	telemetryConfig := &mcpv1beta1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "shared-telemetry", Namespace: "default"},
	}

	requests := reconciler.mapTelemetryConfigToMCPRemoteProxy(ctx, telemetryConfig)

	require.Len(t, requests, 1)
	assert.Equal(t, types.NamespacedName{Name: "proxy1", Namespace: "default"}, requests[0].NamespacedName)
}
