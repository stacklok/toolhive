// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package server implements the Virtual MCP Server that aggregates
// multiple backend MCP servers into a unified interface.
//
// The server exposes aggregated capabilities (tools, resources, prompts)
// and routes incoming MCP protocol requests to appropriate backend workloads.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"

	tcredis "github.com/stacklok/toolhive-core/redis"
	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	asrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/bodylimit"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/recovery"
	"github.com/stacklok/toolhive/pkg/telemetry"
	transportmiddleware "github.com/stacklok/toolhive/pkg/transport/middleware"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/discovery"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	vmcpstatus "github.com/stacklok/toolhive/pkg/vmcp/status"
)

const (
	// defaultReadHeaderTimeout prevents slowloris attacks by limiting time to read request headers.
	defaultReadHeaderTimeout = 10 * time.Second

	// defaultReadTimeout is the maximum duration for reading the entire request, including body.
	defaultReadTimeout = 30 * time.Second

	// defaultWriteTimeout is the server-level write deadline set on http.Server.WriteTimeout.
	// It protects all routes (health, metrics, well-known, etc.) from slow-write clients.
	// For qualifying SSE (GET) connections, transportmiddleware.WriteTimeout clears this
	// per-request via http.ResponseController.SetWriteDeadline(time.Time{}) (golang/go#16100).
	defaultWriteTimeout = 30 * time.Second

	// defaultIdleTimeout is the maximum amount of time to wait for the next request when keep-alive's are enabled.
	defaultIdleTimeout = 120 * time.Second

	// defaultMaxHeaderBytes is the maximum size of request headers in bytes (1 MB).
	defaultMaxHeaderBytes = 1 << 20

	// defaultShutdownTimeout is the maximum time to wait for graceful shutdown.
	defaultShutdownTimeout = 10 * time.Second

	// defaultHeartbeatInterval sends SSE heartbeat pings on GET connections.
	// Prevents proxies/load balancers from closing idle SSE connections.
	defaultHeartbeatInterval = 30 * time.Second

	// defaultSessionTTL is the default session time-to-live duration.
	// Sessions that are inactive for this duration will be automatically cleaned up.
	defaultSessionTTL = 30 * time.Minute

	// Transport defaults shared by New and Serve (buildServeConfig) so the two
	// entry points cannot drift while New is not yet routed through Serve.
	defaultServerName    = "toolhive-vmcp"
	defaultServerVersion = "0.1.0"
	defaultHost          = "127.0.0.1"
	defaultEndpointPath  = "/mcp"
)

//go:generate mockgen -destination=mocks/mock_watcher.go -package=mocks -source=server.go Watcher

// Watcher is the interface for Kubernetes backend watcher integration.
// Used in dynamic mode (outgoingAuth.source: discovered) to gate readiness
// on controller-runtime cache sync before serving requests.
type Watcher interface {
	// WaitForCacheSync waits for the Kubernetes informer caches to sync.
	// Returns true if caches synced successfully, false on timeout or error.
	WaitForCacheSync(ctx context.Context) bool
}

// Config holds the Virtual MCP Server configuration.
type Config struct {
	// Name is the server name exposed in MCP protocol
	Name string

	// Version is the server version
	Version string

	// GroupRef is the name of the MCPGroup containing backend workloads.
	// Used for operational visibility in status endpoint and logging.
	GroupRef string

	// Host is the bind address (default: "127.0.0.1")
	Host string

	// Port is the bind port (default: 4483)
	Port int

	// EndpointPath is the MCP endpoint path (default: "/mcp")
	EndpointPath string

	// SessionTTL is the session time-to-live duration (default: 30 minutes)
	// Sessions inactive for this duration will be automatically cleaned up
	SessionTTL time.Duration

	// AuthMiddleware is the optional authentication middleware to apply to MCP routes.
	// If nil, no authentication is required.
	// This should be a composed middleware chain (e.g., TokenValidator + MCP parser).
	AuthMiddleware func(http.Handler) http.Handler

	// AuthzMiddleware is the optional authorization middleware to apply AFTER discovery.
	// Split from AuthMiddleware so authz can access discovered tool annotations
	// injected by the annotation enrichment middleware.
	// If nil, no authorization is performed.
	AuthzMiddleware func(http.Handler) http.Handler

	// AuthInfoHandler is the optional handler for /.well-known/oauth-protected-resource endpoint.
	// Exposes OIDC discovery information about the protected resource.
	AuthInfoHandler http.Handler

	// PassthroughHeaders is an allowlist of incoming client header names that the
	// vMCP forwards, unchanged, to every backend it calls. They are captured
	// per-request at the incoming edge (headerforward.CaptureMiddleware) and merged
	// into the per-session backend client's header-forward config. Empty disables
	// capture (no middleware is installed).
	PassthroughHeaders []string

	// RateLimitMiddleware is the optional rate-limit middleware to apply after
	// authentication and MCP request parsing.
	RateLimitMiddleware func(http.Handler) http.Handler

	// AuthServer is the optional embedded authorization server.
	// When non-nil, the routes returned by Routes() are registered on the mux
	// alongside the protected resource metadata endpoint.
	AuthServer *asrunner.EmbeddedAuthServer

	// TelemetryProvider is the optional telemetry provider.
	// If nil, no telemetry is recorded.
	TelemetryProvider *telemetry.Provider

	// AuditConfig is the optional audit configuration.
	// If nil, no audit logging is performed.
	// Component should be set to "vmcp-server" to distinguish vMCP audit logs.
	AuditConfig *audit.Config

	// HealthMonitorConfig is the optional health monitoring configuration.
	// If nil, health monitoring is disabled.
	HealthMonitorConfig *health.MonitorConfig

	// StatusReportingInterval is the interval for reporting status updates.
	// If zero, defaults to 30 seconds.
	// Lower values provide faster status updates but increase API server load.
	StatusReportingInterval time.Duration

	// Watcher is the optional Kubernetes backend watcher for dynamic mode.
	// Only set when running in K8s with outgoingAuth.source: discovered.
	// Used for /readyz endpoint to gate readiness on cache sync.
	Watcher Watcher

	// OptimizerFactory builds an optimizer from a list of tools.
	// If not set, the optimizer is disabled.
	OptimizerFactory func(context.Context, []server.ServerTool) (optimizer.Optimizer, error)

	// OptimizerConfig holds the parsed optimizer search parameters (typed values).
	// When non-nil, Start() creates the search store, wires the OptimizerFactory,
	// and registers the store cleanup in shutdownFuncs.
	// A nil value disables the optimizer.
	OptimizerConfig *optimizer.Config

	// StatusReporter enables vMCP runtime to report operational status.
	// In Kubernetes mode: Updates VirtualMCPServer.Status (requires RBAC)
	// In CLI mode: NoOpReporter (no persistent status)
	// If nil, status reporting is disabled.
	StatusReporter vmcpstatus.Reporter

	// SessionFactory creates MultiSessions for session management.
	// Required; must not be nil.
	SessionFactory vmcpsession.MultiSessionFactory

	// SessionStorage configures the session storage backend.
	// When nil or provider is "memory", local in-process storage is used.
	// When provider is "redis", a Redis-backed store is created for cross-pod
	// session persistence; the Redis password is read from the
	// THV_SESSION_REDIS_PASSWORD environment variable.
	SessionStorage *vmcpconfig.SessionStorageConfig

	// Aggregator merges backend capabilities into the advertised set. It is consumed
	// by the domain core (core.Config.Aggregator) that New builds via deriveCoreConfig:
	// the core is the single aggregator on the New/Serve path, so the session factory
	// must NOT also aggregate. Required; New fails (via core.New) when nil. The
	// composition root supplies it (the same aggregator that backs discovery).
	Aggregator aggregator.Aggregator

	// Authz is the authorizer-agnostic authorization config fed to the core admission
	// seam (core.Config.Authz). A nil value means authorization is unconfigured
	// (allow-all), matching the legacy `AuthzMiddleware != nil` guard: the composition
	// root populates it only when Cedar policies exist. It is distinct from the
	// (vestigial) AuthzMiddleware field — the core enforces authz from this config, not
	// from the HTTP middleware. When non-nil, Name must be non-empty (the Cedar resource
	// entity name).
	Authz *authz.Config
}

