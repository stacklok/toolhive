// Package runconfig provides functions to build RunConfigBuilder options for telemetry configuration.
package runconfig

import (
	"strconv"
	"strings"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/runner"
)

// AddTelemetryConfigOptions adds telemetry configuration options to the builder options
func AddTelemetryConfigOptions(
	options *[]runner.RunConfigBuilderOption,
	telemetryConfig *mcpv1alpha1.TelemetryConfig,
	mcpServerName string,
) {
	if telemetryConfig == nil {
		return
	}

	// Default values
	var otelEndpoint string
	var otelEnablePrometheusMetricsPath bool
	var otelTracingEnabled bool
	var otelMetricsEnabled bool
	var otelServiceName string
	var otelSamplingRate = 0.05 // Default sampling rate
	var otelHeaders []string
	var otelInsecure bool
	var otelEnvironmentVariables []string

	// Process OpenTelemetry configuration
	if telemetryConfig.OpenTelemetry != nil && telemetryConfig.OpenTelemetry.Enabled {
		otel := telemetryConfig.OpenTelemetry

		// Strip http:// or https:// prefix if present, as OTLP client expects host:port format
		otelEndpoint = strings.TrimPrefix(strings.TrimPrefix(otel.Endpoint, "https://"), "http://")
		otelInsecure = otel.Insecure
		otelHeaders = otel.Headers

		// Use MCPServer name as service name if not specified
		if otel.ServiceName != "" {
			otelServiceName = otel.ServiceName
		} else {
			otelServiceName = mcpServerName
		}

		// Handle tracing configuration
		if otel.Tracing != nil {
			otelTracingEnabled = otel.Tracing.Enabled
			if otel.Tracing.SamplingRate != "" {
				// Parse sampling rate string to float64
				if rate, err := strconv.ParseFloat(otel.Tracing.SamplingRate, 64); err == nil {
					otelSamplingRate = rate
				}
			}
		}

		// Handle metrics configuration
		if otel.Metrics != nil {
			otelMetricsEnabled = otel.Metrics.Enabled
		}
	}

	// Process Prometheus configuration
	if telemetryConfig.Prometheus != nil {
		otelEnablePrometheusMetricsPath = telemetryConfig.Prometheus.Enabled
	}

	// Add telemetry config to options
	*options = append(*options, runner.WithTelemetryConfig(
		otelEndpoint,
		otelEnablePrometheusMetricsPath,
		otelTracingEnabled,
		otelMetricsEnabled,
		otelServiceName,
		otelSamplingRate,
		otelHeaders,
		otelInsecure,
		otelEnvironmentVariables,
	))
}
