// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/migration"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	authfactory "github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	"github.com/stacklok/toolhive/pkg/vmcp/backendregistry"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/codemode"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	vmcpratelimit "github.com/stacklok/toolhive/pkg/vmcp/ratelimit"
	ratelimitfactory "github.com/stacklok/toolhive/pkg/vmcp/ratelimit/factory"
	vmcprouter "github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
)

// capabilityCacheTTL is the per-identity capability cache TTL for the caching
// aggregator. Matches server.New so both paths use the same caching window.
const capabilityCacheTTL = 30 * time.Second

// errDiscoveredModeRequiresRegistry is returned by both BuildCore and
// BuildServerConfig when the "discovered" (Kubernetes) outgoingAuth source is used
// without WithBackendRegistry. Sharing one error keeps the two entry points symmetric:
// the caller must build the Kubernetes registry once (via
// backendregistry.NewKubernetesBackendRegistry) and pass it to both, so a single
// informer cache and readiness handle are shared rather than two started independently.
var errDiscoveredModeRequiresRegistry = fmt.Errorf(
	"%w: WithBackendRegistry is required for the %q outgoingAuth source; build the registry once "+
		"with backendregistry.NewKubernetesBackendRegistry and pass it to both BuildCore and "+
		"BuildServerConfig to avoid starting two Kubernetes watchers",
	vmcp.ErrInvalidConfig, vmcpconfig.OutgoingAuthSourceDiscovered,
)

// BuildCore derives core.Config from vmcpCfg (plus opts) and calls core.New,
// returning the assembled VMCP for decoration.
//
// BuildCore does NOT return a *server.ServerConfig — the transport config is an
// independent derivation (see BuildServerConfig). The cleanup func calls
// coreVMCP.Close(), which is idempotent; calling it after server.Serve (which also
// registers a Close via its shutdownFuncs) is therefore safe.
//
// # Backend registry
//
// When WithBackendRegistry is provided, it is used directly and no backend discovery
// is performed. When absent, BuildCore resolves the registry from vmcpCfg:
//   - Static mode (vmcpCfg.Backends non-empty): builds an immutable DynamicRegistry.
//   - Discovered mode (vmcpCfg.OutgoingAuth.Source == "discovered"): callers MUST
//     provide WithBackendRegistry — BuildCore returns an error otherwise. This mirrors
//     BuildServerConfig and prevents two independent Kubernetes informer caches (one
//     per Build call) with no shared readiness handle. Build the registry once with
//     backendregistry.NewKubernetesBackendRegistry and pass it to both Build calls.
//   - Dynamic mode (all others): discovers via the local groups manager.
func BuildCore(ctx context.Context, cfg *vmcpconfig.Config, opts ...Option) (core.VMCP, func(), error) {
	if cfg == nil {
		return nil, noop, fmt.Errorf("%w: nil vmcp config", vmcp.ErrInvalidConfig)
	}
	// Validate before assembling: an embedder building a config programmatically must
	// get a loud startup error rather than, say, a nil IncomingAuth silently defaulting
	// to anonymous + allow-all. The CLI/operator path validates at load time too; the
	// second pass here is cheap and closes the gap for the public assembly contract.
	if err := vmcpconfig.NewValidator().Validate(cfg); err != nil {
		return nil, noop, fmt.Errorf("invalid vmcp config: %w", err)
	}
	o := applyOptions(opts)

	// Build outgoing auth registry and backend client for capability aggregation.
	outgoingRegistry, err := authfactory.NewOutgoingAuthRegistry(ctx, &env.OSReader{})
	if err != nil {
		return nil, noop, fmt.Errorf("failed to create outgoing auth registry: %w", err)
	}
	backendClient, err := vmcpclient.NewHTTPBackendClient(outgoingRegistry)
	if err != nil {
		return nil, noop, fmt.Errorf("failed to create backend client: %w", err)
	}

	// Resolve backend registry.
	backendReg, err := resolveBackendRegistryForCore(ctx, cfg, o)
	if err != nil {
		return nil, noop, err
	}

	// Derive the core collaborators from cfg and assemble the domain core.
	coreVMCP, err := buildDomainCore(cfg, o, backendReg, backendClient)
	if err != nil {
		return nil, noop, err
	}

	// Apply the config-driven core decorators (rate limiter, then code mode). On error
	// the freshly-built core is closed so it does not leak.
	decoratedCore, limiterCleanup, err := applyCoreDecorators(ctx, coreVMCP, cfg)
	if err != nil {
		_ = coreVMCP.Close()
		return nil, noop, err
	}
	coreVMCP = decoratedCore

	cleanup := func() {
		// Close is idempotent (sync.Once) and currently always returns nil, but log any
		// error so a future Close that releases real resources is not silently dropped —
		// mirroring the cleanup funcs in BuildServerConfig.
		if err := coreVMCP.Close(); err != nil {
			slog.Warn("failed to close core VMCP", "error", err)
		}
		if limiterCleanup != nil {
			if err := limiterCleanup(context.Background()); err != nil {
				slog.Warn("failed to close rate limiter", "error", err)
			}
		}
	}
	return coreVMCP, cleanup, nil
}

