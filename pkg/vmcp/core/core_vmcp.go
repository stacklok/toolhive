// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yosida95/uritemplate/v3"

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

	// health is the read-only backend health view used for capability filtering. It is the
	// healthMonitor below as a StatusProvider, or a true nil interface when monitoring is
	// disabled/failed (so filterHealthyBackends includes all backends rather than calling
	// through a typed-nil).
	health health.StatusProvider

	// healthMonitor is the backend health monitor the core owns: built and started in New,
	// stopped in Close, and exposed for reporting via BackendHealth. Nil when health
	// monitoring is disabled or failed to start. (#5443 reversal: the monitor's lifecycle
	// moved here from server.New/Serve so it has a single owner.)
	healthMonitor *health.Monitor

	// admission enforces the authorization decision for List* (filter) and
	// Call/Read/Get (deny) from one source. Never nil: New installs an allow-all
	// seam when authz is unconfigured.
	admission Admission

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

	// unregisterBackendHealth releases the backend-health gauge callback
	// registered by backendtelemetry.MonitorBackends. A no-op when telemetry is
	// disabled. Guarded by closeOnce.
	unregisterBackendHealth func() error

	closeOnce sync.Once
}

var _ VMCP = (*coreVMCP)(nil)

// unregisterHealthOnError calls unregister and logs (rather than swallows) a
// failure. Used on New's error paths after the backend-health gauge callback
// has been registered but construction later fails: without this, the
// callback stays attached to the shared meter provider — polling a registry
// for a coreVMCP that was never returned to the caller and so never has
// Close called on it — for every failed New attempt.
func unregisterHealthOnError(unregister func() error) {
	if err := unregister(); err != nil {
		slog.Warn("failed to unregister backend health gauge callback", "error", err)
	}
}

