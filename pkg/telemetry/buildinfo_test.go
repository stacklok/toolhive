// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/telemetry/providers/prometheus"
)

func TestBuildInfoAndConstLabelsAppear(t *testing.T) {
	t.Parallel()
	reader, handler, err := prometheus.NewReader(prometheus.Config{EnableMetricsPath: true})
	require.NoError(t, err)

	res, err := resource.New(context.Background(), resource.WithAttributes(
		attribute.String("stacklok.component", "toolhive"),
		attribute.String("stacklok.product", "stacklok-platform"),
	))
	require.NoError(t, err)

	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))

	// Registered directly rather than through NewHTTPMiddleware: build_info is
	// guarded by a process-wide sync.Once, so if any earlier test in this package
	// constructed a middleware first, the guard is already spent and this
	// provider would never see the gauge.
	registerBuildInfoNow(mp.Meter(instrumentationName))

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)
	body := rw.Body.String()

	// Anchored at line start so the EXPORTED name is pinned, not merely present as
	// a substring: RegisterBuildInfo sets WithUnit("1"), and the Prometheus
	// translator appends _ratio to any gauge with unit "1", so a bare
	// Contains("stacklok_build_info") is satisfied by stacklok_build_info_ratio and
	// cannot detect the discrepancy.
	require.Regexp(t, `(?m)^stacklok_build_info(_ratio)?\{`, body,
		"build_info metric should be exported")

	// build_info always observes 1, so its labels are the entire point of the
	// metric — a series with no version/commit carries no information.
	require.Regexp(t, `(?m)^stacklok_build_info(_ratio)?\{[^}]*component="toolhive"`, body,
		"build_info must identify the component")
	require.Regexp(t, `(?m)^stacklok_build_info(_ratio)?\{[^}]*version="[^"]+"`, body,
		"build_info must carry a non-empty version")
	require.Regexp(t, `(?m)^stacklok_build_info(_ratio)?\{[^}]*commit="[^"]+"`, body,
		"build_info must carry a non-empty commit")

	require.Contains(t, body, `stacklok_component="toolhive"`, "component const label should be promoted")
	require.Contains(t, body, `stacklok_product="stacklok-platform"`, "product const label should be promoted")
}

// TestRegisterBuildInfoIsOncePerProcess pins the guard that makes
// docs/observability.md's "registered once per process" claim true. build_info is
// process identity, and RegisterBuildInfo returns no handle to release its
// observable callback, so a second registration would permanently attach a
// duplicate callback observing the same attribute set.
func TestRegisterBuildInfoIsOncePerProcess(t *testing.T) {
	t.Parallel()

	reader, handler, err := prometheus.NewReader(prometheus.Config{EnableMetricsPath: true})
	require.NoError(t, err)
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	// Two middlewares over the same provider: the second must not add a callback.
	_ = NewHTTPMiddleware(Config{}, tracenoop.NewTracerProvider(), mp, "github", "stdio")
	_ = NewHTTPMiddleware(Config{}, tracenoop.NewTracerProvider(), mp, "fetch", "stdio")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)

	// Exactly one, not "at most one": LessOrEqual(…, 1) is satisfied by zero, so it
	// passes both when the gauge is correctly deduped and when it is missing
	// entirely — which is what a process-wide guard would do to this provider.
	series := regexp.MustCompile(`(?m)^stacklok_build_info(_ratio)?\{`).
		FindAllString(rw.Body.String(), -1)
	require.Len(t, series, 1,
		"build_info must be registered exactly once per MeterProvider, got %d series", len(series))
}

// TestRegisterBuildInfoIsPerProviderNotPerProcess pins the dedup key. A
// process-wide guard leaves the second provider's /metrics endpoint with no
// build_info at all, so any dashboard joining on it returns empty for that scrape
// target — an absence that produces no error and no series.
func TestRegisterBuildInfoIsPerProviderNotPerProcess(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"first", "second"} {
		reader, handler, err := prometheus.NewReader(prometheus.Config{EnableMetricsPath: true})
		require.NoError(t, err)
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

		_ = NewHTTPMiddleware(Config{}, tracenoop.NewTracerProvider(), mp, "github", "stdio")

		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, req)

		series := regexp.MustCompile(`(?m)^stacklok_build_info(_ratio)?\{`).
			FindAllString(rw.Body.String(), -1)
		require.Len(t, series, 1,
			"%s provider must export build_info exactly once; each provider owns its own "+
				"reader and /metrics endpoint, so registration cannot be process-wide", name)
	}
}
