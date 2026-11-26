// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package codemode

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/telemetry"
)

// instrumentationName is the OTel scope shared by every vMCP metric and span,
// so code mode telemetry lands in the same scope as the core, backend and
// optimizer telemetry. It must match pkg/vmcp/core.
const instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// Metric names. They follow the toolhive_vmcp_* naming the other vMCP metrics
// use; the Prometheus exporter adds the _total and _seconds suffixes.
const (
	metricScriptExecutions    = "toolhive_vmcp_script_executions"
	metricScriptDuration      = "toolhive_vmcp_script_duration"
	metricScriptToolCalls     = "toolhive_vmcp_script_tool_calls"
	metricScriptParallelTasks = "toolhive_vmcp_script_parallel_tasks"
)

// Span names, following the <component>.<Operation> form of core.ExecuteWorkflow.
const (
	spanScript   = "codemode.ExecuteToolScript"
	spanToolCall = "codemode.CallTool"
)

// attrStatus is the metric attribute holding the [ScriptOutcome]. It matches
// the status label of the toolhive_mcp_* request metrics.
const attrStatus = "status"

// toolCallBuckets bound the number of inner tool calls per script. Scripts fan
// out with parallel(), so the range reaches into the hundreds.
var toolCallBuckets = []float64{0, 1, 2, 3, 5, 10, 20, 50, 100, 250}

// Telemetry holds the OTel instruments for code mode. Build it once with
// [NewTelemetry] and set it on [Config].Telemetry. A nil *Telemetry records
// nothing and leaves the request context untouched.
type Telemetry struct {
	tracer        trace.Tracer
	executions    metric.Int64Counter
	duration      metric.Float64Histogram
	toolCalls     metric.Int64Histogram
	parallelTasks metric.Int64Counter
}

// NewTelemetry builds the code mode instruments from mp and tp under the vMCP
// instrumentation scope:
//   - toolhive_vmcp_script_executions: counter, attribute status.
//   - toolhive_vmcp_script_duration: histogram in seconds, attribute status.
//   - toolhive_vmcp_script_tool_calls: histogram of inner tool calls started per script, attribute status.
//   - toolhive_vmcp_script_parallel_tasks: counter of callables started by parallel(), attribute status.
//
// status holds one of the four [ScriptOutcome] values. It also creates a
// codemode.ExecuteToolScript span per script and a codemode.CallTool child span
// per inner tool call. A nil provider is replaced by a no-op one.
func NewTelemetry(mp metric.MeterProvider, tp trace.TracerProvider) (*Telemetry, error) {
	if mp == nil {
		mp = metricnoop.NewMeterProvider()
	}
	if tp == nil {
		tp = tracenoop.NewTracerProvider()
	}
	meter := mp.Meter(instrumentationName)

	executions, err := meter.Int64Counter(metricScriptExecutions,
		metric.WithDescription("Total number of code mode script executions"),
	)
	if err != nil {
		return nil, fmt.Errorf("codemode: create %s counter: %w", metricScriptExecutions, err)
	}
	duration, err := meter.Float64Histogram(metricScriptDuration,
		metric.WithDescription("Duration of code mode script executions in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("codemode: create %s histogram: %w", metricScriptDuration, err)
	}
	toolCalls, err := meter.Int64Histogram(metricScriptToolCalls,
		metric.WithDescription("Number of tool calls each code mode script started"),
		metric.WithUnit("{call}"),
		metric.WithExplicitBucketBoundaries(toolCallBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("codemode: create %s histogram: %w", metricScriptToolCalls, err)
	}
	parallelTasks, err := meter.Int64Counter(metricScriptParallelTasks,
		metric.WithDescription("Total number of callables started by parallel() in code mode scripts"),
		metric.WithUnit("{task}"),
	)
	if err != nil {
		return nil, fmt.Errorf("codemode: create %s counter: %w", metricScriptParallelTasks, err)
	}

	return &Telemetry{
		tracer:        tp.Tracer(instrumentationName),
		executions:    executions,
		duration:      duration,
		toolCalls:     toolCalls,
		parallelTasks: parallelTasks,
	}, nil
}

// startScript starts the parent span for one script. With a nil receiver it
// returns ctx unchanged and a nil span.
func (t *Telemetry) startScript(ctx context.Context) (context.Context, trace.Span) {
	if t == nil {
		return ctx, nil
	}
	return t.tracer.Start(ctx, spanScript, trace.WithSpanKind(trace.SpanKindInternal))
}

// startToolCall starts the child span for one inner tool call. ctx must carry
// the script span (the engine passes the script's context to every call,
// including those made under parallel()). With a nil receiver it returns ctx
// unchanged and a nil span.
func (t *Telemetry) startToolCall(ctx context.Context, toolName string) (context.Context, trace.Span) {
	if t == nil {
		return ctx, nil
	}
	return t.tracer.Start(ctx, spanToolCall,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("tool_name", toolName),        // matches backend spans
			attribute.String("gen_ai.tool.name", toolName), // OTel spec
		),
	)
}

// endToolCall ends an inner tool call span, marking it failed when failed is
// true. It does not record the error message, which may carry tool output.
func endToolCall(span trace.Span, failed bool) {
	if span == nil {
		return
	}
	if failed {
		span.SetStatus(codes.Error, "tool call failed")
	}
	span.End()
}

// finishScript records the metrics for one script and ends its span. The
// span's status message is the outcome only: the error text can quote script
// data, so it goes to the debug log instead. A panic is recovered so telemetry
// can never break the script path.
func (t *Telemetry) finishScript(ctx context.Context, span trace.Span, hash string, s ScriptStats) {
	if t == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.WarnContext(ctx, "codemode: recording script telemetry panicked", "panic", r)
		}
	}()

	attrs := metric.WithAttributes(attribute.String(attrStatus, string(s.Outcome)))
	t.executions.Add(ctx, 1, attrs)
	t.duration.Record(ctx, s.Duration.Seconds(), attrs)
	t.toolCalls.Record(ctx, int64(s.ToolCalls), attrs)
	if s.ParallelTasks > 0 {
		t.parallelTasks.Add(ctx, int64(s.ParallelTasks), attrs)
	}

	if span == nil {
		return
	}
	span.SetAttributes(
		attribute.String("script.status", string(s.Outcome)),
		attribute.Int("script.tool_count", s.ToolCalls),
		attribute.Bool("script.parallel_used", s.ParallelCalls > 0),
		attribute.Int("script.parallel_calls", s.ParallelCalls),
		attribute.Int("script.parallel_tasks", s.ParallelTasks),
		attribute.Int64("script.step_count", clampInt64(s.Steps)),
	)
	if hash != "" {
		span.SetAttributes(attribute.String("script.hash", hash))
	}
	if s.Outcome != OutcomeSuccess {
		span.SetStatus(codes.Error, string(s.Outcome))
	}
	span.End()
}

// clampInt64 converts n to int64, saturating at math.MaxInt64.
func clampInt64(n uint64) int64 {
	if n > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(n)
}