// Server is the Virtual MCP Server that aggregates multiple backends.
type Server struct {
	config *Config

	// core is the domain VMCP, set only on the Serve path (nil on the legacy
	// server.New path). When non-nil it is the single source of truth for the
	// advertised capability set and call routing: session registration sources
	// tools/resources from core.ListTools/ListResources, request handlers
	// delegate to core.CallTool/ReadResource, and the discovery middleware +
	// context-based audit enrichment are guarded off (the core applies the
	// admission filter the legacy authz/discovery path applied). The nil/non-nil
	// value is the branch selector throughout this file ("s.core == nil" == legacy).
	core core.VMCP

	// optimizerFactory builds a per-session optimizer over the core's advertised
	// tools. Set only on the Serve path when the optimizer is enabled (nil otherwise,
	// including the entire legacy server.New path, which decorates the session factory
	// instead). When non-nil, Serve-path session registration advertises find_tool/
	// call_tool in place of the raw core tools and dispatches call_tool's inner
	// invocation through core.CallTool. The shared store and cleanup are owned by the
	// session manager; this is the resolved factory surfaced via Manager.OptimizerFactory.
	optimizerFactory func(context.Context, []server.ServerTool) (optimizer.Optimizer, error)

	// MCP protocol server (mark3labs/mcp-go)
	mcpServer *server.MCPServer

	// HTTP server for Streamable HTTP transport
	httpServer *http.Server

	// Network listener (tracks actual bound port when using port 0)
	listener   net.Listener
	listenerMu sync.RWMutex

	// Discovery manager — retained only so Stop() drives its (now no-op) cleanup. The core
	// replaced discovery's aggregation on the New/Serve path, so New no longer wires the
	// router / backend client / handler factory / capability adapter the legacy body held.
	discoveryMgr discovery.Manager

	// Backend registry for capability discovery
	// For static mode (CLI), this is an immutable registry created from initial backends.
	// For dynamic mode (K8s), this is a DynamicRegistry updated by the operator.
	backendRegistry vmcp.BackendRegistry

	// Session manager for tracking MCP protocol sessions
	// This is ToolHive's session.Manager (pkg/transport/session) - the same component
	// used by streamable proxy for MCP session tracking. It handles:
	//   - Session storage and retrieval
	//   - TTL-based cleanup of inactive sessions
	//   - Session lifecycle management
	sessionManager *transportsession.Manager

	// sessionDataStorage is the pluggable key-value backend for session metadata.
	// Currently always LocalSessionDataStorage (in-memory, single-process).
	// Redis-backed storage for multi-pod deployments is not yet wired.
	sessionDataStorage transportsession.DataStorage

	// vmcpSessionMgr manages session-scoped backend client lifecycle.
	vmcpSessionMgr SessionManager

	// Ready channel signals when the server is ready to accept connections.
	// Closed once the listener is created and serving.
	ready     chan struct{}
	readyOnce sync.Once

	// healthMonitor performs periodic health checks on backends.
	// Nil if health monitoring is disabled.
	// Protected by healthMonitorMu: RLock for reads (getter methods, HTTP handlers),
	// Lock for writes (initialization, disabling on start failure).
	healthMonitor   *health.Monitor
	healthMonitorMu sync.RWMutex

	// statusReporter enables vMCP to report operational status to control plane.
	// Nil if status reporting is disabled.
	statusReporter vmcpstatus.Reporter

	// shutdownFuncs contains cleanup functions to run during Stop().
	// Populated during Start() initialization before blocking; no mutex needed
	// since Stop() is only called after Start()'s select returns.
	shutdownFuncs []func(context.Context) error
}

// buildSessionDataStorage constructs the DataStorage backend from cfg.
// When cfg.SessionStorage is nil or provider is "memory" (or empty), local in-process
// storage is used. When provider is "redis", a Redis-backed store is created
// using the address, DB, and key prefix from cfg.SessionStorage; the password
// is read from the THV_SESSION_REDIS_PASSWORD environment variable.
// Any other provider value is a misconfiguration and returns an error.
func buildSessionDataStorage(ctx context.Context, cfg *Config) (transportsession.DataStorage, error) {
	// Default to in-process storage when session storage is not configured,
	// or when the provider is explicitly "memory" or left empty.
	if cfg.SessionStorage == nil ||
		cfg.SessionStorage.Provider == "" ||
		strings.EqualFold(cfg.SessionStorage.Provider, "memory") {
		return transportsession.NewLocalSessionDataStorage(cfg.SessionTTL)
	}
	if cfg.SessionStorage.Provider != "redis" {
		return nil, fmt.Errorf("unsupported session storage provider %q (supported: \"memory\", \"redis\")",
			cfg.SessionStorage.Provider)
	}
	keyPrefix := cfg.SessionStorage.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = "thv:vmcp:session:"
	}
	redisCfg := tcredis.Config{
		Addr:     cfg.SessionStorage.Address,
		Password: os.Getenv(vmcpconfig.RedisPasswordEnvVar),
		DB:       int(cfg.SessionStorage.DB),
	}
	slog.Info("using Redis session storage",
		"address", cfg.SessionStorage.Address,
		"db", cfg.SessionStorage.DB,
		"key_prefix", keyPrefix,
	)
	return transportsession.NewRedisSessionDataStorage(ctx, redisCfg, keyPrefix, cfg.SessionTTL)
}

