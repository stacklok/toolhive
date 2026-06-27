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
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	vmcprouter "github.com/stacklok/toolhive/pkg/vmcp/router"
	vmcpserver "github.com/stacklok/toolhive/pkg/vmcp/server"
)

// capabilityCacheTTL is the per-identity capability cache TTL for the caching
// aggregator. Matches server.New so both paths use the same caching window.
const capabilityCacheTTL = 30 * time.Second

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
//   - Discovered mode (vmcpCfg.OutgoingAuth.Source == "discovered"): calls
//     backendregistry.NewKubernetesBackendRegistry. WithRESTConfig overrides the
//     default in-cluster config.
//   - Dynamic mode (all others): discovers via the local groups manager.
//
// For the Kubernetes discovered mode, callers SHOULD provide WithBackendRegistry so
// the same informer cache is shared with BuildServerConfig.
func BuildCore(ctx context.Context, cfg *vmcpconfig.Config, opts ...Option) (core.VMCP, func(), error) {
	if cfg == nil {
		return nil, noop, fmt.Errorf("%w: nil vmcp config", vmcp.ErrInvalidConfig)
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

	// Build conflict resolver for the aggregator.
	conflictResolver, err := aggregator.NewConflictResolver(cfg.Aggregation)
	if err != nil {
		return nil, noop, fmt.Errorf("failed to create conflict resolver: %w", err)
	}

	// Wire the provided telemetry provider into the aggregator if available.
	var tracerProvider trace.TracerProvider
	if o.telemetryProvider != nil {
		tracerProvider = o.telemetryProvider.TracerProvider()
	}
	agg := aggregator.NewDefaultAggregator(backendClient, conflictResolver, cfg.Aggregation, tracerProvider)
	// The caching aggregator collapses repeated capability sweeps within a short
	// TTL, keyed on identity + forwarded credentials (no cross-principal leakage).
	cachedAgg := aggregator.NewCachingAggregator(agg, capabilityCacheTTL)

	// Empty session router for composite-tool workflow validation; the core does not
	// route through it at request time (per-call SessionRouter is built there).
	rtr := vmcprouter.NewSessionRouter(&vmcp.RoutingTable{})

	// Derive health monitor config from operational settings.
	healthMonitorCfg, err := deriveHealthMonitorConfig(cfg)
	if err != nil {
		return nil, noop, err
	}

	// Convert composite tool config to workflow definitions.
	workflowDefs, err := vmcpserver.ConvertConfigToWorkflowDefinitions(cfg.CompositeTools)
	if err != nil {
		return nil, noop, fmt.Errorf("failed to convert composite tool definitions: %w", err)
	}
	if len(workflowDefs) > 0 {
		slog.Info("loaded composite tool workflow definitions", "count", len(workflowDefs))
	}

	// Build Cedar authz config — feeds the core admission seam (nil = allow-all).
	authzCfg, err := deriveAuthzConfig(cfg)
	if err != nil {
		return nil, noop, err
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
		return nil, noop, fmt.Errorf("failed to create core VMCP: %w", err)
	}

	return coreVMCP, func() { _ = coreVMCP.Close() }, nil
}

// resolveBackendRegistryForCore returns the backend registry BuildCore should use.
func resolveBackendRegistryForCore(ctx context.Context, cfg *vmcpconfig.Config, o *options) (vmcp.BackendRegistry, error) {
	if o.backendRegistry != nil {
		slog.Info("using pre-built backend registry from option")
		return o.backendRegistry, nil
	}
	if len(cfg.Backends) > 0 {
		return buildStaticBackendRegistry(ctx, cfg)
	}
	if cfg.OutgoingAuth != nil && cfg.OutgoingAuth.Source == "discovered" {
		return buildKubernetesBackendRegistry(ctx, cfg, o)
	}
	return buildDynamicBackendRegistry(ctx, cfg)
}

// buildStaticBackendRegistry builds an immutable backend registry from the
// pre-configured backends in vmcpCfg.
func buildStaticBackendRegistry(ctx context.Context, cfg *vmcpconfig.Config) (vmcp.BackendRegistry, error) {
	slog.Info("static mode: using pre-configured backends", "count", len(cfg.Backends))

	outgoingRegistry, err := authfactory.NewOutgoingAuthRegistry(ctx, &env.OSReader{})
	if err != nil {
		return nil, fmt.Errorf("failed to create outgoing auth registry: %w", err)
	}
	backendClient, err := vmcpclient.NewHTTPBackendClient(outgoingRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend client: %w", err)
	}

	discoverer := aggregator.NewUnifiedBackendDiscovererWithStaticBackends(
		cfg.Backends,
		cfg.OutgoingAuth,
		cfg.Group,
		readHeaderForwardFromEnv(os.Environ()),
	)
	backends, err := runDiscovery(ctx, cfg.Group, discoverer, backendClient)
	if err != nil {
		return nil, err
	}
	return vmcp.NewDynamicRegistry(backends), nil
}

// buildKubernetesBackendRegistry builds a live Kubernetes-backed registry using
// backendregistry.NewKubernetesBackendRegistry. The registry starts empty and is
// populated by the watcher goroutine, which is bound to ctx.
func buildKubernetesBackendRegistry(ctx context.Context, cfg *vmcpconfig.Config, o *options) (vmcp.BackendRegistry, error) {
	slog.Info("Kubernetes discovered mode: building backend registry")

	namespace := os.Getenv("VMCP_NAMESPACE")
	if namespace == "" {
		return nil, fmt.Errorf("VMCP_NAMESPACE environment variable not set")
	}

	var regOpts []backendregistry.Option
	if o.restConfig != nil {
		regOpts = append(regOpts, backendregistry.WithRESTConfig(o.restConfig))
	}

	// The watcher return value is intentionally discarded: for this code path the
	// caller did not provide WithBackendRegistry, so there is no server.Watcher to
	// wire into BuildServerConfig. If readiness gating on cache sync is required
	// the caller should use WithBackendRegistry instead.
	reg, _, err := backendregistry.NewKubernetesBackendRegistry(ctx, namespace, cfg.Group, regOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to build Kubernetes backend registry: %w", err)
	}
	slog.Info("Kubernetes backend registry started", "namespace", namespace, "group", cfg.Group)
	return reg, nil
}

// buildDynamicBackendRegistry discovers backends at runtime via the local groups manager.
func buildDynamicBackendRegistry(ctx context.Context, cfg *vmcpconfig.Config) (vmcp.BackendRegistry, error) {
	slog.Info("dynamic mode: initialising group manager for backend discovery")

	// EnsureDefaultGroupExists is a no-op in Kubernetes; if the group does not exist
	// Discover returns ErrGroupNotFound, which runDiscovery treats as zero backends.
	if err := migration.EnsureDefaultGroupExists(); err != nil {
		return nil, fmt.Errorf("failed to ensure default group exists: %w", err)
	}

	outgoingRegistry, err := authfactory.NewOutgoingAuthRegistry(ctx, &env.OSReader{})
	if err != nil {
		return nil, fmt.Errorf("failed to create outgoing auth registry: %w", err)
	}
	backendClient, err := vmcpclient.NewHTTPBackendClient(outgoingRegistry)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend client: %w", err)
	}

	groupsManager, err := groups.NewManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create groups manager: %w", err)
	}

	discoverer, err := aggregator.NewBackendDiscoverer(ctx, groupsManager, cfg.OutgoingAuth)
	if err != nil {
		return nil, fmt.Errorf("failed to create backend discoverer: %w", err)
	}

	backends, err := runDiscovery(ctx, cfg.Group, discoverer, backendClient)
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
	_ vmcp.BackendClient,
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
