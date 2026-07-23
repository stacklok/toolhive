// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package backendtelemetry decorates a [vmcp.BackendClient] so each backend MCP
// call records OpenTelemetry traces and metrics.
//
// It lives in pkg/vmcp/internal so that both the transport server (server.New)
// and the core constructor (core.New) can share a single decorator without an
// import cycle: server and core both depend on this leaf package, and it depends
// on neither.
package backendtelemetry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/telemetry"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

const (
	instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"
)

var (
	reclassCounterOnce sync.Once
	reclassCounter     metric.Int64Counter
)

// RecordRevisionReclassification increments the count of backends whose MCP
// revision was reclassified after a call revealed the cached revision was wrong.
//
// The backend client resolves revisions below the telemetry decorator, so this
// is a free function backed by the global meter provider rather than the injected
// one used by MonitorBackends.
//
// ponytail: no labels yet — old/new revision labels (and the CRD status surface)
// are deferred. If the global provider ever diverges from the injected one, thread
// the meter down instead.
func RecordRevisionReclassification(ctx context.Context) {
	reclassCounterOnce.Do(func() {
		reclassCounter, _ = otel.GetMeterProvider().Meter(instrumentationName).Int64Counter(
			"toolhive_vmcp_backend_revision_reclassifications",
			metric.WithDescription("Number of times a backend's MCP revision was reclassified after a mismatch"),
		)
	})
	if reclassCounter != nil {
		reclassCounter.Add(ctx, 1)
	}
}

// MonitorBackends decorates the backend client so it records telemetry on each method call.
// It also emits a gauge for the number of backends discovered once, since the number of backends is static.
func MonitorBackends(
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

func (t telemetryBackendClient) Complete(
	ctx context.Context,
	target *vmcp.BackendTarget,
	ref vmcp.CompletionRef,
	argName, argValue string,
	contextArgs map[string]string,
) (_ *vmcp.CompletionResult, retErr error) {
	attrs := []attribute.KeyValue{
		attribute.String("completion.ref_type", ref.Type),
		attribute.String("completion.argument_name", argName),
	}
	// Check if caller is authenticated (extract from context)
	if caller, _ := auth.IdentityFromContext(ctx); caller != nil && caller.Subject != "" {
		attrs = append(attrs, attribute.Bool("auth.authenticated", true))
	}
	ctx, done := t.record(ctx, target, "complete", "", &retErr, attrs...)
	defer done()
	return t.backendClient.Complete(ctx, target, ref, argName, argValue, contextArgs)
}

func (t telemetryBackendClient) ListCapabilities(
	ctx context.Context, target *vmcp.BackendTarget,
) (_ *vmcp.CapabilityList, retErr error) {
	ctx, done := t.record(ctx, target, "list_capabilities", "", &retErr)
	defer done()
	return t.backendClient.ListCapabilities(ctx, target)
}