// buildHealthMonitor builds the backend health monitor from cfg, or returns (nil, nil)
// when health monitoring is not configured (cfg.HealthMonitorConfig == nil).
//
// New builds it once at the composition root (A2) so the same *health.Monitor can be
// threaded both into the core as a health.StatusProvider (capability filtering) and into
// Serve as the lifecycle owner (Start/Stop, #5443). Relocated from the old inline
// server.New body.
func buildHealthMonitor(
	ctx context.Context,
	cfg *Config,
	backendClient vmcp.BackendClient,
	backendRegistry vmcp.BackendRegistry,
) (*health.Monitor, error) {
	if cfg.HealthMonitorConfig == nil {
		slog.Info("health monitoring disabled")
		return nil, nil
	}
	// Get initial backends list from registry for health monitoring setup.
	initialBackends := backendRegistry.List(ctx)
	healthMon, err := health.NewMonitor(backendClient, initialBackends, *cfg.HealthMonitorConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create health monitor: %w", err)
	}
	slog.Info("health monitoring enabled",
		"check_interval", cfg.HealthMonitorConfig.CheckInterval,
		"unhealthy_threshold", cfg.HealthMonitorConfig.UnhealthyThreshold,
		"timeout", cfg.HealthMonitorConfig.Timeout,
		"degraded_threshold", cfg.HealthMonitorConfig.DegradedThreshold)
	return healthMon, nil
}

// New creates a Virtual MCP Server by composing the domain core (core.New) with the
// transport (Serve).
//
// It is the composition root for the in-memory Config form: it builds the backend
// health monitor (A2), assembles the core via core.New using the config projected by
// deriveCoreConfig, and hands that core to Serve with the transport config projected by
// deriveServerConfig. The 7-param signature is retained unchanged for existing callers
// (cli/serve.go and external embedders); the transport/core wiring it once performed
// inline now lives behind core.New + Serve.
//
// The backendRegistry parameter provides the list of available backends:
// - For static mode (CLI), pass an immutable registry created from initial backends
// - For dynamic mode (K8s), pass a DynamicRegistry that will be updated by the operator
//
// Runtime-contract change for embedders (the signature is unchanged, but the body now
// routes through core.New + Serve):
//   - Config.Aggregator is now REQUIRED (core.New rejects a nil aggregator); the core is
//     the single source of the advertised capability set.
//   - Config.AuthzMiddleware is vestigial: the Serve path never applies it. Authorization
//     is enforced by the core admission seam from Config.Authz. An embedder that enforced
//     Cedar authz only via AuthzMiddleware must set Config.Authz instead (a WARN is logged
//     below if AuthzMiddleware is set without Authz, to surface a silent allow-all).
func New(
	ctx context.Context,
	cfg *Config,
	rt router.Router,
	backendClient vmcp.BackendClient,
	discoveryMgr discovery.Manager,
	backendRegistry vmcp.BackendRegistry,
	workflowDefs map[string]*composer.WorkflowDefinition,
) (*Server, error) {
	// Resolve transport defaults for the transport side on a COPY (WithDefaults does not
	// mutate cfg). The core receives the RAW cfg so its ServerName is the un-defaulted
	// cfg.Name: Cedar authz keys on the real VirtualMCPServer name, not the synthetic
	// "toolhive-vmcp" transport fallback.
	resolved := WithDefaults(cfg)

	// AuthzMiddleware is vestigial on the New/Serve path (Serve never applies it; authz
	// moved to the core admission seam). Warn when it is set without a corresponding
	// Config.Authz so an embedder that relied on it for Cedar authz gets a diagnosable
	// signal instead of silently degrading to allow-all. cli/serve.go sets Authz whenever
	// it sets AuthzMiddleware, so this never fires in-tree.
	if cfg.AuthzMiddleware != nil && cfg.Authz == nil {
		slog.Warn("AuthzMiddleware is set but ignored on the server.New/Serve path; " +
			"authorization is enforced by the core admission seam from Config.Authz, which is nil " +
			"(allow-all). Set Config.Authz to enforce a policy.")
	}

	// Build the backend health monitor once at the composition root (A2). The same
	// instance is injected into the core as a health.StatusProvider (capability
	// filtering) and into Serve as the lifecycle owner (Start/Stop).
	healthMon, err := buildHealthMonitor(ctx, cfg, backendClient, backendRegistry)
	if err != nil {
		return nil, err
	}
	// Pass a true nil interface (not a typed-nil *health.Monitor) when disabled, so the
	// core's nil check disables filtering rather than calling through a nil pointer.
	var healthProvider health.StatusProvider
	if healthMon != nil {
		healthProvider = healthMon
	}

	// The SDK elicitation adapter wraps the mcp-go server Serve builds below, so it
	// cannot exist before core.New. Give the core a late-bound requester now and bind the
	// real adapter to Serve's server before serving begins (RequestElicitation is only
	// invoked at request time, after bind).
	elicitation := newLateBoundElicitationRequester()

	coreVMCP, err := core.New(deriveCoreConfig(
		cfg, cfg.Aggregator, rt, backendClient, backendRegistry, workflowDefs,
		cfg.Authz, elicitation, healthProvider,
	))
	if err != nil {
		return nil, err
	}

	// On the New/Serve path the core is the single aggregator and the source of the
	// advertised set; the session factory only opens per-session backend connections and
	// binds identity. AdvertiseFromCore makes the session manager source tools from the
	// core and (when the optimizer is enabled) build the Serve-layer optimizer over the
	// core's tools instead of decorating the factory. Composite-tool workflows are owned
	// by the core, so no WorkflowDefs/ComposerFactory are wired here.
	sessMgrCfg := &sessionmanager.FactoryConfig{
		Base:              cfg.SessionFactory,
		OptimizerConfig:   cfg.OptimizerConfig,
		OptimizerFactory:  cfg.OptimizerFactory,
		TelemetryProvider: cfg.TelemetryProvider,
		AdvertiseFromCore: true,
	}

	srv, err := Serve(ctx, coreVMCP, deriveServerConfig(resolved, healthMon, backendRegistry, sessMgrCfg))
	if err != nil {
		return nil, err
	}

	// Retain the discovery manager only so Stop() drives its (now no-op) cleanup for
	// parity with the legacy path. The core replaced discovery's aggregation, so it is
	// otherwise unused here: the shared Handler guards the discovery middleware to
	// s.core == nil, which never holds for a Serve-built server.
	srv.discoveryMgr = discoveryMgr

	// Bind the elicitation adapter to the SDK server Serve built so composite-workflow
	// elicitation reaches the same mcp-go server that serves client traffic.
	elicitation.bind(NewSDKElicitationAdapter(srv.MCPServer()))
	return srv, nil
}

