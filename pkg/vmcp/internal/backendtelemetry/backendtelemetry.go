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
	"maps"
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
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

const instrumentationName = "github.com/stacklok/toolhive/pkg/vmcp"

// healthStateLabel is the label key distinguishing the health state a gauge
// point represents. The gauge emits one point per (mcp_server, state) pair,
// covering every possible vmcp.BackendHealthStatus value, for every backend.
const healthStateLabel = "state"

// healthStates lists every vmcp.BackendHealthStatus value the gauge reports a
// point for, so a dashboard can rely on the series existing (at 0) even for
// states a backend has never been in.
var healthStates = []vmcp.BackendHealthStatus{
	vmcp.BackendHealthy,
	vmcp.BackendDegraded,
	vmcp.BackendUnhealthy,
	vmcp.BackendUnknown,
	vmcp.BackendUnauthenticated,
}

// MonitorBackends decorates the backend client so it records telemetry on each method call.
// It also registers a live per-backend health gauge (stacklok.vmcp.mcp_server.health)
// whose observable callback reports each backend's current health at every collection.
//
// The gauge callback re-reads registry on every collection rather than tracking backend
// membership in backendHealth itself, so a backend removed from the registry (e.g. via
// list_changed) stops being reported instead of leaving an orphaned series behind.
//
// The returned unregister func releases the gauge callback and must be called when the
// decorated client is no longer in use (e.g. from the owning VMCP's Close), so a future
// rebuild of the backend client does not accumulate callbacks against stale health state.
//
// The returned *HealthProviderSetter lets the caller attach a live health.StatusProvider
// once it becomes available (health.Monitor is built after MonitorBackends is called in
// core.New, since it depends on the decorated client already existing). Until it's set —
// or if it's never set because health monitoring is disabled — the gauge falls back to
// registry/record()-derived state exactly as before.
func MonitorBackends(
	_ context.Context,
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
	registry vmcp.BackendRegistry,
	backendClient vmcp.BackendClient,
) (vmcp.BackendClient, *HealthProviderSetter, func() error, error) {
	meter := meterProvider.Meter(instrumentationName)

	// recordedHealth is mutated on request success/failure so the gauge reflects
	// live health within one collection interval. It is never seeded or pruned
	// here; membership at collection time comes from registry.List, below.
	// record() only distinguishes success/failure, so it can only ever set
	// BackendHealthy or BackendUnhealthy; the richer states (degraded, unknown,
	// unauthenticated) can only come from the registry's own HealthStatus (a
	// health monitor's discovery-time assessment), used as a fallback below when
	// no live StatusProvider is set or it doesn't track a given backend.
	recordedHealth := &backendHealth{states: make(map[string]vmcp.BackendHealthStatus)}
	providerSetter := &HealthProviderSetter{}

	clientOperationDuration, err := meter.Float64Histogram(
		"mcp.client.operation.duration",
		metric.WithDescription("Duration of MCP client operations"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPProxy()...),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create client operation duration histogram: %w", err)
	}

	healthGauge, err := meter.Int64ObservableGauge(
		"stacklok.vmcp.mcp_server.health",
		metric.WithDescription("Per-backend health: 1 for the observed state, 0 otherwise, per (mcp_server, state)"),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create backend health gauge: %w", err)
	}
	registration, err := meter.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			states := recordedHealth.snapshot()
			provider := providerSetter.get()
			for _, backend := range registry.List(ctx) {
				current := currentHealthStatus(backend, states, provider)
				for _, state := range healthStates {
					value := int64(0)
					if state == current {
						value = 1
					}
					o.ObserveInt64(healthGauge, value, metric.WithAttributes(
						attribute.String(coremetrics.LabelMCPServer, backend.Name),
						attribute.String(healthStateLabel, string(state)),
					))
				}
			}
			return nil
		},
		healthGauge,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to register backend health callback: %w", err)
	}

	return &telemetryBackendClient{
		backendClient:           backendClient,
		tracer:                  tracerProvider.Tracer(instrumentationName),
		health:                  recordedHealth,
		clientOperationDuration: clientOperationDuration,
	}, providerSetter, registration.Unregister, nil
}

