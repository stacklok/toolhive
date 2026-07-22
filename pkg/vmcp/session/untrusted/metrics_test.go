// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newTestMeterProvider returns an SDK meter provider backed by a manual reader
// so tests can collect and assert on recorded measurements.
func newTestMeterProvider(t *testing.T) (metric.MeterProvider, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	return sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), reader
}

// collectMetrics gathers all recorded metrics from the reader into a name→data
// map for assertions.
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	out := map[string]metricdata.Metrics{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m
		}
	}
	return out
}

func TestNewUntrustedMetrics_Registration(t *testing.T) {
	t.Parallel()

	t.Run("instruments register without error on a real provider", func(t *testing.T) {
		t.Parallel()
		mp, reader := newTestMeterProvider(t)
		m, err := newUntrustedMetrics(mp)
		require.NoError(t, err)
		require.NotNil(t, m)

		// Record one of each so the instruments produce data.
		ctx := context.Background()
		m.recordAdmission(ctx, "admitted")
		m.recordPodCounts(ctx, "github-mcp", 3)

		got := collectMetrics(t, reader)
		assert.Contains(t, got, "untrusted_backend_pods")
		assert.Contains(t, got, "untrusted_pod_admissions_total")
	})

	t.Run("nil metrics are a no-op (never panic)", func(t *testing.T) {
		t.Parallel()
		var m *untrustedMetrics
		ctx := context.Background()
		assert.NotPanics(t, func() {
			m.recordAdmission(ctx, "admitted")
			m.recordPodCounts(ctx, "github-mcp", 1)
		})
	})

	t.Run("noop provider registers instruments (metrics disabled path)", func(t *testing.T) {
		t.Parallel()
		m, err := newUntrustedMetrics(noop.NewMeterProvider())
		require.NoError(t, err)
		require.NotNil(t, m)
	})
}

func TestAdmissionResultLabel(t *testing.T) {
	t.Parallel()

	mp, reader := newTestMeterProvider(t)
	m, err := newUntrustedMetrics(mp)
	require.NoError(t, err)
	ctx := context.Background()

	// The admitted path records the literal "admitted" at the call site;
	// admissionResult maps the two error vocabularies.
	m.recordAdmission(ctx, "admitted")
	m.recordAdmission(ctx, admissionResult(ErrQuotaExceeded)) // quota
	m.recordAdmission(ctx, admissionResult(assert.AnError))   // generic error

	got := collectMetrics(t, reader)
	counter, ok := got["untrusted_pod_admissions_total"]
	require.True(t, ok, "counter must be registered")

	sum, ok := counter.Data.(metricdata.Sum[int64])
	require.True(t, ok, "admissions must be a Sum counter")

	results := map[string]int64{}
	for _, dp := range sum.DataPoints {
		for _, attr := range dp.Attributes.ToSlice() {
			if attr.Key == "result" {
				results[attr.Value.AsString()] = dp.Value
			}
		}
	}
	assert.Equal(t, int64(1), results["admitted"])
	assert.Equal(t, int64(1), results["quota_exceeded"])
	assert.Equal(t, int64(1), results["error"])

	// Low-cardinality guard: the only attribute on any datapoint is "result".
	for _, dp := range sum.DataPoints {
		for _, attr := range dp.Attributes.ToSlice() {
			assert.Equal(t, "result", string(attr.Key), "no user/pod/server labels allowed (ADR D11)")
		}
	}
}

func TestReaperRecordsPodGauge(t *testing.T) {
	t.Parallel()

	mp, reader := newTestMeterProvider(t)
	m, err := newUntrustedMetrics(mp)
	require.NoError(t, err)

	// Two servers, one with two pods, one with one, one pod with no name
	// annotation (grouped under "unknown").
	ctx := context.Background()
	m.recordPodCounts(ctx, "github-mcp", 2)
	m.recordPodCounts(ctx, "gitlab-mcp", 1)
	m.recordPodCounts(ctx, "unknown", 1)

	got := collectMetrics(t, reader)
	gauge, ok := got["untrusted_backend_pods"]
	require.True(t, ok)

	g, ok := gauge.Data.(metricdata.Gauge[int64])
	require.True(t, ok, "backend pods must be a gauge")

	counts := map[string]int64{}
	for _, dp := range g.DataPoints {
		for _, attr := range dp.Attributes.ToSlice() {
			if attr.Key == "mcpserver" {
				counts[attr.Value.AsString()] = dp.Value
			}
		}
	}
	assert.Equal(t, int64(2), counts["github-mcp"])
	assert.Equal(t, int64(1), counts["gitlab-mcp"])
	assert.Equal(t, int64(1), counts["unknown"])

	// Low-cardinality guard: only the mcpserver label is present.
	for _, dp := range g.DataPoints {
		for _, attr := range dp.Attributes.ToSlice() {
			assert.Equal(t, "mcpserver", string(attr.Key))
		}
	}
}

// TestReaperMetricsDisabled proves a nil *untrustedMetrics leaves the reaper's
// metrics disabled (the production "no MeterProvider" path).
func TestReaperMetricsDisabled(t *testing.T) {
	t.Parallel()
	store, _ := newTestStore(t)
	k8sClient := fake.NewClientBuilder().WithScheme(newScheme(t)).Build()
	reaper, err := NewReaper(k8sClient, store, ReaperConfig{IdleTTL: time.Minute}, "vmcp-1", nil)
	require.NoError(t, err)
	assert.Nil(t, reaper.metrics)
}