// buildDomainCore derives the aggregator, router, health-monitor config, workflow
// definitions, and authz config from cfg (plus the injected telemetry provider) and
// assembles the domain core via core.New. backendClient and backendReg are the
// collaborators BuildCore already constructed.
func buildDomainCore(
	cfg *vmcpconfig.Config, o *options, backendReg vmcp.BackendRegistry, backendClient vmcp.BackendClient,
) (core.VMCP, error) {
	conflictResolver, err := aggregator.NewConflictResolver(cfg.Aggregation)
	if err != nil {
		return nil, fmt.Errorf("failed to create conflict resolver: %w", err)
	}

	// Wire the provided telemetry provider into the aggregator if available.
	var tracerProvider trace.TracerProvider
	if o.telemetryProvider != nil {
		tracerProvider = o.telemetryProvider.TracerProvider()
	}
	agg := aggregator.NewDefaultAggregator(backendClient, conflictResolver, cfg.Aggregation, tracerProvider)
	// The caching aggregator collapses repeated capability sweeps within a short TTL,
	// keyed on identity + forwarded credentials (no cross-principal leakage).
	cachedAgg := aggregator.NewCachingAggregator(agg, capabilityCacheTTL)

	// Empty session router for composite-tool workflow validation; the core does not
	// route through it at request time (per-call SessionRouter is built there).
	rtr := vmcprouter.NewSessionRouter(&vmcp.RoutingTable{})

	healthMonitorCfg, err := deriveHealthMonitorConfig(cfg)
	if err != nil {
		return nil, err
	}

	workflowDefs, err := vmcpserver.ConvertConfigToWorkflowDefinitions(cfg.CompositeTools)
	if err != nil {
		return nil, fmt.Errorf("failed to convert composite tool definitions: %w", err)
	}
	if len(workflowDefs) > 0 {
		slog.Info("loaded composite tool workflow definitions", "count", len(workflowDefs))
	}

	// Cedar authz config — feeds the core admission seam (nil = allow-all).
	authzCfg, err := deriveAuthzConfig(cfg)
	if err != nil {
		return nil, err
	}

	coreVMCP, err := core.New(&core.Config{
		Aggregator:          cachedAgg,
		Router:              rtr,
		BackendRegistry:     backendReg,
		BackendClient:       backendClient,
		WorkflowDefs:        workflowDefs,
		Authz:               authzCfg,
		ServerName:          cfg.Name,
		TelemetryProvider:   o.telemetryProvider,
		AuditConfig:         cfg.Audit,
		HealthMonitorConfig: healthMonitorCfg,
		Elicitation:         o.elicitation,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create core VMCP: %w", err)
	}
	return coreVMCP, nil
}

// applyCoreDecorators wraps coreVMCP with the config-driven core decorators and
// returns the decorated core plus the rate limiter's cleanup (nil when rate limiting
// is disabled). Decorator order mirrors the legacy server.New path:
//
//	core.New → rate-limit decorator → code-mode decorator
//
// so the rate limiter sits at the CallTool seam BELOW code mode and any Serve-layer
// optimizer, keying buckets by the resolved backend tool name (#5522). Each decorator
// delegates Close to the inner core, so closing the returned core releases the chain;
// the limiter's own cleanup (its Redis connection) is returned separately.
func applyCoreDecorators(
	ctx context.Context, coreVMCP core.VMCP, cfg *vmcpconfig.Config,
) (core.VMCP, func(context.Context) error, error) {
	limiter, limiterCleanup, err := ratelimitfactory.NewLimiter(ctx, ratelimitfactory.Config{
		Namespace:      vmcpNamespace(),
		ServerName:     cfg.Name,
		RateLimiting:   cfg.RateLimiting,
		SessionStorage: cfg.SessionStorage,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create rate limiter: %w", err)
	}
	if limiter != nil {
		coreVMCP = vmcpratelimit.NewDecorator(coreVMCP, limiter)
	}

	if codeModeCfg := codemode.FromConfig(cfg.CodeMode); codeModeCfg != nil {
		coreVMCP = codemode.NewDecorator(coreVMCP, codeModeCfg)
	}

	return coreVMCP, limiterCleanup, nil
}

// resolveBackendRegistryForCore returns the backend registry BuildCore should use as a
// standalone primitive (no Builder). The branch order matches
// resolveBackendRegistryForServerConfig exactly — injected, then discovered, then
// static, then dynamic — so the two primitives never disagree for a given config; the
// validator additionally rejects the contradictory Backends + discovered combination.
//
// The discovered source requires an injected registry here (it does NOT build one):
// calling both primitives standalone in discovered mode would otherwise start two K8s
// informers. The Builder, which owns single construction, uses resolveBackendRegistry
// instead (which does build the discovered registry, once).
func resolveBackendRegistryForCore(ctx context.Context, cfg *vmcpconfig.Config, o *options) (vmcp.BackendRegistry, error) {
	if o.backendRegistry != nil {
		slog.Info("using pre-built backend registry from option")
		return o.backendRegistry, nil
	}
	if isDiscoveredSource(cfg) {
		return nil, errDiscoveredModeRequiresRegistry
	}
	if len(cfg.Backends) > 0 {
		return buildStaticBackendRegistry(ctx, cfg)
	}
	return buildDynamicBackendRegistry(ctx, cfg)
}

// resolveBackendRegistry constructs (or returns the injected) backend registry for ALL
// modes. The Builder uses it to construct the registry exactly once and inject the same
// instance into both derivations, so core aggregation and the session manager operate
// on one snapshot (satisfying the Builder's "construct once" contract). Returns the
// registry and, for the discovered source, a readiness watcher (nil for static/dynamic).
func resolveBackendRegistry(
	ctx context.Context, cfg *vmcpconfig.Config, o *options,
) (vmcp.BackendRegistry, vmcpserver.Watcher, error) {
	if o.backendRegistry != nil {
		return o.backendRegistry, o.watcher, nil
	}
	if isDiscoveredSource(cfg) {
		return buildDiscoveredRegistry(ctx, cfg)
	}
	if len(cfg.Backends) > 0 {
		reg, err := buildStaticBackendRegistry(ctx, cfg)
		return reg, nil, err
	}
	reg, err := buildDynamicBackendRegistry(ctx, cfg)
	return reg, nil, err
}

// buildDiscoveredRegistry builds the live Kubernetes backend registry + readiness
// watcher for the discovered source. The watcher goroutine is bound to ctx and is
// released when ctx is cancelled (no separate cleanup).
func buildDiscoveredRegistry(ctx context.Context, cfg *vmcpconfig.Config) (vmcp.BackendRegistry, vmcpserver.Watcher, error) {
	namespace := os.Getenv("VMCP_NAMESPACE")
	if namespace == "" {
		return nil, nil, fmt.Errorf("VMCP_NAMESPACE environment variable not set")
	}
	reg, watcher, err := backendregistry.NewKubernetesBackendRegistry(ctx, namespace, cfg.Group)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build Kubernetes backend registry: %w", err)
	}
	return reg, watcher, nil
}

// buildStaticBackendRegistry builds an immutable backend registry from the
// pre-configured backends in vmcpCfg.
func buildStaticBackendRegistry(ctx context.Context, cfg *vmcpconfig.Config) (vmcp.BackendRegistry, error) {
	slog.Info("static mode: using pre-configured backends", "count", len(cfg.Backends))

	discoverer := aggregator.NewUnifiedBackendDiscovererWithStaticBackends(
		cfg.Backends,
		cfg.OutgoingAuth,
		cfg.Group,
		readHeaderForwardFromEnv(os.Environ()),
	)
	backends, err := runDiscovery(ctx, cfg.Group, discoverer)
	if err != nil {
		return nil, err
	}
	return vmcp.NewDynamicRegistry(backends), nil
}

// buildDynamicBackendRegistry discovers backends at runtime via the local groups manager.
func buildDynamicBackendRegistry(ctx context.Context, cfg *vmcpconfig.Config) (vmcp.BackendRegistry, error) {
	slog.Info("dynamic mode: initialising group manager for backend discovery")

	// EnsureDefaultGroupExists is a no-op in Kubernetes; if the group does not exist
	// Discover returns ErrGroupNotFound, which runDiscovery treats as zero backends.
	if err := migration.EnsureDefaultGroupExists(); err != nil {
		return nil, fmt.Errorf("failed to ensure default group exists: %w", err)
	}

	groupsManager, err := groups.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create groups manager: %w", err)
	}

	discoverer, err := aggregator.NewBackendDiscoverer(ctx, groupsManager, cfg.OutgoingAuth)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend discoverer: %w", err)
	}

	backends, err := runDiscovery(ctx, cfg.Group, discoverer)
	if err != nil {
		return nil, err
	}
	return vmcp.NewDynamicRegistry(backends), nil
}