// Handler builds and returns the MCP HTTP handler without starting a listener.
// This enables embedding the vmcp server inside another HTTP server or framework.
//
// The returned handler includes all routes (health, metrics, well-known, MCP)
// and the full middleware chain (recovery, header validation, auth, audit,
// discovery, backend enrichment, MCP parsing, telemetry).
//
// Each call builds a fresh handler. The method is safe to call multiple times.
// All returned handlers share the same underlying MCPServer and SessionManager,
// so callers should not serve concurrent traffic through multiple handlers.
func (s *Server) Handler(_ context.Context) (http.Handler, error) {
	// Create Streamable HTTP server with ToolHive session management
	streamableServer := server.NewStreamableHTTPServer(
		s.mcpServer,
		server.WithEndpointPath(s.config.EndpointPath),
		server.WithSessionIdManager(s.vmcpSessionMgr),
		server.WithHeartbeatInterval(defaultHeartbeatInterval),
	)

	// Create HTTP mux with separated authenticated and unauthenticated routes
	mux := http.NewServeMux()

	// Unauthenticated health endpoints
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ping", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReadiness)
	mux.HandleFunc("/status", s.handleStatus)
	mux.HandleFunc("/api/backends/health", s.handleBackendHealth)

	// Optional Prometheus metrics endpoint (unauthenticated)
	if s.config.TelemetryProvider != nil {
		if prometheusHandler := s.config.TelemetryProvider.PrometheusHandler(); prometheusHandler != nil {
			mux.Handle("/metrics", prometheusHandler)
			slog.Info("prometheus metrics endpoint enabled at /metrics")
		} else {
			slog.Warn("prometheus metrics endpoint is not enabled, but telemetry provider is configured")
		}
	}

	// RFC 9728 protected resource metadata.
	// Always register a .well-known handler so OAuth discovery requests get a
	// clean response (404 JSON when auth is off) instead of falling through to
	// the MCP handler, which rejects GETs with a 406 JSON-RPC error that
	// breaks Claude Code's OAuth error parsing.
	wellKnownHandler := auth.NewWellKnownHandler(s.config.AuthInfoHandler)
	mux.Handle("/.well-known/", wellKnownHandler)
	if s.config.AuthInfoHandler != nil {
		slog.Debug("RFC 9728 OAuth protected resource metadata enabled")
	}

	// Register embedded auth server routes if configured
	if s.config.AuthServer != nil {
		s.config.AuthServer.RegisterHandlers(mux)
		slog.Debug("embedded authorization server routes registered")
	}

	// MCP endpoint - apply middleware chain (wrapping order, execution happens in reverse):
	// Code wraps: auth+parser → rate-limit → audit → discovery → annotation-enrichment →
	//   authz → backend-enrichment → MCP-parsing → telemetry
	// Execution order: recovery → header-val → auth+parser → rate-limit → audit →
	//   discovery → annotation-enrichment → authz → backend-enrichment →
	//   MCP-parsing → telemetry → handler
	//
	// The authz and annotation-enrichment layers are both guarded by
	// s.config.AuthzMiddleware != nil: applied on the server.New path (authz on) and
	// omitted on the Serve path, which leaves AuthzMiddleware nil so authorization
	// moves to the core admission seam (#5438). Both blocks remain in this shared
	// Handler — physical removal is deferred to Phase 3 (#5445), after server.New is
	// routed through Serve and the legacy authz path is gone.

	var mcpHandler http.Handler = streamableServer

	if s.config.TelemetryProvider != nil {
		mcpHandler = s.config.TelemetryProvider.Middleware(s.config.Name, "streamable-http")(mcpHandler)
		slog.Info("telemetry middleware enabled for MCP endpoints")
	}

	// Apply MCP parsing middleware to extract JSON-RPC method from request body.
	// This runs before telemetry so that recordMetrics can label metrics with the
	// actual mcp_method (e.g. "tools/call", "initialize") instead of "unknown".
	// Note: ParsingMiddleware is also composed inside the auth middleware (for audit/authz).
	// The second application here is a no-op because the context already holds a
	// ParsedMCPRequest; it exists only so the telemetry layer works correctly even
	// when auth middleware is nil.
	mcpHandler = mcpparser.ParsingMiddleware(mcpHandler)

	// Apply backend enrichment middleware (legacy path only — see withBackendEnrichment).
	mcpHandler = s.withBackendEnrichment(mcpHandler)

	// Apply authorization middleware if configured (runs AFTER discovery in execution).
	// Wrapping it here (before discovery wrap) means discovery runs first, then authz.
	if s.config.AuthzMiddleware != nil {
		mcpHandler = s.config.AuthzMiddleware(mcpHandler)
		slog.Info("authorization middleware enabled for MCP endpoints (post-discovery)")
	}

	// Apply annotation enrichment middleware (runs after discovery, before authz in execution).
	// Reads tool annotations from discovered capabilities and injects them into the
	// request context so the authz middleware can make annotation-aware decisions.
	if s.config.AuthzMiddleware != nil {
		mcpHandler = AnnotationEnrichmentMiddleware(mcpHandler)
		slog.Info("annotation enrichment middleware enabled for MCP endpoints")
	}

	// Apply discovery middleware (runs after audit/auth middleware) — legacy path only.
	// Discovery middleware performs per-request capability aggregation with user context,
	// injecting the routing table into the request context (the discovery-into-context seam).
	// vmcpSessionMgr (MultiSessionGetter) is used to retrieve the fully-formed MultiSession
	// for subsequent requests so the routing table can be injected into context.
	// The backend registry provides a dynamic backend list (supports DynamicRegistry for K8s).
	// The health monitor enables filtering based on current health status (respects circuit breaker).
	//
	// Guarded to the legacy server.New path (s.core == nil). On the Serve path the core is
	// the single source of truth: session registration aggregates once via core.ListTools and
	// handlers route through the core (#5442), so discovery is skipped — applying it would
	// also nil-deref, since a Serve-built server has a nil discoveryMgr. WithSessionScopedRouting's
	// initialize-skip behavior is preserved here on the legacy path; physical removal of the
	// middleware and the context seam is deferred to Phase 3 (#5445).
	if s.core == nil {
		s.healthMonitorMu.RLock()
		healthMon := s.healthMonitor
		s.healthMonitorMu.RUnlock()

		var healthStatusProvider health.StatusProvider
		if healthMon != nil {
			healthStatusProvider = healthMon
		}
		mcpHandler = discovery.Middleware(
			s.discoveryMgr, s.backendRegistry, s.vmcpSessionMgr, healthStatusProvider,
			discovery.WithSessionScopedRouting(),
		)(mcpHandler)
		slog.Info("discovery middleware enabled for lazy per-user capability discovery")
	}

	// Apply audit middleware if configured (runs after auth, before discovery)
	if s.config.AuditConfig != nil {
		if err := s.config.AuditConfig.Validate(); err != nil {
			return nil, fmt.Errorf("invalid audit configuration: %w", err)
		}
		auditor, err := audit.NewAuditorWithTransport(
			s.config.AuditConfig,
			"streamable-http", // vMCP uses streamable HTTP transport
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create auditor: %w", err)
		}
		mcpHandler = auditor.Middleware(mcpHandler)
		slog.Info("audit middleware enabled for MCP endpoints")
	}

	mcpHandler = s.applyRateLimiting(mcpHandler)

	// Apply authentication middleware if configured (runs first in chain)
	if s.config.AuthMiddleware != nil {
		mcpHandler = s.config.AuthMiddleware(mcpHandler)
		slog.Info("authentication middleware enabled for MCP endpoints")
	}

	mcpHandler = s.applyForwardedHeaderCapture(mcpHandler)

	// Apply Accept header validation (rejects GET requests without Accept: text/event-stream)
	mcpHandler = headerValidatingMiddleware(mcpHandler)

	// Clear the write deadline for qualifying SSE connections (GET +
	// Accept: text/event-stream + MCP endpoint path) so the server-level
	// WriteTimeout does not kill long-lived SSE streams (see golang/go#16100).
	// Non-qualifying requests are left untouched; http.Server.WriteTimeout
	// (defaultWriteTimeout) remains in effect for them.
	mcpHandler = transportmiddleware.WriteTimeout(s.config.EndpointPath)(mcpHandler)

	// Cap request body size before the MCP parser (and all inner middleware)
	// buffers it via io.ReadAll, rejecting oversized bodies with 413. This is
	// parity with the proxy transports (see pkg/bodylimit). It only bounds the
	// request body and does not affect long-lived SSE response streams.
	mcpHandler = bodylimit.Middleware(bodylimit.DefaultMaxRequestBodySize)(mcpHandler)

	// Apply recovery middleware as outermost (catches panics from all inner middleware)
	mcpHandler = recovery.Middleware(mcpHandler)
	slog.Info("recovery middleware enabled for MCP endpoints")

	mux.Handle("/", mcpHandler)

	return mux, nil
}

