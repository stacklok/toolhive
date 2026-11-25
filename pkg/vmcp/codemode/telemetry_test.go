// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/script"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

// callScript runs src through d's execute_tool_script and fails on a transport error.
func callScript(ctx context.Context, t *testing.T, d core.VMCP, src string) *vmcp.ToolCallResult {
	t.Helper()
	res, err := d.CallTool(ctx, nil, script.ExecuteToolScriptName, map[string]any{"script": src}, nil)
	require.NoError(t, err)
	return res
}

func TestTelemetry_RecordsMetrics(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	tel, err := NewTelemetry(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), nil)
	require.NoError(t, err)

	d := NewDecorator(echoBackend(), &Config{Telemetry: tel, StepLimit: 200})
	callScript(context.Background(), t, d, `return echo(value="a")`)
	callScript(context.Background(), t, d, `return parallel([status, status, lambda: echo(value="b")])`)
	callScript(context.Background(), t, d, `return 1 // 0`)
	callScript(context.Background(), t, d, "x = 0\nfor i in range(10000):\n    x = x + 1\nreturn x")

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	require.Len(t, rm.ScopeMetrics, 1)
	assert.Equal(t, instrumentationName, rm.ScopeMetrics[0].Scope.Name)

	byName := map[string]metricdata.Metrics{}
	for _, m := range rm.ScopeMetrics[0].Metrics {
		byName[m.Name] = m
	}
	status := func(o ScriptOutcome) attribute.Set { return attribute.NewSet(attribute.String("status", string(o))) }

	executions, ok := byName[metricScriptExecutions].Data.(metricdata.Sum[int64])
	require.True(t, ok)
	assert.True(t, executions.IsMonotonic)
	counts := map[attribute.Set]int64{}
	for _, dp := range executions.DataPoints {
		counts[dp.Attributes] = dp.Value
	}
	assert.Equal(t, map[attribute.Set]int64{
		status(OutcomeSuccess):   2,
		status(OutcomeError):     1,
		status(OutcomeStepLimit): 1,
	}, counts)

	duration, ok := byName[metricScriptDuration].Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	assert.Equal(t, "s", byName[metricScriptDuration].Unit)
	var durationCount uint64
	for _, dp := range duration.DataPoints {
		durationCount += dp.Count
	}
	assert.Equal(t, uint64(4), durationCount)

	toolCalls, ok := byName[metricScriptToolCalls].Data.(metricdata.Histogram[int64])
	require.True(t, ok)
	sums := map[attribute.Set]int64{}
	for _, dp := range toolCalls.DataPoints {
		sums[dp.Attributes] = dp.Sum
	}
	assert.Equal(t, int64(4), sums[status(OutcomeSuccess)], "1 sequential call + 3 under parallel()")

	parallel, ok := byName[metricScriptParallelTasks].Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, parallel.DataPoints, 1)
	assert.Equal(t, int64(3), parallel.DataPoints[0].Value)
	assert.Equal(t, status(OutcomeSuccess), parallel.DataPoints[0].Attributes)
}

