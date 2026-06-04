// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/internal/backendtelemetry"
	"github.com/stacklok/toolhive/pkg/vmcp/internal/compositetools"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
)

const (
	// stateStoreCleanupInterval is how often the in-memory workflow state store
	// purges stale entries. Matches the legacy server.New value (server.go:386).
	stateStoreCleanupInterval = 5 * time.Minute

	// stateStoreMaxAge is how long completed/failed workflows are retained.
	// Matches the legacy server.New value (server.go:386).
	stateStoreMaxAge = 1 * time.Hour
)

// coreVMCP is the concrete, identity-parameterized [VMCP] assembled by [New].
//
// It is stateless with respect to MCP sessions: every List/Lookup/Call
// re-derives the advertised capability view by health-filtering the backend
// registry and aggregating on demand ("the core filters, Serve caches" — caching
// is added by a Serve decorator, not here). It holds no context-coupled router:
// routing is performed through a [router.NewSessionRouter] bound to the freshly
// aggregated routing table, removing composite tools' dependency on
// context-injected capabilities (vmcp anti-pattern #1).
//
// Safe for concurrent use: all fields are read-only after construction except
// the cleanup guarded by closeOnce.
type coreVMCP struct {
	aggregator      aggregator.Aggregator
	backendRegistry vmcp.BackendRegistry
	backendClient   vmcp.BackendClient

	// health is the injected read-only backend health view. Nil means no health
	// filtering (all backends included), matching today's no-monitor behavior.
	health health.StatusProvider

	// workflowDefs holds the validated composite-tool workflow definitions keyed
	// by advertised tool name.
	workflowDefs map[string]*composer.WorkflowDefinition

	// composerFactory builds a per-call composite-tool engine bound to a routing
	// table, generalizing server.New's sessionComposerFactory (server.go:393).
	composerFactory func(sessionRT *vmcp.RoutingTable, sessionTools []vmcp.Tool) composer.Composer

	// stopStore stops the workflow state store's background cleanup goroutine.
	// Captured at construction (the store is created internally, not injected) so
	// Close is not a silent capability assertion. Guarded by closeOnce.
	stopStore func()

	closeOnce sync.Once
}

var _ VMCP = (*coreVMCP)(nil)

