// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/telemetry"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
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
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create requests duration histogram: %w", err)
	}
	clientOperationDuration, err := meter.Float64Histogram(
		"mcp.client.operation.duration",
		metric.WithDescription("Duration of MCP client operations"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create client operation duration histogram: %w", err)
	}

	return telemetryBackendClient{
		backendClient:           backendClient,
		tracer:                  tracerProvider.Tracer(instrumentationName),
		requestsTotal:           requestsTotal,
		errorsTotal:             errorsTotal,
		requestsDuration:        requestsDuration,
		clientOperationDuration: clientOperationDuration,
	}, nil
}

type telemetryBackendClient struct {
	backendClient vmcp.BackendClient
	tracer        trace.Tracer

	requestsTotal           metric.Int64Counter
	errorsTotal             metric.Int64Counter
	requestsDuration        metric.Float64Histogram
	clientOperationDuration metric.Float64Histogram
}

var _ vmcp.BackendClient = telemetryBackendClient{}

// mapActionToMCPMethod maps internal action names to MCP method names per the OTEL MCP spec.
func mapActionToMCPMethod(action string) string {
	switch action {
	case "call_tool":
		return "tools/call"
	case "read_resource":
		return "resources/read"
	case "get_prompt":
		return "prompts/get"
	default:
		return action
	}
}

// mapTransportTypeToNetworkTransport maps MCP transport types to OTEL network.transport values.
func mapTransportTypeToNetworkTransport(transportType string) string {
	switch transportType {
	case string(transporttypes.TransportTypeStdio):
		return "pipe"
	case string(transporttypes.TransportTypeSSE), string(transporttypes.TransportTypeStreamableHTTP):
		return "tcp"
	default:
		return "tcp"
	}
}

// record updates the metrics and creates a span for each method on the BackendClient interface.
// It returns a function that should be deferred to record the duration, error, and end the span.
func (t telemetryBackendClient) record(
	ctx context.Context, target *vmcp.BackendTarget, action string, targetName string, err *error, attrs ...attribute.KeyValue,
) (context.Context, func()) {
	mcpMethod := mapActionToMCPMethod(action)
	networkTransport := mapTransportTypeToNetworkTransport(target.TransportType)

	// Create span name in format: "{mcp.method.name} {target}" or just "{mcp.method.name}" if no target
	spanName := mcpMethod
	if targetName != "" {
		spanName = mcpMethod + " " + targetName
	}

	// Create span attributes (backward compat + spec-required)
	commonAttrs := []attribute.KeyValue{
		// ToolHive-specific attributes (backward compat)
		attribute.String("target.workload_id", target.WorkloadID),
		attribute.String("target.workload_name", target.WorkloadName),
		attribute.String("target.base_url", target.BaseURL),
		attribute.String("target.transport_type", target.TransportType),
		attribute.String("action", action),
		// OTEL MCP spec-required attributes
		attribute.String("mcp.method.name", mcpMethod),
	}

	commonAttrs = append(commonAttrs, attrs...)

	ctx, span := t.tracer.Start(ctx, spanName,
		// TODO: Add params and results to the span once we have reusable sanitization functions.
		trace.WithAttributes(commonAttrs...),
		trace.WithSpanKind(trace.SpanKindClient),
	)

	// Attributes for legacy metrics
	legacyMetricAttrs := metric.WithAttributes(commonAttrs...)

	// Attributes for mcp.client.operation.duration (spec-required)
	specMetricAttrs := metric.WithAttributes(
		attribute.String("mcp.method.name", mcpMethod),
		attribute.String("network.transport", networkTransport),
	)

	start := time.Now()
	t.requestsTotal.Add(ctx, 1, legacyMetricAttrs)

	return ctx, func() {
		duration := time.Since(start)
		t.requestsDuration.Record(ctx, duration.Seconds(), legacyMetricAttrs)

		// Record mcp.client.operation.duration with spec attributes
		if err != nil && *err != nil {
			// Add error.type attribute for spec compliance
			specMetricAttrsWithError := metric.WithAttributes(
				attribute.String("mcp.method.name", mcpMethod),
				attribute.String("network.transport", networkTransport),
				attribute.String("error.type", fmt.Sprintf("%T", *err)),
			)
			t.clientOperationDuration.Record(ctx, duration.Seconds(), specMetricAttrsWithError)

			t.errorsTotal.Add(ctx, 1, legacyMetricAttrs)
			span.RecordError(*err)
			span.SetStatus(codes.Error, (*err).Error())
		} else {
			t.clientOperationDuration.Record(ctx, duration.Seconds(), specMetricAttrs)
		}
		span.End()
	}
}

