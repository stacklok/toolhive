package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
)

func TestAddTelemetryConfigOptions_UsageAnalytics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		telemetryConfig   *v1alpha1.TelemetryConfig
		expectedAnalytics *bool // nil means not explicitly set
	}{
		{
			name: "usage analytics explicitly enabled",
			telemetryConfig: &v1alpha1.TelemetryConfig{
				OpenTelemetry: &v1alpha1.OpenTelemetryConfig{
					Enabled:               true,
					Endpoint:              "otel-collector:4317",
					UsageAnalyticsEnabled: boolPtr(true),
				},
			},
			expectedAnalytics: boolPtr(true),
		},
		{
			name: "usage analytics explicitly disabled",
			telemetryConfig: &v1alpha1.TelemetryConfig{
				OpenTelemetry: &v1alpha1.OpenTelemetryConfig{
					Enabled:               true,
					Endpoint:              "otel-collector:4317",
					UsageAnalyticsEnabled: boolPtr(false),
				},
			},
			expectedAnalytics: boolPtr(false),
		},
		{
			name: "usage analytics not specified - uses default",
			telemetryConfig: &v1alpha1.TelemetryConfig{
				OpenTelemetry: &v1alpha1.OpenTelemetryConfig{
					Enabled:  true,
					Endpoint: "otel-collector:4317",
					// UsageAnalyticsEnabled is nil, should use default
				},
			},
			expectedAnalytics: nil, // Should use system default
		},
		{
			name:              "no telemetry config",
			telemetryConfig:   nil,
			expectedAnalytics: nil,
		},
		{
			name: "telemetry disabled",
			telemetryConfig: &v1alpha1.TelemetryConfig{
				OpenTelemetry: &v1alpha1.OpenTelemetryConfig{
					Enabled: false,
				},
			},
			expectedAnalytics: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var options []runner.RunConfigBuilderOption

			addTelemetryConfigOptions(&options, tt.telemetryConfig, "test-server")

			// Build the config to verify the options work
			ctx := context.Background()
			imageMetadata := &registry.ImageMetadata{} // Empty metadata for test
			envVars := make(map[string]string)
			envVarValidator := &runner.DetachedEnvVarValidator{} // Use detached validator for test

			config, err := runner.NewRunConfigBuilder(ctx, imageMetadata, envVars, envVarValidator, options...)
			require.NoError(t, err)

			if tt.expectedAnalytics == nil {
				// When not explicitly set, should use default from telemetry config
				if tt.telemetryConfig != nil && tt.telemetryConfig.OpenTelemetry != nil && tt.telemetryConfig.OpenTelemetry.Enabled {
					// If telemetry is enabled, config should be created with defaults
					if config.TelemetryConfig != nil {
						// The default value should be used (true)
						assert.True(t, config.TelemetryConfig.UsageAnalyticsEnabled, "Should use default value when not explicitly set")
					}
				}
			} else {
				// When explicitly set, should match the configured value
				require.NotNil(t, config.TelemetryConfig, "TelemetryConfig should be created when telemetry is enabled")
				assert.Equal(t, *tt.expectedAnalytics, config.TelemetryConfig.UsageAnalyticsEnabled)
			}
		})
	}
}

func TestMCPServerSpec_UsageAnalyticsField(t *testing.T) {
	t.Parallel()
	// Test that the UsageAnalyticsEnabled field is properly defined in the CRD
	mcpServer := &v1alpha1.MCPServer{
		Spec: v1alpha1.MCPServerSpec{
			Image: "test-image:latest",
			Telemetry: &v1alpha1.TelemetryConfig{
				OpenTelemetry: &v1alpha1.OpenTelemetryConfig{
					Enabled:               true,
					Endpoint:              "otel-collector:4317",
					UsageAnalyticsEnabled: boolPtr(false),
				},
			},
		},
	}

	// Verify the field can be set and read
	assert.NotNil(t, mcpServer.Spec.Telemetry)
	assert.NotNil(t, mcpServer.Spec.Telemetry.OpenTelemetry)
	assert.NotNil(t, mcpServer.Spec.Telemetry.OpenTelemetry.UsageAnalyticsEnabled)
	assert.False(t, *mcpServer.Spec.Telemetry.OpenTelemetry.UsageAnalyticsEnabled)
}
