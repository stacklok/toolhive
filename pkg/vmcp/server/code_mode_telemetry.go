// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp/codemode"
)

// metricCodeModeEnabled reports whether code mode is enabled on this vMCP
// server, so an operator can count the servers that have opted in.
const metricCodeModeEnabled = "toolhive_vmcp_code_mode_enabled"

// codeModeInstrumentationName must match the scope of every other vMCP metric.
const codeModeInstrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// withCodeModeTelemetry returns cfg with OTel script metrics and spans
// attached, when a telemetry provider is configured. It copies cfg rather than
// changing the caller's value, and keeps any OnScript observer the caller set.
// A nil cfg (code mode off) stays nil; a nil provider adds nothing.
func withCodeModeTelemetry(cfg *codemode.Config, provider *telemetry.Provider) (*codemode.Config, error) {
	if cfg == nil || provider == nil {
		return cfg, nil
	}
	tel, err := codemode.NewTelemetry(provider.MeterProvider(), provider.TracerProvider())
	if err != nil {
		return nil, fmt.Errorf("failed to create code mode telemetry: %w", err)
	}
	out := *cfg
	out.Telemetry = tel
	return &out, nil
}

// registerCodeModeEnabledGauge registers toolhive_vmcp_code_mode_enabled on
// the provider's meter: 1 when code mode is enabled on the server named
// serverName, 0 when it is not. The value is fixed for the life of the
// process, because a code mode change restarts the vMCP server. A nil
// provider registers nothing.
func registerCodeModeEnabledGauge(provider *telemetry.Provider, serverName string, enabled bool) error {
	if provider == nil || provider.MeterProvider() == nil {
		return nil
	}
	value := int64(0)
	if enabled {
		value = 1
	}
	attrs := metric.WithAttributes(attribute.String("server", serverName))
	_, err := provider.MeterProvider().Meter(codeModeInstrumentationName).Int64ObservableGauge(
		metricCodeModeEnabled,
		metric.WithDescription("Whether code mode is enabled on this vMCP server (1) or not (0)"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(value, attrs)
			return nil
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to create %s gauge: %w", metricCodeModeEnabled, err)
	}
	return nil
}