func (t telemetryBackendClient) CallTool(
	ctx context.Context,
	target *vmcp.BackendTarget,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (_ *vmcp.ToolCallResult, retErr error) {
	attrs := []attribute.KeyValue{
		attribute.String("tool_name", toolName),        // backward compat
		attribute.String("gen_ai.tool.name", toolName), // OTEL spec
	}
	// Check if caller is authenticated (extract from context)
	if caller, _ := auth.IdentityFromContext(ctx); caller != nil && caller.Subject != "" {
		attrs = append(attrs, attribute.Bool("auth.authenticated", true))
	}
	ctx, done := t.record(ctx, target, "call_tool", toolName, &retErr, attrs...)
	defer done()
	return t.backendClient.CallTool(ctx, target, toolName, arguments, meta)
}

func (t telemetryBackendClient) ReadResource(
	ctx context.Context, target *vmcp.BackendTarget, uri string,
) (_ *vmcp.ResourceReadResult, retErr error) {
	// Use empty targetName to avoid unbounded URI cardinality in span names.
	// The URI is captured in span attributes instead.
	attrs := []attribute.KeyValue{
		attribute.String("resource_uri", uri),     // backward compat
		attribute.String("mcp.resource.uri", uri), // OTEL spec
	}
	// Check if caller is authenticated (extract from context)
	if caller, _ := auth.IdentityFromContext(ctx); caller != nil && caller.Subject != "" {
		attrs = append(attrs, attribute.Bool("auth.authenticated", true))
	}
	ctx, done := t.record(ctx, target, "read_resource", "", &retErr, attrs...)
	defer done()
	return t.backendClient.ReadResource(ctx, target, uri)
}

func (t telemetryBackendClient) GetPrompt(
	ctx context.Context, target *vmcp.BackendTarget, name string, arguments map[string]any,
) (_ *vmcp.PromptGetResult, retErr error) {
	attrs := []attribute.KeyValue{
		attribute.String("prompt_name", name),        // backward compat
		attribute.String("gen_ai.prompt.name", name), // OTEL spec
	}
	// Check if caller is authenticated (extract from context)
	if caller, _ := auth.IdentityFromContext(ctx); caller != nil && caller.Subject != "" {
		attrs = append(attrs, attribute.Bool("auth.authenticated", true))
	}
	ctx, done := t.record(ctx, target, "get_prompt", name, &retErr, attrs...)
	defer done()
	return t.backendClient.GetPrompt(ctx, target, name, arguments)
}

func (t telemetryBackendClient) ListCapabilities(
	ctx context.Context, target *vmcp.BackendTarget,
) (_ *vmcp.CapabilityList, retErr error) {
	ctx, done := t.record(ctx, target, "list_capabilities", "", &retErr)
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
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
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

// monitorOptimizer wraps an optimizer factory so that every Optimizer instance
// produced by the factory is decorated with telemetry (metrics + traces).
func monitorOptimizer(
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
	factory func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error),
) (func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error), error) {
	meter := meterProvider.Meter(instrumentationName)

	findToolRequests, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_find_tool_requests",
		metric.WithDescription("Total number of FindTool calls"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool requests counter: %w", err)
	}

	findToolErrors, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_find_tool_errors",
		metric.WithDescription("Total number of FindTool errors"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool errors counter: %w", err)
	}

	findToolDuration, err := meter.Float64Histogram(
		"toolhive_vmcp_optimizer_find_tool_duration",
		metric.WithDescription("Duration of FindTool calls in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool duration histogram: %w", err)
	}

	findToolResults, err := meter.Float64Histogram(
		"toolhive_vmcp_optimizer_find_tool_results",
		metric.WithDescription("Number of tools returned per FindTool call"),
		metric.WithUnit("{tools}"),
		metric.WithExplicitBucketBoundaries(0, 1, 2, 3, 5, 10, 20, 50),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create find_tool results histogram: %w", err)
	}

	tokenSavingsPercent, err := meter.Float64Histogram(
		"toolhive_vmcp_optimizer_token_savings_percent",
		metric.WithDescription("Token savings percentage per FindTool call"),
		metric.WithUnit("%"),
		metric.WithExplicitBucketBoundaries(0, 10, 20, 30, 40, 50, 60, 70, 80, 90, 95, 99, 100),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create token savings histogram: %w", err)
	}

	callToolRequests, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_call_tool_requests",
		metric.WithDescription("Total number of CallTool calls"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool requests counter: %w", err)
	}

	callToolErrors, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_call_tool_errors",
		metric.WithDescription("Total number of CallTool Go errors"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool errors counter: %w", err)
	}

	callToolNotFound, err := meter.Int64Counter(
		"toolhive_vmcp_optimizer_call_tool_not_found",
		metric.WithDescription("Total number of CallTool calls where result.IsError is true"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool not_found counter: %w", err)
	}

	callToolDuration, err := meter.Float64Histogram(
		"toolhive_vmcp_optimizer_call_tool_duration",
		metric.WithDescription("Duration of CallTool calls in seconds"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(telemetry.MCPHistogramBuckets...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create call_tool duration histogram: %w", err)
	}

	tracer := tracerProvider.Tracer(instrumentationName)

	wrapped := func(ctx context.Context, tools []mcpserver.ServerTool) (optimizer.Optimizer, error) {
		opt, err := factory(ctx, tools)
		if err != nil {
			return nil, err
		}
		return &telemetryOptimizer{
			optimizer:           opt,
			tracer:              tracer,
			findToolRequests:    findToolRequests,
			findToolErrors:      findToolErrors,
			findToolDuration:    findToolDuration,
			findToolResults:     findToolResults,
			tokenSavingsPercent: tokenSavingsPercent,
			callToolRequests:    callToolRequests,
			callToolErrors:      callToolErrors,
			callToolNotFound:    callToolNotFound,
			callToolDuration:    callToolDuration,
		}, nil
	}

	return wrapped, nil
}

// telemetryOptimizer wraps an optimizer.Optimizer with telemetry recording.
type telemetryOptimizer struct {
	optimizer optimizer.Optimizer
	tracer    trace.Tracer

	findToolRequests    metric.Int64Counter
	findToolErrors      metric.Int64Counter
	findToolDuration    metric.Float64Histogram
	findToolResults     metric.Float64Histogram
	tokenSavingsPercent metric.Float64Histogram

	callToolRequests metric.Int64Counter
	callToolErrors   metric.Int64Counter
	callToolNotFound metric.Int64Counter
	callToolDuration metric.Float64Histogram
}

var _ optimizer.Optimizer = (*telemetryOptimizer)(nil)

// FindTool delegates to the wrapped optimizer and records metrics and traces.
func (t *telemetryOptimizer) FindTool(ctx context.Context, input optimizer.FindToolInput) (*optimizer.FindToolOutput, error) {
	ctx, span := t.tracer.Start(ctx, "optimizer.FindTool",
		trace.WithAttributes(
			attribute.String("tool_description", input.ToolDescription),
		),
	)
	defer span.End()

	start := time.Now()
	t.findToolRequests.Add(ctx, 1)

	result, err := t.optimizer.FindTool(ctx, input)

	duration := time.Since(start)
	t.findToolDuration.Record(ctx, duration.Seconds())

	if err != nil {
		t.findToolErrors.Add(ctx, 1)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	t.findToolResults.Record(ctx, float64(len(result.Tools)))
	t.tokenSavingsPercent.Record(ctx, result.TokenMetrics.SavingsPercent)

	return result, nil
}

// CallTool delegates to the wrapped optimizer and records metrics and traces.
func (t *telemetryOptimizer) CallTool(ctx context.Context, input optimizer.CallToolInput) (*mcp.CallToolResult, error) {
	toolAttr := attribute.String("tool_name", input.ToolName)

	ctx, span := t.tracer.Start(ctx, "optimizer.CallTool",
		trace.WithAttributes(toolAttr),
	)
	defer span.End()

	metricAttrs := metric.WithAttributes(toolAttr)
	start := time.Now()
	t.callToolRequests.Add(ctx, 1, metricAttrs)

	result, err := t.optimizer.CallTool(ctx, input)

	duration := time.Since(start)
	t.callToolDuration.Record(ctx, duration.Seconds(), metricAttrs)

	if err != nil {
		t.callToolErrors.Add(ctx, 1, metricAttrs)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	if result != nil && result.IsError {
		t.callToolNotFound.Add(ctx, 1, metricAttrs)
	}

	return result, nil
}
