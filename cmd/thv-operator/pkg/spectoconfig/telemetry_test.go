// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spectoconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/telemetry"
)

func TestNormalizeTelemetryConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       *telemetry.Config
		defaultName string
		expected    *telemetry.Config
	}{
		{
			name:        "nil config returns nil",
			input:       nil,
			defaultName: "test-service",
			expected:    nil,
		},
		{
			name: "strips https:// prefix from endpoint",
			input: &telemetry.Config{
				Endpoint:    "https://otlp-collector:4317",
				ServiceName: "my-service",
			},
			defaultName: "default-service",
			expected: &telemetry.Config{
				Endpoint:    "otlp-collector:4317",
				ServiceName: "my-service",
			},
		},
		{
			name: "strips http:// prefix from endpoint",
			input: &telemetry.Config{
				Endpoint:    "http://localhost:4317",
				ServiceName: "my-service",
			},
			defaultName: "default-service",
			expected: &telemetry.Config{
				Endpoint:    "localhost:4317",
				ServiceName: "my-service",
			},
		},
		{
			name: "preserves endpoint without prefix",
			input: &telemetry.Config{
				Endpoint:    "otlp-collector:4317",
				ServiceName: "my-service",
			},
			defaultName: "default-service",
			expected: &telemetry.Config{
				Endpoint:    "otlp-collector:4317",
				ServiceName: "my-service",
			},
		},
		{
			name: "defaults ServiceName when empty",
			input: &telemetry.Config{
				Endpoint:    "localhost:4317",
				ServiceName: "",
			},
			defaultName: "default-service",
			expected: &telemetry.Config{
				Endpoint:    "localhost:4317",
				ServiceName: "default-service",
			},
		},
		{
			name: "ServiceVersion left empty for runtime resolution",
			input: &telemetry.Config{
				Endpoint:       "localhost:4317",
				ServiceName:    "my-service",
				ServiceVersion: "",
			},
			defaultName: "default-service",
			expected: &telemetry.Config{
				Endpoint:    "localhost:4317",
				ServiceName: "my-service",
			},
		},
		{
			name: "preserves explicit ServiceVersion",
			input: &telemetry.Config{
				Endpoint:       "localhost:4317",
				ServiceName:    "my-service",
				ServiceVersion: "v2.0.0",
			},
			defaultName: "default-service",
			expected: &telemetry.Config{
				Endpoint:       "localhost:4317",
				ServiceName:    "my-service",
				ServiceVersion: "v2.0.0",
			},
		},
		{
			name: "preserves all other fields",
			input: &telemetry.Config{
				Endpoint:                    "https://otlp:4317",
				ServiceName:                 "my-service",
				ServiceVersion:              "v1.0.0",
				TracingEnabled:              true,
				MetricsEnabled:              true,
				SamplingRate:                "0.1",
				EnablePrometheusMetricsPath: true,
				Insecure:                    true,
				Headers: map[string]string{
					"Authorization": "Bearer token",
				},
				CustomAttributes: map[string]string{
					"env": "prod",
				},
				EnvironmentVariables: []string{"PATH", "HOME"},
			},
			defaultName: "default-service",
			expected: &telemetry.Config{
				Endpoint:                    "otlp:4317", // Prefix stripped
				ServiceName:                 "my-service",
				ServiceVersion:              "v1.0.0",
				TracingEnabled:              true,
				MetricsEnabled:              true,
				SamplingRate:                "0.1",
				EnablePrometheusMetricsPath: true,
				Insecure:                    true,
				Headers: map[string]string{
					"Authorization": "Bearer token",
				},
				CustomAttributes: map[string]string{
					"env": "prod",
				},
				EnvironmentVariables: []string{"PATH", "HOME"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := NormalizeTelemetryConfig(tt.input, tt.defaultName)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestNormalizeTelemetryConfig_DoesNotModifyInput(t *testing.T) {
	t.Parallel()

	input := &telemetry.Config{
		Endpoint:    "https://otlp-collector:4317",
		ServiceName: "",
	}

	// Keep a copy of the original endpoint to verify it's not modified
	originalEndpoint := input.Endpoint
	originalServiceName := input.ServiceName

	result := NormalizeTelemetryConfig(input, "default-service")

	// Verify input was not modified
	assert.Equal(t, originalEndpoint, input.Endpoint, "Input endpoint should not be modified")
	assert.Equal(t, originalServiceName, input.ServiceName, "Input ServiceName should not be modified")

	// Verify result has normalized values
	assert.Equal(t, "otlp-collector:4317", result.Endpoint)
	assert.Equal(t, "default-service", result.ServiceName)
}

func TestNormalizeMCPTelemetryConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		spec                *v1beta1.MCPTelemetryConfigSpec
		serviceNameOverride string
		defaultServiceName  string
		expected            *telemetry.Config
	}{
		{
			name:                "nil spec returns nil",
			spec:                nil,
			serviceNameOverride: "override",
			defaultServiceName:  "default",
			expected:            nil,
		},
		{
			name: "service name override takes precedence",
			spec: &v1beta1.MCPTelemetryConfigSpec{
				OpenTelemetry: &v1beta1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "https://otel-collector:4317",
				},
			},
			serviceNameOverride: "per-server-override",
			defaultServiceName:  "default-name",
			expected: &telemetry.Config{
				Endpoint:    "otel-collector:4317",
				ServiceName: "per-server-override",
			},
		},
		{
			name: "empty override falls through to defaultServiceName",
			spec: &v1beta1.MCPTelemetryConfigSpec{
				OpenTelemetry: &v1beta1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "otel-collector:4317",
				},
			},
			serviceNameOverride: "",
			defaultServiceName:  "default-server",
			expected: &telemetry.Config{
				Endpoint:    "otel-collector:4317",
				ServiceName: "default-server",
			},
		},
		{
			name: "endpoint normalization strips http:// prefix",
			spec: &v1beta1.MCPTelemetryConfigSpec{
				OpenTelemetry: &v1beta1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "http://collector.monitoring:4317",
					Tracing:  &v1beta1.OpenTelemetryTracingConfig{Enabled: true},
				},
			},
			serviceNameOverride: "my-service",
			defaultServiceName:  "fallback",
			expected: &telemetry.Config{
				Endpoint:       "collector.monitoring:4317",
				ServiceName:    "my-service",
				TracingEnabled: true,
			},
		},
		{
			name: "endpoint normalization strips https:// prefix",
			spec: &v1beta1.MCPTelemetryConfigSpec{
				OpenTelemetry: &v1beta1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "https://secure-collector:4317",
				},
			},
			serviceNameOverride: "my-service",
			defaultServiceName:  "fallback",
			expected: &telemetry.Config{
				Endpoint:    "secure-collector:4317",
				ServiceName: "my-service",
			},
		},
		{
			name: "default service name used when no override",
			spec: &v1beta1.MCPTelemetryConfigSpec{
				OpenTelemetry: &v1beta1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "collector:4317",
				},
			},
			serviceNameOverride: "",
			defaultServiceName:  "fallback",
			expected: &telemetry.Config{
				Endpoint:    "collector:4317",
				ServiceName: "fallback",
			},
		},
		{
			name: "enabled false skips OTel config entirely",
			spec: &v1beta1.MCPTelemetryConfigSpec{
				OpenTelemetry: &v1beta1.MCPTelemetryOTelConfig{
					Enabled:  false,
					Endpoint: "https://otel-collector:4317",
					Tracing:  &v1beta1.OpenTelemetryTracingConfig{Enabled: true},
					Metrics:  &v1beta1.OpenTelemetryMetricsConfig{Enabled: true},
				},
			},
			serviceNameOverride: "my-service",
			defaultServiceName:  "fallback",
			expected: &telemetry.Config{
				ServiceName: "my-service",
			},
		},
		{
			name: "endpoint with nil tracing and metrics produces no tracing or metrics",
			spec: &v1beta1.MCPTelemetryConfigSpec{
				OpenTelemetry: &v1beta1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "otel-collector:4317",
					// Tracing and Metrics are nil
				},
			},
			serviceNameOverride: "",
			defaultServiceName:  "test-server",
			expected: &telemetry.Config{
				Endpoint:    "otel-collector:4317",
				ServiceName: "test-server",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := NormalizeMCPTelemetryConfig(tt.spec, tt.serviceNameOverride, tt.defaultServiceName)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestNormalizeMCPTelemetryConfig_DoesNotModifyInput(t *testing.T) {
	t.Parallel()

	spec := &v1beta1.MCPTelemetryConfigSpec{
		OpenTelemetry: &v1beta1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: "https://otel-collector:4317",
		},
	}

	originalEndpoint := spec.OpenTelemetry.Endpoint

	result := NormalizeMCPTelemetryConfig(spec, "override-name", "default-name")

	// Verify the original spec was not modified
	assert.Equal(t, originalEndpoint, spec.OpenTelemetry.Endpoint, "Input endpoint should not be modified")

	// Verify result has normalized values
	require.NotNil(t, result)
	assert.Equal(t, "otel-collector:4317", result.Endpoint)
	assert.Equal(t, "override-name", result.ServiceName)
}

func TestNormalizeMCPTelemetryConfig_ClampsSamplingRate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		samplingRate string
		expected     string
	}{
		{
			name:         "value above 1.0 is clamped to 1",
			samplingRate: "42",
			expected:     "1",
		},
		{
			name:         "negative value is clamped to 0",
			samplingRate: "-1",
			expected:     "0",
		},
		{
			name:         "valid value is preserved",
			samplingRate: "0.3",
			expected:     "0.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := &v1beta1.MCPTelemetryConfigSpec{
				OpenTelemetry: &v1beta1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "otel-collector:4317",
					Tracing: &v1beta1.OpenTelemetryTracingConfig{
						Enabled:      true,
						SamplingRate: tt.samplingRate,
					},
				},
			}
			result := NormalizeMCPTelemetryConfig(spec, "test-service", "default")
			require.NotNil(t, result)
			assert.Equal(t, tt.expected, result.SamplingRate)
		})
	}
}