func TestTelemetry_SpanParenting(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	tel, err := NewTelemetry(nil, tp)
	require.NoError(t, err)

	// The backend sees the inner call span as the current span, so its own
	// spans (backend client telemetry) nest under it.
	inner := echoBackend()
	base := inner.callTool
	backendParents := make(chan trace.SpanContext, 8)
	inner.callTool = func(ctx context.Context, id *auth.Identity, name string, args, meta map[string]any) (*vmcp.ToolCallResult, error) {
		backendParents <- trace.SpanContextFromContext(ctx)
		return base(ctx, id, name, args, meta)
	}

	ctx, root := tp.Tracer("test").Start(context.Background(), "request")
	d := NewDecorator(inner, &Config{Telemetry: tel})
	res := callScript(ctx, t, d, `
first = status()
rest = parallel([lambda i=i: echo(value=str(i)) for i in range(3)])
return [first, rest]`)
	require.False(t, res.IsError)
	root.End()
	close(backendParents)

	var scriptSpan sdktrace.ReadOnlySpan
	var callSpans []sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		switch s.Name() {
		case spanScript:
			scriptSpan = s
		case spanToolCall:
			callSpans = append(callSpans, s)
		}
	}
	require.NotNil(t, scriptSpan)
	assert.Equal(t, root.SpanContext().SpanID(), scriptSpan.Parent().SpanID(), "script span is a child of the request span")

	attrs := map[attribute.Key]attribute.Value{}
	for _, kv := range scriptSpan.Attributes() {
		attrs[kv.Key] = kv.Value
	}
	assert.Equal(t, int64(4), attrs["script.tool_count"].AsInt64())
	assert.True(t, attrs["script.parallel_used"].AsBool())
	assert.Equal(t, int64(3), attrs["script.parallel_tasks"].AsInt64())
	assert.Positive(t, attrs["script.step_count"].AsInt64())
	assert.Equal(t, "success", attrs["script.status"].AsString())
	assert.Len(t, attrs["script.hash"].AsString(), 16)
	assert.Equal(t, codes.Unset, scriptSpan.Status().Code)

	require.Len(t, callSpans, 4, "one child span per inner call, including those under parallel()")
	callSpanIDs := map[trace.SpanID]bool{}
	for _, s := range callSpans {
		assert.Equal(t, scriptSpan.SpanContext().SpanID(), s.Parent().SpanID())
		assert.Equal(t, scriptSpan.SpanContext().TraceID(), s.SpanContext().TraceID())
		callSpanIDs[s.SpanContext().SpanID()] = true
	}
	for sc := range backendParents {
		assert.True(t, callSpanIDs[sc.SpanID()], "backend call must run under its inner call span")
	}
}

func TestTelemetry_FailedScriptSpanStatus(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	tel, err := NewTelemetry(nil, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	require.NoError(t, err)

	d := NewDecorator(errorBackend(assert.AnError), &Config{Telemetry: tel})
	res := callScript(context.Background(), t, d, `return fail()`)
	require.True(t, res.IsError)

	for _, s := range recorder.Ended() {
		assert.Equal(t, codes.Error, s.Status().Code, s.Name())
		assert.NotContains(t, s.Status().Description, assert.AnError.Error(), "span status must not carry error text")
	}
	assert.Len(t, recorder.Ended(), 2)
}

func TestTelemetry_NilLeavesContextUntouched(t *testing.T) {
	t.Parallel()

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, root := tp.Tracer("test").Start(context.Background(), "request")
	defer root.End()

	var seen trace.SpanContext
	inner := echoBackend()
	base := inner.callTool
	inner.callTool = func(ctx context.Context, id *auth.Identity, name string, args, meta map[string]any) (*vmcp.ToolCallResult, error) {
		seen = trace.SpanContextFromContext(ctx)
		return base(ctx, id, name, args, meta)
	}

	d := NewDecorator(inner, &Config{})
	res := callScript(ctx, t, d, `return status()`)
	require.False(t, res.IsError)
	assert.Equal(t, root.SpanContext(), seen, "without Telemetry the caller's span stays current")
	assert.Empty(t, recorder.Ended())
}

func TestNewTelemetry_NilProviders(t *testing.T) {
	t.Parallel()

	tel, err := NewTelemetry(nil, nil)
	require.NoError(t, err)
	d := NewDecorator(echoBackend(), &Config{Telemetry: tel})
	res := callScript(context.Background(), t, d, `return parallel([status])`)
	assert.False(t, res.IsError)
}

// TestCallTool_LogsOmitScriptBody swaps the default logger, so it must not run
// in parallel with other tests.
//
//nolint:paralleltest // mutates the process-wide default slog logger
func TestCallTool_LogsOmitScriptBody(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const marker = "sk-very-secret-marker"
	d := NewDecorator(echoBackend(), nil)
	callScript(context.Background(), t, d, `return echo(value="`+marker+`")`)
	callScript(context.Background(), t, d, `x = "`+marker+`"`+"\nreturn 1 // 0")

	out := buf.String()
	assert.NotContains(t, out, marker, "script content must never be logged")
	assert.Contains(t, out, `"msg":"codemode: script started"`)
	assert.Contains(t, out, `"msg":"codemode: script finished"`)
	assert.Contains(t, out, `"script_hash":"`)
	assert.Contains(t, out, `"session_id":""`)
	assert.Contains(t, out, `"status":"success"`)
	assert.Contains(t, out, `"status":"error"`)
	assert.Contains(t, out, `"tool_calls":1`)
	assert.Contains(t, out, `"error":"script execution failed`)
	assert.Contains(t, out, `"duration":`)
}
