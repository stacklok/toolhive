// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

func TestGetTelemetryConfigForMCPServer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		mcpServer          *mcpv1beta1.MCPServer
		telemetryConfig    *mcpv1beta1.MCPTelemetryConfig
		expectNil          bool
		expectError        bool
		expectedConfigName string
	}{
		{
			name: "nil ref returns nil without error",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					TelemetryConfigRef: nil,
				},
			},
			telemetryConfig: nil,
			expectNil:       true,
			expectError:     false,
		},
		{
			name: "fetches the right config from the fake client",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{
						Name: "my-telemetry-config",
					},
				},
			},
			telemetryConfig: &mcpv1beta1.MCPTelemetryConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-telemetry-config",
					Namespace: "default",
				},
				Spec: newTelemetrySpec("https://otel-collector:4317", true, false),
			},
			expectNil:          false,
			expectError:        false,
			expectedConfigName: "my-telemetry-config",
		},
		{
			name: "returns nil without error when not found",
			mcpServer: &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{
						Name: "non-existent-config",
					},
				},
			},
			telemetryConfig: nil,
			expectNil:       true,
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			scheme := runtime.NewScheme()
			require.NoError(t, mcpv1beta1.AddToScheme(scheme))

			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tt.telemetryConfig != nil {
				builder = builder.WithObjects(tt.telemetryConfig)
			}
			fakeClient := builder.Build()

			result, err := getTelemetryConfigForMCPServer(ctx, fakeClient, tt.mcpServer)

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
				assert.Equal(t, tt.expectedConfigName, result.Name)
			}
		})
	}
}

func TestGetTelemetryConfigForMCPServer_NamespacedLookup(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1beta1.AddToScheme(scheme))

	// Config exists in namespace-a but server is in namespace-b
	telemetryConfig := &mcpv1beta1.MCPTelemetryConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "shared-config",
			Namespace: "namespace-a",
		},
		Spec: newTelemetrySpec("https://otel-collector:4317", true, false),
	}

	mcpServer := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-server",
			Namespace: "namespace-b",
		},
		Spec: mcpv1beta1.MCPServerSpec{
			TelemetryConfigRef: &mcpv1beta1.MCPTelemetryConfigReference{
				Name: "shared-config",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(telemetryConfig).
		Build()

	// Should return nil (NotFound) because the config is in a different namespace
	result, err := getTelemetryConfigForMCPServer(ctx, fakeClient, mcpServer)
	assert.NoError(t, err, "NotFound should return nil error")
	assert.Nil(t, result, "Should not find config in different namespace")
}