// New constructs the core [VMCP] by relocating the domain wiring that lives in
// server.New today (server.go:330-405): telemetry backend-client decoration, the
// optional workflow auditor, the in-memory state store and workflow engine, the
// per-session composer factory, and fail-fast workflow validation.
//
// cfg.Router is consumed only to build the workflow-validation engine (parity
// with server.New); the core does not route through it at request time. cfg's
// Aggregator, BackendRegistry, and BackendClient are required and New fails
// loudly when any is nil. The admission seam (cfg.Authz) is NOT wired here — it
// lands in #5438.
func New(cfg *Config) (VMCP, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil core config", vmcp.ErrInvalidConfig)
	}
	if cfg.Aggregator == nil {
		return nil, fmt.Errorf("%w: Aggregator is required", vmcp.ErrInvalidConfig)
	}
	if cfg.Router == nil {
		return nil, fmt.Errorf("%w: Router is required", vmcp.ErrInvalidConfig)
	}
	if cfg.BackendRegistry == nil {
		return nil, fmt.Errorf("%w: BackendRegistry is required", vmcp.ErrInvalidConfig)
	}
	if cfg.BackendClient == nil {
		return nil, fmt.Errorf("%w: BackendClient is required", vmcp.ErrInvalidConfig)
	}

	backendClient := cfg.BackendClient

	// Telemetry backend-client decoration must happen BEFORE building the workflow
	// engine so that workflow backend calls are instrumented (server.go:350-367).
	if cfg.TelemetryProvider != nil {
		decorated, err := backendtelemetry.MonitorBackends(
			context.Background(),
			cfg.TelemetryProvider.MeterProvider(),
			cfg.TelemetryProvider.TracerProvider(),
			cfg.BackendRegistry.List(context.Background()),
			backendClient,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to monitor backends: %w", err)
		}
		backendClient = decorated
	}

	// Workflow auditor (server.go:370-381).
	var workflowAuditor *audit.WorkflowAuditor
	if cfg.AuditConfig != nil {
		if err := cfg.AuditConfig.Validate(); err != nil {
			return nil, fmt.Errorf("invalid audit configuration: %w", err)
		}
		var err error
		workflowAuditor, err = audit.NewWorkflowAuditor(cfg.AuditConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create workflow auditor: %w", err)
		}
		slog.Info("workflow audit logging enabled")
	}

	// The elicitation handler depends only on the domain-typed ElicitationRequester
	// (#5436); no mcp-go types cross this boundary (vmcp anti-pattern #5).
	elicitationHandler := composer.NewDefaultElicitationHandler(cfg.Elicitation)

	// State store + workflow engine (server.go:386-387). The state store starts a
	// background cleanup goroutine immediately, so any error path after this point
	// must stop it to avoid a leak.
	stateStore := composer.NewInMemoryStateStore(stateStoreCleanupInterval, stateStoreMaxAge)

	// NewInMemoryStateStore returns *inMemoryStateStore, which owns that cleanup
	// goroutine. Capture its Stop here so Close releases it; warn loudly (rather
	// than a silent no-op, per go-style) if a future store swap drops the
	// capability, so a leaked goroutine is diagnosable instead of invisible.
	stopStore := func() {}
	if s, ok := stateStore.(interface{ Stop() }); ok {
		stopStore = s.Stop
	} else {
		slog.Warn("workflow state store exposes no Stop(); its cleanup goroutine will leak on Close",
			"type", fmt.Sprintf("%T", stateStore))
	}

	// composerFactory builds a composite-tool engine bound to a specific routing
	// table, generalizing server.New's sessionComposerFactory (server.go:393-398).
	// Workflow-level telemetry (toolhive_vmcp_workflow_*) is intentionally NOT wired
	// here: that instrumentation lives in the session/Serve layer
	// (sessionmanager/factory.go:174-176) and is added when the core is wired into
	// Serve (#5439). This mirrors server.go:393-398, which is also uninstrumented.
	composerFactory := func(sessionRT *vmcp.RoutingTable, sessionTools []vmcp.Tool) composer.Composer {
		return composer.NewWorkflowEngine(
			router.NewSessionRouter(sessionRT), backendClient, elicitationHandler,
			stateStore, workflowAuditor, sessionTools,
		)
	}

	// Validate workflows fail-fast (server.go:400-405). The validation engine uses
	// cfg.Router; ValidateWorkflow checks structure (cycles, references) and does
	// not route, so the context-coupled default router is acceptable here.
	validationEngine := composer.NewWorkflowEngine(
		cfg.Router, backendClient, elicitationHandler, stateStore, workflowAuditor, nil,
	)
	workflowDefs, err := validateWorkflowDefs(validationEngine, cfg.WorkflowDefs)
	if err != nil {
		stopStore()
		return nil, fmt.Errorf("workflow validation failed: %w", err)
	}

	return &coreVMCP{
		aggregator:      cfg.Aggregator,
		backendRegistry: cfg.BackendRegistry,
		backendClient:   backendClient,
		health:          cfg.HealthStatusProvider,
		workflowDefs:    workflowDefs,
		composerFactory: composerFactory,
		stopStore:       stopStore,
	}, nil
}

// ListTools health-filters the registry, aggregates backend capabilities on
// demand, and appends the composite tools reachable in the current view.
//
// identity is accepted for the admission seam wired in #5438; this core performs
// no identity-based filtering yet. A nil identity is treated as anonymous,
// consistent with session/types.ShouldAllowAnonymous — bound-session caller
// enforcement (ErrNilCaller/ErrUnauthorizedCaller) lives in the Serve session
// layer, not here. identity is never logged.
func (c *coreVMCP) ListTools(ctx context.Context, _ *auth.Identity) ([]vmcp.Tool, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}
	return c.advertisedTools(agg), nil
}

// ListResources health-filters the registry and aggregates resources on demand.
func (c *coreVMCP) ListResources(ctx context.Context, _ *auth.Identity) ([]vmcp.Resource, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}
	return agg.Resources, nil
}

// ListPrompts health-filters the registry and aggregates prompts on demand.
func (c *coreVMCP) ListPrompts(ctx context.Context, _ *auth.Identity) ([]vmcp.Prompt, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}
	return agg.Prompts, nil
}