func (s *Server) applyRateLimiting(next http.Handler) http.Handler {
	if s.config.RateLimitMiddleware == nil {
		return next
	}
	slog.Info("rate limit middleware enabled for MCP endpoints")
	return s.config.RateLimitMiddleware(next)
}

// applyForwardedHeaderCapture wraps next with the forwarded-header capture
// middleware when passthrough headers are configured. It copies the allowlisted
// incoming headers into the request context so the per-session backend client
// forwards them to backends (see pkg/vmcp/headerforward). Identity-independent
// plumbing: its position relative to auth is immaterial; it must only run before
// session creation, which every inner handler satisfies. No-op when the allowlist
// is empty.
func (s *Server) applyForwardedHeaderCapture(next http.Handler) http.Handler {
	if len(s.config.PassthroughHeaders) == 0 {
		return next
	}
	slog.Info("forwarded-header capture enabled for MCP endpoints", "headers", s.config.PassthroughHeaders)
	return headerforward.CaptureMiddleware(s.config.PassthroughHeaders)(next)
}

// Start starts the Virtual MCP Server and begins serving requests.
//
//nolint:gocyclo // Complexity from health monitoring and startup orchestration is acceptable
func (s *Server) Start(ctx context.Context) error {
	// Build the HTTP handler (middleware chain, routes, mux)
	handler, err := s.Handler(ctx)
	if err != nil {
		return fmt.Errorf("failed to build handler: %w", err)
	}

	// Create HTTP server
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		MaxHeaderBytes:    defaultMaxHeaderBytes,
	}

	// Create listener (allows port 0 to bind to random available port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to create listener: %w", err)
	}

	s.listenerMu.Lock()
	s.listener = listener
	s.listenerMu.Unlock()

	actualAddr := listener.Addr().String()
	slog.Info("starting Virtual MCP Server", "address", actualAddr, "endpoint", s.config.EndpointPath)
	slog.Info("health endpoints available",
		"health", actualAddr+"/health",
		"ping", actualAddr+"/ping",
		"status", actualAddr+"/status",
		"backends_health", actualAddr+"/api/backends/health")

	// Start server in background
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	// Signal that the server is ready (listener created and serving started)
	s.readyOnce.Do(func() {
		close(s.ready)
	})

	// Start health monitor if configured
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon != nil {
		if err := healthMon.Start(ctx); err != nil {
			// Log error and disable health monitoring - treat as if it wasn't configured
			// This ensures getter methods correctly report monitoring as disabled
			slog.Warn("failed to start health monitor, disabling health monitoring", "error", err)
			s.healthMonitorMu.Lock()
			s.healthMonitor = nil
			s.healthMonitorMu.Unlock()
		} else {
			slog.Info("health monitor started")
		}
	}

	// Start status reporter if configured
	if s.statusReporter != nil {
		shutdown, err := s.statusReporter.Start(ctx)
		if err != nil {
			return fmt.Errorf("failed to start status reporter: %w", err)
		}
		s.shutdownFuncs = append(s.shutdownFuncs, shutdown)

		// Create internal context for status reporting goroutine lifecycle
		// This ensures the goroutine is cleaned up on all exit paths
		statusReportingCtx, statusReportingCancel := context.WithCancel(ctx)

		// Prepare status reporting config
		statusConfig := DefaultStatusReportingConfig()
		statusConfig.Reporter = s.statusReporter
		if s.config.StatusReportingInterval > 0 {
			statusConfig.Interval = s.config.StatusReportingInterval
		}

		// Start periodic status reporting in background
		go s.periodicStatusReporting(statusReportingCtx, statusConfig)

		// Append cancel function to shutdownFuncs for cleanup
		// Done after starting goroutine to avoid race if Stop() is called immediately
		s.shutdownFuncs = append(s.shutdownFuncs, func(context.Context) error {
			statusReportingCancel()
			return nil
		})
	}

	// Wait for either context cancellation or server error
	select {
	case <-ctx.Done():
		slog.Info("context cancelled, shutting down server")
		return s.Stop(context.Background())
	case err := <-errCh:
		// HTTP server error - log and tear down cleanly
		slog.Error("hTTP server error", "error", err)
		if stopErr := s.Stop(context.Background()); stopErr != nil {
			// Combine errors if Stop() also fails
			return fmt.Errorf("server error: %w; stop error: %v", err, stopErr)
		}
		return err
	}
}

