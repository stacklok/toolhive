// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"log/slog"
	"strings"

	appconfig "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/telemetry"
)

// BuildTelemetryConfigFromAppConfig builds a *telemetry.Config from an
// application OpenTelemetry config (read from ~/.config/toolhive/config.yaml
// or pushed by an enterprise config server).
//
// Both the "thv run" CLI and the "POST /api/v1/workloads" API path call this
// so workloads created via either surface inherit the same telemetry
// settings. Headers and customAttributes have no equivalent in the
// application config and are passed through from the caller when available
// (the CLI surfaces them as flags; the API does not yet).
//
// Returns nil when no telemetry should be enabled: either no endpoint and no
// Prometheus metrics path, or both tracing and metrics disabled with no
// Prometheus path.
//
// CLI-style defaults are applied: TracingEnabled, MetricsEnabled, and
// UseLegacyAttributes default to true when unset in the config so that
// configuring just an endpoint produces a working setup.
func BuildTelemetryConfigFromAppConfig(
	otel appconfig.OpenTelemetryConfig,
	serviceName string,
	headers []string,
	customAttributes string,
) *telemetry.Config {
	if otel.Endpoint == "" && !otel.EnablePrometheusMetricsPath {
		return nil
	}

	tracingEnabled := true
	if otel.TracingEnabled != nil {
		tracingEnabled = *otel.TracingEnabled
	}
	metricsEnabled := true
	if otel.MetricsEnabled != nil {
		metricsEnabled = *otel.MetricsEnabled
	}
	useLegacyAttributes := true
	if otel.UseLegacyAttributes != nil {
		useLegacyAttributes = *otel.UseLegacyAttributes
	}

	if !tracingEnabled && !metricsEnabled && !otel.EnablePrometheusMetricsPath {
		return nil
	}

	parsedHeaders := make(map[string]string)
	for _, h := range headers {
		parts := strings.SplitN(h, "=", 2)
		if len(parts) == 2 {
			parsedHeaders[parts[0]] = parts[1]
		}
	}

	var processedEnvVars []string
	for _, entry := range otel.EnvVars {
		for ev := range strings.SplitSeq(entry, ",") {
			trimmed := strings.TrimSpace(ev)
			if trimmed != "" {
				processedEnvVars = append(processedEnvVars, trimmed)
			}
		}
	}

	customAttrs, err := telemetry.ParseCustomAttributes(customAttributes)
	if err != nil {
		slog.Warn("Failed to parse custom attributes", "error", err)
		customAttrs = nil
	}

	cfg := &telemetry.Config{
		Endpoint:                    otel.Endpoint,
		ServiceName:                 serviceName,
		ServiceVersion:              "", // resolved at runtime in NewProvider()
		TracingEnabled:              tracingEnabled,
		MetricsEnabled:              metricsEnabled,
		Headers:                     parsedHeaders,
		Insecure:                    otel.Insecure,
		EnablePrometheusMetricsPath: otel.EnablePrometheusMetricsPath,
		EnvironmentVariables:        processedEnvVars,
		CustomAttributes:            customAttrs,
		UseLegacyAttributes:         useLegacyAttributes,
	}
	cfg.SetSamplingRateFromFloat(otel.SamplingRate)
	return cfg
}
