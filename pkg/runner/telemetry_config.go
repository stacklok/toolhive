// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"log/slog"
	"strings"

	appconfig "github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/telemetry"
)

// otelHeadersEnvVar is the standard OpenTelemetry environment variable carrying
// OTLP export headers as a comma-separated list (e.g. "key1=val1,key2=val2").
// A config source may deliver headers through this variable in the app config's
// EnvVars instead of as header flags.
const otelHeadersEnvVar = "OTEL_EXPORTER_OTLP_HEADERS"

// BuildTelemetryConfigFromAppConfig builds a *telemetry.Config from an
// application OpenTelemetry config (read from ~/.config/toolhive/config.yaml
// or supplied by a config source).
//
// Both the "thv run" CLI and the "POST /api/v1/workloads" API path call this
// so workloads created via either surface inherit the same telemetry
// settings. Export headers come from two sources: the caller's header flags
// (the CLI surfaces "--otel-headers"; the API passes none) and an
// OTEL_EXPORTER_OTLP_HEADERS entry in the config's EnvVars. Flags take
// precedence. customAttributes has no equivalent in the application config
// and is passed through by the caller.
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

	parsedHeaders := mergeTelemetryHeaders(headers, otel.EnvVars)

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
		EnableUserIDAttribute:       otel.EnableUserIDAttribute,
	}
	cfg.SetSamplingRateFromFloat(otel.SamplingRate)
	return cfg
}

// mergeTelemetryHeaders builds the OTLP export header map from two sources:
// an OTEL_EXPORTER_OTLP_HEADERS entry in the config's EnvVars (used when the
// caller passes no header flags, such as the API path) and caller-supplied
// header flags ("key=value", from the CLI's --otel-headers). Env headers are
// applied first so flags override them on conflict. Values are split on the
// first '=' only, so a value containing '=' (e.g. base64 padding) or spaces
// (e.g. "Basic <token>") is preserved intact.
func mergeTelemetryHeaders(flagHeaders, envVars []string) map[string]string {
	headers := make(map[string]string)
	for _, entry := range envVars {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name != otelHeadersEnvVar {
			continue
		}
		for pair := range strings.SplitSeq(value, ",") {
			if key, val, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && key != "" {
				headers[key] = val
			}
		}
	}

	for _, h := range flagHeaders {
		if key, val, ok := strings.Cut(h, "="); ok {
			headers[key] = val
		}
	}
	return headers
}
