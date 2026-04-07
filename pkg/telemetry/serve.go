// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/config"
)

// NewServeProvider initialises the OTEL provider for thv serve using the global
// config (set via `thv config otel set-endpoint`). No new CLI flags are
// introduced; serve reuses the same OTEL config as thv run. Any span processors
// registered via RegisterSpanProcessor (e.g. by sentrypkg.Init) are
// automatically included.
//
// The caller is responsible for calling provider.Shutdown when done. The
// returned shutdown function is always safe to call even when otelEnabled is
// false (it is a no-op in that case).
//
// Registration of span processors (e.g. sentrypkg.Init) must happen before
// calling NewServeProvider so that the processors are picked up by NewProvider.
func NewServeProvider(ctx context.Context) (provider *Provider, otelEnabled bool, err error) {
	configProvider := config.NewDefaultProvider()
	appConfig := configProvider.GetConfig()

	otelCfg := appConfig.OTEL
	hasRegisteredProcessors := HasRegisteredSpanProcessors()

	handleUnusedEndpoint(&otelCfg)

	if otelCfg.Endpoint == "" && !otelCfg.EnablePrometheusMetricsPath && !hasRegisteredProcessors {
		return nil, false, nil
	}

	telemetryCfg := Config{
		ServiceName:                 "thv-api",
		Endpoint:                    otelCfg.Endpoint,
		TracingEnabled:              otelCfg.TracingEnabled != nil && *otelCfg.TracingEnabled,
		MetricsEnabled:              otelCfg.MetricsEnabled != nil && *otelCfg.MetricsEnabled,
		Insecure:                    otelCfg.Insecure,
		EnablePrometheusMetricsPath: otelCfg.EnablePrometheusMetricsPath,
		EnvironmentVariables:        otelCfg.EnvVars,
	}
	if otelCfg.SamplingRate != 0.0 {
		telemetryCfg.SetSamplingRateFromFloat(otelCfg.SamplingRate)
	}
	if telemetryCfg.SamplingRate == "" {
		telemetryCfg.SamplingRate = "0.05"
	}

	// No OTLP endpoint but registered processors are active (e.g. a Sentry bridge).
	// Force tracing on with 100% OTEL sampling so every span reaches the processors.
	// Each processor applies its own sampling configuration independently.
	// Note: at high RPS with 100% OTEL sampling, the OTEL SDK still constructs
	// every span even if the processor's own rate drops most of them. This is an
	// acceptable trade-off for Sentry-only mode where an external collector is
	// not running. Configure thv config otel set-endpoint to use a real sampler
	// when throughput is a concern.
	if otelCfg.Endpoint == "" && hasRegisteredProcessors {
		telemetryCfg.TracingEnabled = true
		telemetryCfg.SamplingRate = "1.0"
	}

	p, err := NewProvider(ctx, telemetryCfg)
	if err != nil {
		return nil, false, fmt.Errorf("failed to initialize telemetry: %w", err)
	}

	slog.Debug("OTEL provider initialized for thv serve",
		"endpoint", otelCfg.Endpoint,
		"tracing", telemetryCfg.TracingEnabled,
		"metrics", telemetryCfg.MetricsEnabled)

	return p, true, nil
}

// handleUnusedEndpoint clears the OTLP endpoint when both tracing and metrics
// are disabled, so the server can start normally instead of crashing with a
// fatal validation error.
func handleUnusedEndpoint(otelCfg *config.OpenTelemetryConfig) {
	if otelCfg.Endpoint == "" {
		return
	}
	tracingOff := otelCfg.TracingEnabled == nil || !*otelCfg.TracingEnabled
	metricsOff := otelCfg.MetricsEnabled == nil || !*otelCfg.MetricsEnabled
	if tracingOff && metricsOff {
		slog.Warn("OTLP endpoint is configured but tracing and metrics are both disabled; ignoring endpoint",
			"endpoint", otelCfg.Endpoint)
		otelCfg.Endpoint = ""
	}
}