// Stop gracefully stops the Virtual MCP Server.
func (s *Server) Stop(ctx context.Context) error {
	slog.Info("stopping Virtual MCP Server")

	var errs []error

	// Stop HTTP server (this internally closes the listener)
	if s.httpServer != nil {
		// Create shutdown context with timeout
		shutdownCtx, cancel := context.WithTimeout(ctx, defaultShutdownTimeout)
		defer cancel()

		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("failed to shutdown HTTP server: %w", err))
		}
	}

	// Clear listener reference (already closed by httpServer.Shutdown)
	s.listenerMu.Lock()
	s.listener = nil
	s.listenerMu.Unlock()

	// Stop health monitor to clean up health check goroutines
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon != nil {
		if err := healthMon.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop health monitor: %w", err))
		}
	}

	// Run shutdown functions (e.g., status reporter cleanup, future components)
	for _, shutdown := range s.shutdownFuncs {
		if err := shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to execute shutdown function: %w", err))
		}
	}

	// Stop session manager after HTTP server shutdown
	if s.sessionManager != nil {
		if err := s.sessionManager.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("failed to stop session manager: %w", err))
		}
	}

	// Stop discovery manager to clean up background goroutines
	if s.discoveryMgr != nil {
		s.discoveryMgr.Stop()
	}

	// Close session data storage last: HTTP server is down (no new in-flight requests),
	// all other components have stopped (no further restore or liveness checks).
	if s.sessionDataStorage != nil {
		if err := s.sessionDataStorage.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close session data storage: %w", err))
		}
	}

	if len(errs) > 0 {
		slog.Error("errors during shutdown", "errors", errs)
		return errors.Join(errs...)
	}

	slog.Info("virtual MCP Server stopped")
	return nil
}

// Address returns the server's actual listen address.
// If the server is started with port 0, this returns the actual bound port.
func (s *Server) Address() string {
	s.listenerMu.RLock()
	defer s.listenerMu.RUnlock()

	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
}

// handleHealth handles /health and /ping HTTP requests.
// Returns 200 OK if the server is running and able to respond.
//
// Security Note: This endpoint is unauthenticated and intentionally minimal.
// It only confirms the HTTP server is responding. No version information,
// session counts, or operational metrics are exposed to prevent information
// disclosure in multi-tenant scenarios.
//
// For operational monitoring, implement an authenticated /metrics endpoint
// that requires proper authorization.
func (*Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	response := map[string]string{
		"status": "ok",
	}

	w.Header().Set("Content-Type", "application/json")
	// Always send 200 OK - even if JSON encoding fails below, the server is responding
	w.WriteHeader(http.StatusOK)

	// Encode response. If this fails (extremely unlikely for simple map[string]string),
	// the 200 OK status has already been sent above.
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode health response", "error", err)
	}
}

// handleReadiness handles /readyz HTTP requests for Kubernetes readiness probes.
//
// In dynamic mode (K8s with outgoingAuth.source: discovered), this endpoint gates
// readiness on the controller-runtime manager's cache sync status. The pod will
// not be marked ready until the manager has populated its cache with current
// backend information from the MCPGroup.
//
// In static mode (CLI or K8s with inline backends), this always returns 200 OK
// since there's no cache to sync.
//
// Design Pattern:
// This follows the same readiness gating pattern used by cert-manager and ArgoCD:
// - /health: Always returns 200 if server is responding (liveness probe)
// - /readyz: Returns 503 until caches synced, then 200 (readiness probe)
//
// K8s Configuration:
//
//	readinessProbe:
//	  httpGet:
//	    path: /readyz
//	    port: 4483
//	  initialDelaySeconds: 5
//	  periodSeconds: 5
//	  timeoutSeconds: 5
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	// Static mode: always ready (no watcher, no cache to sync)
	if s.config.Watcher == nil {
		response := map[string]string{
			"status": "ready",
			"mode":   "static",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			slog.Error("failed to encode readiness response", "error", err)
		}
		return
	}

	// Dynamic mode: gate readiness on cache sync
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if !s.config.Watcher.WaitForCacheSync(ctx) {
		// Cache not synced yet - return 503 Service Unavailable
		response := map[string]string{
			"status": "not_ready",
			"mode":   "dynamic",
			"reason": "cache_sync_pending",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		if err := json.NewEncoder(w).Encode(response); err != nil {
			slog.Error("failed to encode readiness response", "error", err)
		}
		return
	}

	// Cache synced - ready to serve requests
	response := map[string]string{
		"status": "ready",
		"mode":   "dynamic",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode readiness response", "error", err)
	}
}

// SessionManager returns the session manager instance.
// This is useful for testing and monitoring.
func (s *Server) SessionManager() *transportsession.Manager {
	return s.sessionManager
}

// MCPServer returns the underlying mark3labs *server.MCPServer instance
// servicing this vMCP server's /mcp endpoint.
//
// Intended for embedders that wrap the vMCP composer in their own pipeline and
// need to drive SDK-level operations (such as RequestElicitation) against the
// same server that handles incoming client traffic. A parallel MCPServer
// constructed by the embedder will not work: ClientSession correlation is
// keyed to the server that received the initialize request.
//
// Trust boundary: this accessor is in-process only; the returned pointer is
// the same instance for the lifetime of the Server and is safe for concurrent
// use per mark3labs guarantees.
//
// Safe operations include RequestElicitation against an active session,
// registering observability hooks, and reading registered
// tools/resources/prompts. Callers MUST NOT call shutdown/close or alter the
// serving lifecycle; that is owned by (*Server).Start and (*Server).Stop.
// Callers SHOULD prefer per-session tool registration over global
// AddTool/SetTools: globally registered tools are served to every session and
// bypass vMCP's per-session capability scoping (they still pass through the
// HTTP auth/authz chain).
//
// Elicitation callers must:
//   - Propagate the inbound request ctx so ClientSession resolves, and pass a
//     bounded deadline; RequestElicitation blocks on the remote client.
//   - Ensure the connected client advertised the "elicitation" capability
//     during initialize.
//   - Handle accept / decline / cancel responses distinctly per MCP 2025-06-18;
//     keep requestedSchema as a flat object of primitives (string / number /
//     integer / boolean / enum), which is what conforming clients accept.
//   - Never include secrets, credentials, tokens, or internal addressing
//     (backend IDs, pod names, routing-table entries) in the prompt message
//     or schema: elicitation payloads are surfaced to end-user clients.
func (s *Server) MCPServer() *server.MCPServer {
	return s.mcpServer
}

