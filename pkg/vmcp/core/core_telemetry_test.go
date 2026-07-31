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

	coremetrics "github.com/stacklok/toolhive-core/telemetry/metrics"
	"github.com/stacklok/toolhive/pkg/telemetry"
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

// counterValueForOutcome sums the data points of an int64 counter whose
// coremetrics.LabelOutcome attribute equals want. Returns 0 if no matching
// point exists.
func counterValueForOutcome(m *metricdata.Metrics, want string) int64 {
	if m == nil {
		return 0
	}
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return 0
	}
	var total int64
	for _, dp := range s.DataPoints {
		if v, present := dp.Attributes.Value(coremetrics.LabelOutcome); present && v.AsString() == want {
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
	assert.Equal(t, int64(1), counterValueForOutcome(execs, "success"),
		`executions counter must increment with outcome="success"`)
	assert.Equal(t, int64(0), counterValueForOutcome(execs, "error"),
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
	assert.Equal(t, int64(1), counterValueForOutcome(execs, "error"),
		`executions counter must increment with outcome="error" on failure`)
	assert.Equal(t, int64(0), counterValueForOutcome(execs, "success"),
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

// TestWorkflowInstruments_LegacyDualEmission pins the overlap window for the
// composite-tool renames: with legacy emission on, each metric appears under both
// its stacklok.* name and its pre-rename toolhive_vmcp_workflow_* name, and the
// errors counter — merged into the outcome label — comes back as a standalone
// series so an existing alert on it keeps firing. With it off, only the current
// names are emitted.
func TestWorkflowInstruments_LegacyDualEmission(t *testing.T) {
	t.Parallel()

	legacyNames := []string{
		"toolhive_vmcp_workflow_executions",
		"toolhive_vmcp_workflow_errors",
		"toolhive_vmcp_workflow_duration",
	}
	currentNames := []string{
		"stacklok.vmcp.composite_tool.executions",
		"stacklok.vmcp.composite_tool.duration",
	}

	for _, legacyEnabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[legacyEnabled], func(t *testing.T) {
			t.Parallel()

			reader := sdkmetric.NewManualReader()
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			meter := mp.Meter(instrumentationName)

			executions, err := meter.Int64Counter("stacklok.vmcp.composite_tool.executions")
			require.NoError(t, err)
			duration, err := meter.Float64Histogram("stacklok.vmcp.composite_tool.duration")
			require.NoError(t, err)

			instruments := &workflowInstruments{
				tracer:            tracesdk.NewTracerProvider().Tracer(instrumentationName),
				executionsTotal:   executions,
				executionDuration: duration,
				legacyExecutions: telemetry.LegacyInt64Counter(meter, legacyEnabled,
					"toolhive_vmcp_workflow_executions"),
				legacyErrors: telemetry.LegacyInt64Counter(meter, legacyEnabled,
					"toolhive_vmcp_workflow_errors"),
				legacyDuration: telemetry.LegacyFloat64Histogram(meter, legacyEnabled,
					"toolhive_vmcp_workflow_duration"),
			}

			// A failing workflow exercises executions, duration and errors at once.
			tc := &telemetryComposer{
				base:        stubComposer{err: errors.New("boom")},
				instruments: instruments,
			}
			_, err = tc.ExecuteWorkflow(context.Background(), &composer.WorkflowDefinition{Name: "wf"}, nil)
			require.Error(t, err)

			rm := collectMetrics(t, reader)
			for _, name := range currentNames {
				assert.NotNil(t, findMetricByName(rm, name), "current metric %q must always be emitted", name)
			}
			for _, name := range legacyNames {
				got := findMetricByName(rm, name)
				if legacyEnabled {
					assert.NotNil(t, got, "legacy metric %q must be dual-emitted", name)
					continue
				}
				assert.Nil(t, got, "legacy metric %q must be suppressed", name)
			}

			// The legacy series must carry the pre-rename workflow.name label key.
			if legacyEnabled {
				m := findMetricByName(rm, "toolhive_vmcp_workflow_executions")
				require.NotNil(t, m)
				sum, ok := m.Data.(metricdata.Sum[int64])
				require.True(t, ok)
				require.Len(t, sum.DataPoints, 1)
				v, present := sum.DataPoints[0].Attributes.Value("workflow.name")
				require.True(t, present, "legacy series must carry workflow.name")
				assert.Equal(t, "wf", v.AsString())
			}
		})
	}
}