// LookupTool resolves an advertised tool name (incl. composite tools) to its
// capability without invoking it. It applies the same health/advertising view as
// ListTools and returns vmcp.ErrNotFound for an unknown or unadvertised name.
func (c *coreVMCP) LookupTool(ctx context.Context, identity *auth.Identity, name string) (*vmcp.Tool, error) {
	tools, err := c.ListTools(ctx, identity)
	if err != nil {
		return nil, err
	}
	for i := range tools {
		if tools[i].Name == name {
			tool := tools[i]
			return &tool, nil
		}
	}
	return nil, fmt.Errorf("%w: tool %q", vmcp.ErrNotFound, name)
}

// LookupResource resolves an advertised resource URI to its capability without
// reading it. Returns vmcp.ErrNotFound for an unknown or unadvertised URI.
func (c *coreVMCP) LookupResource(ctx context.Context, identity *auth.Identity, uri string) (*vmcp.Resource, error) {
	resources, err := c.ListResources(ctx, identity)
	if err != nil {
		return nil, err
	}
	for i := range resources {
		if resources[i].URI == uri {
			resource := resources[i]
			return &resource, nil
		}
	}
	return nil, fmt.Errorf("%w: resource %q", vmcp.ErrNotFound, uri)
}

// LookupPrompt resolves an advertised prompt name to its capability without
// retrieving it. Returns vmcp.ErrNotFound for an unknown or unadvertised name.
func (c *coreVMCP) LookupPrompt(ctx context.Context, identity *auth.Identity, name string) (*vmcp.Prompt, error) {
	prompts, err := c.ListPrompts(ctx, identity)
	if err != nil {
		return nil, err
	}
	for i := range prompts {
		if prompts[i].Name == name {
			prompt := prompts[i]
			return &prompt, nil
		}
	}
	return nil, fmt.Errorf("%w: prompt %q", vmcp.ErrNotFound, name)
}

// Close stops the workflow state store's cleanup goroutine. It is idempotent:
// the underlying Stop closes a channel that cannot be closed twice, so the work
// is guarded by sync.Once and subsequent calls return nil.
func (c *coreVMCP) Close() error {
	c.closeOnce.Do(c.stopStore)
	return nil
}

// aggregatedView health-filters the backend registry and aggregates capabilities
// on demand. The core holds no per-session capability cache (the core filters,
// Serve caches): every List/Lookup/Call re-derives the advertised view, so a
// lookup-then-call flow aggregates twice. This is intentional — caching is the
// Serve layer's responsibility, not the core's (vmcp anti-patterns #8/#9).
func (c *coreVMCP) aggregatedView(ctx context.Context) (*aggregator.AggregatedCapabilities, error) {
	backends := filterHealthyBackends(c.backendRegistry.List(ctx), c.health)
	agg, err := c.aggregator.AggregateCapabilities(ctx, backends)
	if err != nil {
		return nil, fmt.Errorf("capability aggregation failed: %w", err)
	}
	return agg, nil
}

// advertisedTools returns the aggregated backend tools followed by the composite
// tools reachable in agg's routing table.
//
// Composite tools must be added here rather than by a Serve decorator: decorators
// may only SUBTRACT reachability ([VMCP] contract), so the core is the only layer
// that can advertise them.
func (c *coreVMCP) advertisedTools(agg *aggregator.AggregatedCapabilities) []vmcp.Tool {
	defs := c.accessibleComposites(agg)
	if len(defs) == 0 {
		return agg.Tools
	}

	composite := compositetools.ConvertWorkflowDefsToTools(defs)
	out := make([]vmcp.Tool, 0, len(agg.Tools)+len(composite))
	out = append(out, agg.Tools...)
	out = append(out, composite...)
	return out
}

// accessibleComposites returns the composite-tool definitions the core advertises
// (and therefore executes) for agg's view: those whose every tool step is reachable
// in the routing table AND whose names do not collide with a backend tool. On a name
// collision ALL composites are dropped so the backend tool wins, matching the legacy
// compositeToolsDecorator (sessionmanager/factory.go:158-168, decorator.go:83-86).
//
// This is the single source of truth shared by advertisedTools (what ListTools shows)
// and CallTool (what executes), so a withheld composite is never executed — advertised
// equals executed. Returns nil when there are no composites or a conflict drops them.
func (c *coreVMCP) accessibleComposites(
	agg *aggregator.AggregatedCapabilities,
) map[string]*composer.WorkflowDefinition {
	if len(c.workflowDefs) == 0 {
		return nil
	}

	defs := compositetools.FilterWorkflowDefsForSession(c.workflowDefs, agg.RoutingTable)
	if len(defs) == 0 {
		return nil
	}
	if err := compositetools.ValidateNoToolConflicts(agg.Tools, compositetools.ConvertWorkflowDefsToTools(defs)); err != nil {
		slog.Warn("composite tool name conflict detected; omitting composite tools", "error", err)
		return nil
	}
	return defs
}

