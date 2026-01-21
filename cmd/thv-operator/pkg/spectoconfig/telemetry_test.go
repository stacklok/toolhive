// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spectoconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/telemetry"
)

func TestNormalizeTelemetryConfig(t *testing.T) {
	t.Parallel()

	// Get the expected build version for tests
	buildVersion := telemetry.DefaultConfig().ServiceVersion

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
				Endpoint:       "otlp-collector:4317",
				ServiceName:    "my-service",
				ServiceVersion: buildVersion,
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
				Endpoint:       "localhost:4317",
				ServiceName:    "my-service",
				ServiceVersion: buildVersion,
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
				Endpoint:       "otlp-collector:4317",
				ServiceName:    "my-service",
				ServiceVersion: buildVersion,
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
				Endpoint:       "localhost:4317",
				ServiceName:    "default-service",
				ServiceVersion: buildVersion,
			},
		},
		{
			name: "defaults ServiceVersion to build version when empty",
			input: &telemetry.Config{
				Endpoint:       "localhost:4317",
				ServiceName:    "my-service",
				ServiceVersion: "",
			},
			defaultName: "default-service",
			expected: &telemetry.Config{
				Endpoint:       "localhost:4317",
				ServiceName:    "my-service",
				ServiceVersion: buildVersion,
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

func TestConvertTelemetryConfig_UsesNormalization(t *testing.T) {
	t.Parallel()

	// Get the expected build version for tests
	buildVersion := telemetry.DefaultConfig().ServiceVersion

	// This test verifies that ConvertTelemetryConfig uses NormalizeTelemetryConfig
	// to apply endpoint prefix stripping and service name/version defaults
	tests := []struct {
		name       string
		input      *v1alpha1.TelemetryConfig
		serverName string
		expected   *telemetry.Config
	}{
		{
			name: "applies endpoint normalization and service defaults",
			input: &v1alpha1.TelemetryConfig{
				OpenTelemetry: &v1alpha1.OpenTelemetryConfig{
					Enabled:     true,
					Endpoint:    "https://otlp-collector:4317",
					ServiceName: "", // Empty - should default to serverName
					Tracing: &v1alpha1.OpenTelemetryTracingConfig{
						Enabled:      true,
						SamplingRate: "0.1",
					},
				},
			},
			serverName: "my-mcp-server",
			expected: &telemetry.Config{
				Endpoint:       "otlp-collector:4317", // Prefix stripped
				ServiceName:    "my-mcp-server",       // Defaulted
				ServiceVersion: buildVersion,          // Defaulted to build version
				TracingEnabled: true,
				SamplingRate:   "0.1",
			},
		},
		{
			name: "preserves explicit service name and version",
			input: &v1alpha1.TelemetryConfig{
				OpenTelemetry: &v1alpha1.OpenTelemetryConfig{
					Enabled:     true,
					Endpoint:    "http://localhost:4317",
					ServiceName: "custom-service",
				},
			},
			serverName: "default-server",
			expected: &telemetry.Config{
				Endpoint:       "localhost:4317", // Prefix stripped
				ServiceName:    "custom-service", // Preserved
				ServiceVersion: buildVersion,     // Defaulted to build version
			},
		},
		{
			name: "handles prometheus-only config",
			input: &v1alpha1.TelemetryConfig{
				Prometheus: &v1alpha1.PrometheusConfig{
					Enabled: true,
				},
			},
			serverName: "prom-server",
			expected: &telemetry.Config{
				EnablePrometheusMetricsPath: true,
				ServiceName:                 "prom-server", // Defaulted
				ServiceVersion:              buildVersion,  // Defaulted to build version
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			result := ConvertTelemetryConfig(ctx, tt.input, tt.serverName)

			require.NotNil(t, result)
			assert.Equal(t, tt.expected.Endpoint, result.Endpoint)
			assert.Equal(t, tt.expected.ServiceName, result.ServiceName)
			assert.Equal(t, tt.expected.ServiceVersion, result.ServiceVersion)
			assert.Equal(t, tt.expected.TracingEnabled, result.TracingEnabled)
			assert.Equal(t, tt.expected.EnablePrometheusMetricsPath, result.EnablePrometheusMetricsPath)
		})
	}
}