// currentHealthStatus resolves a backend's health for the gauge callback, in the
// same precedence order filterHealthyBackends uses for capability filtering: the
// live health.StatusProvider first (so the gauge agrees with what's actually
// driving capability aggregation and respects circuit-breaker state), then the
// request-outcome map record() maintains, then the registry's discovery-time
// snapshot. An empty/zero-value HealthStatus (no source has classified the
// backend yet) is normalized to BackendHealthy, matching
// filterHealthyBackends's "empty/zero-value: assume healthy" convention —
// otherwise none of healthStates would match and the gauge would silently
// report every state as 0 for that backend instead of a definite one.
func currentHealthStatus(
	backend vmcp.Backend, recorded map[string]vmcp.BackendHealthStatus, provider health.StatusProvider,
) vmcp.BackendHealthStatus {
	status := backend.HealthStatus
	if s, ok := recorded[backend.Name]; ok {
		status = s
	}
	if provider != nil {
		if s, tracked := provider.QueryBackendStatus(backend.ID); tracked {
			status = s
		}
	}
	if status == "" {
		return vmcp.BackendHealthy
	}
	return status
}

// HealthProviderSetter lets core.New attach a live health.StatusProvider to an
// already-registered health gauge once the health.Monitor is built — which happens
// after MonitorBackends is called, since the monitor is constructed from the
// decorated backend client MonitorBackends returns. Safe for concurrent use: Set
// is called at most once from New, and get() may run concurrently from the
// gauge's observable callback.
type HealthProviderSetter struct {
	mu       sync.RWMutex
	provider health.StatusProvider
}

// Set attaches provider so the health gauge callback prefers it over the
// registry/record()-derived fallback. A nil provider (health monitoring
// disabled or failed to start) is a valid, explicit no-op.
func (s *HealthProviderSetter) Set(provider health.StatusProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.provider = provider
}

func (s *HealthProviderSetter) get() health.StatusProvider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.provider
}

// backendHealth tracks the latest observed health of each backend, keyed by
// workload name. It is read by the observable-gauge callback and written on each
// request's success/failure, so the gauge reflects live health. set() only ever
// receives BackendHealthy/BackendUnhealthy (record() has no visibility into the
// finer-grained states); those come from the registry instead, as a fallback for
// backends the map has no entry for yet (see MonitorBackends).
type backendHealth struct {
	mu     sync.RWMutex
	states map[string]vmcp.BackendHealthStatus
}

func (b *backendHealth) set(name string, status vmcp.BackendHealthStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.states[name] = status
}

func (b *backendHealth) snapshot() map[string]vmcp.BackendHealthStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make(map[string]vmcp.BackendHealthStatus, len(b.states))
	maps.Copy(out, b.states)
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

	// Attributes for mcp.client.operation.duration (spec-required + bounded
	// backend identity so per-backend latency/error rate stays queryable —
	// the deleted toolhive_vmcp_backend_requests_duration twin carried this).
	specMetricAttrs := metric.WithAttributes(
		attribute.String("mcp.method.name", mcpMethod),
		attribute.String("network.transport", networkTransport),
		attribute.String(coremetrics.LabelMCPServer, target.WorkloadName),
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
				attribute.String(coremetrics.LabelMCPServer, target.WorkloadName),
				attribute.String("error.type", fmt.Sprintf("%T", *err)),
			)
			t.clientOperationDuration.Record(ctx, duration.Seconds(), specMetricAttrsWithError)

			t.health.set(target.WorkloadName, vmcp.BackendUnhealthy)
			span.RecordError(*err)
			span.SetStatus(codes.Error, (*err).Error())
		} else {
			t.clientOperationDuration.Record(ctx, duration.Seconds(), specMetricAttrs)
			t.health.set(target.WorkloadName, vmcp.BackendHealthy)
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

func (t *telemetryBackendClient) Complete(
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

func (t *telemetryBackendClient) ListCapabilities(
	ctx context.Context, target *vmcp.BackendTarget,
) (_ *vmcp.CapabilityList, retErr error) {
	ctx, done := t.record(ctx, target, "list_capabilities", "", &retErr)
	defer done()
	return t.backendClient.ListCapabilities(ctx, target)
}