// New constructs the core [VMCP] by relocating the domain wiring that lives in
// server.New today (server.go:330-405): telemetry backend-client decoration, the
// optional workflow auditor, the in-memory state store and workflow engine, the
// per-session composer factory, and fail-fast workflow validation.
//
// cfg.Router is consumed only to build the workflow-validation engine (parity
// with server.New); the core does not route through it at request time. cfg's
// Aggregator, BackendRegistry, and BackendClient are required and New fails
// loudly when any is nil. The admission seam is built here from cfg.Authz (with
// cfg.ServerName); a nil cfg.Authz yields an allow-all seam.
func New(cfg *Config) (VMCP, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	// Build the admission seam before acquiring resources so a bad policy fails
	// fast without leaking the state store's cleanup goroutine.
	admission, err := newAdmission(cfg.Authz, cfg.ServerName)
	if err != nil {
		return nil, fmt.Errorf("failed to build admission seam: %w", err)
	}

	backendClient := cfg.BackendClient

	// unregisterBackendHealth releases the health-gauge callback registered by
	// MonitorBackends; a no-op unless telemetry is enabled below. Captured before
	// any later error path so Close always has a valid (possibly no-op) func to call.
	unregisterBackendHealth := func() error { return nil }

	// Telemetry backend-client decoration must happen BEFORE building the workflow
	// engine so that workflow backend calls are instrumented (server.go:350-367).
	if cfg.TelemetryProvider != nil {
		decorated, unregister, err := backendtelemetry.MonitorBackends(
			context.Background(),
			cfg.TelemetryProvider.MeterProvider(),
			cfg.TelemetryProvider.TracerProvider(),
			cfg.BackendRegistry,
			backendClient,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to monitor backends: %w", err)
		}
		backendClient = decorated
		unregisterBackendHealth = unregister
	}

	// Workflow auditor (server.go:370-381).
	var workflowAuditor *audit.WorkflowAuditor
	if cfg.AuditConfig != nil {
		if err := cfg.AuditConfig.Validate(); err != nil {
			unregisterHealthOnError(unregisterBackendHealth)
			return nil, fmt.Errorf("invalid audit configuration: %w", err)
		}
		var err error
		workflowAuditor, err = audit.NewWorkflowAuditor(cfg.AuditConfig)
		if err != nil {
			unregisterHealthOnError(unregisterBackendHealth)
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

	// Build workflow telemetry instruments once here so they are reused across all
	// per-call composer factories. Nil when TelemetryProvider is absent (disabled).
	// Uses the same metric names as the session-factory composite-tool decorator in
	// pkg/vmcp/server/sessionmanager so both execution paths emit into the same
	// Prometheus counters and histograms.
	instruments, err := newWorkflowInstruments(cfg.TelemetryProvider)
	if err != nil {
		stopStore()
		unregisterHealthOnError(unregisterBackendHealth)
		return nil, fmt.Errorf("failed to create workflow telemetry instruments: %w", err)
	}

	// composerFactory builds a composite-tool engine bound to a specific routing
	// table. When telemetry is configured, it wraps the engine with OTEL metrics so
	// workflow executions are instrumented the same way as the session-factory path
	// (sessionmanager/factory.go). The telemetryComposer is in core_telemetry.go.
	composerFactory := func(sessionRT *vmcp.RoutingTable, sessionTools []vmcp.Tool) composer.Composer {
		engine := composer.NewWorkflowEngine(
			router.NewSessionRouter(sessionRT), backendClient, elicitationHandler,
			stateStore, workflowAuditor, sessionTools,
		)
		if instruments != nil {
			return &telemetryComposer{base: engine, instruments: instruments}
		}
		return engine
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
		unregisterHealthOnError(unregisterBackendHealth)
		return nil, fmt.Errorf("workflow validation failed: %w", err)
	}

	// Build and start the backend health monitor (#5443 reversal: the core owns its
	// lifecycle). The core filters capabilities with it and stops it in Close. It is started
	// with context.Background() and torn down via Close — like the state store — since New has
	// no request-scoped context. The monitor uses the undecorated backend client so health
	// checks do not emit backend-call telemetry. Built last so few error paths must stop it.
	healthMonitor, healthProvider, err := buildHealthMonitor(cfg)
	if err != nil {
		stopStore()
		unregisterHealthOnError(unregisterBackendHealth)
		return nil, err
	}

	return &coreVMCP{
		aggregator:              cfg.Aggregator,
		backendRegistry:         cfg.BackendRegistry,
		backendClient:           backendClient,
		health:                  healthProvider,
		healthMonitor:           healthMonitor,
		admission:               admission,
		workflowDefs:            workflowDefs,
		composerFactory:         composerFactory,
		stopStore:               stopStore,
		unregisterBackendHealth: unregisterBackendHealth,
	}, nil
}

// buildHealthMonitor builds and starts the backend health monitor from cfg, returning the
// monitor (for Close/reporting) and its StatusProvider view (for capability filtering).
// Returns (nil, nil, nil) when health monitoring is not configured. A monitor that fails to
// start is logged and disabled — not fatal — preserving the legacy lenient behavior, so the
// returned monitor is nil in that case too. The StatusProvider is returned as a true nil
// interface when disabled so filterHealthyBackends does not call through a typed-nil.
func buildHealthMonitor(cfg *Config) (*health.Monitor, health.StatusProvider, error) {
	if cfg.HealthMonitorConfig == nil {
		slog.Info("health monitoring disabled")
		return nil, nil, nil
	}

	// Use the undecorated client so health checks do not emit backend-call telemetry.
	monitor, err := health.NewMonitor(
		cfg.BackendClient, cfg.BackendRegistry.List(context.Background()), *cfg.HealthMonitorConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create health monitor: %w", err)
	}

	if err := monitor.Start(context.Background()); err != nil {
		slog.Warn("failed to start health monitor, disabling health monitoring", "error", err)
		return nil, nil, nil
	}

	slog.Info("health monitoring enabled",
		"check_interval", cfg.HealthMonitorConfig.CheckInterval,
		"unhealthy_threshold", cfg.HealthMonitorConfig.UnhealthyThreshold,
		"timeout", cfg.HealthMonitorConfig.Timeout,
		"degraded_threshold", cfg.HealthMonitorConfig.DegradedThreshold)
	return monitor, monitor, nil
}

// BackendHealth returns the core's backend health reporter, or nil when health monitoring is
// disabled. The core owns the monitor's lifecycle (build/start in New, stop in Close); the
// transport layer uses this only to report on or sync backend health.
func (c *coreVMCP) BackendHealth() health.Reporter {
	if c.healthMonitor == nil {
		return nil
	}
	return c.healthMonitor
}

// ListTools health-filters the registry, aggregates backend capabilities on
// demand, appends the composite tools reachable in the current view, and returns
// only the subset identity is admitted to call (the admission seam — the same
// decision CallTool enforces).
//
// A nil identity is treated as anonymous, consistent with
// session/types.ShouldAllowAnonymous — bound-session caller enforcement
// (ErrNilCaller/ErrUnauthorizedCaller) lives in the Serve session layer, not
// here. identity is never logged.
func (c *coreVMCP) ListTools(ctx context.Context, identity *auth.Identity) ([]vmcp.Tool, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}
	return c.admission.FilterTools(ctx, identity, c.advertisedTools(agg))
}

// ListResources health-filters the registry, aggregates resources on demand, and
// returns only those identity is admitted to read.
func (c *coreVMCP) ListResources(ctx context.Context, identity *auth.Identity) ([]vmcp.Resource, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}
	return c.admission.FilterResources(ctx, identity, agg.Resources)
}

