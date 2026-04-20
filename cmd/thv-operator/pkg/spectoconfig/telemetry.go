// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package spectoconfig provides functionality to convert CRD Telemetry types into telemetry.Config.
package spectoconfig

import (
	"strconv"
	"strings"

	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/telemetry"
)

// NormalizeMCPTelemetryConfig converts an MCPTelemetryConfigSpec to a normalized telemetry.Config.
// It maps the nested CRD structure (openTelemetry/prometheus) to a flat telemetry.Config,
// applies the per-server ServiceName override from the reference, then delegates to
// NormalizeTelemetryConfig for endpoint normalization and service name defaulting.
func NormalizeMCPTelemetryConfig(
	spec *v1alpha1.MCPTelemetryConfigSpec,
	serviceNameOverride string,
	defaultServiceName string,
) *telemetry.Config {
	if spec == nil {
		return nil
	}

	config := &telemetry.Config{}

	// Map nested OpenTelemetry fields to flat telemetry.Config.
	// Only configure OTLP when Enabled is true.
	if spec.OpenTelemetry != nil && spec.OpenTelemetry.Enabled {
		otel := spec.OpenTelemetry
		config.Endpoint = otel.Endpoint
		config.Insecure = otel.Insecure
		config.Headers = otel.Headers
		config.CustomAttributes = otel.ResourceAttributes
		config.UseLegacyAttributes = otel.UseLegacyAttributes

		if otel.Tracing != nil {
			config.TracingEnabled = otel.Tracing.Enabled
			if otel.Tracing.SamplingRate != "" {
				if rate, err := strconv.ParseFloat(otel.Tracing.SamplingRate, 64); err == nil {
					config.SetSamplingRateFromFloat(clampSamplingRate(rate))
				}
			}
		}
		if otel.Metrics != nil {
			config.MetricsEnabled = otel.Metrics.Enabled
		}
	}

	// Map Prometheus configuration
	if spec.Prometheus != nil {
		config.EnablePrometheusMetricsPath = spec.Prometheus.Enabled
	}

	// Apply per-server service name override from the TelemetryConfigRef
	if serviceNameOverride != "" {
		config.ServiceName = serviceNameOverride
	}

	return NormalizeTelemetryConfig(config, defaultServiceName)
}

// NormalizeTelemetryConfig applies runtime normalization to a telemetry.Config.
// This includes:
// - Stripping http:// or https:// prefixes from the endpoint (OTLP clients expect host:port format)
// - Defaulting ServiceName to the provided default name if not specified
//
// Note: ServiceVersion is intentionally NOT defaulted here. It is resolved at
// runtime in telemetry.NewProvider() to always reflect the running binary version,
// avoiding stale versions persisted in configs. See #2296.
//
// This function is used by both the VirtualMCPServer converter (for spec.config.telemetry)
// and indirectly by NormalizeMCPTelemetryConfig (for MCPTelemetryConfig-based configs).
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

	return &normalized
}

// clampSamplingRate restricts a sampling rate to the valid range [0.0, 1.0].
func clampSamplingRate(rate float64) float64 {
	if rate < 0 {
		return 0
	}
	if rate > 1 {
		return 1
	}
	return rate
}
