// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	_ = NewHTTPMiddleware(Config{}, tracenoop.NewTracerProvider(), mp, "github", "stdio")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)
	body := rw.Body.String()

	require.Contains(t, body, "stacklok_build_info", "build_info metric should be exported")
	require.Contains(t, body, `stacklok_component="toolhive"`, "component const label should be promoted")
	require.Contains(t, body, `stacklok_product="stacklok-platform"`, "product const label should be promoted")
}
