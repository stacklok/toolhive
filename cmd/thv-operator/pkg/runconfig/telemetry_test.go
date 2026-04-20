// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	testImage      = "test-image:latest"
	stdioTransport = "stdio"
)

func TestAddMCPTelemetryConfigRefOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		spec                *mcpv1alpha1.MCPTelemetryConfigSpec
		serviceNameOverride string
		defaultServiceName  string
		caBundleFilePath    string
		//nolint:thelper // We want to see the error at the specific line
		expected func(t *testing.T, config *runner.RunConfig)
	}{
		{
			name:                "nil spec is a no-op",
			spec:                nil,
			serviceNameOverride: "override",
			defaultServiceName:  "default",
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Nil(t, config.TelemetryConfig)
			},
		},
		{
			name: "valid spec adds runner option",
			spec: &mcpv1alpha1.MCPTelemetryConfigSpec{
				OpenTelemetry: &mcpv1alpha1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "https://otel-collector:4317",
					Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true, SamplingRate: "0.1"},
					Metrics:  &mcpv1alpha1.OpenTelemetryMetricsConfig{Enabled: true},
				},
			},
			serviceNameOverride: "my-server-service",
			defaultServiceName:  "fallback-name",
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				require.NotNil(t, config.TelemetryConfig)
				assert.Equal(t, "otel-collector:4317", config.TelemetryConfig.Endpoint)
				assert.Equal(t, "my-server-service", config.TelemetryConfig.ServiceName)
				assert.True(t, config.TelemetryConfig.TracingEnabled)
				assert.True(t, config.TelemetryConfig.MetricsEnabled)
				assert.Equal(t, "0.1", config.TelemetryConfig.SamplingRate)
				assert.Empty(t, config.TelemetryConfig.CACertPath)
			},
		},
		{
			name: "CA bundle file path is threaded through to config",
			spec: &mcpv1alpha1.MCPTelemetryConfigSpec{
				OpenTelemetry: &mcpv1alpha1.MCPTelemetryOTelConfig{
					Enabled:  true,
					Endpoint: "https://otel-collector:4317",
					Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
				},
			},
			serviceNameOverride: "my-server",
			defaultServiceName:  "fallback",
			caBundleFilePath:    "/config/certs/otel/my-ca-bundle/ca.crt",
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				require.NotNil(t, config.TelemetryConfig)
				assert.Equal(t, "/config/certs/otel/my-ca-bundle/ca.crt", config.TelemetryConfig.CACertPath)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := []runner.RunConfigBuilderOption{
				runner.WithName("test-server"),
				runner.WithImage(testImage),
			}
			AddMCPTelemetryConfigRefOptions(&options, tt.spec, tt.serviceNameOverride, tt.defaultServiceName, tt.caBundleFilePath)

			rc, err := runner.NewOperatorRunConfigBuilder(context.Background(), nil, nil, nil, options...)
			assert.NoError(t, err)

			tt.expected(t, rc)
		})
	}
}

func TestAddMCPTelemetryConfigRefOptions_NilOptions(t *testing.T) {
	t.Parallel()

	spec := &mcpv1alpha1.MCPTelemetryConfigSpec{
		OpenTelemetry: &mcpv1alpha1.MCPTelemetryOTelConfig{
			Enabled:  true,
			Endpoint: "otel-collector:4317",
			Tracing:  &mcpv1alpha1.OpenTelemetryTracingConfig{Enabled: true},
		},
	}

	// Test with nil options pointer - should not panic
	assert.NotPanics(t, func() {
		AddMCPTelemetryConfigRefOptions(nil, spec, "override", "default", "")
	}, "AddMCPTelemetryConfigRefOptions should not panic with nil options")
}
