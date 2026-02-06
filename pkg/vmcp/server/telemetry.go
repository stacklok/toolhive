// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/telemetry"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/adapter"
)

const (
	instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"
)

// Standard MCP OpenTelemetry attribute keys.
var (
	attrMCPMethodName       = attribute.Key("mcp.method.name")
	attrMCPProtocolVersion  = attribute.Key("mcp.protocol.version")
	attrGenAIToolName       = attribute.Key("gen_ai.tool.name")
	attrGenAIPromptName     = attribute.Key("gen_ai.prompt.name")
	attrGenAIOperationName  = attribute.Key("gen_ai.operation.name")
	attrNetworkTransport    = attribute.Key("network.transport")
	attrNetworkProtocolName = attribute.Key("network.protocol.name")
	attrServerAddress       = attribute.Key("server.address")
	attrServerPort          = attribute.Key("server.port")
	attrErrorType           = attribute.Key("error.type")
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
	clientOperationDuration, err := meter.Float64Histogram(
		"mcp.client.operation.duration",
		metric.WithDescription("MCP client operation duration"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(telemetry.MCPOperationDurationBuckets...),
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

// record updates the metrics and creates a CLIENT span for each method on the BackendClient interface.
// mcpMethod is the MCP method name (e.g. "tools/call"), spanTarget is the optional target suffix
// for the span name (e.g. tool name), and extraAttrs are additional method-specific attributes.
// It returns a function that should be deferred to record the duration, error, and end the span.
func (t telemetryBackendClient) record(
	ctx context.Context,
	target *vmcp.BackendTarget,
	mcpMethod string,
	spanTarget string,
	extraAttrs []attribute.KeyValue,
	err *error,
) (context.Context, func()) {
	// Build span name per MCP OTEL spec: "{mcp.method.name} {target}" or just "{mcp.method.name}"
	spanName := mcpMethod
	if spanTarget != "" {
		spanName = mcpMethod + " " + spanTarget
	}

	// Resolve network attributes from the target
	serverAddr, serverPort := resolveServerAddrPort(target)

	// Standard MCP OTEL attributes
	standardAttrs := []attribute.KeyValue{
		attrMCPMethodName.String(mcpMethod),
		attrMCPProtocolVersion.String("2025-06-18"),
		attrNetworkTransport.String("tcp"),
		attrNetworkProtocolName.String("http"),
		attrServerAddress.String(serverAddr),
		attrServerPort.Int(serverPort),
	}

	// Custom ToolHive attributes (preserved for backward compatibility)
	customAttrs := []attribute.KeyValue{
		attribute.String("target.workload_id", target.WorkloadID),
		attribute.String("target.workload_name", target.WorkloadName),
		attribute.String("target.base_url", target.BaseURL),
		attribute.String("target.transport_type", target.TransportType),
	}

	allAttrs := make([]attribute.KeyValue, 0, len(standardAttrs)+len(customAttrs)+len(extraAttrs))
	allAttrs = append(allAttrs, standardAttrs...)
	allAttrs = append(allAttrs, customAttrs...)
	allAttrs = append(allAttrs, extraAttrs...)

	ctx, span := t.tracer.Start(ctx, spanName,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(allAttrs...),
	)

	metricAttrs := metric.WithAttributes(allAttrs...)
	start := time.Now()
	t.requestsTotal.Add(ctx, 1, metricAttrs)

	return ctx, func() {
		duration := time.Since(start)
		t.requestsDuration.Record(ctx, duration.Seconds(), metricAttrs)
		t.clientOperationDuration.Record(ctx, duration.Seconds(), metricAttrs)
		if err != nil && *err != nil {
			t.errorsTotal.Add(ctx, 1, metricAttrs)
			span.RecordError(*err)
			span.SetAttributes(attrErrorType.String((*err).Error()))
			span.SetStatus(codes.Error, (*err).Error())
		}
		span.End()
	}
}

// CallTool records CLIENT span for tools/call with gen_ai.tool.name attribute.
func (t telemetryBackendClient) CallTool(
	ctx context.Context, target *vmcp.BackendTarget, toolName string, arguments map[string]any, meta map[string]any,
) (_ *vmcp.ToolCallResult, retErr error) {
	extraAttrs := []attribute.KeyValue{
		attrGenAIToolName.String(toolName),
		attrGenAIOperationName.String("execute_tool"),
	}
	ctx, done := t.record(ctx, target, "tools/call", toolName, extraAttrs, &retErr)
	defer done()
	return t.backendClient.CallTool(ctx, target, toolName, arguments, meta)
}

// ReadResource records CLIENT span for resources/read.
func (t telemetryBackendClient) ReadResource(
	ctx context.Context, target *vmcp.BackendTarget, uri string,
) (_ *vmcp.ResourceReadResult, retErr error) {
	ctx, done := t.record(ctx, target, "resources/read", "", nil, &retErr)
	defer done()
	return t.backendClient.ReadResource(ctx, target, uri)
}

// GetPrompt records CLIENT span for prompts/get with gen_ai.prompt.name attribute.
func (t telemetryBackendClient) GetPrompt(
	ctx context.Context, target *vmcp.BackendTarget, name string, arguments map[string]any,
) (_ *vmcp.PromptGetResult, retErr error) {
	extraAttrs := []attribute.KeyValue{
		attrGenAIPromptName.String(name),
	}
	ctx, done := t.record(ctx, target, "prompts/get", name, extraAttrs, &retErr)
	defer done()
	return t.backendClient.GetPrompt(ctx, target, name, arguments)
}

// ListCapabilities records CLIENT span for initialize (the underlying MCP method).
func (t telemetryBackendClient) ListCapabilities(
	ctx context.Context, target *vmcp.BackendTarget,
) (_ *vmcp.CapabilityList, retErr error) {
	ctx, done := t.record(ctx, target, "initialize", "", nil, &retErr)
	defer done()
	return t.backendClient.ListCapabilities(ctx, target)
}

// resolveServerAddrPort extracts the server address and port from a BackendTarget's BaseURL.
func resolveServerAddrPort(target *vmcp.BackendTarget) (string, int) {
	if target.BaseURL == "" {
		return "", 0
	}

	parsed, err := url.Parse(target.BaseURL)
	if err != nil {
		return target.BaseURL, 0
	}

	addr := parsed.Hostname()
	portStr := parsed.Port()
	if portStr == "" {
		switch parsed.Scheme {
		case "https":
			portStr = "443"
		default:
			portStr = "80"
		}
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return addr, 0
	}

	return addr, port
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
