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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	coremetrics "github.com/stacklok/toolhive-core/telemetry/metrics"
	"github.com/stacklok/toolhive/pkg/auth"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

const (
	instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

	// healthStateLabel is the label key distinguishing the health state a gauge
	// point represents. The gauge emits one point per (mcp_server, state) pair.
	healthStateLabel = "state"

	healthStateHealthy   = "healthy"
	healthStateUnhealthy = "unhealthy"
)

// MonitorBackends decorates the backend client so it records telemetry on each method call.
// It also registers a live per-backend health gauge (stacklok.vmcp.mcp_server.health)
// whose observable callback reports each backend's current health at every collection.
func MonitorBackends(
	_ context.Context,
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
	backends []vmcp.Backend,
	backendClient vmcp.BackendClient,
) (vmcp.BackendClient, error) {
	meter := meterProvider.Meter(instrumentationName)

	// Seed the health-state map from each backend's discovery-time HealthStatus.
	// The map is subsequently mutated on request success/failure so the observable
	// gauge reflects live health within one collection interval.
	health := &backendHealth{states: make(map[string]bool, len(backends))}
	for i := range backends {
		health.set(backends[i].Name, backends[i].HealthStatus == vmcp.BackendHealthy)
	}

	clientOperationDuration, err := meter.Float64Histogram(
		"mcp.client.operation.duration",
		metric.WithDescription("Duration of MCP client operations"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPProxy()...),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create client operation duration histogram: %w", err)
	}

	healthGauge, err := meter.Int64ObservableGauge(
		"stacklok.vmcp.mcp_server.health",
		metric.WithDescription("Per-backend health: 1 for the observed state, 0 otherwise, per (mcp_server, state)"),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend health gauge: %w", err)
	}
	if _, err = meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			for name, healthy := range health.snapshot() {
				healthyVal, unhealthyVal := int64(0), int64(1)
				if healthy {
					healthyVal, unhealthyVal = 1, 0
				}
				o.ObserveInt64(healthGauge, healthyVal, metric.WithAttributes(
					attribute.String(coremetrics.LabelMCPServer, name),
					attribute.String(healthStateLabel, healthStateHealthy),
				))
				o.ObserveInt64(healthGauge, unhealthyVal, metric.WithAttributes(
					attribute.String(coremetrics.LabelMCPServer, name),
					attribute.String(healthStateLabel, healthStateUnhealthy),
				))
			}
			return nil
		},
		healthGauge,
	); err != nil {
		return nil, fmt.Errorf("failed to register backend health callback: %w", err)
	}

	return &telemetryBackendClient{
		backendClient:           backendClient,
		tracer:                  tracerProvider.Tracer(instrumentationName),
		health:                  health,
		clientOperationDuration: clientOperationDuration,
	}, nil
}

// backendHealth tracks the latest observed health of each backend, keyed by
// workload name. It is read by the observable-gauge callback and written on each
// request's success/failure, so the gauge reflects live health.
type backendHealth struct {
	mu     sync.RWMutex
	states map[string]bool
}

func (b *backendHealth) set(name string, healthy bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.states[name] = healthy
}

func (b *backendHealth) snapshot() map[string]bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]bool, len(b.states))
	for k, v := range b.states {
		out[k] = v
	}
	return out
}

type telemetryBackendClient struct {
	backendClient vmcp.BackendClient
	tracer        trace.Tracer
	health        *backendHealth

	clientOperationDuration metric.Float64Histogram
}

var _ vmcp.BackendClient = (*telemetryBackendClient)(nil)

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
func (t *telemetryBackendClient) record(
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

	// Attributes for mcp.client.operation.duration (spec-required)
	specMetricAttrs := metric.WithAttributes(
		attribute.String("mcp.method.name", mcpMethod),
		attribute.String("network.transport", networkTransport),
	)

	start := time.Now()

	return ctx, func() {
		duration := time.Since(start)

		// Record mcp.client.operation.duration with spec attributes
		if err != nil && *err != nil {
			// Add error.type attribute for spec compliance
			specMetricAttrsWithError := metric.WithAttributes(
				attribute.String("mcp.method.name", mcpMethod),
				attribute.String("network.transport", networkTransport),
				attribute.String("error.type", fmt.Sprintf("%T", *err)),
			)
			t.clientOperationDuration.Record(ctx, duration.Seconds(), specMetricAttrsWithError)

			t.health.set(target.WorkloadName, false)
			span.RecordError(*err)
			span.SetStatus(codes.Error, (*err).Error())
		} else {
			t.clientOperationDuration.Record(ctx, duration.Seconds(), specMetricAttrs)
			t.health.set(target.WorkloadName, true)
		}
		span.End()
	}
}

func (t *telemetryBackendClient) CallTool(
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

func (t *telemetryBackendClient) ReadResource(
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

func (t *telemetryBackendClient) GetPrompt(
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
