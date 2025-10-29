package runconfig

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	testImage      = "test-image:latest"
	stdioTransport = "stdio"
)

// TestAddTelemetryConfigOptions tests the addition of telemetry configuration options to the RunConfigBuilder
func TestAddTelemetryConfigOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mcpServer *mcpv1alpha1.MCPServer
		expected  func(t *testing.T, config *runner.RunConfig)
	}{
		{
			name: "with empty telemetry configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "disabled-telemetry-server",
					Namespace: "test-ns",
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Nil(t, config.TelemetryConfig)
			},
		},
		{
			name: "with telemetry configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "telemetry-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					ProxyPort: 8080,
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
							Enabled:     true,
							Endpoint:    "http://otel-collector:4317",
							ServiceName: "custom-service-name",
							Insecure:    true,
							Headers:     []string{"Authorization=Bearer token123", "X-API-Key=abc"},
							Tracing: &mcpv1alpha1.OpenTelemetryTracingConfig{
								Enabled:      true,
								SamplingRate: "0.25",
							},
							Metrics: &mcpv1alpha1.OpenTelemetryMetricsConfig{
								Enabled: true,
							},
						},
						Prometheus: &mcpv1alpha1.PrometheusConfig{
							Enabled: true,
						},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "telemetry-server", config.Name)

				// Verify telemetry config is set
				assert.NotNil(t, config.TelemetryConfig)

				// Check OpenTelemetry settings (endpoint should have http:// prefix stripped)
				assert.Equal(t, "otel-collector:4317", config.TelemetryConfig.Endpoint)
				assert.Equal(t, "custom-service-name", config.TelemetryConfig.ServiceName)
				assert.True(t, config.TelemetryConfig.Insecure)
				assert.True(t, config.TelemetryConfig.TracingEnabled)
				assert.True(t, config.TelemetryConfig.MetricsEnabled)
				assert.Equal(t, 0.25, config.TelemetryConfig.SamplingRate)
				assert.Equal(t, map[string]string{"Authorization": "Bearer token123", "X-API-Key": "abc"}, config.TelemetryConfig.Headers)

				// Check Prometheus settings
				assert.True(t, config.TelemetryConfig.EnablePrometheusMetricsPath)
			},
		},
		{
			name: "with minimal telemetry configuration",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-telemetry-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					ProxyPort: 8080,
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						OpenTelemetry: &mcpv1alpha1.OpenTelemetryConfig{
							Enabled:  true,
							Endpoint: "https://secure-otel:4318",
							// ServiceName not specified - should default to MCPServer name
						},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "minimal-telemetry-server", config.Name)

				// Verify telemetry config is set
				assert.NotNil(t, config.TelemetryConfig)

				// Check that service name defaults to MCPServer name
				assert.Equal(t, "minimal-telemetry-server", config.TelemetryConfig.ServiceName)
				assert.Equal(t, "secure-otel:4318", config.TelemetryConfig.Endpoint)
				assert.False(t, config.TelemetryConfig.Insecure)           // Default should be false
				assert.Equal(t, 0.05, config.TelemetryConfig.SamplingRate) // Default sampling rate
			},
		},
		{
			name: "with prometheus only telemetry",
			mcpServer: &mcpv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "prometheus-only-server",
					Namespace: "test-ns",
				},
				Spec: mcpv1alpha1.MCPServerSpec{
					Image:     testImage,
					Transport: stdioTransport,
					ProxyPort: 8080,
					Telemetry: &mcpv1alpha1.TelemetryConfig{
						Prometheus: &mcpv1alpha1.PrometheusConfig{
							Enabled: true,
						},
					},
				},
			},
			//nolint:thelper // We want to see the error at the specific line
			expected: func(t *testing.T, config *runner.RunConfig) {
				assert.Equal(t, "prometheus-only-server", config.Name)

				// Verify telemetry config is set
				assert.NotNil(t, config.TelemetryConfig)

				// Only Prometheus should be enabled
				assert.True(t, config.TelemetryConfig.EnablePrometheusMetricsPath)
				assert.False(t, config.TelemetryConfig.TracingEnabled)
				assert.False(t, config.TelemetryConfig.MetricsEnabled)
				assert.Equal(t, "", config.TelemetryConfig.Endpoint)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			options := []runner.RunConfigBuilderOption{
				runner.WithName(tt.mcpServer.Name),
				runner.WithImage(tt.mcpServer.Spec.Image),
			}
			AddTelemetryConfigOptions(&options, tt.mcpServer.Spec.Telemetry, tt.mcpServer.Name)

			rc, err := runner.NewOperatorRunConfigBuilder(context.Background(), nil, nil, nil, options...)
			assert.NoError(t, err)

			tt.expected(t, rc)
		})
	}
}