// validateWorkflowDefs validates each workflow definition, returning only the
// valid ones. It fails fast on the first invalid definition.
//
// Relocated from server.New (server.go:1211); the legacy path keeps its own copy
// until server.New is reduced to a wrapper (Phase 3, #5432/#5445).
func validateWorkflowDefs(
	validator composer.Composer,
	workflowDefs map[string]*composer.WorkflowDefinition,
) (map[string]*composer.WorkflowDefinition, error) {
	if len(workflowDefs) == 0 {
		// Nil-when-empty, mirroring legacy server.New; all readers guard for it
		// (advertisedTools' len check and the c.workflowDefs[name] lookup).
		return nil, nil
	}

	validDefs := make(map[string]*composer.WorkflowDefinition, len(workflowDefs))
	for name, def := range workflowDefs {
		if err := validator.ValidateWorkflow(context.Background(), def); err != nil {
			return nil, fmt.Errorf("invalid workflow definition %q: %w", name, err)
		}
		validDefs[name] = def
	}
	slog.Info("loaded valid composite tool workflows", "count", len(validDefs))
	return validDefs, nil
}

// filterHealthyBackends filters backends to only include those that are healthy
// or degraded. Backends that are unhealthy, unknown, or unauthenticated are
// excluded from capability aggregation to prevent exposing tools from
// unavailable backends.
//
// Health status filtering:
//   - healthy: included (fully operational)
//   - degraded: included (slow but working)
//   - empty/zero-value: included (assume healthy when health monitoring is disabled)
//   - unhealthy: excluded (not responding, circuit breaker may be open)
//   - unknown: excluded (status not yet determined)
//   - unauthenticated: excluded (misconfiguration: backend requires auth but none configured)
//
// When healthStatusProvider is provided, the current health status from the
// health monitor is used (respects circuit breaker state). When nil, falls back
// to the initial health status from the backend registry.
//
// This is an intentional, temporary duplication of discovery.filterHealthyBackends
// (discovery/middleware.go:157, rules at 185-188): the discovery middleware keeps
// its own copy on the legacy server.New path until that path is removed in Phase 3
// (#5442/#5445). Keep the include/exclude rules identical across both copies.
func filterHealthyBackends(backends []vmcp.Backend, healthStatusProvider health.StatusProvider) []vmcp.Backend {
	if len(backends) == 0 {
		return backends
	}

	healthy := make([]vmcp.Backend, 0, len(backends))
	excluded := 0

	for i := range backends {
		backend := &backends[i]

		// Get current health status from health monitor if available so the
		// circuit breaker state is respected during capability aggregation.
		var healthStatus vmcp.BackendHealthStatus
		if healthStatusProvider != nil {
			if status, exists := healthStatusProvider.QueryBackendStatus(backend.ID); exists {
				healthStatus = status
			} else {
				// Backend not tracked by health monitor - use registry status.
				healthStatus = backend.HealthStatus
			}
		} else {
			// Health monitoring disabled - use registry status.
			healthStatus = backend.HealthStatus
		}

		// Include healthy, degraded, and empty/zero-value (assume healthy) backends.
		// Explicitly exclude unhealthy, unknown, and unauthenticated backends.
		if healthStatus == "" ||
			healthStatus == vmcp.BackendHealthy ||
			healthStatus == vmcp.BackendDegraded {
			healthy = append(healthy, *backend)
		} else {
			excluded++
			//nolint:gosec // G706: backend fields are internal, not user-controlled
			slog.Debug("excluding backend from capability aggregation due to health status",
				"backend_name", backend.Name,
				"backend_id", backend.ID,
				"health_status", healthStatus,
				"source", func() string {
					if healthStatusProvider != nil {
						return "health_monitor"
					}
					return "registry"
				}())
		}
	}

	if excluded > 0 {
		//nolint:gosec // G706: values are internal counts, not user-controlled
		slog.Debug("filtered backends for capability aggregation",
			"total_backends", len(backends),
			"healthy_backends", len(healthy),
			"excluded_backends", excluded)
	}

	return healthy
}
