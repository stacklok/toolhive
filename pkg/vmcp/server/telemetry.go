// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
)

const (
	instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"
)

// monitorBackends decorates the backend client so it records telemetry on each method call.
// It also emits a gauge for the number of backends discovered once, since the number of backends is static.
func monitorBackends(
	ctx context.Context,
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
	backends []vmcp.Backend,
	backendClient vmcp.BackendClient,
) (vmcp.BackendClient, error) {
	meter := meterProvider.Meter(instrumentationName)

	backendCount, err := meter.Int64Gauge(
		"toolhive_vmcp_backends_discovered",
		metric.WithDescription("Number of backends discovered"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend count gauge: %w", err)
	}
	backendCount.Record(ctx, int64(len(backends)))

	requestsTotal, err := meter.Int64Counter(
		"toolhive_vmcp_backend_requests",
		metric.WithDescription("Total number of requests per backend"))
	if err != nil {
		return nil, fmt.Errorf("failed to create requests total counter: %w", err)
	}
	errorsTotal, err := meter.Int64Counter(
		"toolhive_vmcp_backend_errors",
		metric.WithDescription("Total number of errors per backend"))
	if err != nil {
		return nil, fmt.Errorf("failed to create errors total counter: %w", err)
	}
	requestsDuration, err := meter.Float64Histogram(
		"toolhive_vmcp_backend_requests_duration",
		metric.WithDescription("Duration of requests in seconds per backend"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create requests duration histogram: %w", err)
	}

	return telemetryBackendClient{
		backendClient:    backendClient,
		tracer:           tracerProvider.Tracer(instrumentationName),
		requestsTotal:    requestsTotal,
		errorsTotal:      errorsTotal,
		requestsDuration: requestsDuration,
	}, nil
}

type telemetryBackendClient struct {
	backendClient vmcp.BackendClient
	tracer        trace.Tracer

	requestsTotal    metric.Int64Counter
	errorsTotal      metric.Int64Counter
	requestsDuration metric.Float64Histogram
}

var _ vmcp.BackendClient = telemetryBackendClient{}

// record updates the metrics and creates a span for each method on the BackendClient interface.
// It returns a function that should be deferred to record the duration, error, and end the span.
func (t telemetryBackendClient) record(
	ctx context.Context, target *vmcp.BackendTarget, action string, err *error,
) (context.Context, func()) {
	// Create span attributes
	commonAttrs := []attribute.KeyValue{
		attribute.String("target.workload_id", target.WorkloadID),
		attribute.String("target.workload_name", target.WorkloadName),
		attribute.String("target.base_url", target.BaseURL),
		attribute.String("target.transport_type", target.TransportType),
		attribute.String("action", action),
	}

	ctx, span := t.tracer.Start(ctx, "telemetryBackendClient."+action,
		// TODO: Add params and results to the span once we have reusable sanitization functions.
		trace.WithAttributes(commonAttrs...),
	)

	metricAttrs := metric.WithAttributes(commonAttrs...)
	start := time.Now()
	t.requestsTotal.Add(ctx, 1, metricAttrs)

	return ctx, func() {
		duration := time.Since(start)
		t.requestsDuration.Record(ctx, duration.Seconds(), metricAttrs)
		if err != nil && *err != nil {
			t.errorsTotal.Add(ctx, 1, metricAttrs)
			span.RecordError(*err)
			span.SetStatus(codes.Error, (*err).Error())
		}
		span.End()
	}
}

func (t telemetryBackendClient) CallTool(
	ctx context.Context, target *vmcp.BackendTarget, toolName string, arguments map[string]any, meta map[string]any,
) (_ *vmcp.ToolCallResult, retErr error) {
	ctx, done := t.record(ctx, target, "call_tool", &retErr)
	defer done()
	return t.backendClient.CallTool(ctx, target, toolName, arguments, meta)
}

func (t telemetryBackendClient) ReadResource(
	ctx context.Context, target *vmcp.BackendTarget, uri string,
) (_ *vmcp.ResourceReadResult, retErr error) {
	ctx, done := t.record(ctx, target, "read_resource", &retErr)
	defer done()
	return t.backendClient.ReadResource(ctx, target, uri)
}

func (t telemetryBackendClient) GetPrompt(
	ctx context.Context, target *vmcp.BackendTarget, name string, arguments map[string]any,
) (_ *vmcp.PromptGetResult, retErr error) {
	ctx, done := t.record(ctx, target, "get_prompt", &retErr)
	defer done()
	return t.backendClient.GetPrompt(ctx, target, name, arguments)
}

func (t telemetryBackendClient) ListCapabilities(
	ctx context.Context, target *vmcp.BackendTarget,
) (_ *vmcp.CapabilityList, retErr error) {
	ctx, done := t.record(ctx, target, "list_capabilities", &retErr)
	defer done()
	return t.backendClient.ListCapabilities(ctx, target)
}

// monitorWorkflowExecutors decorates workflow executors with telemetry recording.
// It wraps each executor to emit metrics and traces for execution count, duration, and errors.
func monitorWorkflowExecutors(
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
	executors map[string]adapter.WorkflowExecutor,
) (map[string]adapter.WorkflowExecutor, error) {
	if len(executors) == 0 {
		return executors, nil
	}

	meter := meterProvider.Meter(instrumentationName)

	executionsTotal, err := meter.Int64Counter(
		"toolhive_vmcp_workflow_executions",
		metric.WithDescription("Total number of workflow executions"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow executions counter: %w", err)
	}

	errorsTotal, err := meter.Int64Counter(
		"toolhive_vmcp_workflow_errors",
		metric.WithDescription("Total number of workflow execution errors"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow errors counter: %w", err)
	}

	executionDuration, err := meter.Float64Histogram(
		"toolhive_vmcp_workflow_duration",
		metric.WithDescription("Duration of workflow executions in seconds"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create workflow duration histogram: %w", err)
	}

	tracer := tracerProvider.Tracer(instrumentationName)

	monitored := make(map[string]adapter.WorkflowExecutor, len(executors))
	for name, executor := range executors {
		monitored[name] = &telemetryWorkflowExecutor{
			name:              name,
			executor:          executor,
			tracer:            tracer,
			executionsTotal:   executionsTotal,
			errorsTotal:       errorsTotal,
			executionDuration: executionDuration,
		}
	}

	return monitored, nil
}

// telemetryWorkflowExecutor wraps a WorkflowExecutor with telemetry recording.
type telemetryWorkflowExecutor struct {
	name              string
	executor          adapter.WorkflowExecutor
	tracer            trace.Tracer
	executionsTotal   metric.Int64Counter
	errorsTotal       metric.Int64Counter
	executionDuration metric.Float64Histogram
}

var _ adapter.WorkflowExecutor = (*telemetryWorkflowExecutor)(nil)

// ExecuteWorkflow executes the workflow and records telemetry metrics and traces.
func (t *telemetryWorkflowExecutor) ExecuteWorkflow(ctx context.Context, params map[string]any) (*adapter.WorkflowResult, error) {
	commonAttrs := []attribute.KeyValue{
		attribute.String("workflow.name", t.name),
	}

	ctx, span := t.tracer.Start(ctx, "telemetryWorkflowExecutor.ExecuteWorkflow",
		// TODO: Add params and results to the span once we have reusable sanitization functions.
		trace.WithAttributes(commonAttrs...),
	)
	defer span.End()

	metricAttrs := metric.WithAttributes(commonAttrs...)
	start := time.Now()
	t.executionsTotal.Add(ctx, 1, metricAttrs)

	result, err := t.executor.ExecuteWorkflow(ctx, params)

	duration := time.Since(start)
	t.executionDuration.Record(ctx, duration.Seconds(), metricAttrs)

	if err != nil {
		t.errorsTotal.Add(ctx, 1, metricAttrs)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	return result, err
}