// ListResourceTemplates health-filters the registry, aggregates resource
// templates on demand, and returns only those identity is admitted to read.
//
// Resource templates carry no admission entity of their own: the URI template is
// treated as the resource id, reusing the exact resource-read filter (the same
// per-item skip-on-error, log-and-continue decision ListResources applies). A
// template whose URI-template string is denied is withheld.
func (c *coreVMCP) ListResourceTemplates(
	ctx context.Context, identity *auth.Identity,
) ([]vmcp.ResourceTemplate, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}
	return c.filterResourceTemplates(ctx, identity, agg.ResourceTemplates)
}

// filterResourceTemplates applies the resource-read admission filter to templates
// by projecting each template's URI template onto a vmcp.Resource and reusing
// admission.FilterResources, so templates and resources share one decision path.
// The surviving URI templates select which templates are returned.
func (c *coreVMCP) filterResourceTemplates(
	ctx context.Context, identity *auth.Identity, templates []vmcp.ResourceTemplate,
) ([]vmcp.ResourceTemplate, error) {
	if len(templates) == 0 {
		return []vmcp.ResourceTemplate{}, nil
	}
	proxies := make([]vmcp.Resource, len(templates))
	for i := range templates {
		proxies[i] = vmcp.Resource{URI: templates[i].URITemplate, BackendID: templates[i].BackendID}
	}
	allowedProxies, err := c.admission.FilterResources(ctx, identity, proxies)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(allowedProxies))
	for i := range allowedProxies {
		allowed[allowedProxies[i].URI] = struct{}{}
	}
	filtered := make([]vmcp.ResourceTemplate, 0, len(templates))
	for i := range templates {
		if _, ok := allowed[templates[i].URITemplate]; ok {
			filtered = append(filtered, templates[i])
		}
	}
	return filtered, nil
}

// ListPrompts health-filters the registry, aggregates prompts on demand, and
// returns only those identity is admitted to get.
func (c *coreVMCP) ListPrompts(ctx context.Context, identity *auth.Identity) ([]vmcp.Prompt, error) {
	agg, err := c.aggregatedView(ctx)
	if err != nil {
		return nil, err
	}
	return c.admission.FilterPrompts(ctx, identity, agg.Prompts)
}

// LookupTool resolves an advertised tool name (incl. composite tools) to its
// capability without invoking it. It delegates to ListTools, so it applies the
// same health/advertising AND admission view: a name that is unknown, unadvertised,
// or denied to identity returns vmcp.ErrNotFound (a denied tool is never resolved).
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
// reading it. Delegates to ListResources, so a URI that is unknown, unadvertised,
// or denied to identity returns vmcp.ErrNotFound.
//
// A URI backed by a resource TEMPLATE resolves too: after the concrete-resource
// miss, the URI is checked against the admission-filtered resource templates
// (the same view ListResourceTemplates advertises) — an exact template-string
// match (the form a resources/subscribe or completion ref/resource carries) or
// a template-expansion match. This keeps subscribe's admission aligned with
// read: any URI ReadResource would serve is a valid subscription target.
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

	templates, err := c.ListResourceTemplates(ctx, identity)
	if err != nil {
		return nil, err
	}
	for i := range templates {
		if templates[i].URITemplate == uri || templateMatches(templates[i].URITemplate, uri) {
			// Project the template onto a resource (the same projection
			// filterResourceTemplates uses) so lookup and list agree on the shape.
			return &vmcp.Resource{
				URI:         templates[i].URITemplate,
				Name:        templates[i].Name,
				Description: templates[i].Description,
				MimeType:    templates[i].MimeType,
				BackendID:   templates[i].BackendID,
			}, nil
		}
	}

	return nil, fmt.Errorf("%w: resource %q", vmcp.ErrNotFound, uri)
}

