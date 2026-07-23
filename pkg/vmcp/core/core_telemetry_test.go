// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/vmcp/composer"
)

// stubComposer is already declared in core_calls_test.go (same package).
// Its fields are: result *composer.WorkflowResult, err error.

// newTestInstruments creates a workflowInstruments backed by an in-memory OTEL SDK
// and returns the instruments plus a ManualReader for metric assertions.
// The metric names match production names exactly so the assertions mirror what
// Prometheus would expose.
func newTestInstruments(t *testing.T) (*workflowInstruments, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tp := tracesdk.NewTracerProvider()
	meter := mp.Meter(instrumentationName)

	executions, err := meter.Int64Counter("stacklok.vmcp.composite_tool.executions")
	require.NoError(t, err)
	duration, err := meter.Float64Histogram("stacklok.vmcp.composite_tool.duration")
	require.NoError(t, err)

	return &workflowInstruments{
		tracer:            tp.Tracer(instrumentationName),
		executionsTotal:   executions,
		executionDuration: duration,
	}, reader
}

// collectMetrics gathers all metrics from the reader into a snapshot.
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	return rm
}

// findMetricByName returns the first metric with the given name, or nil.
func findMetricByName(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

// int64CounterValueForOutcome sums the data points of an int64 counter whose
// "outcome" attribute equals want. Returns 0 if no matching point exists.
func int64CounterValueForOutcome(m *metricdata.Metrics, want string) int64 {
	if m == nil {
		return 0
	}
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return 0
	}
	var total int64
	for _, dp := range s.DataPoints {
		if v, present := dp.Attributes.Value("outcome"); present && v.AsString() == want {
			total += dp.Value
		}
	}
	return total
}

func float64HistogramCount(m *metricdata.Metrics) uint64 {
	if m == nil {
		return 0
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		return 0
	}
	var total uint64
	for _, dp := range h.DataPoints {
		total += dp.Count
	}
	return total
}

// TestTelemetryComposer_Success verifies that on a successful ExecuteWorkflow call:
// - the merged executions counter increments by 1 with outcome="success"
// - the same counter records nothing under outcome="error"
// - the duration histogram records 1 observation
// - the result from the inner composer is returned unchanged
func TestTelemetryComposer_Success(t *testing.T) {
	t.Parallel()

	instruments, reader := newTestInstruments(t)
	want := &composer.WorkflowResult{Output: map[string]any{"key": "val"}}
	tc := &telemetryComposer{
		base:        stubComposer{result: want},
		instruments: instruments,
	}

	def := &composer.WorkflowDefinition{Name: "test-workflow"}
	got, err := tc.ExecuteWorkflow(context.Background(), def, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	rm := collectMetrics(t, reader)
	execs := findMetricByName(rm, "stacklok.vmcp.composite_tool.executions")
	assert.Equal(t, int64(1), int64CounterValueForOutcome(execs, "success"),
		`executions counter must increment with outcome="success"`)
	assert.Equal(t, int64(0), int64CounterValueForOutcome(execs, "error"),
		`executions counter must record nothing under outcome="error" on success`)
	assert.Nil(t, findMetricByName(rm, "stacklok.vmcp.composite_tool.errors"),
		"the split _errors counter must no longer exist")
	assert.Equal(t, uint64(1), float64HistogramCount(findMetricByName(rm, "stacklok.vmcp.composite_tool.duration")),
		"duration histogram must record exactly one observation")
}

// TestTelemetryComposer_Error verifies that on a failed ExecuteWorkflow call:
// - the merged executions counter increments by 1 with outcome="error"
// - the same counter records nothing under outcome="success"
// - the duration histogram records 1 observation
// - the error from the inner composer is propagated
func TestTelemetryComposer_Error(t *testing.T) {
	t.Parallel()

	instruments, reader := newTestInstruments(t)
	boom := errors.New("backend exploded")
	tc := &telemetryComposer{
		base:        stubComposer{err: boom},
		instruments: instruments,
	}

	def := &composer.WorkflowDefinition{Name: "failing-workflow"}
	_, err := tc.ExecuteWorkflow(context.Background(), def, nil)
	require.ErrorIs(t, err, boom)

	rm := collectMetrics(t, reader)
	execs := findMetricByName(rm, "stacklok.vmcp.composite_tool.executions")
	assert.Equal(t, int64(1), int64CounterValueForOutcome(execs, "error"),
		`executions counter must increment with outcome="error" on failure`)
	assert.Equal(t, int64(0), int64CounterValueForOutcome(execs, "success"),
		`executions counter must record nothing under outcome="success" on failure`)
	assert.Nil(t, findMetricByName(rm, "stacklok.vmcp.composite_tool.errors"),
		"the split _errors counter must no longer exist")
	assert.Equal(t, uint64(1), float64HistogramCount(findMetricByName(rm, "stacklok.vmcp.composite_tool.duration")),
		"duration histogram must record one observation even on failure")
}

// TestTelemetryComposer_DelegatesNonExecuteMethods verifies that ValidateWorkflow,
// GetWorkflowStatus, and CancelWorkflow delegate to the base without instrumentation.
func TestTelemetryComposer_DelegatesNonExecuteMethods(t *testing.T) {
	t.Parallel()

	tc := &telemetryComposer{
		base:        stubComposer{},
		instruments: &workflowInstruments{tracer: tracenoop.Tracer{}},
	}

	require.NoError(t, tc.ValidateWorkflow(context.Background(), &composer.WorkflowDefinition{}))
	_, err := tc.GetWorkflowStatus(context.Background(), "any-id")
	require.NoError(t, err)
	require.NoError(t, tc.CancelWorkflow(context.Background(), "any-id"))
}
