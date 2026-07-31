// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// legacyProxyMetrics are the deleted/renamed names re-emitted for one release so
// operators have an overlap window in which old and new queries both return data.
var legacyProxyMetrics = []string{
	"toolhive_mcp_requests",
	"toolhive_mcp_request_duration",
	"toolhive_mcp_tool_calls",
	"toolhive_mcp_active_connections",
}

// collectedNames returns every metric name present in a collection.
func collectedNames(t *testing.T, reader sdkmetric.Reader) map[string]bool {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	names := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			names[m.Name] = true
		}
	}
	return names
}

// driveToolCall sends one tools/call request through the middleware so every
// request-path instrument records at least one observation.
func driveToolCall(t *testing.T, cfg Config) sdkmetric.Reader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	mw := NewHTTPMiddleware(cfg, tracenoop.NewTracerProvider(), mp, "github", "streamable-http")

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/messages", nil)
	req = req.WithContext(context.WithValue(req.Context(), mcpparser.MCPRequestContextKey,
		&mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: "github_search"}))
	handler.ServeHTTP(httptest.NewRecorder(), req)

	return reader
}

// TestLegacyMetrics_DualEmittedWhenEnabled pins the overlap window: with the flag
// on, both the legacy and current names must be present in the same scrape, so a
// user can run old and new queries side by side and confirm they agree before
// cutting over.
func TestLegacyMetrics_DualEmittedWhenEnabled(t *testing.T) {
	t.Parallel()

	names := collectedNames(t, driveToolCall(t, Config{UseLegacyMetrics: true}))

	for _, legacy := range legacyProxyMetrics {
		assert.True(t, names[legacy], "legacy metric %q must be emitted when UseLegacyMetrics is set", legacy)
	}

	// The current names must still be emitted — this is dual emission, not a revert.
	for _, current := range []string{
		"mcp.server.operation.duration",
		"http.server.request.duration",
		"stacklok.toolhive.proxy.active_connections",
	} {
		assert.True(t, names[current], "current metric %q must still be emitted", current)
	}
}

// TestLegacyMetrics_AbsentWhenDisabled proves the flag actually gates emission,
// so flipping it off in a later release is a real removal rather than a no-op.
func TestLegacyMetrics_AbsentWhenDisabled(t *testing.T) {
	t.Parallel()

	names := collectedNames(t, driveToolCall(t, Config{UseLegacyMetrics: false}))

	for _, legacy := range legacyProxyMetrics {
		assert.False(t, names[legacy], "legacy metric %q must NOT be emitted when UseLegacyMetrics is unset", legacy)
	}

	assert.True(t, names["mcp.server.operation.duration"],
		"disabling legacy metrics must not affect the current names")
}