// templateMatches reports whether the RFC 6570 URI template matches uri. An
// invalid template string reports no match (it was already logged and skipped
// at routing-table construction).
func templateMatches(tmplStr, uri string) bool {
	tmpl, err := uritemplate.New(tmplStr)
	if err != nil {
		return false
	}
	return tmpl.Match(uri) != nil
}

// LookupPrompt resolves an advertised prompt name to its capability without
// retrieving it. Delegates to ListPrompts, so a name that is unknown, unadvertised,
// or denied to identity returns vmcp.ErrNotFound.
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

// ListBackends returns the backends visible to identity. In admin mode
// (filterUnauthorized=false) it returns the full group registry as-is — no
// aggregation, no admission, no health filter. In authorized mode it returns only
// the backends the identity is admitted at least one capability on. See the [VMCP]
// interface contract for the full semantics.
func (c *coreVMCP) ListBackends(
	ctx context.Context, identity *auth.Identity, filterUnauthorized bool,
) ([]vmcp.Backend, error) {
	all := c.backendRegistry.List(ctx)
	if !filterUnauthorized {
		return all, nil
	}
	// An empty group has nothing to authorize: return an empty (non-nil) set rather
	// than aggregating, which would surface the aggregator's "no backends returned
	// capabilities" sentinel as an error and make the authorized view disagree with
	// the admin view for the same empty group. A non-empty group whose backends are
	// all unreachable still errors (consistent with ListTools) — that is a live
	// failure, not an empty answer.
	if len(all) == 0 {
		return []vmcp.Backend{}, nil
	}
	return c.authorizedBackends(ctx, identity, all)
}

// LookupBackend resolves a single backend id in the authorized view, mirroring
// LookupTool: it delegates to ListBackends(ctx, identity, true) and returns
// vmcp.ErrNotFound for an id that view does not contain (unknown, out-of-group, or
// unauthorized).
func (c *coreVMCP) LookupBackend(
	ctx context.Context, identity *auth.Identity, backendID string,
) (*vmcp.Backend, error) {
	backends, err := c.ListBackends(ctx, identity, true)
	if err != nil {
		return nil, err
	}
	for i := range backends {
		if backends[i].ID == backendID {
			backend := backends[i]
			return &backend, nil
		}
	}
	return nil, fmt.Errorf("%w: backend %q", vmcp.ErrNotFound, backendID)
}

// InvalidateCapabilityCache implements VMCP.InvalidateCapabilityCache. It
// type-asserts the configured aggregator to aggregator.CacheInvalidator and
// delegates; when the aggregator does not implement it, the call is a WARN-logged
// no-op — not a silent one — because a caller relying on invalidation (the
// list_changed resync path) needs to know the request had no effect (go-style:
// no silent no-ops).
func (c *coreVMCP) InvalidateCapabilityCache() {
	invalidator, ok := c.aggregator.(aggregator.CacheInvalidator)
	if !ok {
		slog.Warn("capability cache invalidation requested but the configured aggregator does not support it",
			"aggregatorType", fmt.Sprintf("%T", c.aggregator))
		return
	}
	invalidator.InvalidateAll()
}

// Close stops the workflow state store's cleanup goroutine. It is idempotent:
// the underlying Stop closes a channel that cannot be closed twice, so the work
// is guarded by sync.Once and subsequent calls return nil.
func (c *coreVMCP) Close() error {
	c.closeOnce.Do(func() {
		c.stopStore()
		if c.healthMonitor != nil {
			if err := c.healthMonitor.Stop(); err != nil {
				slog.Warn("failed to stop health monitor", "error", err)
			}
		}
		unregisterHealthOnError(c.unregisterBackendHealth)
	})
	return nil
}

// aggregatedView health-filters the backend registry and aggregates capabilities
// on demand. The core holds no per-session capability cache (the core filters,
// Serve caches): every List/Lookup/Call re-derives the advertised view, so a
// lookup-then-call flow aggregates twice. This is intentional — caching is the
// Serve layer's responsibility, not the core's (vmcp anti-patterns #8/#9).
func (c *coreVMCP) aggregatedView(ctx context.Context) (*aggregator.AggregatedCapabilities, error) {
	return c.aggregateBackends(ctx, filterHealthyBackends(c.backendRegistry.List(ctx), c.health))
}

