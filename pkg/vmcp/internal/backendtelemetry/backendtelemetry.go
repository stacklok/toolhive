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
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	coremetrics "github.com/stacklok/toolhive-core/telemetry/metrics"
	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/telemetry"
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
// NOTE: no labels yet — old/new revision labels (and the CRD status surface)
// are deferred. If the global provider ever diverges from the injected one, thread
// the meter down instead.
func RecordRevisionReclassification(ctx context.Context) {
	reclassCounterOnce.Do(func() {
		reclassCounter, _ = otel.GetMeterProvider().Meter(instrumentationName).Int64Counter(
			"stacklok.vmcp.backend.revision_reclassifications",
			metric.WithDescription("Number of times a backend's MCP revision was reclassified after a mismatch"),
		)
	})
	if reclassCounter != nil {
		reclassCounter.Add(ctx, 1)
	}
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
// once it becomes available (core.New builds health.Monitor after this call; see
// HealthProviderSetter for why the ordering is a resource-acquisition choice rather
// than a data dependency). Until it's set — or if it's never set because health
// monitoring is disabled — the gauge falls back to registry/record()-derived state
// exactly as before.
func MonitorBackends(
	_ context.Context,
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
	registry vmcp.BackendRegistry,
	backendClient vmcp.BackendClient,
	useLegacyMetrics bool,
) (vmcp.BackendClient, *HealthProviderSetter, func() error, error) {
	meter := meterProvider.Meter(instrumentationName)

	// recordedHealth is mutated on request success/failure so the gauge reflects
	// live health within one collection interval. It is never seeded here, and is
	// pruned to the live backend set by the gauge callback below (membership at
	// collection time comes from registry.List either way).
	//
	// record() classifies each outcome through healthStatusForError, so it can set
	// BackendHealthy, BackendUnhealthy or BackendUnauthenticated. The remaining
	// states (degraded, unknown) can only come from the registry's own
	// HealthStatus (a health monitor's discovery-time assessment), used as a
	// fallback below when no live StatusProvider is set or it doesn't track a
	// given backend.
	recordedHealth := &backendHealth{states: make(map[string]vmcp.BackendHealthStatus)}
	providerSetter := &HealthProviderSetter{}

	// Semconv-named metric, so it carries the semconv bucket set exactly rather
	// than the coarser Stacklok proxy preset.
	clientOperationDuration, err := meter.Float64Histogram(
		"mcp.client.operation.duration",
		metric.WithDescription("Duration of MCP client operations"),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPSemconv()...),
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
			backends := registry.List(ctx)
			live := make(map[string]struct{}, len(backends))
			for _, backend := range backends {
				live[backend.ID] = struct{}{}
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
			// Bound the recorded-health map to the live backend set, so its growth
			// matches the gauge's rather than the process lifetime.
			recordedHealth.retain(live)
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
		legacyRequests: telemetry.LegacyInt64Counter(meter, useLegacyMetrics, "toolhive_vmcp_backend_requests",
			metric.WithDescription("DEPRECATED: use mcp.client.operation.duration")),
		legacyErrors: telemetry.LegacyInt64Counter(meter, useLegacyMetrics, "toolhive_vmcp_backend_errors",
			metric.WithDescription(`DEPRECATED: use mcp.client.operation.duration filtered to error.type != ""`)),
		legacyDurations: telemetry.LegacyFloat64Histogram(meter, useLegacyMetrics,
			"toolhive_vmcp_backend_requests_duration",
			metric.WithDescription("DEPRECATED: use mcp.client.operation.duration"),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(coremetrics.BucketsMCPProxy()...)),
	}, providerSetter, registration.Unregister, nil
}

// currentHealthStatus resolves a backend's health for the gauge callback. When a
// live health.StatusProvider is set, its tracked status wins outright — and if it
// doesn't track this backend, the registry's discovery-time snapshot is used
// directly. This matches filterHealthyBackends's precedence exactly whenever a
// provider is set (tracked or not), so the two agree in that case.
//
// When no provider is set at all (health monitoring disabled, or not yet wired up
// during startup), the gauge instead consults the request-outcome map record()
// maintains, giving it a live-ish signal instead of a snapshot frozen at discovery
// time. filterHealthyBackends has no equivalent fallback and always uses the
// registry snapshot when there's no provider. This is the actual — and only —
// divergence window: whenever provider == nil and record() has observed at least
// one outcome for a backend, the gauge and filterHealthyBackends can disagree
// until a health.StatusProvider is attached and starts tracking that backend.
//
// An empty/zero-value HealthStatus (no source has classified the backend yet) is
// normalized to BackendHealthy, matching filterHealthyBackends's "empty/zero-value:
// assume healthy" convention — otherwise none of healthStates would match and the
// gauge would silently report every state as 0 for that backend instead of a
// definite one.
func currentHealthStatus(
	backend vmcp.Backend, recorded map[string]vmcp.BackendHealthStatus, provider health.StatusProvider,
) vmcp.BackendHealthStatus {
	status := backend.HealthStatus
	if provider != nil {
		if s, tracked := provider.QueryBackendStatus(backend.ID); tracked {
			status = s
		}
	} else if s, ok := recorded[backend.ID]; ok {
		status = s
	}
	if status == "" {
		return vmcp.BackendHealthy
	}
	return status
}

// HealthProviderSetter lets core.New attach a live health.StatusProvider to an
// already-registered health gauge once the health.Monitor is built.
//
// The ordering is not a data dependency: buildHealthMonitor deliberately
// constructs the monitor from the UNDECORATED client so health checks emit no
// backend telemetry, so the monitor could in principle be built first. The
// two-phase init exists to keep the gauge's registration — and therefore its
// unregister func, the resource New must release on failure — acquired at a
// single point early in New, rather than ordering resource acquisition around
// the monitor. Building the monitor first would move more of New's error paths
// above the gauge registration.
//
// Safe for concurrent use: Set is called at most once from New, and get() may run
// concurrently from the gauge's observable callback.
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
// workload ID (the same identity space as the registry and health.StatusProvider,
// so a backend rename can't cause a stale/duplicate entry). It is read by the
// observable-gauge callback and written on each request's success/failure, so
// the gauge reflects live health.
//
// set() receives BackendHealthy, BackendUnhealthy or BackendUnauthenticated (see
// healthStatusForError); the remaining states come from the registry instead, as a
// fallback for backends the map has no entry for yet (see MonitorBackends).
//
// The map is bounded by retain(), called from the gauge callback with the live
// registry set — without it, set() would add an entry per distinct workload ID
// and nothing would ever remove one.
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

// retain drops every entry whose key is absent from live, bounding the map to the
// backends the registry currently knows about. set() adds an entry per distinct
// WorkloadID and nothing else removes one, so under dynamic discovery
// (list_changed, K8s watcher churn) the map would otherwise grow for the process
// lifetime over an ID space the process does not control — and snapshot() copies
// all of it on every collection.
func (b *backendHealth) retain(live map[string]struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id := range b.states {
		if _, ok := live[id]; !ok {
			delete(b.states, id)
		}
	}
}

type telemetryBackendClient struct {
	backendClient vmcp.BackendClient
	tracer        trace.Tracer
	health        *backendHealth

	clientOperationDuration metric.Float64Histogram

	// Legacy aliases for the deleted toolhive_vmcp_backend_* twins, emitted
	// alongside mcp.client.operation.duration when Config.UseLegacyMetrics is set.
	// They carry the full target.* attribute set the originals did. No-op
	// instruments when disabled, so record sites need no branch.
	legacyRequests  metric.Int64Counter
	legacyErrors    metric.Int64Counter
	legacyDurations metric.Float64Histogram
}

var _ vmcp.BackendClient = (*telemetryBackendClient)(nil)

// CachedRevision forwards to the wrapped client's optional vmcp.RevisionReporter so
// callers reaching the client THROUGH this decorator (e.g. the health monitor)
// can still read the negotiated revision. Returns (0, false) when the wrapped
// client does not report revisions.
func (t telemetryBackendClient) CachedRevision(workloadID string) (mcpparser.Revision, bool) {
	if r, ok := t.backendClient.(vmcp.RevisionReporter); ok {
		return r.CachedRevision(workloadID)
	}
	return 0, false
}

// revisionLabel returns the backend's negotiated MCP revision as a metric label
// value, or "" when unprobed/unknown (low cardinality: 2 values + empty).
func (t telemetryBackendClient) revisionLabel(workloadID string) string {
	if rev, ok := t.CachedRevision(workloadID); ok {
		return rev.String()
	}
	return ""
}

// healthStatusForError classifies a failed backend call into the health state the
// gauge should report, or (_, false) when the error says nothing about backend
// liveness and the gauge must be left untouched.
//
// Not every error means the backend is unhealthy. A caller that disconnects
// mid-call yields context.Canceled/DeadlineExceeded, which is caller-side; and an
// auth failure has its own state in the gauge's vocabulary. Treating either as
// BackendUnhealthy pins the gauge — and any alert reading it — until the next
// successful call to that backend.
//
// Auth errors map to BackendUnauthenticated to match health.categorizeError's
// vocabulary, though this cannot consult the target's AuthConfig the way
// health.authErrorStatus does, so it does not distinguish an expected auth
// challenge from a misconfiguration.
func healthStatusForError(err error) (vmcp.BackendHealthStatus, bool) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", false
	case errors.Is(err, vmcp.ErrAuthenticationFailed), errors.Is(err, vmcp.ErrAuthorizationFailed):
		return vmcp.BackendUnauthenticated, true
	default:
		return vmcp.BackendUnhealthy, true
	}
}

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
		// Negotiated MCP revision (low cardinality: 2 values + empty when unprobed).
		attribute.String("mcp.protocol.revision", t.revisionLabel(target.WorkloadID)),
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

	// Legacy aliases carried the full target.* attribute set, and the requests
	// counter was incremented before the call — reproduced here so a dashboard
	// reading them sees the same numbers it did before the deletion.
	legacyMetricAttrs := metric.WithAttributes(commonAttrs...)

	start := time.Now()
	t.legacyRequests.Add(ctx, 1, legacyMetricAttrs)

	return ctx, func() {
		duration := time.Since(start)
		t.legacyDurations.Record(ctx, duration.Seconds(), legacyMetricAttrs)

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
			t.legacyErrors.Add(ctx, 1, legacyMetricAttrs)

			if status, ok := healthStatusForError(*err); ok {
				t.health.set(target.WorkloadID, status)
			}
			span.RecordError(*err)
			span.SetStatus(codes.Error, (*err).Error())
		} else {
			t.clientOperationDuration.Record(ctx, duration.Seconds(), specMetricAttrs)
			t.health.set(target.WorkloadID, vmcp.BackendHealthy)
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
	paramHeaders map[string]string,
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
	return t.backendClient.CallTool(ctx, target, toolName, arguments, meta, paramHeaders)
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
