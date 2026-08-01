// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"log/slog"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// This file holds the dual-emission helpers for legacy (pre-stacklok.*) metric
// names. Whether legacy names are emitted is carried explicitly as the `enabled`
// parameter — sourced from Config.UseLegacyMetrics — rather than through package
// state, matching how every other --otel* setting reaches its consumers.
//
// Each helper returns a working instrument when enabled and a no-op otherwise, so
// callers record into the legacy instrument unconditionally instead of branching
// at every record site. An instrument-creation failure also yields a no-op: a
// legacy alias must never be the reason a process fails to start. Such a failure
// is logged rather than swallowed silently — the user asked for these aliases, so
// an alias that never registers has to be diagnosable from the logs instead of
// looking like the flag is broken.

// LegacyInt64Counter creates a counter under legacyName when enabled, and a no-op
// counter otherwise. The result is always safe to call.
func LegacyInt64Counter(
	meter metric.Meter, enabled bool, legacyName string, opts ...metric.Int64CounterOption,
) metric.Int64Counter {
	if !enabled {
		return noop.Int64Counter{}
	}
	c, err := meter.Int64Counter(legacyName, opts...)
	if err != nil {
		warnLegacyInstrumentFailed(legacyName, err)
		return noop.Int64Counter{}
	}
	return c
}

// warnLegacyInstrumentFailed reports an alias that could not be registered. The
// caller still gets a no-op instrument, so this is the only signal that a
// requested legacy name will be missing from the scrape.
func warnLegacyInstrumentFailed(legacyName string, err error) {
	slog.Warn("legacy metric alias could not be created and will not be emitted",
		"metric", legacyName, "error", err)
}

// LegacyFloat64Histogram creates a histogram under legacyName when enabled, and a
// no-op histogram otherwise. See LegacyInt64Counter.
func LegacyFloat64Histogram(
	meter metric.Meter, enabled bool, legacyName string, opts ...metric.Float64HistogramOption,
) metric.Float64Histogram {
	if !enabled {
		return noop.Float64Histogram{}
	}
	h, err := meter.Float64Histogram(legacyName, opts...)
	if err != nil {
		warnLegacyInstrumentFailed(legacyName, err)
		return noop.Float64Histogram{}
	}
	return h
}

// LegacyInt64UpDownCounter creates an up/down counter under legacyName when
// enabled, and a no-op otherwise. See LegacyInt64Counter.
func LegacyInt64UpDownCounter(
	meter metric.Meter, enabled bool, legacyName string, opts ...metric.Int64UpDownCounterOption,
) metric.Int64UpDownCounter {
	if !enabled {
		return noop.Int64UpDownCounter{}
	}
	c, err := meter.Int64UpDownCounter(legacyName, opts...)
	if err != nil {
		warnLegacyInstrumentFailed(legacyName, err)
		return noop.Int64UpDownCounter{}
	}
	return c
}