// Ready returns a channel that is closed when the server is ready to accept connections.
// This is useful for testing and synchronization.
func (s *Server) Ready() <-chan struct{} {
	return s.ready
}

// setSessionResourcesDirect sets resources directly on the session via the SessionWithResources
// interface, analogous to setSessionToolsDirect for resources.
func setSessionResourcesDirect(session server.ClientSession, resources []server.ServerResource) error {
	sessionWithResources, ok := session.(server.SessionWithResources)
	if !ok {
		return fmt.Errorf("session does not support per-session resources")
	}

	existing := sessionWithResources.GetSessionResources()
	resourceMap := make(map[string]server.ServerResource, len(existing)+len(resources))
	for k, v := range existing {
		resourceMap[k] = v
	}
	for _, res := range resources {
		resourceMap[res.Resource.URI] = res
	}
	sessionWithResources.SetSessionResources(resourceMap)
	return nil
}

// setSessionToolsDirect sets tools directly on the session via the SessionWithTools
// interface, bypassing MCPServer.AddSessionTools. This avoids sending notifications
// through the session's notification channel, which would accumulate as stale
// messages during session registration (the notification goroutine from the
// initialize request has already exited at that point).
func setSessionToolsDirect(session server.ClientSession, tools []server.ServerTool) error {
	sessionWithTools, ok := session.(server.SessionWithTools)
	if !ok {
		return fmt.Errorf("session does not support per-session tools")
	}

	// Merge with any existing tools (preserves tools set by earlier calls)
	existing := sessionWithTools.GetSessionTools()
	toolMap := make(map[string]server.ServerTool, len(existing)+len(tools))
	for k, v := range existing {
		toolMap[k] = v
	}
	for _, tool := range tools {
		toolMap[tool.Tool.Name] = tool
	}
	sessionWithTools.SetSessionTools(toolMap)
	return nil
}

// lazyInjectSessionTools injects tools into the SDK ephemeral session for sessions
// that were reconstructed from Redis on a different pod (cross-pod session sharing).
//
// When a client connects to pod B with an existing session ID (established on pod A),
// the SDK creates an ephemeral session with no tools because OnRegisterSession only fires
// during Initialize, which the client doesn't re-send to pod B. This method is called
// from OnBeforeListTools and OnBeforeCallTool hooks to lazily inject the tools before
// the SDK handler reads from the per-session tool store.
//
// For sessions initialized on this pod (normal case), tools are already in the store
// (set by setSessionToolsDirect during OnRegisterSession); this method is a no-op.
func (s *Server) lazyInjectSessionTools(ctx context.Context) {
	sess := server.ClientSessionFromContext(ctx)
	if sess == nil {
		return
	}
	sessionWithTools, ok := sess.(server.SessionWithTools)
	if !ok {
		return
	}
	if len(sessionWithTools.GetSessionTools()) > 0 {
		return // tools already registered (normal pod-local case)
	}
	sessionID := sess.SessionID()

	// Re-derive the tool set the same way registration did, so cross-pod re-injection
	// matches the advertised set. On the Serve path (s.core != nil) that means a fresh
	// core.ListTools for the request identity (the core is stateless, so re-deriving on
	// pod B is the cache-miss equivalent of the once-per-session registration call),
	// optimizer-wrapped into find_tool/call_tool when the optimizer is enabled — both
	// via serveSessionTools, the same helper registration uses; on the legacy path it
	// reads the factory-built session's tools via GetAdaptedTools.
	//
	// Note: on the Serve path this lists under the CURRENT request identity, not the
	// session's bound identity. For the realistic same-principal load-balanced case they
	// are equal, so the advertised set is identical across pods. A cross-identity re-hydration
	// would advertise the requester's own filtered set, but the call-time binding check
	// (enforceSessionBinding, run before core.CallTool/ReadResource) is the backstop — it
	// rejects a mismatched caller, so no other principal's capabilities can be invoked.
	adaptedTools, err := func() ([]server.ServerTool, error) {
		if s.core != nil {
			identity, _ := auth.IdentityFromContext(ctx)
			return s.serveSessionTools(ctx, sessionID, identity)
		}
		return s.vmcpSessionMgr.GetAdaptedTools(sessionID)
	}()
	if err != nil || len(adaptedTools) == 0 {
		slog.Debug("lazyInjectSessionTools: no tools available for session", "session_id", sessionID)
		return
	}
	if err := setSessionToolsDirect(sess, adaptedTools); err != nil {
		slog.Warn("lazyInjectSessionTools: failed to inject tools", "session_id", sessionID, "error", err)
	}
}

// handleSessionRegistration processes a new MCP session registration.
// It fires AFTER the session is registered in the SDK.
func (s *Server) handleSessionRegistration(
	ctx context.Context,
	session server.ClientSession,
) {
	// Error is logged and handled within handleSessionRegistrationImpl.
	// The session is terminated on failure; no further action needed here.
	_ = s.handleSessionRegistrationImpl(ctx, session)
}

