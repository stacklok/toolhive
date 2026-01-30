// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package spectoconfig provides functionality to convert CRD Telemetry types into telemetry.Config.
package spectoconfig

import (
	"context"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/telemetry"
)

// ConvertTelemetryConfig converts the CRD TelemetryConfig to a telemetry.Config.
// It may return nil if no telemetry is configured.
func ConvertTelemetryConfig(
	ctx context.Context,
	telemetryConfig *v1alpha1.TelemetryConfig,
	mcpServerName string,
) *telemetry.Config {
	if telemetryConfig == nil {
		return nil
	}

	// Default values
	// Note: if defaults here are also duplicated on the config struct's annotations.
	var otelEndpoint string
	var otelEnablePrometheusMetricsPath bool
	var otelTracingEnabled bool
	var otelMetricsEnabled bool
	var otelServiceName = mcpServerName // Default to mcpServerName, may be overridden below
	var otelSamplingRate = 0.05         // Default sampling rate
	var otelHeaders []string
	var otelInsecure bool
	var otelEnvironmentVariables []string

	// Process OpenTelemetry configuration
	if telemetryConfig.OpenTelemetry != nil && telemetryConfig.OpenTelemetry.Enabled {
		otel := telemetryConfig.OpenTelemetry

		// Note: Endpoint normalization (prefix stripping) and ServiceVersion defaulting
		// are handled by NormalizeTelemetryConfig below
		otelEndpoint = otel.Endpoint
		otelInsecure = otel.Insecure
		otelHeaders = otel.Headers

		// Override default service name if explicitly specified in OTLP config
		if otel.ServiceName != "" {
			otelServiceName = otel.ServiceName
		}

		// Handle tracing configuration
		if otel.Tracing != nil {
			otelTracingEnabled = otel.Tracing.Enabled
			if otel.Tracing.SamplingRate != "" {
				// Parse sampling rate string to float64
				if rate, err := strconv.ParseFloat(otel.Tracing.SamplingRate, 64); err == nil {
					otelSamplingRate = rate
				} else {
					logger := log.FromContext(ctx)
					logger.Error(err, "Failed to parse sampling rate, using default",
						"samplingRate", otel.Tracing.SamplingRate,
						"default", otelSamplingRate,
						"mcpServer", mcpServerName)
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

	config := telemetry.MaybeMakeConfig(
		otelEndpoint,
		otelEnablePrometheusMetricsPath,
		otelTracingEnabled,
		otelMetricsEnabled,
		otelServiceName,
		otelSamplingRate,
		otelHeaders,
		otelInsecure,
		otelEnvironmentVariables,
	)

	// Apply normalization (endpoint prefix stripping, ServiceName/ServiceVersion defaults)
	return NormalizeTelemetryConfig(config, mcpServerName)
}

// NormalizeTelemetryConfig applies runtime normalization to a telemetry.Config.
// This includes:
// - Stripping http:// or https:// prefixes from the endpoint (OTLP clients expect host:port format)
// - Defaulting ServiceName to the provided default name if not specified
// - Defaulting ServiceVersion to the build version if not specified
//
// This function is used by both the VirtualMCPServer converter (for spec.config.telemetry)
// and indirectly by ConvertTelemetryConfig (for CRD-style configs).
func NormalizeTelemetryConfig(config *telemetry.Config, defaultServiceName string) *telemetry.Config {
	if config == nil {
		return nil
	}

	// Create a copy to avoid modifying the input
	normalized := *config

	// Strip http:// or https:// prefix if present, as OTLP client expects host:port format
	if normalized.Endpoint != "" {
		normalized.Endpoint = strings.TrimPrefix(strings.TrimPrefix(normalized.Endpoint, "https://"), "http://")
	}

	// Default service name if not specified
	if normalized.ServiceName == "" {
		normalized.ServiceName = defaultServiceName
	}

	// Default service version to build version if not specified
	if normalized.ServiceVersion == "" {
		normalized.ServiceVersion = telemetry.DefaultConfig().ServiceVersion
	}

	return &normalized
}