// aggregateBackends aggregates capabilities across the given backends. The caller
// decides the backend set: aggregatedView passes the health-filtered subset (the
// data path), while authorizedBackends passes the full registry (backend
// visibility, per #5741: health is a status, not a visibility filter). It exists so
// both share one aggregation error-wrap. Unreachable backends fail their live
// capability query and simply contribute nothing.
func (c *coreVMCP) aggregateBackends(
	ctx context.Context, backends []vmcp.Backend,
) (*aggregator.AggregatedCapabilities, error) {
	agg, err := c.aggregator.AggregateCapabilities(ctx, backends)
	if err != nil {
		return nil, fmt.Errorf("capability aggregation failed: %w", err)
	}
	return agg, nil
}

// authorizedBackends returns the subset of all on which identity is admitted at
// least one capability. It aggregates over the full set (no health filter — #5741),
// runs the same admission seam List{Tools,Resources,Prompts} use, and unions the
// surviving capabilities' BackendIDs. Composite tools are excluded (agg.Tools, not
// advertisedTools): they are workflows with no single backend, so they cannot make
// a backend visible. Callers guard the empty-group case before calling this (see
// ListBackends) so an empty group never reaches the aggregator's "no backends"
// sentinel here.
func (c *coreVMCP) authorizedBackends(
	ctx context.Context, identity *auth.Identity, all []vmcp.Backend,
) ([]vmcp.Backend, error) {
	agg, err := c.aggregateBackends(ctx, all)
	if err != nil {
		return nil, err
	}

	tools, err := c.admission.FilterTools(ctx, identity, agg.Tools)
	if err != nil {
		return nil, err
	}
	resources, err := c.admission.FilterResources(ctx, identity, agg.Resources)
	if err != nil {
		return nil, err
	}
	prompts, err := c.admission.FilterPrompts(ctx, identity, agg.Prompts)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]struct{})
	for i := range tools {
		allowed[tools[i].BackendID] = struct{}{}
	}
	for i := range resources {
		allowed[resources[i].BackendID] = struct{}{}
	}
	for i := range prompts {
		allowed[prompts[i].BackendID] = struct{}{}
	}

	out := make([]vmcp.Backend, 0, len(allowed))
	for i := range all {
		if _, ok := allowed[all[i].ID]; ok {
			out = append(out, all[i])
		}
	}
	return out, nil
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

// validateConfig checks New's required inputs and the elicitation contract,
// keeping New itself within the cyclomatic-complexity budget. Returns a
// vmcp.ErrInvalidConfig-wrapped error on the first violation.
func validateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("%w: nil core config", vmcp.ErrInvalidConfig)
	}
	if cfg.Aggregator == nil {
		return fmt.Errorf("%w: Aggregator is required", vmcp.ErrInvalidConfig)
	}
	if cfg.Router == nil {
		return fmt.Errorf("%w: Router is required", vmcp.ErrInvalidConfig)
	}
	if cfg.BackendRegistry == nil {
		return fmt.Errorf("%w: BackendRegistry is required", vmcp.ErrInvalidConfig)
	}
	if cfg.BackendClient == nil {
		return fmt.Errorf("%w: BackendClient is required", vmcp.ErrInvalidConfig)
	}
	// Fail fast on the elicitation contract: a workflow with an elicitation step run
	// against a nil Elicitation would nil-deref deep inside the engine at call time
	// (the legacy server.New path always wired a non-nil adapter). Reject it at
	// construction so the misconfiguration surfaces at startup, not mid-workflow.
	if cfg.Elicitation == nil && workflowsRequireElicitation(cfg.WorkflowDefs) {
		return fmt.Errorf(
			"%w: Elicitation is required when a workflow contains an elicitation step", vmcp.ErrInvalidConfig)
	}
	return nil
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

// workflowsRequireElicitation reports whether any workflow definition contains an
// elicitation step, directly or as a forEach inner step. New uses it to enforce
// that a non-nil Elicitation is supplied whenever a workflow could request it.
func workflowsRequireElicitation(defs map[string]*composer.WorkflowDefinition) bool {
	for _, def := range defs {
		for i := range def.Steps {
			step := &def.Steps[i]
			if step.Type == composer.StepTypeElicitation {
				return true
			}
			if step.Type == composer.StepTypeForEach && step.InnerStep != nil &&
				step.InnerStep.Type == composer.StepTypeElicitation {
				return true
			}
		}
	}
	return false
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
// This filtering previously had a second copy in the discovery middleware on the
// legacy server.New path. That path and its copy were removed in Phase 3 (#5445),
// so this is now the single source of truth for backend health filtering.
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