// handleSessionRegistrationImpl handles session registration.
//
// It is invoked from handleSessionRegistration and:
//  1. Creates a MultiSession with real backend HTTP connections via CreateSession().
//  2. Retrieves SDK-format tools and resources with session-scoped routing handlers.
//  3. Registers backend tools, composite tools, and resources with the SDK for the session.
//
// Tool and resource calls are routed directly through the session's backend connections
// rather than through the global router and discovery middleware.
// Composite tool executors use the shared backend client and router.
//
// # Current capability surface
//
//   - Optimizer mode: when configured, all tools (backend + composite) are
//     indexed into the optimizer and only find_tool/call_tool are exposed,
//     using session-scoped tool handlers.
//
//   - Prompts: not supported until the SDK adds AddSessionPrompts.
func (s *Server) handleSessionRegistrationImpl(ctx context.Context, session server.ClientSession) (retErr error) {
	sessionID := session.SessionID()
	slog.Debug("creating session-scoped backends", "session_id", sessionID)

	// Defer cleanup: if any error occurs, terminate the session and log failures.
	defer func() {
		if retErr != nil {
			if _, termErr := s.vmcpSessionMgr.Terminate(sessionID); termErr != nil {
				slog.Warn("failed to clean up session after error",
					"session_id", sessionID,
					"error", termErr,
					"original_error", retErr)
			}
		}
	}()

	// NOTE: the initialize response (including the Mcp-Session-Id header) has
	// already been sent to the client before this hook fires. Any error below
	// terminates the session internally, but the client holds a session ID that
	// will appear to have no tools (tool calls will return "not found" from the
	// SDK). This is an architectural constraint of the two-phase pattern — there
	// is no way to retract the session ID after it has been sent.
	//
	// NOTE: there is a brief race window between the client receiving the session
	// ID and this hook completing. A client that pipelines a tools/call immediately
	// after initialize may receive a "tool not found" error before AddSessionTools
	// completes. Conforming MCP clients call tools/list before tools/call, so this
	// window is expected to be harmless in practice.
	if _, retErr = s.vmcpSessionMgr.CreateSession(ctx, sessionID); retErr != nil {
		slog.Error("failed to create session-scoped backends",
			"session_id", sessionID,
			"error", retErr)
		return retErr
	}

	// Serve path: the core is the single authoritative aggregation. Source the
	// advertised tool/resource set from core.ListTools/ListResources (called once
	// per session here) and install handlers that route through the core; the
	// session factory's own aggregation is not used. CreateSession above still
	// establishes the bound session record (identity binding, TTL, Validate). The
	// returned error becomes retErr (named return), so the defer terminates the
	// session on failure.
	if s.core != nil {
		return s.injectCoreSessionCapabilities(ctx, session)
	}

	// Legacy server.New path: uniform registration — same code path regardless of
	// which decorators are active. session.Tools() returns the final decorated tool list.
	adaptedTools, retErr := s.vmcpSessionMgr.GetAdaptedTools(sessionID)
	if retErr != nil {
		slog.Error("failed to get session-scoped tools",
			"session_id", sessionID,
			"error", retErr)
		return retErr
	}

	adaptedResources, retErr := s.vmcpSessionMgr.GetAdaptedResources(sessionID)
	if retErr != nil {
		slog.Error("failed to get session-scoped resources",
			"session_id", sessionID,
			"error", retErr)
		return retErr
	}

	if len(adaptedResources) > 0 {
		if err := setSessionResourcesDirect(session, adaptedResources); err != nil {
			slog.Error("failed to add session resources", "session_id", sessionID, "error", err)
			return err
		}
	}

	if len(adaptedTools) > 0 {
		if err := setSessionToolsDirect(session, adaptedTools); err != nil {
			slog.Error("failed to add session tools", "session_id", sessionID, "error", err)
			return err
		}
	}

	slog.Info("session capabilities injected",
		"session_id", sessionID,
		"tool_count", len(adaptedTools))
	return nil
}

// GetBackendHealthStatus returns the health status of a specific backend.
// Returns error if health monitoring is disabled or backend not found.
func (s *Server) GetBackendHealthStatus(backendID string) (vmcp.BackendHealthStatus, error) {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon == nil {
		return vmcp.BackendUnknown, fmt.Errorf("health monitoring is disabled")
	}
	return healthMon.GetBackendStatus(backendID)
}

// GetBackendHealthState returns the full health state of a specific backend.
// Returns error if health monitoring is disabled or backend not found.
func (s *Server) GetBackendHealthState(backendID string) (*health.State, error) {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon == nil {
		return nil, fmt.Errorf("health monitoring is disabled")
	}
	return healthMon.GetBackendState(backendID)
}

// GetAllBackendHealthStates returns the health states of all backends.
// Returns empty map if health monitoring is disabled.
func (s *Server) GetAllBackendHealthStates() map[string]*health.State {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon == nil {
		return make(map[string]*health.State)
	}
	return healthMon.GetAllBackendStates()
}

// GetHealthSummary returns a summary of backend health across all backends.
// Returns zero-valued summary if health monitoring is disabled.
func (s *Server) GetHealthSummary() health.Summary {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	if healthMon == nil {
		return health.Summary{}
	}
	return healthMon.GetHealthSummary()
}

// BackendHealthResponse represents the health status response for all backends.
type BackendHealthResponse struct {
	// MonitoringEnabled indicates if health monitoring is active.
	MonitoringEnabled bool `json:"monitoring_enabled"`

	// Summary provides aggregate health statistics.
	// Only populated if MonitoringEnabled is true.
	Summary *health.Summary `json:"summary,omitempty"`

	// Backends contains the detailed health state of each backend.
	// Only populated if MonitoringEnabled is true.
	Backends map[string]*health.State `json:"backends,omitempty"`
}

// handleBackendHealth handles /api/backends/health HTTP requests.
// Returns 200 OK with backend health information.
//
// Security Note: This endpoint is unauthenticated and may expose backend topology.
// Consider applying authentication middleware if operating in multi-tenant mode.
func (s *Server) handleBackendHealth(w http.ResponseWriter, _ *http.Request) {
	s.healthMonitorMu.RLock()
	healthMon := s.healthMonitor
	s.healthMonitorMu.RUnlock()

	response := BackendHealthResponse{
		MonitoringEnabled: healthMon != nil,
	}

	if healthMon != nil {
		summary := s.GetHealthSummary()
		response.Summary = &summary
		response.Backends = s.GetAllBackendHealthStates()
	}

	// Encode response before writing headers to ensure encoding succeeds
	data, err := json.Marshal(response)
	if err != nil {
		slog.Error("failed to encode backend health response", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		slog.Error("failed to write backend health response", "error", err)
	}
}

// notAcceptableBody is the JSON-RPC error returned when a GET request is missing
// the Accept: text/event-stream header required by the Streamable HTTP transport.
var notAcceptableBody = []byte(
	`{"jsonrpc":"2.0","id":"server-error","error":` +
		`{"code":-32600,"message":"Not Acceptable: Client must accept text/event-stream"}}`,
)

// headerValidatingMiddleware rejects GET requests that do not include
// Accept: text/event-stream, as required by the MCP Streamable HTTP transport spec.
func headerValidatingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet &&
			!strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotAcceptable)
			if _, err := w.Write(notAcceptableBody); err != nil {
				slog.Error("failed to write not-acceptable response", "error", err)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}
