// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
)

func TestTelemetryArgs(t *testing.T) {
	t.Parallel()

	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1alpha1.AddToScheme(scheme))

	tests := []struct {
		name              string
		telemetryConfig   *mcpv1alpha1.TelemetryConfig
		prometheusEnabled bool
		expectedArgs      []string
	}{
		{
			name:              "nil telemetry config",
			telemetryConfig:   nil,
			prometheusEnabled: false,
			expectedArgs:      nil,
		},
		{
			name: "basic OpenTelemetry config",
			telemetryConfig: &mcpv1alpha1.TelemetryConfig{
				OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
					ServiceName: "test-service",
					Headers:     []string{"x-api-key=secret", "x-tenant-id=tenant1"},
					Insecure:    true,
				},
			},
			prometheusEnabled: false,
			expectedArgs: []string{
				"--otel-service-name=test-service",
				"--otel-headers=x-api-key=secret",
				"--otel-headers=x-tenant-id=tenant1",
				"--otel-insecure",
			},
		},
		{
			name: "OpenTelemetry with endpoint",
			telemetryConfig: &mcpv1alpha1.TelemetryConfig{
				OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
					Endpoint:    "otel-collector:4318",
					ServiceName: "endpoint-service",
				},
			},
			prometheusEnabled: false,
			expectedArgs: []string{
				"--otel-endpoint=otel-collector:4318",
				"--otel-service-name=endpoint-service",
			},
		},
		{
			name: "OpenTelemetry metrics with Prometheus enabled",
			telemetryConfig: &mcpv1alpha1.TelemetryConfig{
				OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
					Metrics: &mcpv1alpha1.OpenTelemetryMetricsConfig{
						Enabled: true,
					},
				},
				Prometheus: &mcpv1alpha1.PrometheusConfig{
					Enabled: true,
				},
			},
			prometheusEnabled: true,
			expectedArgs: []string{
				"--otel-metrics-enabled=true",
				"--enable-prometheus-metrics-path",
			},
		},
		{
			name: "Prometheus only",
			telemetryConfig: &mcpv1alpha1.TelemetryConfig{
				Prometheus: &mcpv1alpha1.PrometheusConfig{
					Enabled: true,
				},
			},
			prometheusEnabled: true,
			expectedArgs: []string{
				"--enable-prometheus-metrics-path",
			},
		},
		{
			name: "complete OpenTelemetry config and prometheus enabled",
			telemetryConfig: &mcpv1alpha1.TelemetryConfig{
				OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
					ServiceName: "complete-service",
					Headers:     []string{"authorization=bearer token123"},
					Insecure:    false,
					Metrics: &mcpv1alpha1.OpenTelemetryMetricsConfig{
						Enabled: true,
					},
					Tracing: &mcpv1alpha1.OpenTelemetryTracingConfig{
						Enabled:      true,
						SamplingRate: "0.1",
					},
				},
				Prometheus: &mcpv1alpha1.PrometheusConfig{
					Enabled: true,
				},
			},
			prometheusEnabled: true,
			expectedArgs: []string{
				"--otel-service-name=complete-service",
				"--otel-headers=authorization=bearer token123",
				"--otel-tracing-enabled=true",
				"--otel-tracing-sampling-rate=0.1",
				"--otel-metrics-enabled=true",
				"--enable-prometheus-metrics-path",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := fake.NewClientBuilder().WithScheme(scheme).Build()
			r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

			mcpServer := &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server",
					Namespace: "default",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image: "test-image:latest",
					Telemetry: func() *mcpv1alpha1.TelemetryConfig {
						if tt.telemetryConfig == nil && !tt.prometheusEnabled {
							return nil
						}
						telemetryConfig := &mcpv1alpha1.TelemetryConfig{
							OpenTelemetry: tt.telemetryConfig.OpenTelemetry,
						}
						if tt.prometheusEnabled {
							telemetryConfig.Prometheus = &mcpv1alpha1.PrometheusConfig{
								Enabled: true,
							}
						}
						return telemetryConfig
					}(),
				},
			}

			args := r.generateOpenTelemetryArgs(mcpServer)
			args = append(args, r.generatePrometheusArgs(mcpServer)...)

			// Check that all expected arguments are present, regardless of order
			assert.ElementsMatch(t, tt.expectedArgs, args)
		})
	}
}

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

			client := fake.NewClientBuilder().WithScheme(scheme).Build()
			r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

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

			envVars := r.generateOpenTelemetryEnvVars(mcpServer)
			assert.Equal(t, tt.expectedEnv, envVars)
		})
	}
}

func TestEqualOpenTelemetryArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     *mcpv1alpha1.OpenTelemetryConfig
		args     []string
		expected bool
	}{
		{
			name:     "nil spec and no otel args",
			spec:     nil,
			args:     []string{"--transport=stdio", "--name=test"},
			expected: true,
		},
		{
			name:     "nil spec but otel args present",
			spec:     nil,
			args:     []string{"--otel-service-name=test"},
			expected: false,
		},
		{
			name: "matching endpoint",
			spec: &mcpv1alpha1.OpenTelemetryConfig{
				Endpoint: "otel-collector:4318",
			},
			args:     []string{"--otel-endpoint=otel-collector:4318"},
			expected: true,
		},
		{
			name: "matching service name",
			spec: &mcpv1alpha1.OpenTelemetryConfig{
				ServiceName: "test-service",
			},
			args:     []string{"--otel-service-name=test-service"},
			expected: true,
		},
		{
			name: "different service name",
			spec: &mcpv1alpha1.OpenTelemetryConfig{
				ServiceName: "test-service",
			},
			args:     []string{"--otel-service-name=other-service"},
			expected: false,
		},
		{
			name: "matching headers",
			spec: &mcpv1alpha1.OpenTelemetryConfig{
				Headers: []string{"x-api-key=secret", "x-tenant=tenant1"},
			},
			args:     []string{"--otel-headers=x-api-key=secret", "--otel-headers=x-tenant=tenant1"},
			expected: true,
		},
		{
			name: "different number of headers",
			spec: &mcpv1alpha1.OpenTelemetryConfig{
				Headers: []string{"x-api-key=secret"},
			},
			args:     []string{"--otel-headers=x-api-key=secret", "--otel-headers=x-tenant=tenant1"},
			expected: false,
		},
		{
			name: "matching insecure flag",
			spec: &mcpv1alpha1.OpenTelemetryConfig{
				Insecure: true,
			},
			args:     []string{"--otel-insecure"},
			expected: true,
		},
		{
			name: "insecure flag mismatch",
			spec: &mcpv1alpha1.OpenTelemetryConfig{
				Insecure: false,
			},
			args:     []string{"--otel-insecure"},
			expected: false,
		},
		{
			name: "complete config match",
			spec: &mcpv1alpha1.OpenTelemetryConfig{
				ServiceName: "test",
				Headers:     []string{"x-api-key=secret"},
				Insecure:    true,
			},
			args:     []string{"--otel-service-name=test", "--otel-headers=x-api-key=secret", "--otel-insecure"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := equalOpenTelemetryArgs(tt.spec, tt.args)
			assert.Equal(t, tt.expected, result)
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

			client := fake.NewClientBuilder().WithScheme(scheme).Build()
			r := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

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

			envVars := r.generateOpenTelemetryEnvVars(mcpServer)

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
