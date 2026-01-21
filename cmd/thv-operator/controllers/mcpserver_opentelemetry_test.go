// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

func TestOpenTelemetryEnvVars(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	tests := []struct {
		name        string
		otelConfig  *mcpv1alpha1.OpenTelemetryConfig
		expectedEnv []corev1.EnvVar
	}{
		{
			name:        "nil OpenTelemetry config",
			otelConfig:  nil,
			expectedEnv: nil,
		},
		{
			name: "basic OpenTelemetry config with service name",
			otelConfig: &mcpv1alpha1.OpenTelemetryConfig{
				ServiceName: "custom-service",
			},
			expectedEnv: []corev1.EnvVar{
				{Name: "OTEL_RESOURCE_ATTRIBUTES", Value: "service.name=custom-service,service.namespace=default"},
			},
		},
		{
			name:       "OpenTelemetry with default service name",
			otelConfig: &mcpv1alpha1.OpenTelemetryConfig{},
			expectedEnv: []corev1.EnvVar{
				{Name: "OTEL_RESOURCE_ATTRIBUTES", Value: "service.name=test-server,service.namespace=default"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image:latest",
					Telemetry: func() *mcpv1alpha1.TelemetryConfig {
						if tt.otelConfig == nil {
							return nil
						}
						return &mcpv1alpha1.TelemetryConfig{
							OpenTelemetry: tt.otelConfig,
						}
					}(),
				},
			}

			envVars := ctrlutil.GenerateOpenTelemetryEnvVars(mcpServer.Spec.Telemetry, mcpServer.Name, mcpServer.Namespace)
			assert.Equal(t, tt.expectedEnv, envVars)
		})
	}
}

func TestServiceNameDefaulting(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	tests := []struct {
		name                string
		serverName          string
		serverNamespace     string
		providedServiceName string
		expectedServiceName string
	}{
		{
			name:                "default service name",
			serverName:          "my-mcp-server",
			serverNamespace:     "production",
			providedServiceName: "",
			expectedServiceName: "my-mcp-server",
		},
		{
			name:                "custom service name",
			serverName:          "my-mcp-server",
			serverNamespace:     "production",
			providedServiceName: "custom-service",
			expectedServiceName: "custom-service",
		},
		{
			name:                "namespace included in resource attributes",
			serverName:          "test-server",
			serverNamespace:     "test-namespace",
			providedServiceName: "test-service",
			expectedServiceName: "test-service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.serverName,
					Namespace: tt.serverNamespace,
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image:latest",
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
							ServiceName: tt.providedServiceName,
							Metrics: &mcpv1alpha1.OpenTelemetryMetricsConfig{
								Enabled: true,
							},
						},
					},
				},
			}

			envVars := ctrlutil.GenerateOpenTelemetryEnvVars(mcpServer.Spec.Telemetry, mcpServer.Name, mcpServer.Namespace)

			// Check OTEL_RESOURCE_ATTRIBUTES contains correct service name and namespace
			var resourceAttrs string
			for _, env := range envVars {
				if env.Name == "OTEL_RESOURCE_ATTRIBUTES" {
					resourceAttrs = env.Value
					break
				}
			}

			expectedResourceAttrs := "service.name=" + tt.expectedServiceName + ",service.namespace=" + tt.serverNamespace
			assert.Equal(t, expectedResourceAttrs, resourceAttrs, "Resource attributes should contain correct service name and namespace")
		})
	}
}