// runDiscovery calls Discover on the provided discoverer and handles zero-backends
// and Kubernetes ErrGroupNotFound.
func runDiscovery(
	ctx context.Context,
	groupRef string,
	discoverer aggregator.BackendDiscoverer,
) ([]vmcp.Backend, error) {
	slog.Info("discovering backends", "group", groupRef)
	backends, err := discoverer.Discover(ctx, groupRef)
	if err != nil {
		// In Kubernetes mode the MCPGroup CRD may not exist yet. Treat a missing
		// group as zero backends so vMCP can start and serve once backends appear.
		if runtime.IsKubernetesRuntime() && errors.Is(err, groups.ErrGroupNotFound) {
			slog.Warn("group not found — vMCP will start with no backends", "group", groupRef)
			return []vmcp.Backend{}, nil
		}
		return nil, fmt.Errorf("failed to discover backends: %w", err)
	}
	if len(backends) == 0 {
		slog.Warn("no backends discovered — vMCP will start with no backends", "group", groupRef)
		return []vmcp.Backend{}, nil
	}
	slog.Info("discovered backends", "count", len(backends))
	return backends, nil
}

// deriveHealthMonitorConfig extracts the health monitor configuration from the
// operational settings in vmcpCfg. Returns nil when health monitoring is not configured.
func deriveHealthMonitorConfig(cfg *vmcpconfig.Config) (*health.MonitorConfig, error) {
	if cfg.Operational == nil ||
		cfg.Operational.FailureHandling == nil ||
		cfg.Operational.FailureHandling.HealthCheckInterval <= 0 {
		return nil, nil
	}

	fh := cfg.Operational.FailureHandling
	if fh.UnhealthyThreshold < 1 {
		return nil, fmt.Errorf(
			"invalid health check configuration: unhealthy threshold must be >= 1, got %d",
			fh.UnhealthyThreshold,
		)
	}

	defaults := health.DefaultConfig()
	healthCheckTimeout := defaults.Timeout
	if fh.HealthCheckTimeout > 0 {
		healthCheckTimeout = time.Duration(fh.HealthCheckTimeout)
	}

	mc := &health.MonitorConfig{
		CheckInterval:      time.Duration(fh.HealthCheckInterval),
		UnhealthyThreshold: fh.UnhealthyThreshold,
		Timeout:            healthCheckTimeout,
		DegradedThreshold:  defaults.DegradedThreshold,
	}

	if fh.CircuitBreaker != nil {
		cb := fh.CircuitBreaker
		mc.CircuitBreaker = &health.CircuitBreakerConfig{
			Enabled:          cb.Enabled,
			FailureThreshold: cb.FailureThreshold,
			Timeout:          time.Duration(cb.Timeout),
		}
		if cb.Enabled {
			slog.Info("circuit breaker enabled",
				"failure_threshold", cb.FailureThreshold,
				"timeout", time.Duration(cb.Timeout),
			)
		}
	}

	slog.Info("health monitoring configured from operational settings")
	return mc, nil
}

// deriveAuthzConfig builds the Cedar authz config from vmcpCfg.IncomingAuth.Authz.
// Returns nil (allow-all) when IncomingAuth or its Authz field is nil.
func deriveAuthzConfig(cfg *vmcpconfig.Config) (*authz.Config, error) {
	if cfg.IncomingAuth == nil {
		return nil, nil
	}
	result, err := authfactory.BuildAuthzConfig(cfg.IncomingAuth.Authz)
	if err != nil {
		return nil, fmt.Errorf("failed to build authz config: %w", err)
	}
	return result, nil
}

// noop is the zero cleanup function returned on error paths before any resources
// are acquired.
var noop = func() {}
